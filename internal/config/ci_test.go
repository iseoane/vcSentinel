package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCIConfigDefaultsAndStrictValues(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	cfg, err := LoadStrictLocalConfig(worktree)
	if err != nil {
		t.Fatalf("default config: %v", err)
	}
	if cfg.CI.Workflow != "" || cfg.CI.WaitSeconds != 900 || cfg.CI.PollSeconds != 15 {
		t.Fatalf("defaults = %+v, want empty workflow and 900/15 seconds", cfg.CI)
	}

	writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `ci:
  workflow: verify.yml
  wait_seconds: 42
  poll_seconds: 7
`)
	cfg, err = LoadStrictLocalConfig(worktree)
	if err != nil {
		t.Fatalf("valid CI config: %v", err)
	}
	if cfg.CI.Workflow != "verify.yml" || cfg.CI.WaitSeconds != 42 || cfg.CI.PollSeconds != 7 {
		t.Fatalf("parsed CI config = %+v", cfg.CI)
	}
}

func TestCIConfigRejectsInvalidValuesAndUnknownKeys(t *testing.T) {
	cases := []struct {
		name string
		yml  string
		want string
	}{
		{"zero wait", "ci:\n  workflow: verify.yml\n  wait_seconds: 0\n", "ci.wait_seconds"},
		{"negative poll", "ci:\n  workflow: verify.yml\n  poll_seconds: -1\n", "ci.poll_seconds"},
		{"text wait", "ci:\n  workflow: verify.yml\n  wait_seconds: soon\n", "ci.wait_seconds"},
		{"unknown key", "ci:\n  workflow: verify.yml\n  dispatch: true\n", "field dispatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			worktree := t.TempDir()
			setHome(t, home)
			writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), tc.yml)
			_, err := LoadStrictLocalConfig(worktree)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want text %q", err, tc.want)
			}
		})
	}
}

func TestCIConfigWithEmptyWorkflowRemainsDisabled(t *testing.T) {
	worktree := t.TempDir()
	writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `ci:
  workflow: ""
  wait_seconds: 2
  poll_seconds: 1
`)
	cfg, err := LoadStrictLocalConfig(worktree)
	if err != nil {
		t.Fatalf("empty workflow should be valid: %v", err)
	}
	if cfg.CI.Workflow != "" || cfg.CI.WaitSeconds != 2 || cfg.CI.PollSeconds != 1 {
		t.Fatalf("empty-workflow config = %+v", cfg.CI)
	}
}
