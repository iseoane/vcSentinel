package config

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func setHome(t *testing.T, home string) {
	t.Helper()
	// os.UserHomeDir() uses HOME on Unix and USERPROFILE on Windows.
	keys := []string{"HOME"}
	if runtime.GOOS == "windows" {
		keys = append(keys, "USERPROFILE")
	}
	originals := make(map[string]string, len(keys))
	for _, key := range keys {
		originals[key] = os.Getenv(key)
	}
	for _, key := range keys {
		if err := os.Setenv(key, home); err != nil {
			t.Fatalf("could not set %s: %v", key, err)
		}
	}
	t.Cleanup(func() {
		for key, value := range originals {
			os.Setenv(key, value)
		}
	})
}

func writeConfig(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("could not create directory %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("could not write %s: %v", path, err)
	}
}

// TestConfigPrecedence verifies: defaults -> global -> per-project wins.
func TestConfigPrecedence(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	t.Run("defaults only without files", func(t *testing.T) {
		cfg := LoadLocalConfig(worktree)
		if cfg.ActiveAgent != "auto" {
			t.Errorf("expected ActiveAgent 'auto', got %q", cfg.ActiveAgent)
		}
		if cfg.Agents["claude"].Model == "" || cfg.Agents["opencode"].Model == "" {
			t.Errorf("incomplete agent defaults: %+v", cfg.Agents)
		}
	})

	t.Run("global applies when there is no per-project", func(t *testing.T) {
		writeConfig(t, filepath.Join(home, ".vas_sentinel", "vassentinel.yml"),
			"active_agent: \"claude\"\n")
		cfg := LoadLocalConfig(worktree)
		if cfg.ActiveAgent != "claude" {
			t.Errorf("expected ActiveAgent 'claude' from global, got %q", cfg.ActiveAgent)
		}
	})

	t.Run("per-project wins over global", func(t *testing.T) {
		writeConfig(t, filepath.Join(home, ".vas_sentinel", "vassentinel.yml"),
			"active_agent: \"claude\"\n")
		writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
			"active_agent: \"opencode\"\n")
		cfg := LoadLocalConfig(worktree)
		if cfg.ActiveAgent != "opencode" {
			t.Errorf("expected ActiveAgent 'opencode' from per-project, got %q", cfg.ActiveAgent)
		}
	})

	t.Run("per-project overwrites only the fields it defines", func(t *testing.T) {
		writeConfig(t, filepath.Join(home, ".vas_sentinel", "vassentinel.yml"),
			"agents:\n  claude:\n    model: \"claude-global\"\n    reasoning_effort: \"low\"\n")
		writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
			"agents:\n  claude:\n    model: \"claude-local\"\n")
		cfg := LoadLocalConfig(worktree)
		if cfg.Agents["claude"].Model != "claude-local" {
			t.Errorf("expected per-project Model 'claude-local', got %q", cfg.Agents["claude"].Model)
		}
		if cfg.Agents["claude"].ReasoningEffort != "low" {
			t.Errorf("ReasoningEffort should be inherited from global 'low', got %q", cfg.Agents["claude"].ReasoningEffort)
		}
	})
}

func TestRepositoryRequestsExternalAgentDiff(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	cfg := LoadLocalConfig(worktree)
	if cfg.RequestExternalAgentDiff {
		t.Fatal("request_external_agent_diff must be false by default")
	}

	writeConfig(t, filepath.Join(home, ".vas_sentinel", "vassentinel.yml"),
		"request_external_agent_diff: true\n")
	if RepositoryRequestsExternalAgentDiff(worktree) {
		t.Fatal("global config must not request diffs from the repository")
	}
	writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
		"request_external_agent_diff: true\n")
	cfg = LoadLocalConfig(worktree)
	if !cfg.RequestExternalAgentDiff || !RepositoryRequestsExternalAgentDiff(worktree) {
		t.Fatal("request_external_agent_diff: true was not applied as a repository request")
	}
}

func TestCodeGraphReviewerContextIsOptIn(t *testing.T) {
	worktree := t.TempDir()
	if LoadLocalConfig(worktree).Review.CodeGraphContext {
		t.Fatal("CodeGraph reviewer context must be disabled by default")
	}
	writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), "review:\n  codegraph_context: true\n")
	if !LoadLocalConfig(worktree).Review.CodeGraphContext {
		t.Fatal("review.codegraph_context was not applied")
	}
}

// TestConfigChangeClasses verifies change.classes: without user config the
// defaults apply, and when the user declares it, it replaces the defaults
// preserving the textual order (the classifier's precedence).
func TestConfigChangeClasses(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	t.Run("without user config the defaults apply", func(t *testing.T) {
		cfg := LoadLocalConfig(worktree)
		if len(cfg.Change) == 0 {
			t.Fatal("Change should hold the defaults, it is empty")
		}
	})

	t.Run("user change.classes replaces the defaults preserving order", func(t *testing.T) {
		writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
			"change:\n  classes:\n    infra: [\"**/*.tf\"]\n    docs: [\"**/*.md\"]\n")
		cfg := LoadLocalConfig(worktree)
		if len(cfg.Change) != 2 {
			t.Fatalf("expected 2 user Change rules, got %d", len(cfg.Change))
		}
		if cfg.Change[0].Class != "infra" || cfg.Change[1].Class != "docs" {
			t.Errorf("declaration order not preserved: %+v", cfg.Change)
		}
	})

	// An absent change.classes and an explicitly empty change.classes ("{}")
	// are two distinct user intents: before, both fell into the same "no
	// changes" because the code only looked at len(classes) == 0 (finding of
	// the T3.1 review, internal/config/parser.go:598).
	t.Run("explicit empty change.classes empties the rules, unlike omitting it", func(t *testing.T) {
		writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), "change:\n  classes: {}\n")
		cfg := LoadLocalConfig(worktree)
		if cfg.Change == nil || len(cfg.Change) != 0 {
			t.Fatalf("expected Change empty-but-declared (not nil), got %#v", cfg.Change)
		}
	})

	// The T3.1 fix review pointed out that the nil/empty distinction was only
	// exercised at the per-project layer: confirm that the GLOBAL layer, which
	// goes through the same applyFromPath/applyClassOrder, behaves the same.
	t.Run("the nil/empty distinction also applies at the global layer", func(t *testing.T) {
		home2 := t.TempDir()
		worktree2 := t.TempDir()
		setHome(t, home2)
		writeConfig(t, filepath.Join(home2, ".vas_sentinel", "vassentinel.yml"), "change:\n  classes: {}\n")

		cfg := LoadLocalConfig(worktree2)
		if cfg.Change == nil || len(cfg.Change) != 0 {
			t.Fatalf("expected Change empty-but-declared (not nil) from global config, got %#v", cfg.Change)
		}
	})
}

// TestConfigRoutes verifies the exact paths where each level is searched.
func TestConfigRoutes(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	globalPath, err := globalConfigPath()
	if err != nil {
		t.Fatalf("globalConfigPath failed: %v", err)
	}
	expectedGlobal := filepath.Join(home, ".vas_sentinel", "vassentinel.yml")
	if globalPath != expectedGlobal {
		t.Errorf("expected global path %q, got %q", expectedGlobal, globalPath)
	}

	localPath := perProjectConfigPath(worktree)
	expectedLocal := filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml")
	if localPath != expectedLocal {
		t.Errorf("expected per-project path %q, got %q", expectedLocal, localPath)
	}
}
