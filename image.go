package driverqemu

import "github.com/kuttiproject/imagecore"

// Image implements the drivercore.Image interface for QEMU. It is the image
// of the imagecore package, which the qemu driver shares with the other
// drivers.
type Image = imagecore.Image
