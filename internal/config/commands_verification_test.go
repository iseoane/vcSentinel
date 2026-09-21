package config

import (
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestVerificationCommands verifies the parsing of test_commands and
// build_commands (sibling sections of lint_commands) with the same list
// semantics.
func TestVerificationCommands(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	writeConfig(t, filepath.Join(worktree, ".vcsentinel", "vcsentinel.yml"), `
test_commands:
  - "go test ./..."
  - "go test -race ./internal/review"
build_commands:
  - "go build ./..."
`)

	cfg := LoadLocalConfig(worktree)

	expectedTest := []string{"go test ./...", "go test -race ./internal/review"}
	if !reflect.DeepEqual(cfg.TestCommands, expectedTest) {
		t.Errorf("TestCommands = %+v, expected %+v", cfg.TestCommands, expectedTest)
	}
	expectedBuild := []string{"go build ./..."}
	if !reflect.DeepEqual(cfg.BuildCommands, expectedBuild) {
		t.Errorf("BuildCommands = %+v, expected %+v", cfg.BuildCommands, expectedBuild)
	}
	if len(cfg.LintCommands) != 0 {
		t.Errorf("LintCommands = %+v, expected empty (no lint configured)", cfg.LintCommands)
	}
}

// TestVerificationCommandsDefaults verifies that without configuration the
// verification lists are empty (nothing runs by default).
func TestVerificationCommandsDefaults(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	cfg := LoadLocalConfig(worktree)
	if len(cfg.TestCommands) != 0 || len(cfg.BuildCommands) != 0 {
		t.Errorf("verification defaults = test:%v build:%v, expected empty",
			cfg.TestCommands, cfg.BuildCommands)
	}
}

// yamlList serializes items as a list of YAML strings (with double quotes,
// like the rest of the fixtures).
func yamlList(items []string) string {
	var b strings.Builder
	for _, it := range items {
		b.WriteString("  - " + strconv.Quote(it) + "\n")
	}
	return b.String()
}

// checkCommandMerge verifies the global + per-project merge of a command key
// (lint/test/build): the result must contain exactly the expected commands,
// without duplicates or additions. Extracted to a helper because the three
// precedence tests share the same structure (ADVISORY from the dogfooding:
// the pattern must not be copied a fourth time in S4).
func checkCommandMerge(t *testing.T, key string, get func(Config) []string, global, local, expected []string) {
	t.Helper()
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	writeConfig(t, filepath.Join(home, ".vcsentinel", "vcsentinel.yml"),
		fmt.Sprintf("%s:\n%s", key, yamlList(global)))
	writeConfig(t, filepath.Join(worktree, ".vcsentinel", "vcsentinel.yml"),
		fmt.Sprintf("%s:\n%s", key, yamlList(local)))

	commands := get(LoadLocalConfig(worktree))
	if len(commands) != len(expected) {
		t.Fatalf("%s = %+v, expected exactly %d (global + per-project)", key, commands, len(expected))
	}
	for _, want := range expected {
		if !slices.Contains(commands, want) {
			t.Errorf("%s = %+v, missing %q", key, commands, want)
		}
	}
}

// TestVerificationCommandsPrecedence verifies the global + per-project merge:
// per-project commands accumulate onto the global ones (same semantics as
// lint_commands).
func TestVerificationCommandsPrecedence(t *testing.T) {
	checkCommandMerge(t, "test_commands",
		func(c Config) []string { return c.TestCommands },
		[]string{"go test ./..."},
		[]string{"go test -race ./internal/review"},
		[]string{"go test ./...", "go test -race ./internal/review"})
}

// TestVerificationCommandsPrecedenceBuild verifies the merge of
// build_commands (ADVISORY 4): per-project commands accumulate onto the
// global ones, just like test_commands and lint_commands.
func TestVerificationCommandsPrecedenceBuild(t *testing.T) {
	checkCommandMerge(t, "build_commands",
		func(c Config) []string { return c.BuildCommands },
		[]string{"go build ./..."},
		[]string{"go build -race ./cmd/vcsentinel"},
		[]string{"go build ./...", "go build -race ./cmd/vcsentinel"})
}
