package art

import "strings"

// Dashboard renders the Control Center mock at the given terminal width in
// ANSI color. Layout follows the operator-approved contract: a full-width
// double rule opens and closes the frame and separates every major section;
// the header carries the title and the colored daemon summary; the tree pane
// sits beside the right pane (LOCATION over a double rule, then ACTIVITY)
// joined by a dim vertical bar; the navigation footer closes the frame.
// Below 84 columns the panes stack at full width. Data is fictional and
// fixed: this is the visual contract, not a live view.
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
	{"Path", "/home/operator/workspace/vas.sentinel"},
	{"Worktree", "main · ~/workspace/vas.sentinel"},
	{"Branch", "main"},
	{"Origin", "git@github.com:org/vas.sentinel"},
	{"Daemon", "● managed by this session · pid 4321"},
	{"Status", "clean · 3 worktrees · 3 runs"},
}

// mockSummary is the fixed header daemon summary of the visual contract.
var mockSummary = []span{
	{"● 2 daemons ", Rose},
	{"· 3 active ", Blue},
	{"· 1 attention", Yellow},
}

type activityRow struct {
	icon  string
	flow  string
	stage string
	state string
	kind  statusKind
	age   string

	// Navigation affordances for real snapshots; the fixed mock leaves both
	// at their zero values. focused prefixes a purple "▸" over the leading
	// icon column; detail appends one dim line right after the row.
	focused bool
	detail  string
}

var mockActivity = []activityRow{
	{icon: "⠹", flow: "gate pre-push", stage: "review · security", state: "RUNNING", kind: stRun, age: "02:14"},
	{icon: "⏸", flow: "review", stage: "decision required", state: "DECISION", kind: stWarn, age: "05:32"},
	{icon: "✓", flow: "review", stage: "passed", state: "PASSED", kind: stOK, age: "01:48"},
}

// mockKeys is the navigation footer: keys render in the purple accent,
// descriptions in dim.
var mockKeys = []struct{ key, desc string }{
	{"↑↓", "navigate"}, {"enter", "open"}, {"a", "abort"}, {"r", "retry"},
	{"/", "filter"}, {"?", "help"}, {"q", "quit"},
}

// helpKeys is the KEYS overlay shown while help is open: one row per
// navigation key with a short description, including the keys whose action
// is not wired yet.
var helpKeys = []struct{ key, desc string }{
	{"↑↓", "navigate panes"},
	{"tab", "focus"},
	{"enter", "open/close run"},
	{"a", "abort (soon)"},
	{"r", "retry (soon)"},
	{"?", "help"},
	{"q", "quit"},
}

func keyLine(p painter, width int) string {
	spans := []span{{" ", Reset}}
	for i, k := range mockKeys {
		if i > 0 {
			spans = append(spans, span{" · ", Dim})
		}
		spans = append(spans, span{k.key, Purple}, span{" " + k.desc, Dim})
	}
	return p.spanLine(width, spans...)
}

func renderDashboard(width int, colors bool) string {
	p := painter{colors: colors}
	return renderFrame(p, width, mockSummary,
		func(w int) []string { return treeLines(p, w, mockRepos) },
		func(w int) []string { return rightLines(p, w, mockLocation, mockActivity, false) })
}

// renderFrame assembles the approved frame: rules, header with the daemon
// summary, panes (stacked below 84 columns), and navigation footer. Pane
// builders run lazily because the two modes need different pane widths;
// mock and real-data renders share this frame unchanged.
func renderFrame(p painter, width int, summary []span, tree, right func(int) []string) string {
	var lines []string
	lines = append(lines, rule(p, width, true))
	lines = append(lines, headerLine(p, width, summary))
	lines = append(lines, rule(p, width, true))

	if width < 84 {
		// Stacked: every block gets the full width.
		lines = append(lines, tree(width)...)
		lines = append(lines, rule(p, width, false))
		lines = append(lines, right(width)...)
	} else {
		leftWidth := leftPaneWidth(width)
		rightWidth := width - leftWidth - 3
		lines = append(lines, zipPanes(p, tree(leftWidth), right(rightWidth), leftWidth, width)...)
	}

	lines = append(lines, rule(p, width, true))
	lines = append(lines, keyLine(p, width))
	return strings.Join(lines, "\n")
}

// headerLine renders the title with the colored daemon summary right-aligned.
func headerLine(p painter, width int, summary []span) string {
	title := span{" SENTINEL CONTROL CENTER", Purple}
	summaryWidth := 0
	for _, s := range summary {
		summaryWidth += runeLen(s.text)
	}
	gap := width - runeLen(title.text) - summaryWidth
	if gap < 1 {
		return p.spanLine(width, title)
	}
	spans := make([]span, 0, len(summary)+2)
	spans = append(spans, title, span{spaces(gap), Reset})
	return p.spanLine(width, append(spans, summary...)...)
}

// treeLines renders the repository/worktree tree pane with fixed state
// columns so statuses align down the panel.
func treeLines(p painter, w int, repos []repoRow) []string {
	lines := []string{p.spanLine(w, span{" REPOSITORIES", White})}
	for _, r := range repos {
		marker := "▸"
		if r.expanded {
			marker = "▾"
		}
		dot := "○"
		if r.kind != stOff {
			dot = "●"
		}
		lines = append(lines, p.spanLine(w,
			span{" " + marker + " " + fitRunes(r.name, 22), White},
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
				lines = append(lines, p.spanLine(w,
					span{"   " + branch + " " + fitRunes(name, 17), Dim},
					span{" " + wt.state, statusColor[wt.kind]}))
			}
		}
	}
	return lines
}

// fitRunes truncates s to n visible runes and right-pads it, so every row
// that promises a fixed column actually keeps it.
func fitRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		r = r[:n]
	}
	return string(r) + spaces(n-len(r))
}

// rightLines renders the LOCATION block, an inner double separator, and the
// ACTIVITY block for the pane width. runsFocused marks the ACTIVITY heading
// as the focused pane; unfocused rendering keeps the exact historical bytes.
func rightLines(p painter, w int, location [][2]string, activity []activityRow, runsFocused bool) []string {
	var lines []string
	lines = append(lines, p.spanLine(w, span{" LOCATION", White}))
	for _, kv := range location {
		lines = append(lines, p.spanLine(w,
			span{" " + kv[0] + spaces(12-runeLen(kv[0])), Dim},
			span{" " + kv[1], White}))
	}
	lines = append(lines, p.spanLine(w, span{" " + strings.Repeat("═", max(4, w-2)), Purple}))
	header := span{" ACTIVITY", White}
	if runsFocused {
		header = span{" ▸ ACTIVITY", Purple}
	}
	lines = append(lines, p.spanLine(w, header))
	for _, a := range activity {
		spans := make([]span, 0, 6)
		if a.focused {
			spans = append(spans, span{"▸", Purple})
		}
		spans = append(spans,
			span{" " + a.icon, statusColor[a.kind]},
			span{" " + fitRunes(a.flow, 16), White},
			span{fitRunes(a.stage, 20), Dim},
			span{fitRunes(a.state, 9), statusColor[a.kind]},
			span{a.age, Dim})
		lines = append(lines, p.spanLine(w, spans...))
		if a.detail != "" {
			lines = append(lines, p.spanLine(w, span{a.detail, Dim}))
		}
	}
	return lines
}

// zipPanes places the tree beside the right pane with a vertical separator.
// Both inputs may already carry ANSI colors, so alignment measures visible
// runes only.
func zipPanes(p painter, left, right []string, leftWidth, width int) []string {
	rows := max(len(left), len(right))
	out := make([]string, 0, rows)
	separator := p.paint(Dim, " │ ")
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
func rule(p painter, width int, major bool) string {
	char := "─"
	c := Dim
	if major {
		char = "═"
		c = Purple
	}
	return p.spanLine(width, span{" " + strings.Repeat(char, max(4, width-2)), c})
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
