package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestInjectRulesIdempotent: running init twice must NOT duplicate the
// rules block in the target files (regression of the bug where init appended
// unconditionally and duplicated the insertion).
func TestInjectRulesIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	baseContent := "# Project\n\n## Conventions\n"

	if err := os.WriteFile(path, []byte(baseContent), 0644); err != nil {
		t.Fatal(err)
	}

	// First injection: it must write the block.
	written, err := injectRulesIntoFile(path)
	if err != nil {
		t.Fatalf("first injection returned an error: %v", err)
	}
	if !written {
		t.Fatal("the first injection should write the block")
	}

	data, _ := os.ReadFile(path)
	if n := strings.Count(string(data), "## CRITICAL VOLUME RULE"); n != 1 {
		t.Fatalf("after the first injection there are %d blocks, expected 1", n)
	}

	// Second injection (repeated init): it must not duplicate.
	written, err = injectRulesIntoFile(path)
	if err != nil {
		t.Fatalf("second injection returned an error: %v", err)
	}
	if written {
		t.Error("the second injection should not modify the file (block already present)")
	}

	data, _ = os.ReadFile(path)
	if n := strings.Count(string(data), "## CRITICAL VOLUME RULE"); n != 1 {
		t.Fatalf("after the second injection there are %d blocks, expected 1 (idempotence broken)", n)
	}
	// The original content is preserved intact.
	if !strings.Contains(string(data), baseContent) {
		t.Error("the injection lost the file's original content")
	}
}

// TestInjectRulesCreatesFile: when the file does not exist, init creates it
// with the block (same behavior as before).
func TestInjectRulesCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "CLAUDE.md")

	written, err := injectRulesIntoFile(path)
	if err != nil {
		t.Fatalf("injection returned an error: %v", err)
	}
	if !written {
		t.Error("with a missing file the block should be written")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the file was not created: %v", err)
	}
	if !strings.Contains(string(data), volumeRules) {
		t.Error("the created file does not contain the rules block")
	}
}

// TestRemoveRulesRepairsDuplicates: uninit must remove ALL occurrences of
// the block, including the duplicates init left in previous versions.
func TestRemoveRulesRepairsDuplicates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".claudecode.md")
	duplicated := "# previous\n" + volumeRules + volumeRules + "end\n"
	if err := os.WriteFile(path, []byte(duplicated), 0644); err != nil {
		t.Fatal(err)
	}

	removed, err := removeRulesFromFile(path)
	if err != nil {
		t.Fatalf("removeRulesFromFile returned an error: %v", err)
	}
	if !removed {
		t.Fatal("something should have been removed (there were duplicates)")
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "## CRITICAL VOLUME RULE") {
		t.Errorf("rules blocks remain after uninit: %q", data)
	}
	if !strings.Contains(string(data), "# previous") || !strings.Contains(string(data), "end\n") {
		t.Errorf("uninit deleted foreign content: %q", data)
	}
}

func TestInstallVCSentinelSkillCreatesCanonicalFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".agents", "skills", "vcsentinel", "SKILL.md")

	written, err := installVCSentinelSkill(path)
	if err != nil {
		t.Fatalf("installVCSentinelSkill returned an error: %v", err)
	}
	if !written {
		t.Fatal("installVCSentinelSkill should create a missing skill")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the skill was not created: %v", err)
	}
	if string(data) != vcsentinelSkillContent {
		t.Errorf("unexpected skill content:\n%s\nexpected:\n%s", data, vcsentinelSkillContent)
	}
}

func TestInstallVCSentinelSkillIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".agents", "skills", "vcsentinel", "SKILL.md")
	if _, err := installVCSentinelSkill(path); err != nil {
		t.Fatalf("first install returned an error: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	written, err := installVCSentinelSkill(path)
	if err != nil {
		t.Fatalf("second install returned an error: %v", err)
	}
	if written {
		t.Error("second install rewrote the canonical skill")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("second install changed the canonical skill")
	}
}

func TestVCSentinelSkillContentCoversGovernance(t *testing.T) {
	for _, fragment := range []string{
		"---\nname: vcsentinel",
		"<!-- vcsentinel:managed-skill -->",
		"vcsentinel check",
		"vcsentinel check --staged",
		"More than 400 authored",
		"vcsentinel slice plan --json",
		"pending_decisions",
		`{"plan_id":"<plan_id>","answers":{}}`,
		"vcsentinel slice apply --plan plan.json --answers answers.json",
		"review audits commits",
		"gate runs deterministic validation",
		"pr review records",
		"runs controls and observes",
		"human-controlled",
	} {
		if !strings.Contains(vcsentinelSkillContent, fragment) {
			t.Errorf("canonical skill does not contain %q", fragment)
		}
	}

	sections := []string{
		"## Activation Contract",
		"## Hard Rules",
		"## Decision Gates",
		"## Execution Steps",
		"## Output Contract",
		"## References",
	}
	previous := -1
	for _, section := range sections {
		position := strings.Index(vcsentinelSkillContent, section)
		if position == -1 {
			t.Errorf("canonical skill does not contain %q", section)
			continue
		}
		if position <= previous {
			t.Errorf("canonical skill places %q out of order", section)
		}
		previous = position
	}
	if !strings.HasSuffix(vcsentinelSkillContent, "\n") {
		t.Error("canonical skill should end with a newline")
	}
}

func TestInstallVCSentinelSkillPreservesForeignOrModifiedContent(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "foreign", content: "# A different skill\n"},
		{name: "modified", content: vcsentinelSkillContent + "\nLocal instructions.\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".agents", "skills", "vcsentinel", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.content), 0644); err != nil {
				t.Fatal(err)
			}

			written, err := installVCSentinelSkill(path)
			if err != nil {
				t.Fatalf("installVCSentinelSkill returned an error: %v", err)
			}
			if written {
				t.Error("install overwrote a pre-existing skill")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != test.content {
				t.Errorf("install changed pre-existing content: %q", data)
			}
		})
	}
}

func TestRemoveVCSentinelSkillIfOwnedRemovesCanonicalFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".agents", "skills", "vcsentinel", "SKILL.md")
	if _, err := installVCSentinelSkill(path); err != nil {
		t.Fatalf("install returned an error: %v", err)
	}

	removed, err := removeVCSentinelSkillIfOwned(path)
	if err != nil {
		t.Fatalf("removeVCSentinelSkillIfOwned returned an error: %v", err)
	}
	if !removed {
		t.Fatal("removeVCSentinelSkillIfOwned should remove the canonical skill")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("canonical skill still exists after removal: %v", err)
	}
}

func TestRemoveVCSentinelSkillIfOwnedPreservesForeignOrModifiedContent(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "foreign", content: "# A different skill\n"},
		{name: "modified", content: vcsentinelSkillContent + "\nLocal instructions.\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".agents", "skills", "vcsentinel", "SKILL.md")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.content), 0644); err != nil {
				t.Fatal(err)
			}

			removed, err := removeVCSentinelSkillIfOwned(path)
			if err != nil {
				t.Fatalf("removeVCSentinelSkillIfOwned returned an error: %v", err)
			}
			if removed {
				t.Error("remove deleted a non-owned skill")
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != test.content {
				t.Errorf("remove changed pre-existing content: %q", data)
			}
		})
	}
}
