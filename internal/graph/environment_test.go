package graph

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeExecutableFixture writes an executable fixture file and returns its path.
func writeExecutableFixture(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEnvironmentCodeGraphIncludesShebangInterpreterDir(t *testing.T) {
	toolDir := t.TempDir()
	interpDir := t.TempDir()
	interp := "vcsentinel-test-interp"
	writeExecutableFixture(t, interpDir, interp, "#!/bin/sh\necho interp-output\n")
	tool := writeExecutableFixture(t, toolDir, "tool", "#!/usr/bin/env "+interp+"\necho tool-output\n")

	t.Setenv("PATH", interpDir)
	var pathEntry string
	for _, entry := range codeGraphEnv(tool) {
		if rest, ok := strings.CutPrefix(entry, "PATH="); ok {
			pathEntry = rest
		}
	}
	if pathEntry == "" {
		t.Fatal("sanitized environment carries no PATH")
	}
	for _, want := range []string{toolDir, interpDir} {
		if !strings.Contains(pathEntry, want) {
			t.Errorf("child PATH = %q, want it to contain %q", pathEntry, want)
		}
	}
}

func TestEnvironmentCodeGraphNativeBinaryKeepsSingleDir(t *testing.T) {
	toolDir := t.TempDir()
	tool := writeExecutableFixture(t, toolDir, "tool", "not a script, no shebang\n")
	var pathEntry string
	for _, entry := range codeGraphEnv(tool) {
		if rest, ok := strings.CutPrefix(entry, "PATH="); ok {
			pathEntry = rest
		}
	}
	if want := "PATH=" + toolDir; "PATH="+pathEntry != want {
		t.Errorf("child PATH = %q, want exactly %q", pathEntry, want)
	}
}

func TestShebangToolRunsWithSanitizedEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shebang resolution needs /usr/bin/env and a POSIX shell")
	}
	toolDir := t.TempDir()
	interpDir := t.TempDir()
	interp := "vcsentinel-test-interp"
	writeExecutableFixture(t, interpDir, interp, "#!/bin/sh\necho interp-output\n")
	tool := writeExecutableFixture(t, toolDir, "tool", "#!/usr/bin/env "+interp+"\necho tool-output\n")

	t.Setenv("PATH", interpDir)
	out, err := runCodeGraph(t.Context(), tool, []string{"status"}, toolDir, codeGraphEnv(tool), "", 1<<20)
	if err != nil {
		t.Fatalf("shebang tool failed under the sanitized environment: %v", err)
	}
	if !strings.Contains(string(out), "interp-output") {
		t.Errorf("output = %q, want the interpreter to have run", out)
	}
}
