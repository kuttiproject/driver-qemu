package driverqemu

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/kuttiproject/kuttilog"
	"github.com/kuttiproject/workspace"
)

// quietlogger is a kuttilog logger that discards everything, to keep the
// output of the driver off the output of the tests.
type quietlogger struct{}

func (quietlogger) MaxLevel() int                     { return kuttilog.Debug }
func (quietlogger) LevelPrefix(level int) string      { return "" }
func (quietlogger) Print(v ...interface{})            {}
func (quietlogger) Printf(f string, v ...interface{}) {}
func (quietlogger) Println(v ...interface{})          {}

// testStorage points the driver's storage root, and the kutti workspace, at
// new temporary directories for the duration of a test, and returns the
// storage root. Both are global, so tests that use this must not run in
// parallel.
func testStorage(t *testing.T) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("the qemu driver does not run on Windows")
	}

	root := filepath.Join(t.TempDir(), "kutti", "driver-qemu")
	saved := qemuStorageRoot
	qemuStorageRoot = root
	t.Cleanup(func() { qemuStorageRoot = saved })

	if err := workspace.Set(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(workspace.Reset)

	kuttilog.SetLogger(quietlogger{})
	t.Cleanup(kuttilog.ResetLogger)

	return root
}

// fakeQemuImgScript stands in for qemu-img. It answers "info" for the disk
// named by its last argument, according to files next to the disk:
//
//	<disk>.fail     exit with an error
//	<disk>.garbage  print something that is not JSON
//	<disk>.backing  contains the backing file name to report
//
// With none of these, it reports a disk without a backing file. It appends
// its arguments to the file named by FAKE_QEMU_IMG_LOG. It uses only shell
// builtins, so it works with nothing else on the path.
const fakeQemuImgScript = `#!/bin/sh
for last; do :; done
echo "$@" >> "$FAKE_QEMU_IMG_LOG"
if [ -f "$last.fail" ]; then
  echo "qemu-img: Could not open '$last'" >&2
  exit 1
fi
if [ -f "$last.garbage" ]; then
  echo "this is not json"
  exit 0
fi
if [ -f "$last.backing" ]; then
  read -r backing < "$last.backing"
  printf '{"filename": "%s", "backing-filename": "%s"}\n' "$last" "$backing"
else
  printf '{"filename": "%s"}\n' "$last"
fi
`

// installFakeQemuImg puts a fake qemu-img on the path, as the only thing on
// it, and returns the path of the file that records its calls.
func installFakeQemuImg(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "qemu-img"), []byte(fakeQemuImgScript), 0o755); err != nil {
		t.Fatal(err)
	}

	logpath := filepath.Join(dir, "calls.log")
	t.Setenv("PATH", dir)
	t.Setenv("FAKE_QEMU_IMG_LOG", logpath)

	return logpath
}

// emptyPath makes sure that no qemu-img can be found.
func emptyPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// cachedImagePath is where the cached image for a version of Kubernetes
// must be, written out in full on purpose, so that a change to the layout is
// noticed.
func cachedImagePath(root string, version string) string {
	return filepath.Join(root, "images", "kutti-"+version+".qcow2")
}

// addDisk creates a VM disk file in the disks directory. It reports the given
// backing file, if there is one.
func addDisk(t *testing.T, root string, name string, backing string) string {
	t.Helper()

	dir := filepath.Join(root, "disks")
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}

	disk := filepath.Join(dir, name)
	if err := os.WriteFile(disk, []byte("disk"), 0o666); err != nil {
		t.Fatal(err)
	}
	if backing != "" {
		if err := os.WriteFile(disk+".backing", []byte(backing+"\n"), 0o666); err != nil {
			t.Fatal(err)
		}
	}

	return disk
}

func touch(t *testing.T, path string) {
	t.Helper()

	if err := os.WriteFile(path, nil, 0o666); err != nil {
		t.Fatal(err)
	}
}
