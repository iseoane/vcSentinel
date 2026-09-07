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
