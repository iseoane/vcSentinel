package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadStrictLocalConfig_UnknownKey_ReturnsErrorWithLine covers
// the requirement added by the orchestrator to T1.7 (outside the ticket's
// original text): an unknown key in the per-project yml is no longer silently
// discarded the way LoadLocalConfig does, and the error includes the
// line where it occurs (T1.1).
func TestLoadStrictLocalConfig_UnknownKey_ReturnsErrorWithLine(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
		"active_agent: \"claude\"\nkey_inexistsnte: true\n")

	_, err := LoadStrictLocalConfig(worktree)
	if err == nil {
		t.Fatal("expected an error for the unknown key, got nil")
	}
	if !strings.Contains(err.Error(), "line") {
		t.Errorf("the error must include the yml line, got: %v", err)
	}
}

// TestLoadStrictLocalConfig_ValidConfig_ChangesNothing verifies
// that, without errors in the yml, the result is identical to
// LoadLocalConfig's.
func TestLoadStrictLocalConfig_ValidConfig_ChangesNothing(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
		"active_agent: \"opencode\"\n")

	cfg, err := LoadStrictLocalConfig(worktree)
	if err != nil {
		t.Fatalf("did not expect an error: %v", err)
	}
	if cfg.ActiveAgent != "opencode" {
		t.Errorf("expected ActiveAgent 'opencode', got %q", cfg.ActiveAgent)
	}
}
