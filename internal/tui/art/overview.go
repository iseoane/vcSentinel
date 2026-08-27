package art

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
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

var runningGlyphs = []string{"⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏", "⠋", "⠙"}

// maxRunFlowRunes bounds how many runes of a run identifier label the flow
// column before the layout engine pads it to the approved width.
const maxRunFlowRunes = 12

// maxOperationFlowRunes bounds how many runes of an admitted operation label
// reach the flow column; labels keep the full 16-rune flow width the rows
// already reserve, two more than the legacy id fallback.
const maxOperationFlowRunes = 16

// maxVisibleRuns is the operator-visible history depth for every activity
// scope. Repo.Runs may carry a larger union so selecting a worktree can still
// reach runs older than the repository-global newest rows.
const maxVisibleRuns = 10

// FocusPane names the pane that owns keyboard navigation.
type FocusPane int

const (
	// FocusTree navigates repositories in the left tree pane.
	FocusTree FocusPane = iota
	// FocusRuns navigates durable-run rows in the ACTIVITY pane.
	FocusRuns
)

// RunPos identifies one navigable run row: Repo indexes ViewState.Repos and
// Run indexes that repository's Runs slice.
type RunPos struct {
	Repo int
	Run  int
}

// TreePos identifies one navigable row in the repository tree: Repo indexes
// ViewState.Repos and Worktree indexes that repository's Worktrees slice.
// Worktree == -1 means the repository row itself; 0..n-1 means a worktree
// child. The pair is the single cursor for the tree pane.
type TreePos struct {
	Repo     int
	Worktree int
}

// ViewState is the single render-state value of the overview screen. The
// zero value renders the historical default frame: every repository, cursor
// on the first one (repo row), tree focus, nothing open, help closed.
type ViewState struct {
	Repos      []overview.Repo
	RepoCursor int // deprecated: use TreeCursor.Repo; kept for test migration
	TreeCursor TreePos
	// TreeCursorSet distinguishes an explicit first-worktree selection
	// (Repo=0, Worktree=0) from the zero-value cursor, which means the first
	// repository row for backwards-compatible render callers.
	TreeCursorSet bool
	Focus         FocusPane
	RunCursor     RunPos
	OpenRunID     string
	Help          bool
	Filter        string
	Filtering     bool // true while the operator is typing a filter query
}

// RenderOverview renders a real overview snapshot through the layout engine
// approved in the Slice 1 contract: same frame, rules, header shape, pane
// split, state colors, and footer. Every line derives from the view state;
// see overviewRight for the cursor-clamping rule.
func RenderOverview(width int, s ViewState) string {
	return renderOverview(width, true, s)
}

// RenderOverviewPlain renders the same real-data layout without ANSI escapes.
func RenderOverviewPlain(width int, s ViewState) string {
	return renderOverview(width, false, s)
}

func renderOverview(width int, colors bool, s ViewState) string {
	p := painter{colors: colors}
	repos := ProjectOverview(s.Repos, s.Filter)
	s.Repos = repos
	if s.Filter != "" {
		// Clamp cursors to filtered view.
		if s.TreeCursor.Repo >= len(repos) {
			s.TreeCursor = TreePos{Repo: len(repos) - 1, Worktree: -1}
		}
		if s.TreeCursor.Repo < 0 && len(repos) > 0 {
			s.TreeCursor = TreePos{Repo: 0, Worktree: -1}
		}
	}
	return renderFrame(p, width, overviewSummary(repos),
		func(w int) []string { return overviewTree(p, w, s) },
		func(w int) []string { return overviewRight(p, w, s) }, activityMinRightWidth)
}

// ProjectOverview returns the immutable view of repos that matches filter.
// Repository matches retain all descendants; worktree and run matches retain
// only the matching descendants. The control model uses this same projection
// for cursor navigation and actions, so every command targets a row the
// renderer can show.
func ProjectOverview(repos []overview.Repo, filter string) []overview.Repo {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return repos
	}
	filtered := make([]overview.Repo, 0, len(repos))
	for _, r := range repos {
		// A repository identity match keeps all of its descendants visible.
		repoMatch := strings.Contains(strings.ToLower(r.Name), filter) ||
			strings.Contains(strings.ToLower(r.Path), filter) ||
			strings.Contains(strings.ToLower(r.Origin), filter) ||
			strings.Contains(strings.ToLower(r.Error), filter)
		if !repoMatch {
			// A worktree match keeps only the matching worktree and runs
			// explicitly attributed to it.
			var matchedWorktrees []inventory.Worktree
			for _, wt := range r.Worktrees[:min(len(r.Worktrees), overview.MaxSelectableWorktrees)] {
				if strings.Contains(strings.ToLower(wt.Branch), filter) ||
					strings.Contains(strings.ToLower(wt.Path), filter) {
					matchedWorktrees = append(matchedWorktrees, wt)
				}
			}
			if len(matchedWorktrees) > 0 {
				matchedRuns := make([]presence.RunSummary, 0, len(r.Runs))
				for _, run := range r.Runs {
					for _, wt := range matchedWorktrees {
						if sameWorktreePath(wt.Path, run.Worktree) {
							matchedRuns = append(matchedRuns, run)
							break
						}
					}
				}
				copy := r
				copy.Worktrees = matchedWorktrees
				copy.Runs = matchedRuns
				filtered = append(filtered, copy)
				continue
			}
		}
		if repoMatch {
			filtered = append(filtered, r)
			continue
		}
		// A run-level match keeps only matching runs, so the query is useful
		// for narrowing operation, commit, state, reason, or run id.
		matchedRuns := make([]presence.RunSummary, 0, len(r.Runs))
		for _, run := range r.Runs {
			if strings.Contains(strings.ToLower(run.Operation), filter) ||
				strings.Contains(strings.ToLower(run.Commit), filter) ||
				strings.Contains(strings.ToLower(run.Worktree), filter) ||
				strings.Contains(strings.ToLower(run.Reason), filter) ||
				strings.Contains(strings.ToLower(run.RunID), filter) ||
				strings.Contains(strings.ToLower(string(run.State)), filter) {
				matchedRuns = append(matchedRuns, run)
			}
		}
		if len(matchedRuns) > 0 {
			copy := r
			copy.Runs = matchedRuns
			filtered = append(filtered, copy)
		}
	}
	return filtered
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

// VisibleRuns returns the capped render-order positions drawn by ACTIVITY.
func VisibleRuns(s ViewState) []RunPos {
	var positions []RunPos
	cursor := effectiveTreeCursor(s)
	worktreePath := selectedWorktreePath(s)
	selectedRepo := -1
	if worktreePath != "" {
		selectedRepo = cursor.Repo
	}
	for i, r := range s.Repos {
		if selectedRepo >= 0 && i != selectedRepo {
			continue
		}
		visible := 0
		for j, run := range r.Runs {
			if worktreePath != "" && !sameWorktreePath(run.Worktree, worktreePath) {
				continue
			}
			if visible == maxVisibleRuns {
				break
			}
			positions = append(positions, RunPos{Repo: i, Run: j})
			visible++
		}
	}
	return positions
}

// selectedWorktreePath returns the selected child path, or empty for a repo row.
func selectedWorktreePath(s ViewState) string {
	cursor := effectiveTreeCursor(s)
	if cursor.Worktree < 0 || cursor.Repo < 0 || cursor.Repo >= len(s.Repos) {
		return ""
	}
	worktrees := s.Repos[cursor.Repo].Worktrees
	if cursor.Worktree >= len(worktrees) {
		return ""
	}
	return worktrees[cursor.Worktree].Path
}

// sameWorktreePath compares persisted paths using host filesystem semantics.
func sameWorktreePath(left, right string) bool {
	if left == "" || right == "" {
		return left == right
	}
	left = filepath.Clean(filepath.FromSlash(left))
	right = filepath.Clean(filepath.FromSlash(right))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

// VisibleTreePositions returns the render-order positions of the navigable
// TREE rows in s.Repos: one per repository plus one per visible worktree of
// the selected repository. Only the selected repo's worktrees are expanded,
// so the sequence is repos in order, with the selected repo's worktrees
// interleaved directly after it.
func VisibleTreePositions(s ViewState) []TreePos {
	var positions []TreePos
	selected := s.TreeCursor.Repo
	if selected < 0 {
		selected = 0
	}
	if selected >= len(s.Repos) && len(s.Repos) > 0 {
		selected = len(s.Repos) - 1
	}
	for i, r := range s.Repos {
		positions = append(positions, TreePos{Repo: i, Worktree: -1})
		if i == selected && r.Error == "" {
			shown := len(r.Worktrees)
			if shown > overview.MaxSelectableWorktrees {
				shown = overview.MaxSelectableWorktrees
			}
			for j := 0; j < shown; j++ {
				positions = append(positions, TreePos{Repo: i, Worktree: j})
			}
		}
	}
	return positions
}

// effectiveTreeCursor resolves legacy render callers that leave TreeCursor at
// its zero value. The control model marks its cursor explicitly, so selecting
// the first worktree remains distinguishable from the default repository row.
func effectiveTreeCursor(s ViewState) TreePos {
	cursor := s.TreeCursor
	if !s.TreeCursorSet && cursor.Repo == 0 && cursor.Worktree == 0 {
		if s.RepoCursor != 0 {
			cursor = TreePos{Repo: s.RepoCursor, Worktree: -1}
		} else {
			cursor.Worktree = -1
		}
	}
	if cursor.Worktree < -1 {
		cursor.Worktree = -1
	}
	return cursor
}

// overviewTree renders the tree pane: one line per repo with its daemon
// state word, then one child line per worktree — capped at the shared limit,
// with any further children collapsed into one final dim "… N more" line —
// or, for a degraded repo, its recorded Error in place of the children
// (degraded repos never count nor collapse). The overflow line always draws
// the "└─" connector and the child above it keeps "├─", since the overflow
// line is by construction the last visible row. An empty registry renders
// the empty-state line inside the untouched frame. While the tree owns focus
// its heading carries a purple marker and the repository under the cursor
// shows "▸" in place of its expand glyph; unfocused rendering keeps the
// exact historical bytes.
func overviewTree(p painter, w int, s ViewState) []string {
	heading := span{" REPOSITORIES", White}
	if s.Focus == FocusTree {
		heading = span{" ▸ REPOSITORIES", Purple}
	}
	lines := []string{p.spanLine(w, heading)}
	// Show the filter bar when the operator is typing or a filter is active.
	if s.Filtering || s.Filter != "" {
		filterText := s.Filter
		if s.Filtering {
			filterText += "▏"
		} else if filterText == "" {
			filterText = "▏"
		}
		lines = append(lines, p.spanLine(w,
			span{" / FILTER: ", Purple},
			span{filterText, White}))
	}
	repos := s.Repos
	if len(repos) == 0 {
		return append(lines, p.spanLine(w, span{" no repositories registered", Dim}))
	}
	treeCursor := effectiveTreeCursor(s)
	for i, r := range repos {
		state := classifyRepo(r)
		expanded := i == treeCursor.Repo
		marker := "▸"
		if expanded {
			marker = "▾"
		}
		if s.Focus == FocusTree && i == treeCursor.Repo && treeCursor.Worktree == -1 {
			marker = "▸"
		}
		lines = append(lines, p.spanLine(w,
			span{" " + marker + " " + fitRunes(r.Name, 22), White},
			span{" ● " + state.word, statusColor[state.kind]}))
		if r.Error != "" {
			lines = append(lines, p.spanLine(w, span{"   └─ ", Dim}, span{r.Error, Dim}))
			continue
		}
		if !expanded {
			continue
		}
		total := len(r.Worktrees)
		shown := total
		if shown > overview.MaxSelectableWorktrees {
			shown = overview.MaxSelectableWorktrees
		}
		for j, wt := range r.Worktrees[:shown] {
			child := classifyWorktree(wt)
			branch := "├─"
			if j == total-1 { // genuinely the final visible row of this repo
				branch = "└─"
			}
			// Highlight the worktree row when the tree cursor points at it.
			wtStyle := Dim
			if s.Focus == FocusTree && i == treeCursor.Repo && j == treeCursor.Worktree {
				wtStyle = White
				branch = "▸─"
			}
			lines = append(lines, p.spanLine(w,
				span{"   " + branch + " " + fitRunes(worktreeName(wt), 17), wtStyle},
				span{" " + child.state, statusColor[child.kind]}))
		}
		if total > overview.MaxSelectableWorktrees {
			lines = append(lines, p.spanLine(w,
				span{"   └─ ", Dim},
				span{fmt.Sprintf("… %d more", total-overview.MaxSelectableWorktrees), Dim}))
		}
	}
	return lines
}

// overviewRight builds the right pane around the operator-selected
// repository: LOCATION fields plus one derived ACTIVITY row per repository.
// Clamping rule: a cursor inside [0, len(repos)-1] renders exactly that
// repository; an empty registry has no selection at all and keeps the dash
// placeholder; a negative cursor falls back to index 0, and one past the
// last repository falls back to the last index, so rendering never panics.
// While help is open the LOCATION+ACTIVITY content is replaced wholesale by
// the KEYS block behind the same inner double rule.
func overviewRight(p painter, w int, s ViewState) []string {
	if s.Help {
		return helpLines(p, w)
	}
	repos := s.Repos
	var location [][2]string
	if len(repos) == 0 {
		location = [][2]string{{"Repository", "-"}, {"Path", "-"}, {"Branch", "-"},
			{"Origin", "-"}, {"Daemon", "-"}, {"Status", "-"}}
	} else {
		cursor := effectiveTreeCursor(s)
		selectedRepo := cursor.Repo
		if selectedRepo < 0 {
			selectedRepo = 0
		}
		if selectedRepo > len(repos)-1 {
			selectedRepo = len(repos) - 1
		}
		repo := repos[selectedRepo]
		if cursor.Worktree >= 0 && cursor.Worktree < len(repo.Worktrees) {
			location = overviewLocationForWorktree(repo, repo.Worktrees[cursor.Worktree])
		} else {
			location = overviewLocation(repo)
		}
	}
	return rightLines(p, w, location, overviewActivity(s), s.Focus == FocusRuns, true)
}

// helpLines replaces the pane content while help is open: the inner double
// rule stays for visual continuity, then the KEYS heading followed by one
// row per footer key in the key/desc column style.
func helpLines(p painter, w int) []string {
	lines := []string{rule(p, w, true), p.spanLine(w, span{" KEYS", White})}
	for _, k := range helpKeys {
		lines = append(lines, p.spanLine(w,
			span{" " + fitRunes(k.key, 7), Purple},
			span{" " + k.desc, Dim}))
	}
	return lines
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

// overviewLocationForWorktree projects one worktree of a repository into the
// LOCATION block: the worktree's branch and path are shown explicitly.
func overviewLocationForWorktree(r overview.Repo, wt inventory.Worktree) [][2]string {
	location := [][2]string{
		{"Repository", r.Name},
		{"Path", wt.Path},
		{"Branch", worktreeName(wt)},
		{"Origin", r.Origin},
		{"Daemon", daemonField(r.Daemon)},
		{"Status", statusField(r)},
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
// Navigation: while the runs pane owns focus, the row at RunCursor gets a
// purple "▸" over its leading icon column; a row whose full run id matches
// OpenRunID grows one dim detail line (mismatched ids render nothing).
func overviewActivity(s ViewState) []activityRow {
	rows := make([]activityRow, 0, len(s.Repos))
	visibleRuns := VisibleRuns(s)
	visibleSet := make(map[RunPos]struct{}, len(visibleRuns))
	visibleByRepo := make(map[int]int, len(s.Repos))
	for _, pos := range visibleRuns {
		visibleSet[pos] = struct{}{}
		visibleByRepo[pos.Repo]++
	}
	for i, r := range s.Repos {
		state := classifyRepo(r)
		if !(visibleByRepo[i] > 0 && (state.kind == stOwn || state.kind == stStop)) {
			rows = append(rows, summaryActivityRow(r, state))
		}
		for j, runSummary := range r.Runs {
			if _, ok := visibleSet[RunPos{Repo: i, Run: j}]; !ok {
				continue
			}
			row := runRow(runSummary)
			if s.Focus == FocusRuns && s.RunCursor == (RunPos{Repo: i, Run: j}) {
				row.focused = true
			}
			if s.OpenRunID != "" && s.OpenRunID == runSummary.RunID {
				row.detail = runDetail(runSummary)
			}
			rows = append(rows, row)
		}
	}
	return rows
}

// runDetail renders the inline expansion of one open run on a single dim line.
// A failure reason is sanitized and rendered first as "└─ error: ...";
// otherwise the line shows the full id, state word, revision, local system
// StartedAt and UpdatedAt timestamps, and the same compact age as the row.
// Narrow panes clamp it through spanLine.
func runDetail(run presence.RunSummary) string {
	reason := truncateRunes(sanitizeLabel(run.Reason), 120)
	if reason != "" {
		return "   └─ error: " + reason
	}
	_, _, word := runState(run.State)
	base := fmt.Sprintf("   └─ %s · %s · rev %d · started %s · updated %s · age %s",
		run.RunID, word, run.Revision,
		formatRunTimestamp(run.StartedAt), formatRunTimestamp(run.UpdatedAt), runAge(run))
	return base
}

// summaryActivityRow builds the classic per-repository row: icon by worst
// observable state (attention beats a live daemon beats stopped), repo name
// in flow, worktree count — or the degradation Error itself — in stage,
// empty AGE and WHEN.
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
// active. Note the deliberate tension with runState: terminated renders as
// a quiet dim CANCELED row while the header still counts it active — that
// mirrors the domain contract, where the canceled settlement, not the
// terminated marker, is the authoritative terminal frame.
func isTerminalRun(state agentrun.LifecycleState) bool {
	return state.TerminalClass() != agentrun.TerminalNone
}

// runState maps every agentrun.LifecycleState onto the operator-approved
// activity semantics: the running family (created, queued, admitted,
// running, terminating) spins blue as RUNNING, awaiting_decision pauses
// yellow as DECISION, succeeded passes green as PASSED,
// failed/timed_out/unavailable fail red as FAILED, and canceled/terminated
// settle dim as CANCELED. Unknown values degrade to the running family:
// absence of terminal evidence must not render as quiet. This switch and
// isTerminalRun must stay aligned: a new agentrun state lands here via the
// default case (RUNNING) while the header predicate reads TerminalClass,
// so a state gaining a terminal class must also gain its explicit row.
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

// runRow projects one durable-run summary into an ACTIVITY row: the flow
// column carries the admitted operation label when the run has one (truncated
// to the 16-rune flow width; the layout engine pads it downstream), else the
// first 12 runes of the run identifier as today; the revision labels stage,
// AGE counts elapsed time since admission, and WHEN shows the local admission
// clock.
func runRow(run presence.RunSummary) activityRow {
	icon, kind, word := runState(run.State)
	if kind == stRun {
		icon = runningGlyph(timeNow())
	}
	flow := truncateRunes(run.RunID, maxRunFlowRunes)
	if run.Operation != "" {
		flow = truncateRunes(sanitizeLabel(run.Operation), maxOperationFlowRunes)
	}
	commit := ""
	if run.Commit != "" {
		commit = truncateRunes(run.Commit, activityCommitWidth)
	}
	return activityRow{
		icon:   icon,
		commit: commit,
		flow:   flow,
		stage:  fmt.Sprintf("rev %d", run.Revision),
		state:  word,
		kind:   kind,
		age:    runAge(run),
		when:   runWhen(run),
	}
}

// sanitizeLabel strips control characters (newlines, tabs, escapes) from an
// operation label at the render boundary: labels may embed operator-supplied
// command text, and a raw control rune would split or recolor TUI rows.
func sanitizeLabel(label string) string {
	clean := make([]rune, 0, len(label))
	for _, r := range label {
		if r >= 0x20 && r != 0x7f {
			clean = append(clean, r)
		}
	}
	return string(clean)
}

// runAge renders a run's compact elapsed duration: active runs use the
// approved clock from StartedAt, while terminal runs use UpdatedAt-StartedAt
// so the AGE column stops ticking once the run has settled. Missing timestamps
// render a dash, and formatAge clamps clock skew to zero.
func runAge(run presence.RunSummary) string {
	if run.StartedAt.IsZero() {
		return "-"
	}
	end := timeNow()
	if isTerminalRun(run.State) {
		if run.UpdatedAt.IsZero() {
			return "-"
		}
		end = run.UpdatedAt
	}
	return formatAge(end.Sub(run.StartedAt))
}

// runWhen renders the local wall-clock time at which the run was admitted.
// Calling Local at the render boundary keeps the output on the operator's
// platform without mutating the process-wide time zone in tests.
func runWhen(run presence.RunSummary) string {
	if run.StartedAt.IsZero() {
		return "-"
	}
	return run.StartedAt.Local().Format("2006-01-02 15:04")
}

func formatRunTimestamp(at time.Time) string {
	if at.IsZero() {
		return "-"
	}
	return at.Local().Format("2006-01-02 15:04:05")
}

func runningGlyph(now time.Time) string {
	frame := now.UnixNano() / int64(500*time.Millisecond)
	index := frame % int64(len(runningGlyphs))
	if index < 0 {
		index += int64(len(runningGlyphs))
	}
	return runningGlyphs[index]
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
