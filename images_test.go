package driverqemu

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kuttiproject/drivercore"
	"github.com/kuttiproject/workspace"
)

// These tests use only what a user of the driver can use: the Driver methods
// that deal with images, and the Image methods. They check what the driver
// does with a list and image files from a local server, with the storage in
// temporary directories and a fake qemu-img.

type testimage struct {
	version    string
	content    []byte
	deprecated bool

	// path, if set, is where the server serves the file of the image, and
	// so what its source URL is. Otherwise it is "/kutti-<version>.qcow2".
	path string
}

func (ti testimage) urlpath() string {
	if ti.path != "" {
		return ti.path
	}

	return "/kutti-" + ti.version + ".qcow2"
}

func (ti testimage) checksum() string {
	return fmt.Sprintf("%x", sha256.Sum256(ti.content))
}

// content makes content for a test image that is different for different
// seeds.
func content(seed string, size int) []byte {
	result := make([]byte, size)
	for i := range result {
		result[i] = seed[i%len(seed)] + byte(i%251)
	}

	return result
}

// imageserver serves an image list, and the images in it. A test can change
// what it serves.
type imageserver struct {
	*httptest.Server

	mu       sync.Mutex
	images   []testimage
	listcode int
	files    map[string][]byte
}

func newImageServer(t *testing.T, images ...testimage) *imageserver {
	t.Helper()

	is := &imageserver{listcode: http.StatusOK, files: map[string][]byte{}}
	is.set(images...)

	is.Server = httptest.NewServer(http.HandlerFunc(is.handle))
	t.Cleanup(is.Close)

	saved := ImagesSourceURL
	ImagesSourceURL = is.URL + "/" + imagesConfigFile
	t.Cleanup(func() { ImagesSourceURL = saved })

	return is
}

// set changes the images in the list. The files served are those the images
// have.
func (is *imageserver) set(images ...testimage) {
	is.mu.Lock()
	defer is.mu.Unlock()

	is.images = images
	for _, img := range images {
		is.files[img.urlpath()] = img.content
	}
}

// serveFile changes what is served as the file of an image, without changing
// the list.
func (is *imageserver) serveFile(version string, data []byte) {
	is.mu.Lock()
	defer is.mu.Unlock()

	is.files["/kutti-"+version+".qcow2"] = data
}

func (is *imageserver) failList(code int) {
	is.mu.Lock()
	defer is.mu.Unlock()

	is.listcode = code
}

func (is *imageserver) handle(w http.ResponseWriter, r *http.Request) {
	is.mu.Lock()
	defer is.mu.Unlock()

	if r.URL.Path == "/"+imagesConfigFile {
		if is.listcode != http.StatusOK {
			http.Error(w, "no list", is.listcode)
			return
		}

		list := map[string]map[string]interface{}{}
		for _, img := range is.images {
			list[img.version] = map[string]interface{}{
				"ImageK8sVersion": img.version,
				"ImageChecksum":   img.checksum(),
				"ImageSourceURL":  "http://" + r.Host + img.urlpath(),
				"ImageStatus":     "NotDownloaded",
				"ImageDeprecated": img.deprecated,
			}
		}
		json.NewEncoder(w).Encode(list)
		return
	}

	data, ok := is.files[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.Write(data)
}

func getImage(t *testing.T, version string) drivercore.Image {
	t.Helper()

	img, err := (&Driver{}).GetImage(version)
	if err != nil {
		t.Fatal(err)
	}

	return img
}

func isDownloaded(t *testing.T, version string) bool {
	t.Helper()
	return getImage(t, version).Status() == drivercore.ImageStatusDownloaded
}

// updateList gets the list from the server.
func updateList(t *testing.T) {
	t.Helper()

	if err := (&Driver{}).UpdateImageList(); err != nil {
		t.Fatalf("UpdateImageList failed: %v", err)
	}
}

// statusInListFile reads the status of an image from the saved list.
func statusInListFile(t *testing.T, version string) string {
	t.Helper()

	dir, err := workspace.ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, imagesConfigFile))
	if err != nil {
		t.Fatal(err)
	}

	var list map[string]map[string]interface{}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}

	status, _ := list[version]["ImageStatus"].(string)
	return status
}

func fileNames(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	return names
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.WriteFile(path, data, 0o666); err != nil {
		t.Fatal(err)
	}
}

func TestImageListQueries(t *testing.T) {
	testStorage(t)
	newImageServer(t,
		testimage{version: "1.36", content: content("a", 100), deprecated: true},
		testimage{version: "1.37", content: content("b", 100), deprecated: false},
	)
	updateList(t)

	d := &Driver{}

	versions := d.K8sVersions()
	sort.Strings(versions)
	if strings.Join(versions, ",") != "1.36,1.37" {
		t.Errorf("versions are %v", versions)
	}

	if !d.ValidK8sVersion("1.37") || d.ValidK8sVersion("1.99") {
		t.Error("ValidK8sVersion gave the wrong answer")
	}

	images, err := d.ListImages()
	if err != nil || len(images) != 2 {
		t.Fatalf("ListImages returned %d images, %v", len(images), err)
	}

	old := getImage(t, "1.36")
	if old.K8sVersion() != "1.36" || !old.Deprecated() {
		t.Errorf("image 1.36 is %q, deprecated %v", old.K8sVersion(), old.Deprecated())
	}
	current := getImage(t, "1.37")
	if current.K8sVersion() != "1.37" || current.Deprecated() {
		t.Errorf("image 1.37 is %q, deprecated %v", current.K8sVersion(), current.Deprecated())
	}
	if current.Status() == drivercore.ImageStatusDownloaded {
		t.Error("a new image is marked as downloaded")
	}

	img, err := d.GetImage("1.99")
	if err == nil || img != nil {
		t.Errorf("GetImage of an unknown version returned %v, %v", img, err)
	}
	if err != nil && !strings.Contains(err.Error(), "1.99") {
		t.Errorf("the error does not name the version: %v", err)
	}
}

func TestUpdateImageListFailureKeepsList(t *testing.T) {
	testStorage(t)
	server := newImageServer(t, testimage{version: "1.37", content: content("b", 100), deprecated: false})
	updateList(t)

	server.failList(http.StatusNotFound)
	if err := (&Driver{}).UpdateImageList(); err == nil {
		t.Error("UpdateImageList succeeded although the server failed")
	}

	if versions := (&Driver{}).K8sVersions(); len(versions) != 1 || versions[0] != "1.37" {
		t.Errorf("versions are %v", versions)
	}
}

func TestFetch(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	image := testimage{version: "1.37", content: content("b", 3<<20), deprecated: false}
	newImageServer(t, image)
	updateList(t)

	if err := getImage(t, "1.37").Fetch(); err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}

	cached := cachedImagePath(root, "1.37")
	if string(readFile(t, cached)) != string(image.content) {
		t.Error("the cached file does not hold the image")
	}
	if names := fileNames(t, filepath.Dir(cached)); strings.Join(names, ",") != "kutti-1.37.qcow2" {
		t.Errorf("the cache directory holds %v", names)
	}

	if !isDownloaded(t, "1.37") {
		t.Error("the image is not marked as downloaded")
	}
	if status := statusInListFile(t, "1.37"); status != string(drivercore.ImageStatusDownloaded) {
		t.Errorf("the saved list has status %q", status)
	}
}

func TestFetchWithProgress(t *testing.T) {
	testStorage(t)
	installFakeQemuImg(t)
	image := testimage{version: "1.37", content: content("b", 3<<20), deprecated: false}
	newImageServer(t, image)
	updateList(t)

	var calls int
	var last, total int64
	err := getImage(t, "1.37").FetchWithProgress(func(current int64, size int64) {
		calls++
		last, total = current, size
	})
	if err != nil {
		t.Fatal(err)
	}

	if calls == 0 || last != int64(len(image.content)) || total != last {
		t.Errorf("progress was called %d times, ending at %d of %d", calls, last, total)
	}
}

func TestFetchOfCorruptImageKeepsNothing(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	server := newImageServer(t, testimage{version: "1.37", content: content("b", 1000), deprecated: false})
	updateList(t)

	server.serveFile("1.37", content("x", 1000))

	err := getImage(t, "1.37").Fetch()
	if err == nil || !strings.Contains(err.Error(), "file is not valid") {
		t.Fatalf("Fetch returned %v, expected an error that the file is not valid", err)
	}

	if names := fileNames(t, filepath.Join(root, "images")); len(names) != 0 {
		t.Errorf("the cache directory holds %v", names)
	}
	if isDownloaded(t, "1.37") {
		t.Error("the image is marked as downloaded")
	}
}

func TestFromFile(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	image := testimage{version: "1.37", content: content("b", 5000), deprecated: false}
	newImageServer(t, image)
	updateList(t)

	source := filepath.Join(t.TempDir(), "mine.qcow2")
	writeFile(t, source, image.content)

	if err := getImage(t, "1.37").FromFile(source); err != nil {
		t.Fatalf("FromFile failed: %v", err)
	}

	if string(readFile(t, cachedImagePath(root, "1.37"))) != string(image.content) {
		t.Error("the cached file does not hold the image")
	}
	if !isDownloaded(t, "1.37") {
		t.Error("the image is not marked as downloaded")
	}
}

func TestFromFileRejectsWrongFile(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	newImageServer(t, testimage{version: "1.37", content: content("b", 5000), deprecated: false})
	updateList(t)

	source := filepath.Join(t.TempDir(), "mine.qcow2")
	writeFile(t, source, content("x", 5000))

	err := getImage(t, "1.37").FromFile(source)
	if err == nil || !strings.Contains(err.Error(), "file is not valid") {
		t.Errorf("FromFile returned %v, expected an error that the file is not valid", err)
	}
	if _, statErr := os.Stat(cachedImagePath(root, "1.37")); statErr == nil {
		t.Error("a file was cached")
	}
	if isDownloaded(t, "1.37") {
		t.Error("the image is marked as downloaded")
	}

	err = getImage(t, "1.37").FromFile(filepath.Join(t.TempDir(), "missing.qcow2"))
	if err == nil {
		t.Error("FromFile of a file that does not exist succeeded")
	}
}

// Importing what is cached already does not copy it again.
func TestFromFileOfCachedContentDoesNotCopy(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	image := testimage{version: "1.37", content: content("b", 5000), deprecated: false}
	newImageServer(t, image)
	updateList(t)

	source := filepath.Join(t.TempDir(), "mine.qcow2")
	writeFile(t, source, image.content)
	if err := getImage(t, "1.37").FromFile(source); err != nil {
		t.Fatal(err)
	}

	cached := cachedImagePath(root, "1.37")
	longago := time.Now().Add(-48 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(cached, longago, longago); err != nil {
		t.Fatal(err)
	}

	if err := getImage(t, "1.37").FromFile(source); err != nil {
		t.Fatalf("second FromFile failed: %v", err)
	}

	info, err := os.Stat(cached)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(longago) {
		t.Error("the cached file was written again")
	}
}

// cacheOldAndPublishNew caches an image, and then changes the list so that
// the image has other content. It returns a file with the new content.
func cacheOldAndPublishNew(t *testing.T) (oldcontent []byte, newfile string, newcontent []byte) {
	t.Helper()

	oldimage := testimage{version: "1.37", content: content("old", 5000), deprecated: false}
	newimage := testimage{version: "1.37", content: content("new", 6000), deprecated: false}
	server := newImageServer(t, oldimage)
	updateList(t)

	source := filepath.Join(t.TempDir(), "old.qcow2")
	writeFile(t, source, oldimage.content)
	if err := getImage(t, "1.37").FromFile(source); err != nil {
		t.Fatal(err)
	}

	server.set(newimage)
	updateList(t)

	newfile = filepath.Join(t.TempDir(), "new.qcow2")
	writeFile(t, newfile, newimage.content)

	return oldimage.content, newfile, newimage.content
}

// A cached image that machines are using must not change under them.
func TestFromFileDoesNotReplaceImageInUse(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	oldcontent, newfile, _ := cacheOldAndPublishNew(t)

	addDisk(t, root, "node.qcow2", cachedImagePath(root, "1.37"))

	err := getImage(t, "1.37").FromFile(newfile)
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("FromFile returned %v, expected an error that the image is in use", err)
	}
	if string(readFile(t, cachedImagePath(root, "1.37"))) != string(oldcontent) {
		t.Error("the cached image was changed")
	}
}

func TestFromFileReplacesImageNotInUse(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	_, newfile, newcontent := cacheOldAndPublishNew(t)

	addDisk(t, root, "node.qcow2", cachedImagePath(root, "1.36"))

	if err := getImage(t, "1.37").FromFile(newfile); err != nil {
		t.Fatalf("FromFile failed: %v", err)
	}
	if string(readFile(t, cachedImagePath(root, "1.37"))) != string(newcontent) {
		t.Error("the cached image was not replaced")
	}
	if !isDownloaded(t, "1.37") {
		t.Error("the image is not marked as downloaded")
	}
}

func TestPurgeLocal(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	image := testimage{version: "1.37", content: content("b", 5000), deprecated: false}
	newImageServer(t, image)
	updateList(t)

	// Purging an image that is not downloaded does nothing.
	if err := getImage(t, "1.37").PurgeLocal(); err != nil {
		t.Errorf("PurgeLocal of an image that is not downloaded failed: %v", err)
	}

	source := filepath.Join(t.TempDir(), "mine.qcow2")
	writeFile(t, source, image.content)
	if err := getImage(t, "1.37").FromFile(source); err != nil {
		t.Fatal(err)
	}

	if err := getImage(t, "1.37").PurgeLocal(); err != nil {
		t.Fatalf("PurgeLocal failed: %v", err)
	}
	if _, err := os.Stat(cachedImagePath(root, "1.37")); err == nil {
		t.Error("the cached file is still there")
	}
	if isDownloaded(t, "1.37") {
		t.Error("the image is still marked as downloaded")
	}
	if status := statusInListFile(t, "1.37"); status == string(drivercore.ImageStatusDownloaded) {
		t.Errorf("the saved list has status %q", status)
	}
}

func TestPurgeLocalKeepsImageInUse(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	image := testimage{version: "1.37", content: content("b", 5000), deprecated: false}
	newImageServer(t, image)
	updateList(t)

	source := filepath.Join(t.TempDir(), "mine.qcow2")
	writeFile(t, source, image.content)
	if err := getImage(t, "1.37").FromFile(source); err != nil {
		t.Fatal(err)
	}
	addDisk(t, root, "node.qcow2", cachedImagePath(root, "1.37"))

	err := getImage(t, "1.37").PurgeLocal()
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("PurgeLocal returned %v, expected an error that the image is in use", err)
	}
	if _, statErr := os.Stat(cachedImagePath(root, "1.37")); statErr != nil {
		t.Error("the cached file was removed")
	}
	if !isDownloaded(t, "1.37") {
		t.Error("the image is no longer marked as downloaded")
	}
}

// An update keeps the downloaded status of an image only if the image has
// not changed, and never makes an image downloaded.
func TestUpdateImageListKeepsDownloadedStatusOfUnchangedImage(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	image := testimage{version: "1.37", content: content("b", 5000)}
	newImageServer(t, image)
	updateList(t)

	updateList(t)
	if isDownloaded(t, "1.37") {
		t.Error("an update made an image that was not downloaded look downloaded")
	}

	if err := getImage(t, "1.37").Fetch(); err != nil {
		t.Fatal(err)
	}

	updateList(t)
	if !isDownloaded(t, "1.37") {
		t.Error("an update of an unchanged list lost the downloaded status")
	}
	if _, err := os.Stat(cachedImagePath(root, "1.37")); err != nil {
		t.Error("the cached file was removed by the update")
	}
}

func TestUpdateImageListDropsDownloadedStatusOfChangedImage(t *testing.T) {
	root := testStorage(t)
	installFakeQemuImg(t)
	original := testimage{version: "1.37", content: content("b", 5000)}
	server := newImageServer(t, original)
	updateList(t)
	if err := getImage(t, "1.37").Fetch(); err != nil {
		t.Fatal(err)
	}

	// Other content.
	server.set(testimage{version: "1.37", content: content("new", 5000)})
	updateList(t)
	if isDownloaded(t, "1.37") {
		t.Error("an image whose checksum changed is still marked as downloaded")
	}

	// The same content, from somewhere else, is another image too.
	server.set(original)
	updateList(t)
	if err := getImage(t, "1.37").Fetch(); err != nil {
		t.Fatal(err)
	}
	moved := original
	moved.path = "/mirror/kutti-1.37.qcow2"
	server.set(moved)
	updateList(t)
	if isDownloaded(t, "1.37") {
		t.Error("an image whose source URL changed is still marked as downloaded")
	}

	if _, err := os.Stat(cachedImagePath(root, "1.37")); err != nil {
		t.Error("the cached file was removed by an update")
	}
}
