package driverqemu

import (
	"errors"
	"os/exec"

	"github.com/kuttiproject/drivercore"
	"github.com/kuttiproject/workspace"
)

const (
	driverName        = "qemu"
	driverDescription = "Kutti driver for QEMU/KVM via libvirt"
)

// Driver implements the drivercore.Driver interface for QEMU/libvirt.
type Driver struct {
	qemuPath     string
	qemuImgPath  string
	virshPath    string
	validated    bool
	status       string
	errormessage string
}

// Name returns "qemu".
func (d *Driver) Name() string {
	return driverName
}

// Description returns a short description of the driver.
func (d *Driver) Description() string {
	return driverDescription
}

// UsesPerClusterNetworking returns true as we build dedicated networks.
func (d *Driver) UsesPerClusterNetworking() bool {
	return true
}

// UsesNATNetworking returns false. The host bridge directly routes traffic
// to guest IPs without port forwarding.
func (d *Driver) UsesNATNetworking() bool {
	return false
}

func (d *Driver) validate() bool {
	if d.validated {
		return true
	}

	qemuPath, err := exec.LookPath("qemu-system-x86_64")
	if err != nil {
		d.status = "Error"
		d.errormessage = "qemu-system-x86_64 not found on path"
		return false
	}
	d.qemuPath = qemuPath

	qemuImgPath, err := exec.LookPath("qemu-img")
	if err != nil {
		d.status = "Error"
		d.errormessage = "qemu-img not found on path"
		return false
	}
	d.qemuImgPath = qemuImgPath

	virshPath, err := exec.LookPath("virsh")
	if err != nil {
		d.status = "Error"
		d.errormessage = "virsh not found on path"
		return false
	}
	d.virshPath = virshPath

	// Check if libvirt connection works
	_, err = workspace.RunWithResults(virshPath, "uri")
	if err != nil {
		d.status = "Error"
		d.errormessage = "could not connect to libvirt daemon: " + err.Error()
		return false
	}

	d.status = "Ready"
	d.validated = true
	return true
}

// Status returns the current status of the Driver.
func (d *Driver) Status() string {
	d.validate()
	return d.status
}

// Error returns the last error reported by the Driver.
func (d *Driver) Error() string {
	d.validate()
	if d.status != "Error" {
		return ""
	}
	return d.errormessage
}

func (d *Driver) runVirsh(args ...string) (string, error) {
	if !d.validate() {
		return "", errors.New(d.errormessage)
	}
	return workspace.RunWithResults(d.virshPath, args...)
}

func init() {
	driver := &Driver{}
	drivercore.RegisterDriver(driverName, driver)
}
