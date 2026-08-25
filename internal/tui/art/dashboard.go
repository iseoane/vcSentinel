package art

import "strings"

// Dashboard renders the Control Center mock at the given terminal width in
// ANSI color. Layout follows the operator-approved contract:
//
//	╔═ header ═══════════════════════════════════╗
//	║ title                        daemon totals  ║
//	╠═ tree ════════╦═ location ═════════════════╣
//	║ repos/workt.  ║ selected repo/worktree info ║
//	║               ╟═ activity ═════════════════╣
//	║               ║ live runs and their states  ║
//	╠═ keys ════════╩═════════════════════════════╣
//	║ navigation                                  ║
//	╚═════════════════════════════════════════════╝
//
// Data is fictional and fixed: this is the visual contract, not a live view.
func Dashboard(width int) string { return renderDashboard(width, true) }

// DashboardPlain renders the same layout without any ANSI escapes.
func DashboardPlain(width int) string { return renderDashboard(width, false) }

// statusKind selects the color of a daemon, worktree, or run state.
type statusKind int

const (
	stOK   statusKind = iota // green: active runs, passed, healthy work
	stRun                    // blue: manual daemon, running activity
	stWarn                   // yellow: decision required / attention
	stStop                   // red: daemon stopped
	stOwn                    // rose: daemon managed by this TUI session
	stOff                    // dim: clean / idle
)

var statusColor = map[statusKind]Color{
	stOK: Green, stRun: Blue, stWarn: Yellow, stStop: Red, stOwn: Rose, stOff: Dim,
}

type worktreeRow struct {
	name  string
	state string
	kind  statusKind
}

type repoRow struct {
	name      string
	daemon    string
	kind      statusKind
	expanded  bool
	worktrees []worktreeRow
}

var mockRepos = []repoRow{
	{
		name: "vas.sentinel", daemon: "managed", kind: stOwn, expanded: true,
		worktrees: []worktreeRow{
			{name: "main", state: "clean", kind: stOff},
			{name: "tui-control-center", state: "2 runs", kind: stOK},
			{name: "f8-t8-2-own-diff", state: "1 attention", kind: stWarn},
		},
	},
	{name: "ue.capability.analizer", daemon: "manual", kind: stRun},
	{name: "iseoane.dots", daemon: "stopped", kind: stStop},
}

var mockLocation = [][2]string{
	{"Repository", "vas.sentinel"},
	{"Path", "/home/iseoane/0-workspace/vas.sentinel"},
	{"Worktree", "main · ~/0-workspace/vas.sentinel"},
	{"Branch", "main"},
	{"Origin", "git@github.com:ISeoane-Quental/vas.sentinel"},
	{"Daemon", "● managed by this session · pid 43120"},
	{"Status", "clean · 3 worktrees · 3 runs"},
}

type activityRow struct {
	icon  string
	flow  string
	stage string
	state string
	kind  statusKind
	age   string
}

var mockActivity = []activityRow{
	{icon: "⠹", flow: "gate pre-push", stage: "review · security", state: "RUNNING", kind: stRun, age: "02:14"},
	{icon: "⏸", flow: "review", stage: "decision required", state: "DECISION", kind: stWarn, age: "05:32"},
	{icon: "✓", flow: "review", stage: "passed", state: "PASSED", kind: stOK, age: "01:48"},
}

const mockKeys = " ↑↓ navigate · enter open · a abort · r retry · / filter · ? help · q quit"

func renderDashboard(width int, colors bool) string {
	old := colorsEnabled
	SetColors(colors)
	defer SetColors(old)

	var lines []string
	lines = append(lines, rule(width, true))
	lines = append(lines, headerLine(width))
	lines = append(lines, rule(width, true))

	if width < 84 {
		// Stacked: every block gets the full width.
		lines = append(lines, treeLines(width)...)
		lines = append(lines, rule(width, false))
		lines = append(lines, rightLines(width)...)
	} else {
		leftWidth := leftPaneWidth(width)
		tree := treeLines(leftWidth)
		right := rightLines(width - leftWidth - 3)
		lines = append(lines, zipPanes(tree, right, leftWidth, width)...)
	}

	lines = append(lines, rule(width, true))
	lines = append(lines, spanLine(width, span{mockKeys, Dim}))
	return strings.Join(lines, "\n")
}

// headerLine renders the title with the colored daemon summary right-aligned.
func headerLine(width int) string {
	title := span{" SENTINEL CONTROL CENTER", Purple}
	summary := []span{
		{"● 2 daemons ", Rose},
		{"· 3 active ", Blue},
		{"· 1 attention", Yellow},
	}
	summaryWidth := 0
	for _, s := range summary {
		summaryWidth += runeLen(s.text)
	}
	gap := width - runeLen(title.text) - summaryWidth
	if gap < 1 {
		return spanLine(width, title)
	}
	return spanLine(width, title, span{spaces(gap), Reset}, summary[0], summary[1], summary[2])
}

// treeLines renders the repository/worktree tree pane with fixed state
// columns so statuses align down the panel.
func treeLines(w int) []string {
	lines := []string{spanLine(w, span{" REPOSITORIES", White})}
	for _, r := range mockRepos {
		marker := "▸"
		if r.expanded {
			marker = "▾"
		}
		dot := "○"
		if r.kind != stOff {
			dot = "●"
		}
		lines = append(lines, spanLine(w,
			span{" " + marker + " " + padRunes(r.name, 22), White},
			span{" " + dot + " " + r.daemon, statusColor[r.kind]}))
		if r.expanded {
			for i, wt := range r.worktrees {
				branch := "├─"
				if i == len(r.worktrees)-1 {
					branch = "└─"
				}
				name := wt.name
				if wt.kind == stWarn {
					name = "⚠ " + name
				}
				lines = append(lines, spanLine(w,
					span{"   " + branch + " " + padRunes(name, 17), Dim},
					span{" " + wt.state, statusColor[wt.kind]}))
			}
		}
	}
	return lines
}

// padRunes right-pads s with spaces to n visible runes.
func padRunes(s string, n int) string {
	if runeLen(s) >= n {
		return s
	}
	return s + spaces(n-runeLen(s))
}

// rightLines renders the LOCATION block, an inner double separator, and the
// ACTIVITY block for the pane width.
func rightLines(w int) []string {
	var lines []string
	lines = append(lines, spanLine(w, span{" LOCATION", White}))
	for _, kv := range mockLocation {
		lines = append(lines, spanLine(w,
			span{" " + kv[0] + spaces(12-runeLen(kv[0])), Dim},
			span{" " + kv[1], White}))
	}
	lines = append(lines, spanLine(w, span{" " + strings.Repeat("═", maxInt(4, w-2)), Purple}))
	lines = append(lines, spanLine(w, span{" ACTIVITY", White}))
	for _, a := range mockActivity {
		lines = append(lines, spanLine(w,
			span{" " + a.icon, statusColor[a.kind]},
			span{" " + padRunes(a.flow, 16), White},
			span{padRunes(a.stage, 20), Dim},
			span{padRunes(a.state, 9), statusColor[a.kind]},
			span{a.age, Dim}))
	}
	return lines
}

// zipPanes places the tree beside the right pane with a vertical separator.
// Both inputs may already carry ANSI colors, so alignment measures visible
// runes only.
func zipPanes(left, right []string, leftWidth, width int) []string {
	rows := maxInt(len(left), len(right))
	out := make([]string, 0, rows)
	separator := Paint(Dim, " │ ")
	for i := 0; i < rows; i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		visible := runeLen(stripEscapes(l))
		if visible > leftWidth {
			l = truncateVisible(l, leftWidth)
			visible = leftWidth
		}
		out = append(out, l+spaces(leftWidth-visible)+separator+r)
	}
	return out
}

// truncateVisible cuts a possibly colored string to n visible runes without
// breaking escape sequences.
func truncateVisible(s string, n int) string {
	var b strings.Builder
	visible := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			if end := strings.IndexByte(s[i:], 'm'); end >= 0 {
				b.WriteString(s[i : i+end+1])
				i += end
				continue
			}
		}
		if visible >= n {
			continue
		}
		b.WriteByte(s[i])
		visible++
	}
	b.WriteString("\x1b[0m")
	return b.String()
}

// stripEscapes removes ANSI sequences so pane alignment measures real runes.
func stripEscapes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			if end := strings.IndexByte(s[i:], 'm'); end >= 0 {
				i += end
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// rule renders a full-width separator: ═ for major sections, ─ inside.
func rule(width int, major bool) string {
	char := "─"
	c := Dim
	if major {
		char = "═"
		c = Purple
	}
	return spanLine(width, span{" " + strings.Repeat(char, maxInt(4, width-2)), c})
}

// leftPaneWidth fixes the tree pane: 38% of the width, clamped so the
// longest mock rows never truncate.
func leftPaneWidth(width int) int {
	w := width * 38 / 100
	if w < 30 {
		w = 30
	}
	if w > 44 {
		w = 44
	}
	return w
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
