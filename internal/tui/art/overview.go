package art

import (
	"fmt"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/inventory"
	"github.com/ISeoane-Quental/vas.sentinel/internal/overview"
	"github.com/ISeoane-Quental/vas.sentinel/internal/presence"
)

// timeNow is the clock seam for run ages: a package-level variable so tests
// can pin exact elapsed strings. Rendering code reads it and never reassigns
// it.
var timeNow = time.Now

// maxRunFlowRunes bounds how many runes of a run identifier label the flow
// column before the layout engine pads it to the approved width.
const maxRunFlowRunes = 12

// RenderOverview renders a real overview snapshot through the layout engine
// approved in the Slice 1 contract: same frame, rules, header shape, pane
// split, state colors, and footer. Every line derives from the snapshot.
// selected picks the repository shown in the LOCATION pane; see overviewRight
// for the clamping rule.
func RenderOverview(width int, repos []overview.Repo, selected int) string {
	return renderOverview(width, true, repos, selected)
}

// RenderOverviewPlain renders the same real-data layout without ANSI escapes.
func RenderOverviewPlain(width int, repos []overview.Repo, selected int) string {
	return renderOverview(width, false, repos, selected)
}

func renderOverview(width int, colors bool, repos []overview.Repo, selected int) string {
	p := painter{colors: colors}
	return renderFrame(p, width, overviewSummary(repos),
		func(w int) []string { return overviewTree(p, w, repos) },
		func(w int) []string { return overviewRight(p, w, repos, selected) })
}

// overviewSummary computes the header counts under the approved semantics:
// live daemons rose, NON-terminal durable runs across every repository's
// snapshot as active blue, Error-or-Missing repos as yellow attention.
// Attention reuses classifyRepo so the header and the tree pane can never
// disagree about which repositories need attention. Since Slice 10 active
// counts real runs — lifecycle states without a terminal class — instead of
// proxying dirty worktrees; the dirty-worktrees stand-in disappeared.
func overviewSummary(repos []overview.Repo) []span {
	daemons, active, attention := 0, 0, 0
	for _, r := range repos {
		if r.Daemon.Live {
			daemons++
		}
		if classifyRepo(r).kind == stWarn {
			attention++
		}
		for _, run := range r.Runs {
			if !isTerminalRun(run.State) {
				active++
			}
		}
	}
	return []span{
		{fmt.Sprintf("● %d daemons ", daemons), Rose},
		{fmt.Sprintf("· %d active ", active), Blue},
		{fmt.Sprintf("· %d attention", attention), Yellow},
	}
}

type repoState struct {
	word string
	kind statusKind
}

// classifyRepo maps a repo onto the approved semantics: Missing or Error
// wins as yellow attention (word "missing" / "attention" respectively), a
// live daemon is rose "live", anything else red "stopped".
func classifyRepo(r overview.Repo) repoState {
	switch {
	case r.Missing:
		return repoState{"missing", stWarn}
	case r.Error != "":
		return repoState{"attention", stWarn}
	case r.Daemon.Live:
		return repoState{"live", stOwn}
	default:
		return repoState{"stopped", stStop}
	}
}

// classifyWorktree maps one worktree onto the approved child-line states.
func classifyWorktree(wt inventory.Worktree) worktreeRow {
	if wt.Clean {
		return worktreeRow{state: "clean", kind: stOff}
	}
	return worktreeRow{state: "dirty", kind: stRun}
}

// worktreeName labels a child line; detached worktrees carry no branch name.
func worktreeName(wt inventory.Worktree) string {
	if wt.Branch == "" {
		return "(detached)"
	}
	return wt.Branch
}

// overviewTree renders the tree pane: one line per repo with its daemon
// state word, then one child line per worktree or, for a degraded repo, its
// recorded Error in place of the children. An empty registry renders the
// empty-state line inside the untouched frame.
func overviewTree(p painter, w int, repos []overview.Repo) []string {
	lines := []string{p.spanLine(w, span{" REPOSITORIES", White})}
	if len(repos) == 0 {
		return append(lines, p.spanLine(w, span{" no repositories registered", Dim}))
	}
	for _, r := range repos {
		state := classifyRepo(r)
		lines = append(lines, p.spanLine(w,
			span{" ▾ " + fitRunes(r.Name, 22), White},
			span{" ● " + state.word, statusColor[state.kind]}))
		if r.Error != "" {
			lines = append(lines, p.spanLine(w, span{"   └─ ", Dim}, span{r.Error, Dim}))
			continue
		}
		for i, wt := range r.Worktrees {
			child := classifyWorktree(wt)
			branch := "├─"
			if i == len(r.Worktrees)-1 {
				branch = "└─"
			}
			lines = append(lines, p.spanLine(w,
				span{"   " + branch + " " + fitRunes(worktreeName(wt), 17), Dim},
				span{" " + child.state, statusColor[child.kind]}))
		}
	}
	return lines
}

// overviewRight builds the right pane around the operator-selected
// repository: LOCATION fields plus one derived ACTIVITY row per repository.
// Clamping rule: a selected inside [0, len(repos)-1] renders exactly that
// repository; an empty registry has no selection at all and keeps the dash
// placeholder; a negative selected falls back to index 0, and one past the
// last repository falls back to the last index, so rendering never panics.
func overviewRight(p painter, w int, repos []overview.Repo, selected int) []string {
	var location [][2]string
	if len(repos) == 0 {
		location = [][2]string{{"Repository", "-"}, {"Path", "-"}, {"Branch", "-"},
			{"Origin", "-"}, {"Daemon", "-"}, {"Status", "-"}}
	} else {
		if selected < 0 {
			selected = 0
		}
		if selected > len(repos)-1 {
			selected = len(repos) - 1
		}
		location = overviewLocation(repos[selected])
	}
	return rightLines(p, w, location, overviewActivity(repos))
}

// overviewLocation projects one repo into the LOCATION field block: Branch
// comes from the first worktree when present, Origin degrades to a dash.
func overviewLocation(r overview.Repo) [][2]string {
	location := [][2]string{
		{"Repository", r.Name},
		{"Path", r.Path},
		{"Branch", "-"},
		{"Origin", r.Origin},
		{"Daemon", daemonField(r.Daemon)},
		{"Status", statusField(r)},
	}
	if len(r.Worktrees) > 0 {
		location[2][1] = worktreeName(r.Worktrees[0])
	}
	if location[3][1] == "" {
		location[3][1] = "-"
	}
	return location
}

// daemonField renders the LOCATION daemon line: live pid versus stopped.
func daemonField(d presence.Presence) string {
	if d.Live {
		return fmt.Sprintf("● live · pid %d", d.PID)
	}
	return "○ stopped"
}

// statusField summarizes the repository's worktree counts for LOCATION.
func statusField(r overview.Repo) string {
	clean, dirty := 0, 0
	for _, wt := range r.Worktrees {
		if wt.Clean {
			clean++
		} else {
			dirty++
		}
	}
	if len(r.Worktrees) == 0 {
		return "no worktrees"
	}
	return fmt.Sprintf("%d clean · %d dirty · %d worktrees", clean, dirty, len(r.Worktrees))
}

// overviewActivity derives the ACTIVITY rows without inventing data. Row
// layout rule: every repository keeps its classic summary row EXCEPT a
// plainly live-or-stopped repository that carries runs — there the run rows
// replace the summary row entirely so the pane never duplicates the
// repository line. Attention and degraded repositories ALWAYS keep their
// summary row and append their run rows after it. A repository with neither
// children nor runs nor degradation renders exactly one summary row.
func overviewActivity(repos []overview.Repo) []activityRow {
	rows := make([]activityRow, 0, len(repos))
	for _, r := range repos {
		state := classifyRepo(r)
		if !(len(r.Runs) > 0 && (state.kind == stOwn || state.kind == stStop)) {
			rows = append(rows, summaryActivityRow(r, state))
		}
		for _, run := range r.Runs {
			rows = append(rows, runRow(run))
		}
	}
	return rows
}

// summaryActivityRow builds the classic per-repository row: icon by worst
// observable state (attention beats a live daemon beats stopped), repo name
// in flow, worktree count — or the degradation Error itself — in stage,
// empty age.
func summaryActivityRow(r overview.Repo, state repoState) activityRow {
	row := activityRow{flow: r.Name}
	switch state.kind {
	case stWarn:
		row.icon, row.kind, row.state = "⚠", stWarn, "ATTENTION"
		row.stage = r.Error
		if row.stage == "" {
			row.stage = state.word
		}
	case stOwn:
		row.icon, row.kind, row.state = "●", stOwn, "LIVE"
		row.stage = worktreeCount(r)
	case stStop:
		row.icon, row.kind, row.state = "○", stStop, "STOPPED"
		row.stage = worktreeCount(r)
	default:
		row.icon, row.kind, row.state = "✓", stOK, "HEALTHY"
		row.stage = worktreeCount(r)
	}
	return row
}

// isTerminalRun reports whether a lifecycle state settled into a terminal
// class. It rides the exported agentrun vocabulary (TerminalClass) instead
// of duplicating a local state list; transient evidence states such as
// terminated carry no terminal class yet and therefore still count as
// active.
func isTerminalRun(state agentrun.LifecycleState) bool {
	return state.TerminalClass() != agentrun.TerminalNone
}

// runState maps every agentrun.LifecycleState onto the operator-approved
// activity semantics: the running family (created, queued, admitted,
// running, terminating) spins blue as RUNNING, awaiting_decision pauses
// yellow as DECISION, succeeded passes green as PASSED,
// failed/timed_out/unavailable fail red as FAILED, and canceled/terminated
// settle dim as CANCELED. Unknown values degrade to the running family:
// absence of terminal evidence must not render as quiet.
func runState(state agentrun.LifecycleState) (icon string, kind statusKind, word string) {
	switch state {
	case agentrun.StateAwaitingDecision:
		return "⏸", stWarn, "DECISION"
	case agentrun.StateSucceeded:
		return "✓", stOK, "PASSED"
	case agentrun.StateFailed, agentrun.StateTimedOut, agentrun.StateUnavailable:
		return "✗", stStop, "FAILED"
	case agentrun.StateCanceled, agentrun.StateTerminated:
		return "○", stOff, "CANCELED"
	default: // created, queued, admitted, running, terminating, unknown
		return "⠹", stRun, "RUNNING"
	}
}

// runRow projects one durable-run summary into an ACTIVITY row: the first 12
// runes of the run identifier label the flow column (the layout engine pads
// it to the approved width), the revision labels stage, and age counts
// elapsed time since the stored projection timestamp — a dash when the
// projection carried none.
func runRow(run presence.RunSummary) activityRow {
	icon, kind, word := runState(run.State)
	stage := fmt.Sprintf("rev %d", run.Revision)
	age := "-"
	if !run.UpdatedAt.IsZero() {
		age = formatAge(timeNow().Sub(run.UpdatedAt))
	}
	return activityRow{
		icon:  icon,
		flow:  truncateRunes(run.RunID, maxRunFlowRunes),
		stage: stage,
		state: word,
		kind:  kind,
		age:   age,
	}
}

// formatAge renders a duration as the approved compact clock: mm:ss below
// one hour, h:mm:ss afterwards. Negative durations clamp to zero so clock
// skew can never paint a run as started in the future.
func formatAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d / time.Second)
	hours := total / 3600
	minutes := (total % 3600) / 60
	seconds := total % 60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

func worktreeCount(r overview.Repo) string {
	n := len(r.Worktrees)
	if n == 1 {
		return "1 worktree"
	}
	return fmt.Sprintf("%d worktrees", n)
}
