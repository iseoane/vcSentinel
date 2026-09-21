package config

import (
	"path/filepath"
	"regexp"
	"testing"
)

// TestUnknownKeyFailsWithLine verifies that a key that does not belong to the
// schema (e.g. "comand" instead of "command") is not silently ignored: strict
// decoding (yaml.Decoder.KnownFields) must return an error that includes the
// exact line where the invalid key appears. For a guardian gate, a silent
// failure is the worst possible failure mode.
func TestUnknownKeyFailsWithLine(t *testing.T) {
	worktree := t.TempDir()
	path := filepath.Join(worktree, ".vcsentinel", "vcsentinel.yml")
	// The invented key "comand" is on line 4 (1-based, counting from the
	// first line of the file).
	writeConfig(t, path, `active_agent: "claude"
lint_commands:
  - "gofmt -l ."
comand: "typo"
`)

	cfg := defaultConfig()
	err := applyFromPath(&cfg, path)
	if err == nil {
		t.Fatal("expected an error for the unknown key 'comand', got nil")
	}

	linePattern := regexp.MustCompile(`line 4\b`)
	if !linePattern.MatchString(err.Error()) {
		t.Errorf("the error does not mention the expected line 4: %v", err)
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("test path badly built: %q", path)
	}
}
