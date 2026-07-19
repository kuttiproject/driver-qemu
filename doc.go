// Package driverqemu implements a kutti driver for QEMU/KVM on Linux using libvirt.
// It orchestrates VMs and networks via the system-wide libvirt daemon (qemu:///system)
// through the virsh command line utility.
//
// For cluster networking, it creates dedicated virtual networks for each cluster
// with their own bridge interface and NAT routing.
// For VM creation, it copies a cached Debian QCOW2 image and defines KVM domains.
// IP addresses are leased from dnsmasq and kept stable using static DHCP host mappings.
package driverqemu
