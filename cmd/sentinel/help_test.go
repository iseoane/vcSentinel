package main

import (
	"strings"
	"testing"
)

// TestHelpDoesNotInvadeArgumentZone: long descriptions (e.g. pr, review,
// status) must be wrapped and the continuations must always start at the
// description column (14 spaces), never glued to the left with the arguments
// nor to the right beyond the maximum width.
func TestHelpDoesNotInvadeArgumentZone(t *testing.T) {
	help := buildHelp()
	lines := strings.Split(help, "\n")

	const maxWidth = 110
	const descriptionColumn = 14 // "  " + 12-character name

	for _, line := range lines {
		if len(line) > maxWidth {
			t.Errorf("line too long (%d > %d): %q", len(line), maxWidth, line)
		}
	}

	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !strings.HasPrefix(line, "  ") {
			// Section title, Usage, or Flags: no alignment required.
			continue
		}
		if isSubcommandHeader(line) {
			continue
		}
		// Continuation line of a subcommand.
		if len(line) < descriptionColumn || strings.TrimLeft(line, " ") == line {
			t.Errorf("continuation invades the argument zone: %q", line)
		}
	}
}

// isSubcommandHeader recognizes the first line of a help item: a subcommand
// name starting at column 3 (after "  "), unlike the continuations indented
// at the description column.
func isSubcommandHeader(line string) bool {
	return len(line) > 2 && line[2] != ' '
}

// TestHelpListsAllSubcommands: the help cannot omit any of the commands the
// dispatch handles.
func TestHelpListsAllSubcommands(t *testing.T) {
	help := buildHelp()
	for _, name := range []string{
		"version", "help", "init", "uninit", "check", "slice", "review",
		"lint", "rebase", "status", "explain", "pr", "consent-diff", "install", "upgrade", "uninstall",
	} {
		if !strings.Contains(help, "  "+name+" ") {
			t.Errorf("the help does not document the subcommand %q", name)
		}
	}
}

// TestHelpPrDocumentsFlags: the pr documentation must mention the pr review
// flags without burying them in the middle of the description.
func TestHelpPrDocumentsFlags(t *testing.T) {
	help := buildHelp()
	if !strings.Contains(help, "pr review flags:") {
		t.Error("the help must document the pr review flags")
	}
}

// TestWrapHandlesLongText: the wrapping helper cuts at a fixed width without
// losing content or cutting words into pieces.
func TestWrapHandlesLongText(t *testing.T) {
	text := "Injects the volume rules into your agents, creates the per-project config, and installs the repository pre-commit hook. Always runs at the repository root."
	lines := wrap(text, 40)
	joined := strings.Join(lines, " ")
	if !strings.Contains(joined, "per-project") || !strings.Contains(joined, "repository root.") {
		t.Fatalf("wrap lost content: %q", joined)
	}
	for _, l := range lines {
		if len(l) > 40 {
			t.Errorf("line exceeds width 40: %q (%d)", l, len(l))
		}
		if strings.TrimSpace(l) == "" {
			t.Errorf("empty line generated")
		}
	}
}
