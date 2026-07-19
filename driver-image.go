package driverqemu

import (
	"encoding/json"
	"errors"
	"fmt"
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

func qemuCacheDir() (string, error) {
	return workspace.CacheSubDir("driver-qemu")
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

	kuttilog.Println(kuttilog.Info, "Copying image to local cache...")
	const BUFSIZE = 131072
	err = workspace.CopyFile(filepath, localfilepath, BUFSIZE, true)
	if err != nil {
		return err
	}

	return nil
}

func removefile(k8sversion string) error {
	filename, err := imagepathfromk8sversion(k8sversion)
	if err != nil {
		return err
	}
	return workspace.RemoveFile(filename)
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
