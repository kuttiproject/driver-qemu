package driverqemu

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"

	"github.com/kuttiproject/drivercore"
	"github.com/kuttiproject/kuttilog"
	"github.com/kuttiproject/workspace"
)

// ImagesVersion defines the image repository version for the current version
// of the driver.
const ImagesVersion = "0.4"

const imagesConfigFile = "driver-qemu-images.json"

// ImagesSourceURL is the location where the master list of images can be found.
// This is declared as a package-level variable so tests can override it.
var ImagesSourceURL = "https://github.com/kuttiproject/driver-qemu-images/releases/download/v" + ImagesVersion + "/" + imagesConfigFile

var (
	imagedata             = &imageconfigdata{}
	imageconfigmanager, _ = workspace.NewFileConfigManager(imagesConfigFile, imagedata)
)

type imageconfigdata struct {
	images map[string]*Image
}

func (icd *imageconfigdata) Serialize() ([]byte, error) {
	return json.Marshal(icd.images)
}

func (icd *imageconfigdata) Deserialize(data []byte) error {
	loaddata := make(map[string]*Image)
	err := json.Unmarshal(data, &loaddata)
	if err == nil {
		icd.images = loaddata
	}
	return err
}

func (icd *imageconfigdata) SetDefaults() {
	icd.images = make(map[string]*Image)
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
const qemuStorageRoot = "/var/tmp/kutti/driver-qemu"

// ensureQemuStorageSubdir creates (if needed) and returns the given
// subdirectory of qemuStorageRoot, chmod'ing the whole chain down to it
// permissively so the qemu process can traverse into it regardless of
// which user it runs as.
func ensureQemuStorageSubdir(name string) (string, error) {
	dir := filepath.Join(qemuStorageRoot, name)
	if err := os.MkdirAll(dir, 0777); err != nil {
		return "", err
	}
	_ = os.Chmod("/var/tmp/kutti", 0777)
	_ = os.Chmod(qemuStorageRoot, 0777)
	_ = os.Chmod(dir, 0777)

	return dir, nil
}

// qemuCacheDir returns the directory where cached master images (and
// scratch files related to them, such as in-progress downloads) are
// stored.
func qemuCacheDir() (string, error) {
	return ensureQemuStorageSubdir("images")
}

func qemuConfigDir() (string, error) {
	return workspace.ConfigDir()
}

func imagenamefromk8sversion(k8sversion string) string {
	return "kutti-" + k8sversion + ".qcow2"
}

func imagepathfromk8sversion(k8sversion string) (string, error) {
	cachedir, err := qemuCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cachedir, imagenamefromk8sversion(k8sversion)), nil
}

func addfromfile(k8sversion string, filepath string, checksum string) error {
	kuttilog.Println(kuttilog.Info, "Checking image validity...")
	filechecksum, err := workspace.ChecksumFile(filepath)
	if err != nil {
		return err
	}

	if filechecksum != checksum {
		kuttilog.Printf(kuttilog.Debug, "checksum for file %v failed.\nWanted: %v\nGot   : %v\n", filepath, checksum, filechecksum)
		return errors.New("file is not valid")
	}

	localfilepath, err := imagepathfromk8sversion(k8sversion)
	if err != nil {
		return err
	}

	// If a cached copy of this image already exists, machines may already
	// have differencing disks backed by it. Overwriting it in place with
	// different content would silently corrupt every one of those disks.
	if existingchecksum, chkerr := workspace.ChecksumFile(localfilepath); chkerr == nil {
		if existingchecksum == checksum {
			// Already have this exact file cached; nothing to do.
			return nil
		}

		inuse, useerr := isImageInUse(k8sversion)
		if useerr != nil {
			return fmt.Errorf(
				"cannot replace cached image for K8s version %s: could not verify it is unused: %v",
				k8sversion, useerr,
			)
		}
		if inuse {
			return fmt.Errorf(
				"cannot replace cached image for K8s version %s: it is in use as a backing disk by one or more machines",
				k8sversion,
			)
		}
	}

	kuttilog.Println(kuttilog.Info, "Copying image to local cache...")
	const BUFSIZE = 131072
	err = workspace.CopyFile(filepath, localfilepath, BUFSIZE, true)
	if err != nil {
		return err
	}

	return nil
}

func removefile(k8sversion string) error {
	inuse, err := isImageInUse(k8sversion)
	if err != nil {
		return fmt.Errorf(
			"cannot remove cached image for K8s version %s: could not verify it is unused: %v",
			k8sversion, err,
		)
	}
	if inuse {
		return fmt.Errorf(
			"cannot remove cached image for K8s version %s: it is in use as a backing disk by one or more machines",
			k8sversion,
		)
	}

	filename, err := imagepathfromk8sversion(k8sversion)
	if err != nil {
		return err
	}
	return workspace.RemoveFile(filename)
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
	imagePath, err := imagepathfromk8sversion(k8sversion)
	if err != nil {
		return false, err
	}
	imagePath, err = filepath.Abs(imagePath)
	if err != nil {
		return false, err
	}

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
			return true, nil
		}
	}

	return false, nil
}

func fetchimagelist() error {
	confdir, _ := qemuConfigDir()
	tempfilename := "qemuimagesnewlist.json"
	tempfilepath := path.Join(confdir, tempfilename)

	kuttilog.Printf(kuttilog.Debug, "confdir: %v\ntempfilepath: %v\n", confdir, tempfilepath)
	kuttilog.Println(kuttilog.Info, "Fetching image list...")
	kuttilog.Printf(kuttilog.Debug, "Fetching from %v into %v.", ImagesSourceURL, tempfilepath)

	err := workspace.DownloadFile(ImagesSourceURL, tempfilepath)
	kuttilog.Printf(kuttilog.Debug, "Error: %v", err)
	if err != nil {
		return err
	}
	defer workspace.RemoveFile(tempfilepath)

	tempimagedata := &imageconfigdata{}
	tempconfigmanager, err := workspace.NewFileConfigManager(tempfilename, tempimagedata)
	if err != nil {
		return err
	}

	err = tempconfigmanager.Load()
	if err != nil {
		return err
	}

	// Compare and preserve Downloaded status if checkums match
	for key, newimage := range tempimagedata.images {
		oldimage := imagedata.images[key]
		if oldimage != nil &&
			newimage.imageChecksum == oldimage.imageChecksum &&
			newimage.imageSourceURL == oldimage.imageSourceURL &&
			oldimage.imageStatus == drivercore.ImageStatusDownloaded {
			newimage.imageStatus = drivercore.ImageStatusDownloaded
		}
	}

	imagedata.images = tempimagedata.images
	imageconfigmanager.Save()

	return nil
}

// UpdateImageList fetches the latest list of VM images from the driver source URL.
func (d *Driver) UpdateImageList() error {
	return fetchimagelist()
}

// ValidK8sVersion returns true if the specified Kubernetes version is available.
func (d *Driver) ValidK8sVersion(k8sversion string) bool {
	err := imageconfigmanager.Load()
	if err != nil {
		return false
	}
	_, ok := imagedata.images[k8sversion]
	return ok
}

// K8sVersions returns all Kubernetes versions currently supported by kutti.
func (d *Driver) K8sVersions() []string {
	err := imageconfigmanager.Load()
	if err != nil {
		return []string{}
	}

	result := make([]string, len(imagedata.images))
	index := 0
	for _, value := range imagedata.images {
		result[index] = value.imageK8sVersion
		index++
	}
	return result
}

// ListImages lists the currently available Images.
func (d *Driver) ListImages() ([]drivercore.Image, error) {
	err := imageconfigmanager.Load()
	if err != nil {
		return []drivercore.Image{}, err
	}

	result := make([]drivercore.Image, len(imagedata.images))
	index := 0
	for _, value := range imagedata.images {
		result[index] = value
		index++
	}
	return result, nil
}

// GetImage returns an image corresponding to a Kubernetes version, or an error.
func (d *Driver) GetImage(k8sversion string) (drivercore.Image, error) {
	err := imageconfigmanager.Load()
	if err != nil {
		return nil, err
	}

	img, ok := imagedata.images[k8sversion]
	if !ok {
		return nil, fmt.Errorf("no image present for K8s version %s", k8sversion)
	}
	return img, nil
}
