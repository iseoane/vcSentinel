package main

import (
	"strings"
	"testing"
)

var expectedTopLevelHelpOrder = []string{
	"version", "help",
	"check", "slice",
	"review", "refute", "accept", "reopen",
	"gate", "lint",
	"explain",
	"pr",
	"runs", "tui",
	"consent-diff",
	"rebase", "status", "metrics", "doctor",
	"init", "uninit",
	"install", "upgrade", "uninstall",
}

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
	for _, name := range expectedTopLevelHelpOrder {
		if !strings.Contains(help, "  "+name+" ") {
			t.Errorf("the help does not document the subcommand %q", name)
		}
	}
}

func TestHelpUsesRequestedWorkflowOrder(t *testing.T) {
	help := buildHelp()
	var got []string
	inSubcommands := false
	for _, line := range strings.Split(help, "\n") {
		if line == "Subcommands:" {
			inSubcommands = true
			continue
		}
		if line == "Flags:" {
			break
		}
		if !inSubcommands || len(line) < 3 || !strings.HasPrefix(line, "  ") || line[2] == ' ' {
			continue
		}
		got = append(got, strings.Fields(line)[0])
	}
	if strings.Join(got, "|") != strings.Join(expectedTopLevelHelpOrder, "|") {
		t.Fatalf("top-level help order = %v, want %v", got, expectedTopLevelHelpOrder)
	}

	usageStart := strings.Index(help, "Usage: ")
	usageEnd := strings.Index(help[usageStart:], "\n\nSubcommands:")
	if usageStart < 0 || usageEnd < 0 {
		t.Fatalf("top-level help has no complete usage synopsis:\n%s", help)
	}
	usage := strings.Join(strings.Fields(strings.ReplaceAll(help[usageStart:usageStart+usageEnd], "\n", " ")), " ")
	wantUsage := "Usage: vcsentinel [" + strings.Join(expectedTopLevelHelpOrder, " | ") + "]"
	if usage != wantUsage {
		t.Errorf("top-level help usage = %q, want %q", usage, wantUsage)
	}
	if topLevelUsage != wantUsage {
		t.Errorf("printUsage synopsis = %q, want %q", topLevelUsage, wantUsage)
	}
}

func TestHelpUsesPlainLanguageAndAccurateCommandGuidance(t *testing.T) {
	var all strings.Builder
	all.WriteString(buildHelp())
	for key, text := range commandHelpTexts {
		all.WriteString("\n")
		all.WriteString(key)
		all.WriteString("\n")
		all.WriteString(text)
	}
	lower := strings.ToLower(all.String())
	for _, term := range []string{
		"worktree", "durable run", "durable-run", "event stream", "append-only",
		"dispositions log", "optimistic concurrency", "prober", "lifecycle stage",
		"cohesion", "guardian summary", "legacy passthrough",
	} {
		if strings.Contains(lower, term) {
			t.Errorf("help exposes internal term %q", term)
		}
	}

	prCreate := commandHelpTexts["pr create"]
	for _, phrase := range []string{
		"previously saved pr review result",
		"does not review the branch",
	} {
		if !strings.Contains(strings.ToLower(prCreate), phrase) {
			t.Errorf("pr create help is missing %q:\n%s", phrase, prCreate)
		}
	}
	if strings.Contains(prCreate, "--audit-pending") {
		t.Error("pr create help documents the retired --audit-pending flag")
	}

	for _, key := range []string{"init", "uninit"} {
		text := commandHelpTexts[key]
		if !strings.Contains(text, ".agents/skills/vcsentinel/SKILL.md") {
			t.Errorf("%s help does not name the repository-local skill", key)
		}
		if !strings.Contains(strings.ToLower(text), "foreign or modified") || !strings.Contains(strings.ToLower(text), "preserved") {
			t.Errorf("%s help does not explain preservation of foreign or modified files:\n%s", key, text)
		}
	}
	for _, key := range []string{"install", "upgrade", "uninstall"} {
		text := strings.ToLower(commandHelpTexts[key])
		if !strings.Contains(text, "global") || !strings.Contains(text, "repository") {
			t.Errorf("%s help does not explain global and per-repository scope:\n%s", key, commandHelpTexts[key])
		}
	}

	runs := strings.ToLower(commandHelpTexts["runs"])
	for _, subcommand := range []string{"attach", "daemon", "prune"} {
		if !strings.Contains(runs, subcommand) {
			t.Errorf("runs help does not mention %s:\n%s", subcommand, commandHelpTexts["runs"])
		}
	}
	if !strings.Contains(runs, "exit codes") {
		t.Error("runs help omits its exit-code contract")
	}

	review := strings.ToLower(commandHelpTexts["review"])
	for _, phrase := range []string{"default target is head", "branch or range through the target"} {
		if !strings.Contains(review, phrase) {
			t.Errorf("review help is missing %q:\n%s", phrase, commandHelpTexts["review"])
		}
	}
	if strings.Contains(review, "upstream/main") {
		t.Error("review help exposes an inaccurate upstream/main chain base")
	}

	rebase := strings.ToLower(commandHelpTexts["rebase"])
	for _, phrase := range []string{"configured upstream", "local main", "master", "pending changes", "confirm"} {
		if !strings.Contains(rebase, phrase) {
			t.Errorf("rebase help is missing %q:\n%s", phrase, commandHelpTexts["rebase"])
		}
	}

	for _, key := range []string{"runs attach", "runs logs"} {
		text := strings.ToLower(commandHelpTexts[key])
		if !strings.Contains(text, "--after") || !strings.Contains(text, "activity number") {
			t.Errorf("%s help does not explain --after as an activity number:\n%s", key, commandHelpTexts[key])
		}
		if strings.Contains(text, "cursor") {
			t.Errorf("%s help exposes internal cursor terminology:\n%s", key, commandHelpTexts[key])
		}
	}

	tui := strings.ToLower(commandHelpTexts["tui"])
	if !strings.Contains(tui, "interactive dashboard") || strings.Contains(tui, "control center") {
		t.Errorf("tui help should use interactive dashboard wording:\n%s", commandHelpTexts["tui"])
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
