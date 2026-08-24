package art

import (
	"fmt"
	"strings"
)

// Dashboard renders a static mock of the Control Center dashboard at the
// given terminal width. Data is fictional and fixed: this is the visual
// contract for the operator gate, not a live view. Layout adapts: three
// panes from 100 columns, stacked single-pane below.
func Dashboard(width int) string {
	switch {
	case width >= 100:
		return renderWide(width)
	default:
		return renderNarrow(width)
	}
}

var mockRepos = []struct {
	Name    string
	Root    string
	Daemon  string
	Runs    string
	Current bool
}{
	{Name: "vas.sentinel", Root: "~/0-workspace/vas.sentinel", Daemon: "● owned", Runs: "2 active · 1 attention", Current: true},
	{Name: "ue.capability.analizer", Root: "~/0-workspace/ue.capability.analizer", Daemon: "● external", Runs: "1 active"},
	{Name: "iseoane.dots", Root: "~/0-workspace/iseoane.dots", Daemon: "○ stopped", Runs: "idle"},
}

var mockWorktrees = []struct {
	Name    string
	Branch  string
	State   string
	Current bool
}{
	{Name: "main", Branch: "main", State: "clean", Current: true},
	{Name: "tui-control-center", Branch: "feat/tui-control-center", State: "clean"},
	{Name: "f8-t8-2-own-diff", Branch: "feat/f8-t8-2-own-diff", State: "2 runs"},
}

var mockActivity = []struct {
	Icon  string
	Flow  string
	Stage string
	Age   string
}{
	{Icon: "⠹", Flow: "gate pre-push", Stage: "review · security", Age: "02:14"},
	{Icon: "⏸", Flow: "review", Stage: "decision required", Age: "05:32"},
	{Icon: "✓", Flow: "review", Stage: "passed", Age: "01:48"},
}

const mockFooter = " ↑↓ navigate · enter open · a abort · r retry · / filter · ? help · q quit "

func renderWide(width int) string {
	left := width * 34 / 100
	var b strings.Builder
	b.WriteString(header(width))
	b.WriteString("\n")
	// Pane: repositories | worktrees+activity stacked on the right.
	repoLines := make([]string, 0, len(mockRepos)+1)
	repoLines = append(repoLines, "REPOSITORIES")
	for _, r := range mockRepos {
		cursor := " "
		if r.Current {
			cursor = ">"
		}
		repoLines = append(repoLines, fmt.Sprintf("%s %-21s %s", cursor, r.Name, r.Daemon))
	}
	wtLines := make([]string, 0, len(mockWorktrees)+len(mockActivity)+2)
	wtLines = append(wtLines, "WORKTREES")
	for _, w := range mockWorktrees {
		cursor := " "
		if w.Current {
			cursor = ">"
		}
		wtLines = append(wtLines, fmt.Sprintf("%s %-18s %-24s %s", cursor, w.Name, w.Branch, w.State))
	}
	wtLines = append(wtLines, "", "ACTIVITY")
	for _, a := range mockActivity {
		wtLines = append(wtLines, fmt.Sprintf("  %s %-14s %-22s %s", a.Icon, a.Flow, a.Stage, a.Age))
	}
	rows := max(len(repoLines), len(wtLines))
	for i := 0; i < rows; i++ {
		l, r := "", ""
		if i < len(repoLines) {
			l = repoLines[i]
		}
		if i < len(wtLines) {
			r = wtLines[i]
		}
		b.WriteString(padRow(l, left) + "  " + r + "\n")
	}
	b.WriteString(strings.Repeat("─", width) + "\n")
	b.WriteString(" Repository  vas.sentinel\n")
	b.WriteString(" Worktree    ~/0-workspace/vas.sentinel  ·  branch main  ·  clean\n")
	b.WriteString(" Selected    gate pre-push · run 01K9Q… · state running · stage review/security\n")
	b.WriteString(strings.Repeat("─", width) + "\n")
	b.WriteString(truncate(mockFooter, width))
	return b.String()
}

func renderNarrow(width int) string {
	var b strings.Builder
	b.WriteString(header(width))
	b.WriteString("\n")
	for _, r := range mockRepos {
		cursor := " "
		if r.Current {
			cursor = ">"
		}
		b.WriteString(fmt.Sprintf("%s %-22s %s\n", cursor, r.Name, r.Daemon))
	}
	b.WriteString("\n")
	for _, a := range mockActivity {
		b.WriteString(fmt.Sprintf(" %s %-14s %s\n", a.Icon, a.Flow, a.Stage))
	}
	b.WriteString(strings.Repeat("─", width) + "\n")
	b.WriteString(truncate(mockFooter, width))
	return b.String()
}

func header(width int) string {
	title := " SENTINEL CONTROL CENTER"
	status := "● 2 daemons · 3 active · 1 attention"
	gap := width - runeLen(title) - runeLen(status)
	if gap < 1 {
		// Narrow terminals drop the status before overflowing.
		return truncate(title, width)
	}
	return title + strings.Repeat(" ", gap) + status
}

func runeLen(s string) int { return len([]rune(s)) }

func padRow(s string, w int) string {
	if runeLen(s) > w {
		return truncate(s, w)
	}
	return s + strings.Repeat(" ", w-runeLen(s))
}

func truncate(s string, w int) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w < 1 {
		return ""
	}
	return string(r[:w-1]) + "…"
}
