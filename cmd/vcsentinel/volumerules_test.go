package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// toCRLF converts every line ending to CRLF, to reproduce the real file that
// caused B10 on Windows with `* text=auto`.
func toCRLF(text string) string {
	return strings.ReplaceAll(text, "\n", "\r\n")
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("could not write %s: %v", path, err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read %s: %v", path, err)
	}
	return string(data)
}

// TestInjectDoesNotDuplicateWithCRLF: a block already present in CRLF must
// be recognized. This is the exact case of B10: init stopped being idempotent.
func TestInjectDoesNotDuplicateWithCRLF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	content := toCRLF("# Guide\n" + volumeRules)
	writeTestFile(t, path, content)

	written, err := injectRulesIntoFile(path)
	if err != nil {
		t.Fatalf("injectRulesIntoFile returned an error: %v", err)
	}
	if written {
		t.Error("init duplicated the block: it did not recognize the CRLF version")
	}
	if readTestFile(t, path) != content {
		t.Error("the file changed even though the block was already there")
	}
}

func TestInjectMigratesOlderManagedRule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	oldRule := "\n" + markerBegin + "\n## Old guardian rule\n- Block all worktree changes.\n" + markerEnd + "\n"
	writeTestFile(t, path, toCRLF(oldRule))

	written, err := injectRulesIntoFile(path)
	if err != nil {
		t.Fatalf("injecting managed rules returned an error: %v", err)
	}
	if !written {
		t.Fatal("init did not migrate the old marked rule")
	}

	content := readTestFile(t, path)
	if strings.Contains(content, "Block all worktree changes") {
		t.Errorf("the old marked rule remained after migration: %q", content)
	}
	if !strings.Contains(content, "vcsentinel check --staged") {
		t.Errorf("the current staged enforcement rule was not injected: %q", content)
	}
	if strings.Contains(strings.ReplaceAll(content, "\r\n", ""), "\n") {
		t.Errorf("migration changed a CRLF file to LF: %q", content)
	}

	written, err = injectRulesIntoFile(path)
	if err != nil {
		t.Fatalf("second injectRulesIntoFile returned an error: %v", err)
	}
	if written {
		t.Error("init was not idempotent after migrating the marked rule")
	}
}

// TestRemoveWithCRLF: uninit must be able to remove a block in CRLF. If it
// cannot, uninit does not revert what init did, which is its contract.
func TestRemoveWithCRLF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	writeTestFile(t, path, toCRLF("# Guide\n"+volumeRules))

	removed, err := removeRulesFromFile(path)
	if err != nil {
		t.Fatalf("removeRulesFromFile returned an error: %v", err)
	}
	if !removed {
		t.Fatal("uninit did not recognize the block in CRLF")
	}
	remaining := readTestFile(t, path)
	if strings.Contains(remaining, "CRITICAL VOLUME RULE") {
		t.Errorf("the block is still present: %q", remaining)
	}
	if !strings.Contains(remaining, "# Guide") {
		t.Errorf("the file's own content was lost: %q", remaining)
	}
}

// TestRemoveMixedDuplicateBlocks: uninit must repair the files that older
// versions of init left with the repeated block, even when each copy has
// different line endings.
func TestRemoveMixedDuplicateBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	writeTestFile(t, path, "# Guide\n"+toCRLF(volumeRules)+volumeRules)

	removed, err := removeRulesFromFile(path)
	if err != nil {
		t.Fatalf("removeRulesFromFile returned an error: %v", err)
	}
	if !removed {
		t.Fatal("uninit removed nothing")
	}
	remaining := readTestFile(t, path)
	if strings.Contains(remaining, "CRITICAL VOLUME RULE") {
		t.Errorf("blocks were left unremoved: %q", remaining)
	}
}

// TestInjectRespectsDominantLineEnding: writing the block in LF inside a
// CRLF file would create exactly the mix that caused B10.
func TestInjectRespectsDominantLineEnding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "CLAUDE.md")
	writeTestFile(t, path, toCRLF("# Guide\nPrior content.\n"))

	if _, err := injectRulesIntoFile(path); err != nil {
		t.Fatalf("injectRulesIntoFile returned an error: %v", err)
	}
	content := readTestFile(t, path)
	if strings.Contains(strings.ReplaceAll(content, "\r\n", ""), "\n") {
		t.Errorf("the block was written with LF into a CRLF file: %q", content)
	}

	// And it stays idempotent after writing it in CRLF.
	written, err := injectRulesIntoFile(path)
	if err != nil {
		t.Fatalf("second injection returned an error: %v", err)
	}
	if written {
		t.Error("the second init run duplicated the block it wrote itself")
	}
}

// TestInjectOnLFFileKeepsUsingLF protects the current behavior on Debian: a
// file with LF endings must not receive CRLF.
func TestInjectOnLFFileKeepsUsingLF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "AGENTS.md")
	writeTestFile(t, path, "# Guide\nPrior content.\n")

	if _, err := injectRulesIntoFile(path); err != nil {
		t.Fatalf("injectRulesIntoFile returned an error: %v", err)
	}
	if strings.Contains(readTestFile(t, path), "\r\n") {
		t.Error("CRLF was introduced into a file with LF endings")
	}
}

// TestInjectMigratesDuplicateLegacyBlock reproduces the EXACT case this
// repo's .claudecode.md has today: two copies of the legacy block (without
// markers), the fruit of a binary older than the idempotency fix. init must
// migrate it: remove both legacy copies and leave a single marked block.
func TestInjectMigratesDuplicateLegacyBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claudecode.md")
	writeTestFile(t, path, "# Guide\n"+legacyVolumeRules+legacyVolumeRules)

	written, err := injectRulesIntoFile(path)
	if err != nil {
		t.Fatalf("injectRulesIntoFile returned an error: %v", err)
	}
	if !written {
		t.Fatal("init did not migrate the duplicated legacy block")
	}

	content := readTestFile(t, path)
	if strings.Count(content, markerBegin) != 1 {
		t.Errorf("expected exactly 1 markerBegin, content: %q", content)
	}
	if strings.Count(content, markerEnd) != 1 {
		t.Errorf("expected exactly 1 markerEnd, content: %q", content)
	}
	if strings.Count(content, "CRITICAL VOLUME RULE") != 1 {
		t.Errorf("expected exactly 1 occurrence of the rule, content: %q", content)
	}
	if !strings.Contains(content, "# Guide") {
		t.Errorf("the file's own content was lost: %q", content)
	}

	writtenAgain, err := injectRulesIntoFile(path)
	if err != nil {
		t.Fatalf("second injection returned an error: %v", err)
	}
	if writtenAgain {
		t.Error("init is not idempotent after the migration: it wrote again")
	}
}

// TestInjectMigratesLegacyBlockWithoutPriorContent covers this repo's real
// case: a file whose ONLY content is two copies of the legacy block glued
// from byte 0 (no prior line, so the first copy has no newline in front of
// it). Before this test, the regex always required a "\r?\n" in front of the
// block, so it left that first copy unrecognized: init migrated only the
// second one and the file ended up with an orphan legacy block plus the new
// marked block.
func TestInjectMigratesLegacyBlockWithoutPriorContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claudecode.md")
	withoutLeadingNewline := strings.TrimPrefix(legacyVolumeRules, "\n")
	writeTestFile(t, path, toCRLF(withoutLeadingNewline+withoutLeadingNewline))

	written, err := injectRulesIntoFile(path)
	if err != nil {
		t.Fatalf("injectRulesIntoFile returned an error: %v", err)
	}
	if !written {
		t.Fatal("init did not migrate the legacy block without prior content")
	}

	content := readTestFile(t, path)
	if strings.Count(content, markerBegin) != 1 {
		t.Errorf("expected exactly 1 markerBegin, content: %q", content)
	}
	if strings.Count(content, "CRITICAL VOLUME RULE") != 1 {
		t.Errorf("expected exactly 1 occurrence of the rule, content: %q", content)
	}
	if strings.Contains(strings.ReplaceAll(content, "\r\n", ""), "\n") {
		t.Errorf("migration changed a CRLF legacy-only file to LF: %q", content)
	}
}

// TestContainsRecognizesMarkedBlockWithOtherWording simulates a future
// version that reworded the block's inner text. Detection must keep working
// because it depends only on the markers, not on the text.
func TestContainsRecognizesMarkedBlockWithOtherWording(t *testing.T) {
	futureBlock := "\n" + markerBegin + "\nA completely different wording.\nMore lines.\n" + markerEnd + "\n"

	if !containsVolumeRules("# Guide\n" + futureBlock) {
		t.Error("a marked block with wording different from the current one was not recognized")
	}
}

// TestRemoveRemovesMarkedBlockWithOtherWording checks that a marked block
// with a different future wording can be removed just the same, without
// being left behind as an orphan.
func TestRemoveRemovesMarkedBlockWithOtherWording(t *testing.T) {
	futureBlock := "\n" + markerBegin + "\nA completely different wording.\nMore lines.\n" + markerEnd + "\n"

	remaining := removeVolumeRules("# Guide\n" + futureBlock)
	if strings.Contains(remaining, markerBegin) {
		t.Errorf("markerBegin was left unremoved: %q", remaining)
	}
	if strings.Contains(remaining, markerEnd) {
		t.Errorf("markerEnd was left unremoved: %q", remaining)
	}
	if !strings.Contains(remaining, "# Guide") {
		t.Errorf("the file's own content was lost: %q", remaining)
	}
}

// TestRemoveRemovesLegacyAndMarkedMix covers a repo in mid-migration: a
// legacy copy and a marked copy at the same time. uninit must remove both.
func TestRemoveRemovesLegacyAndMarkedMix(t *testing.T) {
	content := "# Guide\n" + legacyVolumeRules + volumeRules

	remaining := removeVolumeRules(content)
	if strings.Contains(remaining, "CRITICAL VOLUME RULE") {
		t.Errorf("rule text was left unremoved: %q", remaining)
	}
	if strings.Contains(remaining, markerBegin) || strings.Contains(remaining, markerEnd) {
		t.Errorf("markers were left unremoved: %q", remaining)
	}
}

func TestInjectedRuleDescribesAdvisoryWorktreeAndStagedEnforcement(t *testing.T) {
	required := []string{
		"vcsentinel check",
		"whole worktree",
		"is advisory",
		"vcsentinel check --staged",
		"staged authored code",
		"vcsentinel slice plan --json",
		"vcsentinel slice apply --plan plan.json --answers answers.json",
	}
	for _, fragment := range required {
		if !strings.Contains(volumeRulesBody, fragment) {
			t.Errorf("injected rule does not contain %q: %q", fragment, volumeRulesBody)
		}
	}
	if strings.Contains(volumeRulesBody, "STRICTLY PROHIBITED") {
		t.Error("injected rule still blocks implementation because of advisory worktree volume")
	}
}
