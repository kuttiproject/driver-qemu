package driverqemu_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	driverqemu "github.com/kuttiproject/driver-qemu"
	"github.com/kuttiproject/drivercore/drivercoretest"
	"github.com/kuttiproject/kuttilog"
	"github.com/kuttiproject/workspace"
)

const (
	TESTK8SVERSION = "1.35"
	imageSource    = "/home/rajch/projects/kuttiproject/driver-qemu-images/out/kutti-qemu/kutti-qemu.qcow2"
)

func TestDriverQemu(t *testing.T) {
	// Calculate the checksum dynamically to avoid hardcoding
	if _, err := os.Stat(imageSource); err != nil {
		t.Fatalf("Could not locate local QEMU image at %s: %v", imageSource, err)
	}

	t.Log("Calculating image checksum...")
	checksum, err := workspace.ChecksumFile(imageSource)
	if err != nil {
		t.Fatalf("Failed to calculate checksum: %v", err)
	}
	t.Logf("Checksum: %s", checksum)

	// Start local mock HTTP server
	serverMux := http.NewServeMux()
	server := http.Server{Addr: "localhost:8181", Handler: serverMux}
	defer server.Shutdown(context.Background())

	serverMux.HandleFunc(
		"/images.json",
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(
				w,
				`{"%v":{"ImageK8sVersion":"%v","ImageChecksum":"%v","ImageStatus":"NotDownloaded", "ImageSourceURL":"http://localhost:8181/kutti-%v.qcow2"}}`,
				TESTK8SVERSION,
				TESTK8SVERSION,
				checksum,
				TESTK8SVERSION,
			)
		},
	)

	serverMux.HandleFunc(
		fmt.Sprintf("/kutti-%v.qcow2", TESTK8SVERSION),
		func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, imageSource)
		},
	)

	go func() {
		t.Log("Server starting on http://localhost:8181...")
		err := server.ListenAndServe()
		if err != nil && err != http.ErrServerClosed {
			t.Logf("Server error: %v", err)
		}
		t.Log("Server stopped.")
	}()

	t.Log("Waiting 3 seconds for mock server to start...")
	time.Sleep(3 * time.Second)

	// Sandbox the kutti workspace folder inside a temporary "out" dir relative to test
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current working directory: %v", err)
	}
	outPath := filepath.Join(wd, "out")
	err = os.MkdirAll(outPath, 0777)
	if err != nil {
		t.Fatalf("Failed to create out dir: %v", err)
	}

	err = workspace.Set(outPath)
	if err != nil {
		t.Fatalf("Failed to set workspace directory: %v", err)
	}

	// Override the image list source to point to local mock server
	driverqemu.ImagesSourceURL = "http://localhost:8181/images.json"

	t.Log("Starting drivercoretest suite...")
	kuttilog.SetLogLevel(kuttilog.Debug)
	drivercoretest.TestDriver(t, "qemu", TESTK8SVERSION)
}
