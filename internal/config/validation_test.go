package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestValidationCapabilitiesInvalidShape verifies the three shape validations
// of validation.capabilities/profiles that must fail at load time with an
// explicit error (never with a silent default): a profile referencing a
// nonexistsnt capability, supports_scope without scoped_command, and
// scoped_command without the {packages} marker.
func TestValidationCapabilitiesInvalidShape(t *testing.T) {
	cases := []struct {
		name     string
		yml      string
		contains string
	}{
		{
			name: "profile references a nonexistsnt capability",
			yml: `
validation:
  capabilities:
    format:
      command: "gofmt -l ."
  profiles:
    fast: [format, lint]
`,
			contains: "lint",
		},
		{
			name: "supports_scope without scoped_command",
			yml: `
validation:
  capabilities:
    lint:
      command: "go vet ./..."
      supports_scope: true
`,
			contains: "scoped_command",
		},
		{
			name: "scoped_command without the {packages} marker",
			yml: `
validation:
  capabilities:
    lint:
      command: "go vet ./..."
      supports_scope: true
      scoped_command: "go vet ./internal/..."
`,
			contains: "{packages}",
		},
		{
			// fails_when has a closed domain (exit_code/output_not_empty): a
			// typo like "exit-cede" must fail at load time, not silently
			// degrade into a different failure criterion at runtime.
			name: "fails_when with a value outside the closed domain",
			yml: `
validation:
  capabilities:
    lint:
      command: "go vet ./..."
      fails_when: "exit-cede"
`,
			contains: "lint",
		},
		{
			// mode also has a closed domain (worktree/inplace): a typo like
			// "worktre" cannot be accepted silently.
			name: "mode with a value outside the closed domain",
			yml: `
validation:
  mode: "worktre"
`,
			contains: "worktre",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			worktree := t.TempDir()
			path := filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml")
			writeConfig(t, path, c.yml)

			cfg := defaultConfig()
			err := applyFromPath(&cfg, path)
			if err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), c.contains) {
				t.Errorf("error = %v, expected it to contain %q", err, c.contains)
			}
		})
	}
}

// TestValidationFailsWhenInvalidNamesCapabilityAndValue covers the
// orchestrator's finding: fails_when was not validated against its closed
// domain (FailsWhenExitCode/FailsWhenOutputNotEmpty), so a typo was accepted
// silently. The error must name both the capability and the received value
// explicitly, not just one of the two.
func TestValidationFailsWhenInvalidNamesCapabilityAndValue(t *testing.T) {
	worktree := t.TempDir()
	path := filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml")
	writeConfig(t, path, `
validation:
  capabilities:
    lint:
      command: "go vet ./..."
      fails_when: "exit-cede"
`)

	cfg := defaultConfig()
	err := applyFromPath(&cfg, path)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "lint") {
		t.Errorf("error = %v, expected it to name the capability 'lint'", err)
	}
	if !strings.Contains(err.Error(), "exit-cede") {
		t.Errorf("error = %v, expected it to name the invalid value 'exit-cede'", err)
	}
}

// TestValidationModeInvalidNamesValue covers the same finding for mode: the
// error must name the received value, without silently degrading to the
// default.
func TestValidationModeInvalidNamesValue(t *testing.T) {
	worktree := t.TempDir()
	path := filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml")
	writeConfig(t, path, `
validation:
  mode: "worktre"
`)

	cfg := defaultConfig()
	err := applyFromPath(&cfg, path)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if !strings.Contains(err.Error(), "worktre") {
		t.Errorf("error = %v, expected it to name the invalid value 'worktre'", err)
	}
}

// TestImplicitCapabilitiesFromLegacyCommands verifies that a config without a
// validation section, with only lint_commands/test_commands/build_commands
// (the schema before T1.2), still produces usable capabilities: the implicit
// translation is what lets a future consumer of "the configured
// capabilities" not depend on the user rewriting their yml.
func TestImplicitCapabilitiesFromLegacyCommands(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `
lint_commands:
  - "gofmt -l ."
  - "go vet ./..."
test_commands:
  - "go test ./..."
build_commands:
  - "go build ./..."
`)

	cfg := LoadLocalConfig(worktree)

	lint, ok := cfg.Validation.Capabilities["lint"]
	if !ok {
		t.Fatalf("Validation.Capabilities = %+v, expected the implicit capability 'lint'", cfg.Validation.Capabilities)
	}
	if lint.FailsWhen != FailsWhenExitCode {
		t.Errorf("lint.FailsWhen = %q, expected %q", lint.FailsWhen, FailsWhenExitCode)
	}
	if !strings.Contains(lint.Command, "gofmt -l .") || !strings.Contains(lint.Command, "go vet ./...") {
		t.Errorf("lint.Command = %q, expected it to include both lint_commands", lint.Command)
	}

	unitTest, ok := cfg.Validation.Capabilities["unit_test"]
	if !ok || unitTest.Command != "go test ./..." {
		t.Errorf("Validation.Capabilities[unit_test] = %+v, expected go test ./...", unitTest)
	}

	build, ok := cfg.Validation.Capabilities["build"]
	if !ok || build.Command != "go build ./..." {
		t.Errorf("Validation.Capabilities[build] = %+v, expected go build ./...", build)
	}
}

// TestCompleteValidationParses verifies that a complete validation section
// (capabilities with their four fields, profiles and mode) parses and lands
// accessible in the business struct. Compared to the example in the ticket,
// the build/static_analysis/security capabilities are added (with a minimal
// command) because the "full" profile references them and the shape
// validation requires every capability named in a profile to be declared.
func TestCompleteValidationParses(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	setHome(t, home)

	writeConfig(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), `
validation:
  capabilities:
    format:
      command: "gofmt -l ."
      fails_when: output_not_empty
    lint:
      command: "go vet ./..."
      supports_scope: true
      scoped_command: "go vet {packages}"
    unit_test:
      command: "go test ./..."
      supports_scope: true
      scoped_command: "go test {packages}"
      timeout: 300
    build:
      command: "go build ./..."
    static_analysis:
      command: "staticcheck ./..."
    security:
      command: "gosec ./..."
  profiles:
    fast: [format, lint]
    standard: [format, lint, build, unit_test]
    full: [format, lint, build, unit_test, static_analysis, security]
  mode: worktree
`)

	cfg := LoadLocalConfig(worktree)

	if cfg.Validation.Mode != ModeWorktree {
		t.Errorf("Validation.Mode = %q, expected %q", cfg.Validation.Mode, ModeWorktree)
	}
	format := cfg.Validation.Capabilities["format"]
	if format.Command != "gofmt -l ." || format.FailsWhen != FailsWhenOutputNotEmpty {
		t.Errorf("capability format = %+v, expected gofmt -l . / output_not_empty", format)
	}
	lint := cfg.Validation.Capabilities["lint"]
	if !lint.SupportsScope || lint.ScopedCommand != "go vet {packages}" {
		t.Errorf("capability lint = %+v, expected supports_scope true and scoped_command with {packages}", lint)
	}
	unitTest := cfg.Validation.Capabilities["unit_test"]
	if unitTest.Timeout != 300 {
		t.Errorf("capability unit_test.Timeout = %d, expected 300", unitTest.Timeout)
	}
	if len(cfg.Validation.Profiles["standard"]) != 4 {
		t.Errorf("profiles.standard = %+v, expected 4 capabilities", cfg.Validation.Profiles["standard"])
	}
	if len(cfg.Validation.Profiles["full"]) != 6 {
		t.Errorf("profiles.full = %+v, expected 6 capabilities", cfg.Validation.Profiles["full"])
	}
}
