package agentadapter

import (
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// TestPromptFixesDefaultLanguage covers T0.13: without stating the language,
// the model chose at random and the T0.1 fragmentation came out with 11
// messages in English and 2 in Spanish within the same run.
func TestPromptFixesDefaultLanguage(t *testing.T) {
	prompt := buildAgentPrompt("backend", 1, []string{"a.go"}, "")

	if !strings.Contains(prompt, "English") {
		t.Errorf("the prompt does not fix the language: %s", prompt)
	}
	if !strings.Contains(prompt, exampleByLanguage[DefaultLanguage]) {
		t.Errorf("the prompt does not carry the language's example: %s", prompt)
	}
}

// TestPromptRespectsConfiguredLanguage: not every repository writes in
// Spanish, so the language is configurable.
func TestPromptRespectsConfiguredLanguage(t *testing.T) {
	prompt := buildAgentPrompt("backend", 1, []string{"a.go"}, "en")

	if !strings.Contains(prompt, "English") {
		t.Errorf("the prompt does not fix English: %s", prompt)
	}
	if strings.Contains(prompt, "castellano") {
		t.Errorf("the prompt mixes languages: %s", prompt)
	}
	if !strings.Contains(prompt, exampleByLanguage["en"]) {
		t.Errorf("the prompt does not carry the English example: %s", prompt)
	}
}

// TestUnknownLanguageFallsBackToDefault: an unrecognized value in the yml must
// degrade to the default behavior, never leave the prompt without a language.
func TestUnknownLanguageFallsBackToDefault(t *testing.T) {
	prompt := buildAgentPrompt("backend", 1, []string{"a.go"}, "klingon")

	if !strings.Contains(prompt, instructionByLanguage[DefaultLanguage]) {
		t.Errorf("an unknown language must fall back to the default: %s", prompt)
	}
}

// TestPromptPreservesContract: fixing the language cannot break what the
// prompt already demanded — Conventional Commits and a single line without
// markdown.
func TestPromptPreservesContract(t *testing.T) {
	prompt := buildAgentPrompt("config", 3, []string{"a.yml", "b.yml"}, "")

	for _, want := range []string{"Conventional Commits", "ÚNICAMENTE", "a.yml", "b.yml", "config", "#3"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt lost %q: %s", want, prompt)
		}
	}
}

// TestConfigDefaultMatchesAgentadapterDefault ties the two values the import
// cycle forces to duplicate: config cannot reference
// agentadapter.DefaultLanguage because agentadapter already imports config.
func TestConfigDefaultMatchesAgentadapterDefault(t *testing.T) {
	cfg := config.LoadLocalConfig(t.TempDir())
	if cfg.CommitLanguage != DefaultLanguage {
		t.Errorf("config.CommitLanguage = %q, agentadapter.DefaultLanguage = %q: they must match",
			cfg.CommitLanguage, DefaultLanguage)
	}
}

// TestFactoryPropagatesLanguageToChain: the yml's language must reach every
// adapter, not stay in the configuration.
func TestFactoryPropagatesLanguageToChain(t *testing.T) {
	cfg := config.LoadLocalConfig(t.TempDir())
	cfg.CommitLanguage = "en"

	chain, err := buildChain(cfg, []string{"claude", "opencode"}, "")
	if err != nil {
		t.Fatalf("buildChain returned an error: %v", err)
	}
	if len(chain.adapters) != 2 {
		t.Fatalf("adapters = %d, want 2", len(chain.adapters))
	}
	for _, adapter := range chain.adapters {
		cli, ok := adapter.(*CLIAdapter)
		if !ok {
			t.Fatalf("unexpected adapter: %T", adapter)
		}
		if cli.commitLanguage() != "en" {
			t.Errorf("%s: language = %q, want en", cli.BinaryName, cli.commitLanguage())
		}
	}
}

// TestCLIAdapterUsesItsLanguage: the adapter propagates the configured
// language to the prompt instead of recomputing it or ignoring it.
func TestCLIAdapterUsesItsLanguage(t *testing.T) {
	adapter := &CLIAdapter{BinaryName: "claude", CommitLanguage: "en"}
	if adapter.commitLanguage() != "en" {
		t.Errorf("commitLanguage = %q, want en", adapter.commitLanguage())
	}

	withoutLanguage := &CLIAdapter{BinaryName: "claude"}
	if withoutLanguage.commitLanguage() != DefaultLanguage {
		t.Errorf("commitLanguage = %q, want %q", withoutLanguage.commitLanguage(), DefaultLanguage)
	}
}
