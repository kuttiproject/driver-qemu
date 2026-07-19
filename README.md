# driver-qemu

kutti driver for Qemu using libvirt

[![Go Report Card](https://goreportcard.com/badge/github.com/kuttiproject/driver-qemu)](https://goreportcard.com/report/github.com/kuttiproject/driver-qemu)
[![PkgGoDev](https://pkg.go.dev/badge/github.com/kuttiproject/driver-qemu)](https://pkg.go.dev/github.com/kuttiproject/driver-qemu)
![GitHub release (latest by date)](https://img.shields.io/github/v/release/kuttiproject/driver-qemu?include_prereleases)


## Images

This driver depends on VirtualBox VM images published via the [kuttiproject/driver-qemu-images](https://github.com/kuttiproject/driver-qemu-images) repository. The details of the driver-to-VM interface are documented there.

The releases of that repository are the default source for this driver. The list of available/deprecated images and the images themselves are published there. The releases of that repository follow the major and minor (rarely, also the patch) versions of this repository, but sometimes may lag by one version. The `ImagesVersion` constant specifies the version of the images repository that is used by a particular version of this driver.
