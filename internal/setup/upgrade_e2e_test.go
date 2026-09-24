package setup

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

const (
	upgradeE2EInvalidHelperEnv = "VCSENTINEL_UPGRADE_E2E_INVALID_HELPER"
	upgradeE2EHelperEnv        = "VCSENTINEL_UPGRADE_E2E_HELPER"
	upgradeE2ETempDirEnv       = "VCSENTINEL_UPGRADE_E2E_TEMP_DIR"
	upgradeE2EDestinationEnv   = "VCSENTINEL_UPGRADE_E2E_DESTINATION_DIR"
	upgradeE2EVersion          = "vcSentinel version: e2e-upgraded"
	upgradeE2EReleaseTag       = "v9.9.9-e2e"
	upgradeE2EAssetURLPath     = "/assets/vcsentinel"
	upgradeE2ESourceDirectory  = "/tmp"
	upgradeE2EDestDirectory    = "/dev/shm"
)

// TestUpgradeFromGitHubAcrossFilesystems runs the upgrade in a copied test
// process whose executable lives on /dev/shm while temporary downloads are
// forced onto /tmp. The copied executable is disposable and never represents
// the installed vcSentinel binary.
func TestUpgradeFromGitHubAcrossFilesystems(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cross-filesystem upgrade coverage requires Linux /tmp and /dev/shm mounts")
	}

	if _, err := os.Stat(upgradeE2ESourceDirectory); err != nil {
		t.Skipf("cross-filesystem upgrade coverage requires %s: %v", upgradeE2ESourceDirectory, err)
	}
	if _, err := os.Stat(upgradeE2EDestDirectory); err != nil {
		t.Skipf("cross-filesystem upgrade coverage requires %s: %v", upgradeE2EDestDirectory, err)
	}

	sourceDevice, sourceOK := filesystemDevice(upgradeE2ESourceDirectory)
	destinationDevice, destinationOK := filesystemDevice(upgradeE2EDestDirectory)
	if !sourceOK || !destinationOK {
		t.Skip("the platform does not expose filesystem device identifiers needed to prove distinct mounts")
	}
	if sourceDevice == destinationDevice {
		t.Skipf("%s and %s are on the same filesystem; cross-device coverage is unavailable", upgradeE2ESourceDirectory, upgradeE2EDestDirectory)
	}

	sourceDir, err := os.MkdirTemp(upgradeE2ESourceDirectory, "vcsentinel-upgrade-e2e-source-*")
	if err != nil {
		t.Skipf("cannot create a writable temporary source directory on %s: %v", upgradeE2ESourceDirectory, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sourceDir) })

	destinationDir, err := os.MkdirTemp(upgradeE2EDestDirectory, "vcsentinel-upgrade-e2e-destination-*")
	if err != nil {
		t.Skipf("cannot create a writable destination directory on %s: %v", upgradeE2EDestDirectory, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(destinationDir) })

	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("could not locate the test executable: %v", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatalf("could not resolve the test executable: %v", err)
	}
	original, err := os.ReadFile(executable)
	if err != nil {
		t.Fatalf("could not read the test executable: %v", err)
	}

	destination := filepath.Join(destinationDir, filepath.Base(executable))
	if err := os.WriteFile(destination, original, 0755); err != nil {
		t.Fatalf("could not copy the test executable to %s: %v", destination, err)
	}

	probe := exec.Command(destination, "-test.run=^$")
	probe.Env = append(os.Environ(), "TMPDIR="+sourceDir)
	if output, err := probe.CombinedOutput(); err != nil {
		t.Skipf("cannot execute a test binary from %s; cross-filesystem coverage is unavailable: %v\n%s", upgradeE2EDestDirectory, err, output)
	}

	cmd := exec.Command(destination, "-test.run=^TestUpgradeFromGitHubAcrossFilesystemsHelper$", "-test.v")
	cmd.Env = append(os.Environ(),
		upgradeE2EHelperEnv+"=1",
		upgradeE2ETempDirEnv+"="+sourceDir,
		upgradeE2EDestinationEnv+"="+destinationDir,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cross-filesystem upgrade helper failed: %v\n%s", err, output)
	}

	installed, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("could not read the replaced binary: %v", err)
	}
	if bytes.Equal(installed, original) {
		t.Fatal("the downloaded binary did not replace the old test executable")
	}
	if string(installed) != string(upgradeE2EBinary()) {
		t.Fatalf("the replaced binary does not contain the downloaded asset")
	}

	versionOutput, err := exec.Command(destination, "--version").Output()
	if err != nil {
		t.Fatalf("the replaced binary did not answer --version: %v", err)
	}
	if strings.TrimSpace(string(versionOutput)) != upgradeE2EVersion {
		t.Fatalf("--version output = %q, want %q", strings.TrimSpace(string(versionOutput)), upgradeE2EVersion)
	}

	assertDirectoryContainsOnly(t, sourceDir)
	assertDirectoryContainsOnly(t, destinationDir, filepath.Base(destination))
}

// TestUpgradeFromGitHubAcrossFilesystemsHelper is executed by the copied test
// binary from the destination filesystem. Running it in a child process makes
// os.Executable point at the disposable copy instead of the real test binary.
func TestUpgradeFromGitHubAcrossFilesystemsHelper(t *testing.T) {
	if os.Getenv(upgradeE2EHelperEnv) != "1" {
		t.Skip("helper test is only run by TestUpgradeFromGitHubAcrossFilesystems")
	}
	if runtime.GOOS != "linux" {
		t.Skip("cross-filesystem upgrade coverage requires Linux")
	}

	t.Setenv("TMPDIR", os.Getenv(upgradeE2ETempDirEnv))
	t.Setenv("GITHUB_TOKEN", "e2e-test-token")
	disableGoInstallFallback(t)

	currentBinary, err := locateCurrentBinary()
	if err != nil {
		t.Fatalf("could not locate the disposable current binary: %v", err)
	}
	destinationDir := os.Getenv(upgradeE2EDestinationEnv)
	if filepath.Clean(filepath.Dir(currentBinary)) != filepath.Clean(destinationDir) {
		t.Fatalf("current binary directory = %q, want %q", filepath.Dir(currentBinary), destinationDir)
	}
	if filepath.Clean(currentBinary) == filepath.Join("/usr/local/bin", goBinaryName()) {
		t.Fatal("the E2E must never target the real installed binary")
	}

	assetName := "vcsentinel-" + runtime.GOOS + "-" + runtime.GOARCH
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != upgradeE2EAssetURLPath+"/"+assetName {
			t.Errorf("asset path = %q, want %q", r.URL.Path, upgradeE2EAssetURLPath+"/"+assetName)
		}
		if got := r.Header.Get("Accept"); got != "application/octet-stream" {
			t.Errorf("asset Accept header = %q, want application/octet-stream", got)
		}
		if got := r.Header.Get("User-Agent"); got != "vcsentinel" {
			t.Errorf("asset User-Agent = %q, want vcsentinel", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer e2e-test-token" {
			t.Errorf("asset Authorization = %q, want Bearer e2e-test-token", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(upgradeE2EBinary())
	}))
	defer assetServer.Close()

	release, err := json.Marshal(ReleaseInfo{
		TagName: upgradeE2EReleaseTag,
		Assets: []ReleaseAsset{{
			Name:               assetName,
			BrowserDownloadURL: "https://unused.example.invalid/" + assetName,
			URL:                assetServer.URL + upgradeE2EAssetURLPath + "/" + assetName,
		}},
	})
	if err != nil {
		t.Fatalf("could not encode the fake release: %v", err)
	}

	previousClient := http.DefaultClient
	http.DefaultClient = &http.Client{Transport: &upgradeE2ETransport{
		releaseBody:    release,
		assetServerURL: assetServer.URL,
	}}
	t.Cleanup(func() { http.DefaultClient = previousClient })

	if err := RunUpgradeFromGitHub(); err != nil {
		t.Fatalf("RunUpgradeFromGitHub returned an error: %v", err)
	}

	installed, err := os.ReadFile(currentBinary)
	if err != nil {
		t.Fatalf("could not read the installed disposable binary: %v", err)
	}
	if string(installed) != string(upgradeE2EBinary()) {
		t.Fatalf("RunUpgradeFromGitHub did not install the downloaded asset")
	}

	versionOutput, err := exec.Command(currentBinary, "--version").Output()
	if err != nil {
		t.Fatalf("the installed disposable binary did not answer --version: %v", err)
	}
	if strings.TrimSpace(string(versionOutput)) != upgradeE2EVersion {
		t.Fatalf("installed --version output = %q, want %q", strings.TrimSpace(string(versionOutput)), upgradeE2EVersion)
	}

	assertDirectoryContainsOnly(t, os.Getenv(upgradeE2ETempDirEnv))
	assertDirectoryContainsOnly(t, destinationDir, filepath.Base(currentBinary))
}

type upgradeE2ETransport struct {
	releaseBody    []byte
	assetServerURL string
}

func (t *upgradeE2ETransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "api.github.com" && req.URL.Path == "/repos/"+repoOwner+"/"+repoName+"/releases/latest" {
		if got := req.Header.Get("Accept"); got != "application/vnd.github+json" {
			return nil, fmt.Errorf("release Accept header = %q, want application/vnd.github+json", got)
		}
		if got := req.Header.Get("User-Agent"); got != "vcsentinel" {
			return nil, fmt.Errorf("release User-Agent = %q, want vcsentinel", got)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer e2e-test-token" {
			return nil, fmt.Errorf("release Authorization = %q, want Bearer e2e-test-token", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(t.releaseBody)),
			Request:    req,
		}, nil
	}
	if strings.HasPrefix(req.URL.String(), t.assetServerURL+"/") {
		return http.DefaultTransport.RoundTrip(req)
	}
	return nil, fmt.Errorf("unexpected HTTP request to %s", req.URL)
}

func filesystemDevice(path string) (uint64, bool) {
	info, err := os.Stat(path)
	if err != nil || info.Sys() == nil {
		return 0, false
	}
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	field := value.FieldByName("Dev")
	if !field.IsValid() || !field.CanUint() {
		return 0, false
	}
	return field.Uint(), true
}

func upgradeE2EBinary() []byte {
	return []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then\n  printf '%s\\n' '" + upgradeE2EVersion + "'\n  exit 0\nfi\nexit 2\n")
}

func assertDirectoryContainsOnly(t *testing.T, directory string, expected ...string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("could not inspect %s: %v", directory, err)
	}
	if len(entries) != len(expected) {
		t.Fatalf("%s contains %d entries, want %d: %v", directory, len(entries), len(expected), entries)
	}
	for _, entry := range entries {
		found := false
		for _, name := range expected {
			if entry.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("unexpected artifact %s in %s", entry.Name(), directory)
		}
	}
}

func TestUpgradeRejectsInvalidDownloadedBinaryBeforeReplacement(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("could not locate the test executable: %v", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatalf("could not resolve the test executable: %v", err)
	}
	original, err := os.ReadFile(executable)
	if err != nil {
		t.Fatalf("could not read the test executable: %v", err)
	}

	tempRoot := t.TempDir()
	tempDir := filepath.Join(tempRoot, "tmp")
	homeDir := filepath.Join(tempRoot, "home")
	configDir := filepath.Join(homeDir, ".vcsentinel")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("could not create the disposable home: %v", err)
	}
	configPath := filepath.Join(configDir, "vcsentinel.yml")
	configBefore := []byte("version: \"test\"\n")
	if err := os.WriteFile(configPath, configBefore, 0644); err != nil {
		t.Fatalf("could not create the disposable configuration: %v", err)
	}
	destinationDir := filepath.Join(tempDir, "destination")
	if err := os.MkdirAll(destinationDir, 0755); err != nil {
		t.Fatalf("could not create the disposable destination: %v", err)
	}
	destination := filepath.Join(destinationDir, filepath.Base(executable))
	if err := os.WriteFile(destination, original, 0755); err != nil {
		t.Fatalf("could not copy the test executable: %v", err)
	}

	assetName := "vcsentinel-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		assetName += ".exe"
	}
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/release":
			body, err := json.Marshal(ReleaseInfo{
				TagName: "v-invalid-e2e",
				Assets: []ReleaseAsset{{
					Name: assetName,
					URL:  assetServerURL(r, "/assets/invalid"),
				}},
			})
			if err != nil {
				t.Errorf("could not encode the invalid release: %v", err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		case "/assets/invalid":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not-a-runnable-vcsentinel-binary"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer assetServer.Close()

	// The release endpoint is only used by the copied helper process. It is
	// intentionally loopback so this test cannot contact GitHub.
	releaseURL := assetServer.URL + "/release"
	cmd := exec.Command(destination, "-test.run=^TestUpgradeRejectsInvalidDownloadedBinaryHelper$", "-test.v")
	cmd.Env = append(os.Environ(),
		upgradeE2EInvalidHelperEnv+"=1",
		testReleaseAPIURLEnv+"="+releaseURL,
		testInstallRootEnv+"="+filepath.Join(tempDir, "install-root"),
		testDisableGoInstallEnv+"=1",
		"GITHUB_TOKEN=e2e-test-token",
		"TMPDIR="+tempDir,
		"TMP="+tempDir,
		"TEMP="+tempDir,
		"HOME="+homeDir,
		"USERPROFILE="+homeDir,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("invalid downloaded binary helper failed: %v\n%s", err, output)
	}

	installed, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("could not read the disposable binary after rejection: %v", err)
	}
	if !bytes.Equal(installed, original) {
		t.Fatal("invalid downloaded binary replaced the current binary")
	}
	configAfter, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("could not read the disposable configuration after rejection: %v", err)
	}
	if !bytes.Equal(configAfter, configBefore) {
		t.Fatal("invalid downloaded binary changed the configuration")
	}
	assertDirectoryContainsOnly(t, destinationDir, filepath.Base(destination))
}

func TestUpgradeRejectsInvalidDownloadedBinaryHelper(t *testing.T) {
	if os.Getenv(upgradeE2EInvalidHelperEnv) != "1" {
		t.Skip("helper test is only run by TestUpgradeRejectsInvalidDownloadedBinary")
	}

	currentBinary, err := locateCurrentBinary()
	if err != nil {
		t.Fatalf("could not locate the disposable current binary: %v", err)
	}
	before, err := os.ReadFile(currentBinary)
	if err != nil {
		t.Fatalf("could not read the current binary before upgrade: %v", err)
	}
	if err := RunUpgradeFromGitHub(); err == nil {
		t.Fatal("RunUpgradeFromGitHub accepted an invalid downloaded binary")
	}
	after, err := os.ReadFile(currentBinary)
	if err != nil {
		t.Fatalf("could not read the current binary after rejection: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("the current binary changed after staged validation failed")
	}
	assertDirectoryContainsOnly(t, filepath.Dir(currentBinary), filepath.Base(currentBinary))
}

func assetServerURL(request *http.Request, path string) string {
	return "http://" + request.Host + path
}
