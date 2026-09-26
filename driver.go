package driverqemu

import (
	"errors"
	"os/exec"

	"github.com/kuttiproject/drivercore"
	"github.com/kuttiproject/kuttilog"
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
		kuttilog.Printf(kuttilog.Debug, "driver-qemu validation failed: %s", d.errormessage)
		return false
	}
	d.qemuPath = qemuPath
	kuttilog.Printf(kuttilog.Debug, "Found qemu-system-x86_64 at: %s", qemuPath)

	qemuImgPath, err := exec.LookPath("qemu-img")
	if err != nil {
		d.status = "Error"
		d.errormessage = "qemu-img not found on path"
		kuttilog.Printf(kuttilog.Debug, "driver-qemu validation failed: %s", d.errormessage)
		return false
	}
	d.qemuImgPath = qemuImgPath
	kuttilog.Printf(kuttilog.Debug, "Found qemu-img at: %s", qemuImgPath)

	virshPath, err := exec.LookPath("virsh")
	if err != nil {
		d.status = "Error"
		d.errormessage = "virsh not found on path"
		kuttilog.Printf(kuttilog.Debug, "driver-qemu validation failed: %s", d.errormessage)
		return false
	}
	d.virshPath = virshPath
	kuttilog.Printf(kuttilog.Debug, "Found virsh at: %s", virshPath)

	// Check if libvirt connection works
	uriOut, err := workspace.RunWithResults(virshPath, "uri")
	if err != nil {
		d.status = "Error"
		d.errormessage = "could not connect to libvirt daemon: " + err.Error()
		kuttilog.Printf(kuttilog.Debug, "driver-qemu validation failed: %s", d.errormessage)
		return false
	}
	kuttilog.Printf(kuttilog.Debug, "Connected to libvirt URI: %s", uriOut)

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
	kuttilog.Printf(kuttilog.Debug, "Executing virsh %v", args)
	output, err := workspace.RunWithResults(d.virshPath, args...)
	if err != nil {
		kuttilog.Printf(kuttilog.Debug, "virsh error: %v, output: %s", err, output)
	} else {
		kuttilog.Printf(kuttilog.Debug, "virsh result: %s", output)
	}
	return output, err
}

func (d *Driver) runQemuImg(args ...string) (string, error) {
	if !d.validate() {
		return "", errors.New(d.errormessage)
	}
	kuttilog.Printf(kuttilog.Debug, "Executing qemu-img %v", args)
	output, err := workspace.RunWithResults(d.qemuImgPath, args...)
	if err != nil {
		kuttilog.Printf(kuttilog.Debug, "qemu-img error: %v, output: %s", err, output)
	} else {
		kuttilog.Printf(kuttilog.Debug, "qemu-img result: %s", output)
	}
	return output, err
}

func init() {
	driver := &Driver{}
	drivercore.RegisterDriver(driverName, driver)
}
