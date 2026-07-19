package driverqemu

// Network implements the drivercore.Network interface.
type Network struct {
	name string
	cidr string
}

// Name returns the name of the network.
func (n *Network) Name() string {
	return n.name
}

// CIDR returns the network's IPv4 address range.
func (n *Network) CIDR() string {
	return n.cidr
}

// SetCIDR is not supported/implemented for QEMU driver.
func (n *Network) SetCIDR(cidr string) {
	panic("SetCIDR not implemented")
}
