package driverqemu

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kuttiproject/drivercore"
	"github.com/kuttiproject/kuttilog"
	"github.com/kuttiproject/workspace"
)

// QualifiedMachineName returns the qualified name of a machine.
func (d *Driver) QualifiedMachineName(machinename string, clustername string) string {
	return fmt.Sprintf("kutti-%s-%s", clustername, machinename)
}

// GetMachine returns a Machine instance if defined in libvirt.
func (d *Driver) GetMachine(machinename string, clustername string) (drivercore.Machine, error) {
	qname := d.QualifiedMachineName(machinename, clustername)

	// Verify the VM exists in libvirt
	_, err := d.runVirsh("dominfo", qname)
	if err != nil {
		return nil, fmt.Errorf("machine %s does not exist: %v", machinename, err)
	}

	m := &Machine{
		driver:      d,
		name:        machinename,
		clustername: clustername,
		qname:       qname,
	}
	m.Status() // Load status

	return m, nil
}

// NewMachine defines a new QEMU VM in libvirt and configures its static DHCP lease.
func (d *Driver) NewMachine(machinename string, clustername string, k8sversion string) (drivercore.Machine, error) {
	qname := d.QualifiedMachineName(machinename, clustername)
	netname := d.QualifiedNetworkName(clustername)

	// Fail fast if a domain with this name already exists, before anything
	// (disk, DHCP lease) is touched. Discovering this later, via `virsh
	// define` failing, means steps 3-5 have already mutated shared state
	// for this qname.
	if _, err := d.runVirsh("dominfo", qname); err == nil {
		return nil, fmt.Errorf("machine %s already exists in cluster %s", machinename, clustername)
	}

	// 1. Get the local cached image
	imagePath, err := imagepathfromk8sversion(k8sversion)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(imagePath); err != nil {
		return nil, fmt.Errorf("cached image not found for K8s version %s at %s: %v", k8sversion, imagePath, err)
	}

	// 2. Prepare VM disks directory in /var/tmp/kutti
	disksDir, err := qemuDisksDir()
	if err != nil {
		return nil, err
	}

	// 3. Create a differencing disk backed by the cached master image.
	// The master image is never copied or modified; this VM's disk only
	// stores the deltas from it. The master image path is stored inside
	// the new disk's qcow2 header, so it must remain at this exact path
	// and remain unchanged for as long as this machine (or any other
	// machine backed by it) exists.
	imagePath, err = filepath.Abs(imagePath)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve absolute path for image %s: %v", imagePath, err)
	}
	_ = os.Chmod(imagePath, 0644) // Ensure libvirt-qemu can read the backing file

	diskPath := filepath.Join(disksDir, qname+".qcow2")
	_, err = d.runQemuImg(
		"create",
		"-f", "qcow2",
		"-F", "qcow2", // Pin the backing file's format explicitly; never let qemu probe it.
		"-b", imagePath,
		diskPath,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create differencing disk: %v", err)
	}
	_ = os.Chmod(diskPath, 0666) // Allow read/write by other processes (like libvirt-qemu)

	// 4. Resolve free IP and MAC on the network.
	// First, clear out any stale DHCP lease left behind by a previous
	// failed attempt to create this exact machine (e.g. if an earlier
	// run got this far but failed later, at Start() or during guest
	// rename). Without this, a retry can collide with its own leftover
	// lease. This is a no-op if no stale entry exists.
	d.removeStaleDHCPHostEntries(netname, qname)

	ip, mac, err := d.findNextFreeIPAndMAC(netname)
	if err != nil {
		workspace.RemoveFile(diskPath)
		return nil, fmt.Errorf("failed to find free IP/MAC: %v", err)
	}

	// 5. Add static DHCP host lease
	hostXML := fmt.Sprintf("<host mac='%s' name='%s' ip='%s'/>", mac, qname, ip)
	_, err = d.runVirsh("net-update", netname, "add", "ip-dhcp-host", hostXML, "--live", "--config")
	if err != nil {
		workspace.RemoveFile(diskPath)
		return nil, fmt.Errorf("failed to add DHCP lease to network %s: %v", netname, err)
	}

	// 6. Generate domain XML
	xmlContent := fmt.Sprintf(`<domain type='kvm'>
  <name>%s</name>
  <memory unit='KiB'>2097152</memory>
  <vcpu placement='static'>2</vcpu>
  <os>
    <type arch='x86_64' machine='pc'>hvm</type>
    <boot dev='hd'/>
  </os>
  <features>
    <acpi/>
    <apic/>
  </features>
  <cpu mode='host-passthrough'/>
  <devices>
    <emulator>%s</emulator>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2'/>
      <source file='%s'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <interface type='network'>
      <source network='%s'/>
      <mac address='%s'/>
      <model type='virtio'/>
    </interface>
    <serial type='pty'>
      <target type='isa-serial' port='0'>
        <model name='isa-serial'/>
      </target>
    </serial>
    <console type='pty'>
      <target type='serial' port='0'/>
    </console>
    <graphics type='vnc' port='-1' autoport='yes' listen='127.0.0.1'>
      <listen type='address' address='127.0.0.1'/>
    </graphics>
    <video>
      <model type='vga' vram='16384' heads='1' primary='yes'/>
    </video>
  </devices>
</domain>`, qname, d.qemuPath, diskPath, netname, mac)

	// Write domain XML to temp file
	cacheDir, err := qemuCacheDir()
	if err != nil {
		workspace.RemoveFile(diskPath)
		d.runVirsh("net-update", netname, "delete", "ip-dhcp-host", hostXML, "--live", "--config")
		return nil, err
	}
	xmlPath := filepath.Join(cacheDir, qname+".xml")
	err = os.WriteFile(xmlPath, []byte(xmlContent), 0644)
	if err != nil {
		workspace.RemoveFile(diskPath)
		return nil, err
	}
	defer os.Remove(xmlPath)

	// Define the domain
	_, err = d.runVirsh("define", xmlPath)
	if err != nil {
		workspace.RemoveFile(diskPath)
		d.runVirsh("net-update", netname, "delete", "ip-dhcp-host", hostXML, "--live", "--config")
		return nil, fmt.Errorf("failed to define VM %s: %v", qname, err)
	}

	newmachine := &Machine{
		driver:      d,
		name:        machinename,
		clustername: clustername,
		qname:       qname,
	}
	newmachine.Status() // Load initial status

	// Start Machine
	kuttilog.Println(kuttilog.Info, "Starting host...")
	err = newmachine.Start()
	if err != nil {
		kuttilog.Printf(kuttilog.Info, "Failed to start host: %v. Rolling back...", err)
		if delErr := d.DeleteMachine(machinename, clustername); delErr != nil {
			kuttilog.Printf(kuttilog.Info, "Rollback also failed: %v", delErr)
		}
		return nil, fmt.Errorf("could not start machine %s: %v", machinename, err)
	}

	// TODO: Try to parameterize the timeout
	newmachine.WaitForStateChange(25)

	// Change the name
	for renameretries := 1; renameretries < 4; renameretries++ {
		kuttilog.Printf(kuttilog.Info, "Renaming host (attempt %v/3)...", renameretries)
		// err = renamemachine(newmachine, machinename)
		err = newmachine.ExecuteCommand(drivercore.RenameMachine, machinename)
		if err == nil {
			break
		}
		kuttilog.Printf(kuttilog.Info, "Failed. Waiting %v seconds before retry...", renameretries*10)
		time.Sleep(time.Duration(renameretries*10) * time.Second)
	}

	if err != nil {
		kuttilog.Printf(kuttilog.Info, "Failed to rename host after 3 attempts: %v. Rolling back...", err)
		if delErr := d.DeleteMachine(machinename, clustername); delErr != nil {
			kuttilog.Printf(kuttilog.Info, "Rollback also failed: %v", delErr)
		}
		return nil, fmt.Errorf("could not rename machine %s: %v", machinename, err)
	}

	kuttilog.Println(kuttilog.Info, "Host renamed.")

	kuttilog.Println(kuttilog.Info, "Stopping host...")
	newmachine.Stop()

	newmachine.status = drivercore.MachineStatusStopped

	return newmachine, nil
}

// DeleteMachine stops, undefines the VM, removes its lease, and deletes its disk file.
func (d *Driver) DeleteMachine(machinename string, clustername string) error {
	qname := d.QualifiedMachineName(machinename, clustername)
	netname := d.QualifiedNetworkName(clustername)

	// Stop VM if running
	_, _ = d.runVirsh("destroy", qname)

	// Clean up DHCP lease
	mac, ip, err := d.getVMNetworkConfig(qname)
	if err == nil && mac != "" && ip != "" {
		hostXML := fmt.Sprintf("<host mac='%s' name='%s' ip='%s'/>", mac, qname, ip)
		_, _ = d.runVirsh("net-update", netname, "delete", "ip-dhcp-host", hostXML, "--live", "--config")
	}

	// Undefine domain
	_, undefineErr := d.runVirsh("undefine", qname)

	// Delete disk file
	disksDir, err := qemuDisksDir()
	if err == nil {
		diskPath := filepath.Join(disksDir, qname+".qcow2")
		_ = workspace.RemoveFile(diskPath)
	}

	if undefineErr != nil {
		return fmt.Errorf("failed to undefine machine %s: %v", machinename, undefineErr)
	}

	return nil
}

// removeStaleDHCPHostEntries deletes any existing DHCP host entries for
// qname from netname. Static leases in this driver are keyed by qname
// (there should only ever be one), so this makes lease allocation
// idempotent: if a previous attempt to create this exact machine failed
// partway through and left a lease behind, this clears it out before a
// fresh one is computed and added. It is a no-op if no entry exists.
func (d *Driver) removeStaleDHCPHostEntries(netname string, qname string) {
	xmlOut, err := d.runVirsh("net-dumpxml", netname)
	if err != nil {
		return
	}

	nameSearch := fmt.Sprintf("name='%s'", qname)
	pos := 0
	for {
		i := strings.Index(xmlOut[pos:], nameSearch)
		if i == -1 {
			return
		}
		absIdx := pos + i

		entryStart := strings.LastIndex(xmlOut[:absIdx], "<host ")
		if entryStart == -1 {
			pos = absIdx + len(nameSearch)
			continue
		}
		relEnd := strings.Index(xmlOut[entryStart:], "/>")
		if relEnd == -1 {
			pos = absIdx + len(nameSearch)
			continue
		}
		entryEnd := entryStart + relEnd + len("/>")
		hostEntry := xmlOut[entryStart:entryEnd]

		_, _ = d.runVirsh("net-update", netname, "delete", "ip-dhcp-host", hostEntry, "--live", "--config")

		pos = entryEnd
	}
}

// findNextFreeIPAndMAC resolves a free IP and MAC on a given network.
func (d *Driver) findNextFreeIPAndMAC(netname string) (string, string, error) {
	xmlOut, err := d.runVirsh("net-dumpxml", netname)
	if err != nil {
		return "", "", err
	}

	// Parse X from address='192.168.X.1'
	var x int
	idx := strings.Index(xmlOut, "address='192.168.")
	if idx == -1 {
		return "", "", fmt.Errorf("could not parse subnet from network %s", netname)
	}
	_, err = fmt.Sscanf(xmlOut[idx+len("address='192.168."):], "%d", &x)
	if err != nil {
		return "", "", fmt.Errorf("could not parse subnet third octet from network %s", netname)
	}

	// Scan for used IPs in current hosts definition
	usedIPs := make(map[int]bool)
	searchStr := fmt.Sprintf("192.168.%d.", x)
	pos := 0
	for {
		i := strings.Index(xmlOut[pos:], searchStr)
		if i == -1 {
			break
		}
		absIdx := pos + i
		var y int
		_, err := fmt.Sscanf(xmlOut[absIdx+len(searchStr):], "%d", &y)
		if err == nil {
			usedIPs[y] = true
		}
		pos = absIdx + 1
	}

	// Find the first free Y starting from 10
	for y := 10; y <= 250; y++ {
		if !usedIPs[y] {
			ip := fmt.Sprintf("192.168.%d.%d", x, y)
			mac := fmt.Sprintf("52:54:00:12:%02x:%02x", x, y)
			return ip, mac, nil
		}
	}

	return "", "", errors.New("no free IPs available in network DHCP range")
}

// getVMNetworkConfig extracts the MAC and IP of a machine from its domain XML and network XML.
func (d *Driver) getVMNetworkConfig(qname string) (mac string, ip string, err error) {
	xmlOut, err := d.runVirsh("dumpxml", qname)
	if err != nil {
		return "", "", err
	}

	idx := strings.Index(xmlOut, "<mac address='")
	if idx == -1 {
		return "", "", fmt.Errorf("could not find MAC address in VM %s XML", qname)
	}
	mac = xmlOut[idx+len("<mac address='") : idx+len("<mac address='")+17]

	parts := strings.Split(qname, "-")
	if len(parts) < 3 {
		return mac, "", nil
	}
	clustername := parts[1]
	netname := d.QualifiedNetworkName(clustername)

	netXml, err := d.runVirsh("net-dumpxml", netname)
	if err != nil {
		return mac, "", nil
	}

	// Find matching host entry in DHCP hosts
	macSearch := fmt.Sprintf("mac='%s'", mac)
	hIdx := strings.Index(netXml, macSearch)
	if hIdx == -1 {
		return mac, "", nil
	}

	hostEntryStart := strings.LastIndex(netXml[:hIdx], "<host ")
	if hostEntryStart == -1 {
		return mac, "", nil
	}
	hostEntryEnd := strings.Index(netXml[hostEntryStart:], "/>")
	if hostEntryEnd == -1 {
		return mac, "", nil
	}
	hostEntry := netXml[hostEntryStart : hostEntryStart+hostEntryEnd]

	ipIdx := strings.Index(hostEntry, "ip='")
	if ipIdx == -1 {
		return mac, "", nil
	}
	ipEnd := strings.Index(hostEntry[ipIdx+4:], "'")
	if ipEnd == -1 {
		return mac, "", nil
	}
	ip = hostEntry[ipIdx+4 : ipIdx+4+ipEnd]

	return mac, ip, nil
}

func qemuDisksDir() (string, error) {
	disksDir := "/var/tmp/kutti/driver-qemu/disks"
	err := os.MkdirAll(disksDir, 0777)
	if err != nil {
		return "", err
	}
	_ = os.Chmod("/var/tmp/kutti", 0777)
	_ = os.Chmod("/var/tmp/kutti/driver-qemu", 0777)
	_ = os.Chmod(disksDir, 0777)
	return disksDir, nil
}
