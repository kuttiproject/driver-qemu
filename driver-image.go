package driverqemu

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/kuttiproject/drivercore"
	"github.com/kuttiproject/imagecore"
	"github.com/kuttiproject/kuttilog"
	"github.com/kuttiproject/workspace"
)

// ImagesVersion defines the image repository version for the current version
// of the driver.
const ImagesVersion = "0.1"

const imagesConfigFile = "driver-qemu-images.json"

// ImagesSourceURL is the location where the master list of images can be found.
var ImagesSourceURL = "https://github.com/kuttiproject/driver-qemu-images/releases/download/v" + ImagesVersion + "/" + imagesConfigFile

// imagestore manages the image list and the cached image files. See the
// imagecore package.
//
// It is made by init, and not in the declaration, because isImageInUse needs
// it to find the path of an image, and Go does not allow a variable to
// depend on itself in this way. Making it does no I/O.
var imagestore *imagecore.Store

func init() {
	imagestore = imagecore.New(imagecore.Config{
		DriverName:     "driver-qemu",
		ConfigFile:     imagesConfigFile,
		ImageExtension: ".qcow2",
		DownloadSuffix: ".qcow2download",
		SourceURL:      func() string { return ImagesSourceURL },
		CacheDir:       qemuCacheDir,
		InUse:          isImageInUse,
	})
}

// qemuStorageRoot is the base directory under which this driver stores
// cached master images and VM disks.
//
// This deliberately uses /var/tmp rather than a location under the
// user's home directory or an OS-standard cache directory (e.g.
// ~/.cache). Both were tried and rejected:
//   - Home directory: qemu, running as a distinct, unprivileged system
//     user under libvirt's system-wide daemon (qemu:///system), needs
//     every directory in the path to a disk file to be traversable by
//     that user. Home directories are private (0700) by default, and
//     making them traversable requires a manual, per-machine admin step
//     (a POSIX ACL, or loosening the home directory itself) that
//     libvirt does not set up automatically - not even when the
//     directory is wrapped in a proper libvirt storage pool via `virsh
//     pool-define-as`/`pool-build` (a well-documented libvirt gap; see
//     e.g. Red Hat bug 714997).
//   - OS-standard cache directories: qemu's default confinement refuses
//     to open files under any dot-prefixed directory component (e.g.
//     ~/.cache/...), which most per-OS cache directories are.
//
// /var/tmp is world-writable (mode 1777) by default on essentially
// every distro, so it needs no manual permission setup at all - at the
// cost of being subject to periodic clean-out by tools like
// systemd-tmpfiles (typically only for files untouched for ~30 days, so
// disks belonging to actively-used machines are unlikely to be swept,
// but a long-stopped machine's disk could be). This is a known,
// accepted trade-off, not an oversight.
//
// It is a variable only so that tests can point it at a temporary
// directory. Nothing else changes it.
var qemuStorageRoot = "/var/tmp/kutti/driver-qemu"

// sharedDirMode is the mode of the directories of the storage chain: open to
// everyone, with the sticky bit, like /var/tmp itself. Everyone can create
// files in them and get to the files, but only the owner of a file, or of the
// directory, can delete or rename it. Without the sticky bit, any user of the
// machine could replace the cached image, or the disk, of another.
//
// The sticky bit has to be given as os.ModeSticky. The octal 01777, which is
// what chmod takes, does not work: os.Chmod drops the 01000, and leaves the
// directory at 0777.
const sharedDirMode = os.ModeSticky | 0o777

// ensureQemuStorageSubdir creates (if needed) and returns the given
// subdirectory of qemuStorageRoot, setting the whole chain down to it to
// sharedDirMode, so that the qemu process can traverse into it regardless of
// which user it runs as.
//
// A directory made by another user cannot be changed, and keeps the mode it
// has. Only the owner can do that, when they next use the driver.
func ensureQemuStorageSubdir(name string) (string, error) {
	dir := filepath.Join(qemuStorageRoot, name)
	if err := os.MkdirAll(dir, 0777); err != nil {
		return "", err
	}
	_ = os.Chmod(filepath.Dir(qemuStorageRoot), sharedDirMode)
	_ = os.Chmod(qemuStorageRoot, sharedDirMode)
	_ = os.Chmod(dir, sharedDirMode)

	return dir, nil
}

// qemuCacheDir returns the directory where cached master images (and
// scratch files related to them, such as in-progress downloads) are
// stored.
func qemuCacheDir() (string, error) {
	return ensureQemuStorageSubdir("images")
}

// qcowinfo is the subset of `qemu-img info --output=json` fields we need
// to determine a qcow2 disk's backing file.
type qcowinfo struct {
	BackingFilename string `json:"backing-filename"`
}

// isImageInUse reports whether the cached master image for k8sversion is
// currently set as the backing file of any differencing disk in the VM
// disks directory. It is used to prevent removing or overwriting a master
// image out from under machines that depend on it.
func isImageInUse(k8sversion string) (bool, error) {
	imagePath, err := imagestore.ImagePath(k8sversion)
	if err != nil {
		return false, err
	}
	imagePath, err = filepath.Abs(imagePath)
	if err != nil {
		return false, err
	}
	kuttilog.Printf(kuttilog.Debug, "Checking if image %s is in use by any differencing disks", imagePath)

	disksDir, err := qemuDisksDir()
	if err != nil {
		return false, err
	}

	entries, err := os.ReadDir(disksDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	qemuImgPath, err := exec.LookPath("qemu-img")
	if err != nil {
		return false, err
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".qcow2" {
			continue
		}

		diskPath := filepath.Join(disksDir, entry.Name())
		// -U (--force-share) lets qemu-img open the disk in shared/read-only
		// mode even though a running machine holds qemu's image lock on it.
		// Without this flag, inspecting a running machine's disk fails, and
		// that's exactly the disk most likely to matter.
		out, err := workspace.RunWithResults(qemuImgPath, "info", "-U", "--output=json", diskPath)
		if err != nil {
			// We cannot prove this disk isn't backed by the image in
			// question, so we cannot conclude the image is safe to
			// delete or overwrite. Fail closed rather than skip it.
			return false, fmt.Errorf("could not inspect disk %s: %v", diskPath, err)
		}

		var info qcowinfo
		if err := json.Unmarshal([]byte(out), &info); err != nil {
			return false, fmt.Errorf("could not parse qemu-img info for %s: %v", diskPath, err)
		}

		if info.BackingFilename == "" {
			continue
		}

		backingAbs, err := filepath.Abs(info.BackingFilename)
		if err != nil {
			return false, fmt.Errorf("could not resolve backing file path for %s: %v", diskPath, err)
		}

		if backingAbs == imagePath {
			kuttilog.Printf(kuttilog.Debug, "Image %s is in use by disk %s", imagePath, diskPath)
			return true, nil
		}
	}

	kuttilog.Printf(kuttilog.Debug, "Image %s is not in use by any differencing disks", imagePath)
	return false, nil
}

// UpdateImageList fetches the latest list of VM images from the driver source URL.
func (d *Driver) UpdateImageList() error {
	return imagestore.UpdateImageList()
}

// ValidK8sVersion returns true if the specified Kubernetes version is available.
func (d *Driver) ValidK8sVersion(k8sversion string) bool {
	return imagestore.ValidK8sVersion(k8sversion)
}

// K8sVersions returns all Kubernetes versions currently supported by kutti.
func (d *Driver) K8sVersions() []string {
	return imagestore.K8sVersions()
}

// ListImages lists the currently available Images.
func (d *Driver) ListImages() ([]drivercore.Image, error) {
	return imagestore.ListImages()
}

// GetImage returns an image corresponding to a Kubernetes version, or an error.
func (d *Driver) GetImage(k8sversion string) (drivercore.Image, error) {
	return imagestore.GetImage(k8sversion)
}
