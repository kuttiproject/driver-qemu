package driverqemu

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultStorageRoot(t *testing.T) {
	if qemuStorageRoot != "/var/tmp/kutti/driver-qemu" {
		t.Errorf("storage root is %s", qemuStorageRoot)
	}
}

func TestDefaultImagesSourceURL(t *testing.T) {
	expected := "https://github.com/kuttiproject/driver-qemu-images/releases/download/v0.1/driver-qemu-images.json"
	if ImagesSourceURL != expected {
		t.Errorf("source URL is %s, expected %s", ImagesSourceURL, expected)
	}
}

func TestStorageDirectories(t *testing.T) {
	root := testStorage(t)

	cache, err := qemuCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if cache != filepath.Join(root, "images") {
		t.Errorf("cache dir is %s", cache)
	}

	disks, err := qemuDisksDir()
	if err != nil {
		t.Fatal(err)
	}
	if disks != filepath.Join(root, "disks") {
		t.Errorf("disks dir is %s", disks)
	}

	for _, dir := range []string{cache, disks} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Errorf("%s was not created as a directory: %v", dir, err)
		}
	}
}

func TestImageNameAndPath(t *testing.T) {
	root := testStorage(t)

	if name := imagenamefromk8sversion("1.37"); name != "kutti-1.37.qcow2" {
		t.Errorf("image name is %s", name)
	}

	path, err := imagepathfromk8sversion("1.37")
	if err != nil {
		t.Fatal(err)
	}
	if path != cachedImagePath(root, "1.37") {
		t.Errorf("image path is %s, expected %s", path, cachedImagePath(root, "1.37"))
	}
}

// The directories are open to everyone, because qemu runs as another user
// and has to get to the files. The permissions are set on the whole chain,
// including directories that already exist.
func TestStorageDirectoryPermissions(t *testing.T) {
	root := testStorage(t)

	// Directories that exist already, and are closed.
	parent := filepath.Dir(root)
	if err := os.MkdirAll(filepath.Join(root, "images"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{parent, root, filepath.Join(root, "images")} {
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := qemuCacheDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := qemuDisksDir(); err != nil {
		t.Fatal(err)
	}

	for _, dir := range []string{
		parent,
		root,
		filepath.Join(root, "images"),
		filepath.Join(root, "disks"),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o777 {
			t.Errorf("%s has permissions %v, expected 0777", dir, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSticky != 0 {
			t.Errorf("%s has the sticky bit set", dir)
		}
	}
}
