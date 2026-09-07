package setup

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReplaceWindowsBinary(t *testing.T) {
	dir := t.TempDir()
	currentBinary := filepath.Join(dir, "sentinel.exe")
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
	currentBinary := filepath.Join(dir, "sentinel")
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

func TestReplaceWindowsBinaryWithoutBackup(t *testing.T) {
	dir := t.TempDir()
	currentBinary := filepath.Join(dir, "sentinel.exe")
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
	currentBinary := filepath.Join(dir, "sentinel.exe")

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
	currentBinary := filepath.Join(dir, "sentinel")
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
	path := filepath.Join(dir, "sentinel")
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
	path := filepath.Join(dir, "sentinel")
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
