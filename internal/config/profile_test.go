package config

import (
	"testing"
)

func TestResolveProfileUsesSuppliedContractDefault(t *testing.T) {
	cfg := defaultConfig()

	profile := ResolveProfile(cfg, "normal", "")
	if profile.Name != "normal" {
		t.Errorf("logic -> profile %q, expected normal", profile.Name)
	}

	profile = ResolveProfile(cfg, "deep", "")
	if profile.Name != "deep" {
		t.Errorf("security -> profile %q, expected deep", profile.Name)
	}

	profile = ResolveProfile(cfg, "cheap", "")
	if profile.Name != "cheap" {
		t.Errorf("spec -> %+v, expected cheap", profile)
	}
}

func TestResolveProfileV2AgentDotProfile(t *testing.T) {
	cfg := defaultConfig()

	profile := ResolveProfile(cfg, "logic", "opencode.cheap")
	if profile.Binary != "opencode" || profile.Model != "deepseek-v4-flash-free" || profile.Effort != "default" {
		t.Errorf("opencode.cheap = %+v, expected opencode/deepseek-v4-flash-free/default", profile)
	}

	profile = ResolveProfile(cfg, "logic", "claude.deep")
	if profile.Binary != "claude" || profile.Model != "claude-opus" {
		t.Errorf("claude.deep = %+v, expected claude/claude-opus", profile)
	}
}

func TestResolveProfileV2InheritsFromAgent(t *testing.T) {
	cfg := defaultConfig()
	// Nested profile without its own model: inherits the agent's.
	cfg.Agents["opencode"].Profiles["extra"] = ProfileConfig{ReasoningEffort: "medium"}

	profile := ResolveProfile(cfg, "logic", "opencode.extra")
	if profile.Model != "deepseek-v4-flash-free" || profile.Effort != "medium" {
		t.Errorf("opencode.extra = %+v, expected to inherit the agent's model + medium", profile)
	}
}

// TestResolveProfileDotWithoutAgentDoesNotSplit verifies that a profile name
// with a dot that does NOT correspond to a configured agent (e.g. "gpt-4.1")
// is not split as "agent.profile": splitting it would leave Binary pointing
// at a nonexistsnt binary and lose the model/effort inheritance. It must fall
// back to the v1 path.
func TestResolveProfileDotWithoutAgentDoesNotSplit(t *testing.T) {
	cfg := defaultConfig()

	profile := ResolveProfile(cfg, "logic", "gpt-4.1")
	if profile.Binary == "gpt-4" {
		t.Errorf("profile = %+v: 'gpt-4' is not a configured agent, it must not be used as a binary", profile)
	}
	if profile.Name != "gpt-4.1" {
		t.Errorf("Name = %q, expected the full name gpt-4.1", profile.Name)
	}
}

// TestResolveProfileDotWithoutAgentInheritsFromActive verifies that, besides
// not being split, the unknown dotted profile inherits from the active agent
// like any other v1 profile: it must never be left without a model or an
// effort.
func TestResolveProfileDotWithoutAgentInheritsFromActive(t *testing.T) {
	cfg := defaultConfig()
	cfg.ActiveAgent = "claude"

	profile := ResolveProfile(cfg, "logic", "gpt-4.1")
	if profile.Binary != "claude" {
		t.Errorf("Binary = %q, expected the active agent claude", profile.Binary)
	}
	if profile.Model == "" || profile.Effort == "" {
		t.Errorf("profile = %+v, expected to inherit model and effort from the active agent", profile)
	}
}

// TestResolveProfileV2StillSplitsWithConfiguredAgent hardens the v2 path
// against the previous fix: a dotted name whose prefix IS a configured agent
// must still be split.
func TestResolveProfileV2StillSplitsWithConfiguredAgent(t *testing.T) {
	cfg := defaultConfig()

	profile := ResolveProfile(cfg, "logic", "claude.cheap")
	if profile.Binary != "claude" {
		t.Errorf("Binary = %q, expected claude", profile.Binary)
	}
}

func TestResolveProfileWithOverride(t *testing.T) {
	cfg := defaultConfig()

	profile := ResolveProfile(cfg, "spec", "deep")
	if profile.Name != "deep" {
		t.Errorf("override deep over spec -> %q, expected deep", profile.Name)
	}
}

func TestResolveProfileUsesExplicitProviderProfile(t *testing.T) {
	cfg := defaultConfig()
	cfg.Agents["opencode"].Profiles["cheap"] = ProfileConfig{Model: "mini"}

	profile := ResolveProfile(cfg, "opencode.cheap", "")
	if profile.Name != "opencode.cheap" || profile.Binary != "opencode" || profile.Model != "mini" {
		t.Errorf("profile = %+v, expected opencode.cheap with opencode/mini", profile)
	}
}

func TestResolveProfileUsesNormalWhenRequested(t *testing.T) {
	cfg := defaultConfig()

	profile := ResolveProfile(cfg, "normal", "")
	if profile.Name != "normal" {
		t.Errorf("default profile -> %q, expected normal", profile.Name)
	}
}

// TestResolveAgentProfile verifies the nested-profile helper of a specific
// agent: a defined profile, a profile without a model (inherits from the
// agent) and a nonexistsnt profile (inherits everything from the agent).
func TestResolveAgentProfile(t *testing.T) {
	cfg := defaultConfig()

	t.Run("defined nested profile", func(t *testing.T) {
		model, effort := ResolveAgentProfile(cfg, "opencode", "cheap")
		if model != "deepseek-v4-flash-free" || effort != "default" {
			t.Errorf("opencode.cheap = %s/%s, expected deepseek-v4-flash-free/default", model, effort)
		}
	})

	t.Run("profile without model inherits from the agent", func(t *testing.T) {
		cfg.Agents["opencode"].Profiles["extra"] = ProfileConfig{ReasoningEffort: "medium"}
		model, effort := ResolveAgentProfile(cfg, "opencode", "extra")
		if model != "deepseek-v4-flash-free" || effort != "medium" {
			t.Errorf("opencode.extra = %s/%s, expected to inherit the agent's model + medium", model, effort)
		}
	})

	t.Run("nonexistsnt profile inherits everything from the agent", func(t *testing.T) {
		model, effort := ResolveAgentProfile(cfg, "claude", "does-not-exist")
		if model != "claude-5-sonnet" || effort != "high" {
			t.Errorf("claude.does-not-exist = %s/%s, expected to inherit everything from the agent", model, effort)
		}
	})
}
