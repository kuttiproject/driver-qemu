package driverqemu

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kuttiproject/drivercore"
	"github.com/kuttiproject/workspace"
)

// QualifiedNetworkName returns the name of the libvirt network for the given cluster.
func (d *Driver) QualifiedNetworkName(clustername string) string {
	return clustername + "kuttinet"
}

// NewNetwork creates a new libvirt NAT-based bridge network with an unused IP subnet.
func (d *Driver) NewNetwork(clustername string) (drivercore.Network, error) {
	netname := d.QualifiedNetworkName(clustername)

	// Check if the network already exists
	_, err := d.runVirsh("net-info", netname)
	if err == nil {
		return nil, fmt.Errorf("network %s already exists", netname)
	}

	// Find a free subnet third octet
	x, err := d.findFreeSubnet()
	if err != nil {
		return nil, err
	}

	bridgeName := fmt.Sprintf("kuttibr%d", x)
	cidr := fmt.Sprintf("192.168.%d.0/24", x)
	ipAddr := fmt.Sprintf("192.168.%d.1", x)
	dhcpStart := fmt.Sprintf("192.168.%d.10", x)
	dhcpEnd := fmt.Sprintf("192.168.%d.250", x)

	xmlContent := fmt.Sprintf(`<network>
  <name>%s</name>
  <bridge name='%s' stp='on' delay='0'/>
  <forward mode='nat'/>
  <ip address='%s' netmask='255.255.255.0'>
    <dhcp>
      <range start='%s' end='%s'/>
    </dhcp>
  </ip>
</network>`, netname, bridgeName, ipAddr, dhcpStart, dhcpEnd)

	// Write definition XML to cache directory temporary file
	cacheDir, err := workspace.CacheSubDir("driver-qemu")
	if err != nil {
		return nil, err
	}
	xmlPath := filepath.Join(cacheDir, netname+".xml")
	err = os.WriteFile(xmlPath, []byte(xmlContent), 0644)
	if err != nil {
		return nil, err
	}
	defer os.Remove(xmlPath)

	// Define network
	_, err = d.runVirsh("net-define", xmlPath)
	if err != nil {
		return nil, fmt.Errorf("failed to define network %s: %v", netname, err)
	}

	// Start network
	_, err = d.runVirsh("net-start", netname)
	if err != nil {
		d.runVirsh("net-undefine", netname)
		return nil, fmt.Errorf("failed to start network %s: %v", netname, err)
	}

	// Autostart network
	_, _ = d.runVirsh("net-autostart", netname)

	return &Network{
		name: netname,
		cidr: cidr,
	}, nil
}

// DeleteNetwork destroys and undefines the libvirt virtual network.
func (d *Driver) DeleteNetwork(clustername string) error {
	netname := d.QualifiedNetworkName(clustername)

	// Stop/destroy running network bridge/iptables rules
	_, _ = d.runVirsh("net-destroy", netname)

	// Undefine network definition
	_, err := d.runVirsh("net-undefine", netname)
	if err != nil {
		return fmt.Errorf("failed to delete network %s: %v", netname, err)
	}
	return nil
}

// findFreeSubnet scans existing libvirt networks to find an unused third-octet in 192.168.X.0/24.
func (d *Driver) findFreeSubnet() (int, error) {
	output, err := d.runVirsh("net-list", "--all", "--name")
	if err != nil {
		return 0, err
	}
	lines := strings.Split(output, "\n")
	usedSubnets := make(map[int]bool)

	for _, line := range lines {
		netName := strings.TrimSpace(line)
		if netName == "" {
			continue
		}

		xmlOut, err := d.runVirsh("net-dumpxml", netName)
		if err == nil {
			// Find address='192.168.X.1'
			idx := strings.Index(xmlOut, "address='192.168.")
			if idx != -1 {
				var x int
				_, err := fmt.Sscanf(xmlOut[idx+len("address='192.168."):], "%d", &x)
				if err == nil {
					usedSubnets[x] = true
				}
			}
		}
	}

	// Find first unused X starting from 125
	for x := 125; x < 255; x++ {
		if !usedSubnets[x] {
			return x, nil
		}
	}

	return 0, errors.New("no free subnets found in 192.168.X.0 range")
}
