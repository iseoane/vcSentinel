package setup

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

func TestReplaceWindowsBinary(t *testing.T) {
	dir := t.TempDir()
	currentBinary := filepath.Join(dir, "vcsentinel.exe")
	tmpPath := filepath.Join(dir, "new.exe")

	if err := os.WriteFile(currentBinary, []byte("version-old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpPath, []byte("version-new"), 0644); err != nil {
		t.Fatal(err)
	}

	backup, err := replaceWindowsBinary(tmpPath, currentBinary)
	if err != nil {
		t.Fatalf("replaceWindowsBinary returned error: %v", err)
	}
	if backup != currentBinary+".old" {
		t.Errorf("backup = %q, want %q", backup, currentBinary+".old")
	}

	content, err := os.ReadFile(currentBinary)
	if err != nil {
		t.Fatalf("could not read the current binary: %v", err)
	}
	if string(content) != "version-new" {
		t.Errorf("the current binary was not replaced, got %q", content)
	}

	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Errorf("the backup %s should have been cleaned up, err=%v", backup, err)
	}
}

func TestReplaceLinuxBinary(t *testing.T) {
	dir := t.TempDir()
	currentBinary := filepath.Join(dir, "vcsentinel")
	tmpPath := filepath.Join(dir, "new")

	if err := os.WriteFile(currentBinary, []byte("version-old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpPath, []byte("version-new"), 0755); err != nil {
		t.Fatal(err)
	}

	if err := replaceLinuxBinary(tmpPath, currentBinary); err != nil {
		t.Fatalf("replaceLinuxBinary returned error: %v", err)
	}

	content, err := os.ReadFile(currentBinary)
	if err != nil {
		t.Fatalf("could not read the current binary: %v", err)
	}
	if string(content) != "version-new" {
		t.Errorf("the binary was not replaced, got %q", content)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(currentBinary)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0755 {
			t.Errorf("expected permissions 0755, got %o", perm)
		}
	}
}

func TestReplaceLinuxBinaryCopyErrorLeavesCurrentBinaryIntact(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux replacement coverage is not exercised on Windows")
	}

	dir := t.TempDir()
	currentBinary := filepath.Join(dir, "vcsentinel")
	sourceDir := filepath.Join(dir, "source-dir")
	if err := os.Mkdir(sourceDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(currentBinary, []byte("version-old"), 0755); err != nil {
		t.Fatal(err)
	}

	err := replaceLinuxBinary(sourceDir, currentBinary)
	if err == nil {
		t.Fatal("expected staging a directory to fail")
	}

	content, readErr := os.ReadFile(currentBinary)
	if readErr != nil {
		t.Fatalf("could not read the original binary after staging failed: %v", readErr)
	}
	if string(content) != "version-old" {
		t.Fatalf("the original binary changed after staging failed: %q", content)
	}
}

func TestReplaceLinuxBinaryAcrossFilesystems(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cross-filesystem replacement coverage requires Linux")
	}

	sourceDir, err := os.MkdirTemp("/tmp", "vcsentinel-upgrade-source-*")
	if err != nil {
		t.Skipf("cannot create a source directory on /tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sourceDir) })

	destinationDir, err := os.MkdirTemp("/dev/shm", "vcsentinel-upgrade-destination-*")
	if err != nil {
		t.Skipf("cannot create a destination directory on /dev/shm: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(destinationDir) })

	sourceDevice, sourceOK := filesystemDevice(sourceDir)
	destinationDevice, destinationOK := filesystemDevice(destinationDir)
	if !sourceOK || !destinationOK {
		t.Skip("the platform does not expose filesystem device identifiers needed to prove distinct mounts")
	}
	if sourceDevice == destinationDevice {
		t.Skip("/tmp and /dev/shm are on the same filesystem; cross-device coverage is unavailable")
	}

	tmpPath := filepath.Join(sourceDir, "new")
	currentBinary := filepath.Join(destinationDir, "vcsentinel")
	if err := os.WriteFile(tmpPath, []byte("version-new"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(currentBinary, []byte("version-old"), 0755); err != nil {
		t.Fatal(err)
	}

	err = replaceLinuxBinary(tmpPath, currentBinary)
	if err != nil {
		if errors.Is(err, syscall.EXDEV) {
			t.Fatalf("replaceLinuxBinary returned EXDEV across filesystems: %v", err)
		}
		t.Fatalf("replaceLinuxBinary returned error: %v", err)
	}

	content, readErr := os.ReadFile(currentBinary)
	if readErr != nil {
		t.Fatalf("could not read the replaced binary: %v", readErr)
	}
	if string(content) != "version-new" {
		t.Fatalf("the binary was not replaced across filesystems: %q", content)
	}

	info, statErr := os.Stat(currentBinary)
	if statErr != nil {
		t.Fatalf("could not stat the replaced binary: %v", statErr)
	}
	if perm := info.Mode().Perm(); perm != 0755 {
		t.Errorf("expected permissions 0755, got %o", perm)
	}
}

func TestReplaceWindowsBinaryWithoutBackup(t *testing.T) {
	dir := t.TempDir()
	currentBinary := filepath.Join(dir, "vcsentinel.exe")
	tmpPath := filepath.Join(dir, "new.exe")

	if err := os.WriteFile(tmpPath, []byte("version-new"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := replaceWindowsBinary(tmpPath, currentBinary)
	if err == nil {
		t.Fatalf("expected error when the current binary does not exist")
	}
	if !strings.Contains(err.Error(), "back up") {
		t.Errorf("the error must mention the backup, got: %v", err)
	}
}

func TestReplaceWindowsBinaryFailureRestoresBackup(t *testing.T) {
	dir := t.TempDir()
	currentBinary := filepath.Join(dir, "vcsentinel.exe")

	if err := os.WriteFile(currentBinary, []byte("version-old"), 0644); err != nil {
		t.Fatal(err)
	}

	// tmpPath does not exist: the second rename fails and the backup must be
	// restored.
	tmpPath := filepath.Join(dir, "nonexistent.exe")
	_, err := replaceWindowsBinary(tmpPath, currentBinary)
	if err == nil {
		t.Fatalf("expected error when installing the new version")
	}
	if !strings.Contains(err.Error(), "install") {
		t.Errorf("the error must mention the installation, got: %v", err)
	}

	content, err := os.ReadFile(currentBinary)
	if err != nil {
		t.Fatalf("the current binary should exist again: %v", err)
	}
	if string(content) != "version-old" {
		t.Errorf("the current binary was not restored, got %q", content)
	}
}

func TestReplaceLinuxBinaryReplaceError(t *testing.T) {
	dir := t.TempDir()
	currentBinary := filepath.Join(dir, "vcsentinel")
	tmpPath := filepath.Join(dir, "new")

	if err := os.Mkdir(currentBinary, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpPath, []byte("version-new"), 0755); err != nil {
		t.Fatal(err)
	}

	err := replaceLinuxBinary(tmpPath, currentBinary)
	if err == nil {
		t.Fatalf("expected error when replacing over a directory")
	}
	if !strings.Contains(err.Error(), "replace") {
		t.Errorf("the error must mention the replacement, got: %v", err)
	}
}

func TestVerifyBinary(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vcsentinel")
	if runtime.GOOS == "windows" {
		path += ".cmd"
		if err := os.WriteFile(path, []byte("@echo off\r\necho v1.2.3\r\n"), 0755); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho v1.2.3\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0755); err != nil {
			t.Fatal(err)
		}
	}

	if err := verifyBinary(path); err != nil {
		t.Fatalf("verifyBinary returned error: %v", err)
	}
}

func TestVerifyBinaryFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vcsentinel")
	if runtime.GOOS == "windows" {
		path += ".cmd"
		if err := os.WriteFile(path, []byte("@echo off\r\nexit /b 1\r\n"), 0755); err != nil {
			t.Fatal(err)
		}
	} else {
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0755); err != nil {
			t.Fatal(err)
		}
	}

	err := verifyBinary(path)
	if err == nil {
		t.Fatalf("expected error when the binary does not answer --version")
	}
	if !strings.Contains(err.Error(), "--version") {
		t.Errorf("the error must mention --version, got: %v", err)
	}
}

func TestLocateCurrentBinary(t *testing.T) {
	path, err := locateCurrentBinary()
	if err != nil {
		t.Fatalf("locateCurrentBinary returned error: %v", err)
	}
	if path == "" {
		t.Error("locateCurrentBinary returned an empty path")
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the returned path does not exist: %v", err)
	}
}

func TestRenderWindowsManualUpgradeCommand(t *testing.T) {
	tests := []struct {
		name        string
		installRoot string
		want        string
	}{
		{
			name:        "resolved install root",
			installRoot: `C:\Users\Alice\.vcsentinel\bin`,
			want:        `$env:GOBIN = 'C:\Users\Alice\.vcsentinel\bin'`,
		},
		{
			name:        "apostrophe in install root",
			installRoot: `C:\Users\O'Brien\.vcsentinel\bin`,
			want:        `$env:GOBIN = 'C:\Users\O''Brien\.vcsentinel\bin'`,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := renderWindowsManualUpgradeCommand(testCase.installRoot)
			if !strings.Contains(got, "$previousGOBIN = $env:GOBIN") {
				t.Fatalf("command does not save the caller's GOBIN: %q", got)
			}
			if !strings.Contains(got, testCase.want) {
				t.Fatalf("command = %q, want install-root assignment %q", got, testCase.want)
			}
			if !strings.Contains(got, "go install github.com/ISeoane-Quental/vcSentinel/cmd/vcsentinel@latest") {
				t.Fatalf("command = %q, want source-based go install", got)
			}
			if !strings.Contains(got, "finally") || !strings.Contains(got, "Remove-Item Env:GOBIN") || !strings.Contains(got, "$env:GOBIN = $previousGOBIN") {
				t.Fatalf("command = %q, want GOBIN restoration", got)
			}
		})
	}
}
