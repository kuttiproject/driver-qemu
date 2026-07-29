package driverqemu

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kuttiproject/drivercore"
	"github.com/kuttiproject/sshclient"
)

// Machine implements the drivercore.Machine interface.
type Machine struct {
	driver      *Driver
	name        string
	clustername string
	qname       string
	status      drivercore.MachineStatus
	errormsg    string
}

// Name returns the name of the machine.
func (m *Machine) Name() string {
	return m.name
}

// Status returns the current running status of the machine.
func (m *Machine) Status() drivercore.MachineStatus {
	output, err := m.driver.runVirsh("domstate", m.qname)
	if err != nil {
		m.status = drivercore.MachineStatusError
		m.errormsg = err.Error()
		return m.status
	}

	state := strings.TrimSpace(strings.ToLower(output))
	if strings.Contains(state, "running") {
		m.status = drivercore.MachineStatusRunning
	} else if strings.Contains(state, "shut off") || strings.Contains(state, "paused") {
		m.status = drivercore.MachineStatusStopped
	} else {
		m.status = drivercore.MachineStatusUnknown
	}

	return m.status
}

// Error returns the last error message from driver interaction.
func (m *Machine) Error() string {
	return m.errormsg
}

// IPAddress returns the assigned IP address of this machine.
func (m *Machine) IPAddress() string {
	_, ip, err := m.driver.getVMNetworkConfig(m.qname)
	if err != nil {
		return ""
	}
	return ip
}

// SSHAddress returns the SSH host and port.
func (m *Machine) SSHAddress() string {
	ip := m.IPAddress()
	if ip != "" {
		return ip + ":22"
	}
	return ""
}

// Start boots the VM via virsh.
func (m *Machine) Start() error {
	_, err := m.driver.runVirsh("start", m.qname)
	if err != nil {
		m.status = drivercore.MachineStatusError
		m.errormsg = err.Error()
		return fmt.Errorf("could not start machine %s: %v", m.name, err)
	}

	m.status = drivercore.MachineStatusRunning
	return nil
}

// Stop stops the VM gracefully via ACPI shutdown.
func (m *Machine) Stop() error {
	_, err := m.driver.runVirsh("shutdown", m.qname)
	if err != nil {
		m.status = drivercore.MachineStatusError
		m.errormsg = err.Error()
		return fmt.Errorf("could not stop machine %s: %v", m.name, err)
	}

	m.status = drivercore.MachineStatusStopped
	return nil
}

// ForceStop kills the VM immediately.
func (m *Machine) ForceStop() error {
	_, err := m.driver.runVirsh("destroy", m.qname)
	if err != nil {
		m.status = drivercore.MachineStatusError
		m.errormsg = err.Error()
		return fmt.Errorf("could not force stop machine %s: %v", m.name, err)
	}

	m.status = drivercore.MachineStatusStopped
	return nil
}

// WaitForStateChange polls status until it changes or timeout.
func (m *Machine) WaitForStateChange(timeoutinseconds int) {
	initialState := m.status
	for i := 0; i < timeoutinseconds; i++ {
		time.Sleep(1 * time.Second)
		if m.Status() != initialState {
			return
		}
	}
}

// ForwardPort is a no-op as direct routing is used.
func (m *Machine) ForwardPort(hostport int, machineport int) error {
	return nil
}

// UnforwardPort is a no-op as direct routing is used.
func (m *Machine) UnforwardPort(machineport int) error {
	return nil
}

// ForwardSSHPort is a no-op as direct routing is used.
func (m *Machine) ForwardSSHPort(hostport int) error {
	return nil
}

// ImplementsCommand returns true if the predefined command is supported.
func (m *Machine) ImplementsCommand(command drivercore.PredefinedCommand) bool {
	return command == drivercore.RenameMachine
}

// ExecuteCommand runs a predefined command in the VM guest.
func (m *Machine) ExecuteCommand(command drivercore.PredefinedCommand, params ...string) error {
	if command != drivercore.RenameMachine {
		return fmt.Errorf("command %s not supported", command)
	}

	if len(params) == 0 {
		return errors.New("missing new hostname parameter")
	}
	newname := params[0]

	sshAddr := m.SSHAddress()
	if sshAddr == "" {
		return errors.New("machine does not have an SSH address")
	}

	// Qemu Kutti image uses 'kuttiadmin' / 'Pass@word1'
	client := sshclient.NewWithPassword("kuttiadmin", "Pass@word1")
	scriptPath := "/opt/kutti/scripts/set-hostname.sh"
	_, err := client.RunWithResults(sshAddr, fmt.Sprintf("sudo %s %s", scriptPath, newname))
	return err
}
