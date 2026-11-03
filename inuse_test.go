package driverqemu

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInUseBackedByImage(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	addDisk(t, root, "node.qcow2", cachedImagePath(root, "1.37"))

	inuse, err := isImageInUse("1.37")
	if err != nil || !inuse {
		t.Errorf("got %v, %v, expected true", inuse, err)
	}
}

func TestInUseBackedByOtherVersion(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	addDisk(t, root, "node.qcow2", cachedImagePath(root, "1.36"))

	inuse, err := isImageInUse("1.37")
	if err != nil || inuse {
		t.Errorf("got %v, %v, expected false", inuse, err)
	}
}

// The comparison is of whole paths, not of beginnings.
func TestInUseNeedsTheSamePath(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	addDisk(t, root, "node.qcow2", cachedImagePath(root, "1.37"))

	for _, version := range []string{"1.3", "1.37.1"} {
		inuse, err := isImageInUse(version)
		if err != nil || inuse {
			t.Errorf("version %s: got %v, %v, expected false", version, inuse, err)
		}
	}

	addDisk(t, root, "other.qcow2", cachedImagePath(root, "1.37")+".old")
	addDisk(t, root, "third.qcow2", cachedImagePath(root, "1.37")[1:])
	os.Remove(filepath.Join(root, "disks", "node.qcow2.backing"))

	inuse, err := isImageInUse("1.37")
	if err != nil || inuse {
		t.Errorf("got %v, %v, expected false", inuse, err)
	}
}

func TestInUseDiskWithoutBacking(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	addDisk(t, root, "node.qcow2", "")

	inuse, err := isImageInUse("1.37")
	if err != nil || inuse {
		t.Errorf("got %v, %v, expected false", inuse, err)
	}
}

func TestInUseNoDisks(t *testing.T) {
	testStorage(t)
	installFakeQemuImg(t)

	inuse, err := isImageInUse("1.37")
	if err != nil || inuse {
		t.Errorf("got %v, %v, expected false", inuse, err)
	}
}

func TestInUseOneDiskOfSeveral(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	addDisk(t, root, "a.qcow2", cachedImagePath(root, "1.36"))
	addDisk(t, root, "b.qcow2", cachedImagePath(root, "1.37"))
	addDisk(t, root, "c.qcow2", "")

	inuse, err := isImageInUse("1.37")
	if err != nil || !inuse {
		t.Errorf("1.37: got %v, %v, expected true", inuse, err)
	}

	inuse, err = isImageInUse("1.38")
	if err != nil || inuse {
		t.Errorf("1.38: got %v, %v, expected false", inuse, err)
	}
}

// Only files that end in .qcow2 are VM disks.
func TestInUseIgnoresOtherFilesAndDirectories(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)

	addDisk(t, root, "other.img", cachedImagePath(root, "1.37"))

	dir := filepath.Join(root, "disks", "dir.qcow2")
	if err := os.Mkdir(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+".backing", []byte(cachedImagePath(root, "1.37")), 0o666); err != nil {
		t.Fatal(err)
	}

	inuse, err := isImageInUse("1.37")
	if err != nil || inuse {
		t.Errorf("got %v, %v, expected false", inuse, err)
	}
}

// If a disk cannot be checked, the image cannot be shown to be unused.
func TestInUseFailsClosedWhenDiskCannotBeInspected(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	disk := addDisk(t, root, "node.qcow2", "")
	touch(t, disk+".fail")

	inuse, err := isImageInUse("1.37")
	if err == nil || inuse {
		t.Fatalf("got %v, %v, expected an error", inuse, err)
	}
	if !strings.Contains(err.Error(), "could not inspect disk") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestInUseFailsClosedOnOutputThatIsNotJSON(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	disk := addDisk(t, root, "node.qcow2", "")
	touch(t, disk+".garbage")

	inuse, err := isImageInUse("1.37")
	if err == nil || inuse {
		t.Fatalf("got %v, %v, expected an error", inuse, err)
	}
	if !strings.Contains(err.Error(), "could not parse") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestInUseFailsWithoutQemuImg(t *testing.T) {
	root := testStorage(t)
	emptyPath(t)
	addDisk(t, root, "node.qcow2", "")

	inuse, err := isImageInUse("1.37")
	if err == nil || inuse {
		t.Errorf("got %v, %v, expected an error", inuse, err)
	}
}

// -U matters: a running machine holds a lock on its disk, and without -U the
// disk that most needs checking cannot be opened.
func TestInUseAsksQemuImgForJSONWithoutLocking(t *testing.T) {
	root := testStorage(t)
	logpath := installFakeQemuImg(t)
	disk := addDisk(t, root, "node.qcow2", "")

	if _, err := isImageInUse("1.37"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(logpath)
	if err != nil {
		t.Fatal(err)
	}

	expected := "info -U --output=json " + disk + "\n"
	if string(data) != expected {
		t.Errorf("qemu-img was called as %q, expected %q", data, expected)
	}
}
