package art

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/inventory"
	"github.com/ISeoane-Quental/vas.sentinel/internal/overview"
	"github.com/ISeoane-Quental/vas.sentinel/internal/presence"
)

// Fixtures build snapshot values directly (no git needed). colorPrefix
// matches painted spans whose text spanLine right-pads before wrapping.
func wt(branch string, clean bool) inventory.Worktree {
	return inventory.Worktree{Path: filepath.Join("/tmp/repo", branch), Branch: branch, Clean: clean}
}

// run builds a run summary fixture with a fixed id and revision; updatedAt
// is interpreted against the current timeNow seam.
func run(id string, state agentrun.LifecycleState, revision uint64, updatedAt time.Time) presence.RunSummary {
	return presence.RunSummary{RunID: id, State: state, Revision: revision, UpdatedAt: updatedAt}
}

// runWithOperation extends run with an admitted operator-facing operation
// label (slice 14).
func runWithOperation(id string, state agentrun.LifecycleState, revision uint64, updatedAt time.Time, operation string) presence.RunSummary {
	summary := run(id, state, revision, updatedAt)
	summary.Operation = operation
	return summary
}

// pinClock freezes the age clock for the duration of the test.
func pinClock(t *testing.T, now time.Time) {
	t.Helper()
	previous := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = previous })
}

func liveRepo(name string, worktrees ...inventory.Worktree) overview.Repo {
	return overview.Repo{Name: name, Path: filepath.Join("/tmp", name), Enabled: true,
		Origin: "git@github.com:org/" + name, Worktrees: worktrees,
		Daemon: presence.Presence{Live: true, PID: 4321}}
}

func stoppedRepo(name string, worktrees ...inventory.Worktree) overview.Repo {
	return overview.Repo{Name: name, Path: filepath.Join("/tmp", name), Enabled: true, Worktrees: worktrees}
}

// manyWorktrees builds n distinct child fixtures alternating clean/dirty,
// with zero-padded names so hidden and shown children are easy to assert.
func manyWorktrees(n int) []inventory.Worktree {
	worktrees := make([]inventory.Worktree, n)
	for i := range worktrees {
		worktrees[i] = wt(fmt.Sprintf("branch-%02d", i), i%2 == 0)
	}
	return worktrees
}

// liveRepoWithRuns builds a live repository whose snapshot carries durable-run
// summaries; per the ACTIVITY rule its runs replace the summary row.
func liveRepoWithRuns(name string, runs ...presence.RunSummary) overview.Repo {
	repo := liveRepo(name)
	repo.Runs = runs
	return repo
}

func colorPrefix(c Color, s string) string {
	return "\x1b[38;5;" + itoa(int(c)) + "m" + s
}

func assertWidth(t *testing.T, dash string, width int) {
	t.Helper()
	for i, line := range strings.Split(dash, "\n") {
		if utf8.RuneCountInString(line) > width {
			t.Errorf("line %d overflows width %d: %q", i, width, line)
		}
	}
}

func assertContains(t *testing.T, got string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("dashboard missing %q in:\n%s", want, got)
		}
	}
}

func TestRenderOverviewHealthySingleRepo(t *testing.T) {
	dash := RenderOverviewPlain(100, ViewState{Repos: []overview.Repo{liveRepo("vas.sentinel", wt("main", true))}})
	assertContains(t, dash, "SENTINEL CONTROL CENTER", "REPOSITORIES", "LOCATION",
		"ACTIVITY", "vas.sentinel", "● live", "main", "clean",
		"git@github.com:org/vas.sentinel", "pid 4321",
		"1 clean · 0 dirty · 1 worktrees", "navigate")
	assertWidth(t, dash, 100)
}

func TestRenderOverviewMultiWorktree(t *testing.T) {
	repos := []overview.Repo{stoppedRepo("multi", wt("main", true), wt("feature", false))}
	dash := RenderOverviewPlain(100, ViewState{Repos: repos, TreeCursor: TreePos{Repo: 0, Worktree: -1}})
	// Since Slice 10 the header counts runs, not dirty worktrees: this
	// fixture carries none, so active stays zero even with a dirty tree.
	assertContains(t, dash, "├─", "└─", " dirty", "· 0 active")
	if !strings.Contains(RenderOverview(100, ViewState{Repos: repos}), colorPrefix(Blue, " dirty")) {
		t.Error("dirty child must be blue")
	}
}

// TestOverviewTreeCollapsesBeyondMaxChildren pins the slice-13 overflow
// rule: exactly 20 children render — every one of them keeping
// the "├─" connector — followed by one final dim "… N more" line carrying
// the "└─"; hidden children never leak into the render.
func TestOverviewTreeCollapsesBeyondMaxChildren(t *testing.T) {
	const expectedTreeChildren = 20
	repos := []overview.Repo{stoppedRepo("big", manyWorktrees(expectedTreeChildren+3)...)}
	dash := RenderOverviewPlain(100, ViewState{Repos: repos, TreeCursor: TreePos{Repo: 0, Worktree: -1}})
	if got := strings.Count(dash, "   ├─"); got != expectedTreeChildren {
		t.Errorf("%d child lines rendered, want exactly %d:\n%s", got, expectedTreeChildren, dash)
	}
	assertContains(t, dash, "   └─ … 3 more")
	if got := strings.Count(dash, "   └─"); got != 1 {
		t.Errorf("the overflow line must be the only └─ child row, got %d:\n%s", got, dash)
	}
	for i := expectedTreeChildren; i < expectedTreeChildren+3; i++ {
		hidden := fmt.Sprintf("branch-%02d", i)
		if strings.Contains(dash, hidden) {
			t.Errorf("collapsed child %q must not render:\n%s", hidden, dash)
		}
	}
	lastVisible := fmt.Sprintf("branch-%02d ", expectedTreeChildren-1)
	if !strings.Contains(dash, lastVisible) {
		t.Errorf("the last visible child %q must render right above the overflow line:\n%s", lastVisible, dash)
	}
	assertWidth(t, dash, 100)
	colored := RenderOverview(100, ViewState{Repos: repos})
	if !strings.Contains(colored, colorPrefix(Dim, "… 3 more")) {
		t.Errorf("the overflow line must render dim:\n%s", colored)
	}
}

// TestOverviewTreeAtLimitAndDegradedExempt pins both non-collapse sides: at
// exactly 20 children every child renders with the classic final "└─"
// and no overflow line appears; fewer children stay untouched; and a
// degraded repository is exempt from counting and collapsing entirely — its
// Error line replaces the children wholesale.
func TestOverviewTreeAtLimitAndDegradedExempt(t *testing.T) {
	exact := RenderOverviewPlain(100, ViewState{Repos: []overview.Repo{
		stoppedRepo("edge", manyWorktrees(20)...)}, TreeCursor: TreePos{Repo: 0, Worktree: -1}})
	lastBranch := fmt.Sprintf("   └─ branch-%02d", 19)
	assertContains(t, exact, lastBranch)
	if got := strings.Count(exact, "   ├─"); got != 19 {
		t.Errorf("%d connected children at the limit, want 19:\n%s", got, exact)
	}
	if strings.Contains(exact, "more") {
		t.Errorf("a repository at exactly maxTreeChildren must not collapse:\n%s", exact)
	}

	small := RenderOverviewPlain(100, ViewState{Repos: []overview.Repo{
		stoppedRepo("small", wt("main", true), wt("dev", true), wt("spike", false))}})
	assertContains(t, small, "   └─ spike")
	if strings.Contains(small, "more") {
		t.Errorf("a repository under maxTreeChildren must not grow an overflow line:\n%s", small)
	}

	degraded := overview.Repo{Name: "broken", Error: "overview: inspect repository boom",
		Worktrees: manyWorktrees(15)}
	dash := RenderOverviewPlain(100, ViewState{Repos: []overview.Repo{degraded}})
	// The one-line Error clamps at the pane width: assert on the prefix that
	// survives truncation.
	assertContains(t, dash, "   └─ overview:")
	if got := strings.Count(dash, "   ├─"); got != 0 {
		t.Errorf("a degraded repository must render no counted children, got %d:\n%s", got, dash)
	}
	if strings.Contains(dash, "… ") {
		t.Errorf("a degraded repository must not grow an overflow line:\n%s", dash)
	}
}

func TestRenderOverviewDegradedRepoShowsError(t *testing.T) {
	degraded := overview.Repo{Name: "broken", Error: "overview: inspect repository boom"}
	dash := RenderOverviewPlain(100, ViewState{Repos: []overview.Repo{degraded}})
	// Pane width truncates the one-line error: assert on the prefix that
	// survives both the tree child line and the ACTIVITY stage column.
	if n := strings.Count(dash, "overview: inspect re"); n < 2 {
		t.Errorf("error text must reach tree (%d hits) and activity stage:\n%s", n, dash)
	}
	assertContains(t, dash, "ATTENTION", "⚠", "· 1 attention")
	if !strings.Contains(RenderOverview(100, ViewState{Repos: []overview.Repo{degraded}}), colorPrefix(Yellow, " ● attention")) {
		t.Error("attention state must be yellow")
	}
}

func TestRenderOverviewMissingDirFlagged(t *testing.T) {
	dash := RenderOverviewPlain(100, ViewState{Repos: []overview.Repo{{Name: "ghost", Missing: true}}})
	assertContains(t, dash, "● missing", "· 1 attention")
}

func TestRenderOverviewDaemonColors(t *testing.T) {
	colored := RenderOverview(100, ViewState{Repos: []overview.Repo{liveRepo("a"), stoppedRepo("b")}})
	assertContains(t, colored, colorPrefix(Rose, " ● live"), colorPrefix(Red, " ● stopped"),
		colorPrefix(Rose, " ●"), colorPrefix(Red, " ○"),
		colorPrefix(Rose, "LIVE"), colorPrefix(Red, "STOPPED"))
	dash := RenderOverviewPlain(100, ViewState{Repos: []overview.Repo{liveRepo("a"), stoppedRepo("b")}})
	assertContains(t, dash, "LIVE", "STOPPED")
	if strings.Contains(dash, "HEALTHY") {
		t.Errorf("a stopped repository must not render as HEALTHY:\n%s", dash)
	}
}

func TestRenderOverviewEmptyRegistry(t *testing.T) {
	dash := RenderOverviewPlain(100, ViewState{Repos: nil})
	assertContains(t, dash, "no repositories registered", "REPOSITORIES",
		"LOCATION", "ACTIVITY", "navigate", "● 0 daemons")
	assertWidth(t, dash, 100)
}

func TestRenderOverviewStacksBelow84(t *testing.T) {
	dash := RenderOverviewPlain(70, ViewState{Repos: []overview.Repo{
		liveRepo("vas.sentinel", wt("main", true)), stoppedRepo("second"),
	}})
	assertContains(t, dash, "REPOSITORIES", "LOCATION", "ACTIVITY", "vas.sentinel", "second")
	assertWidth(t, dash, 70)
	if strings.Contains(dash, " │ ") {
		t.Error("stacked layout must not draw the pane separator")
	}
}

// TestRenderOverviewPlainMatchesColoredRuneParity pins the color contract:
// stripping escapes from a colored render yields the plain render for every
// fixture and both layout modes, including the Slice 11 navigation states.
func TestRenderOverviewPlainMatchesColoredRuneParity(t *testing.T) {
	// Run rows carry zero timestamps so the age column stays the
	// deterministic dash across both sequential renders. Slice 14: one run
	// carries an operation label, exercising caption + labeled-flow parity
	// at every width.
	runsRepos := []overview.Repo{
		liveRepoWithRuns("alpha",
			run("11111111111111111111", agentrun.StateRunning, 4, time.Time{}),
			runWithOperation("22222222222222222222", agentrun.StateSucceeded, 5, time.Time{}, "gate pre-push")),
		{Name: "gamma", Error: "probe failed",
			Runs: []presence.RunSummary{run("33333333333333333333", agentrun.StateAwaitingDecision, 2, time.Time{})}},
		stoppedRepo("beta", wt("dev", true)),
	}
	mixed := []overview.Repo{
		liveRepo("alpha", wt("main", false)),
		stoppedRepo("beta", wt("dev", true), wt("spike", false)),
		{Name: "gamma", Error: "probe failed"},
		{Name: "delta", Missing: true},
	}
	fixtures := []struct {
		name string
		view ViewState
	}{
		{"healthy", ViewState{Repos: []overview.Repo{liveRepo("vas.sentinel", wt("main", true))}}},
		{"multi", ViewState{Repos: []overview.Repo{stoppedRepo("multi", wt("main", true), wt("feature", false))}}},
		{"overflow", ViewState{Repos: []overview.Repo{
			stoppedRepo("big", manyWorktrees(15)...), stoppedRepo("after")}}},
		{"degraded", ViewState{Repos: []overview.Repo{{Name: "broken", Error: "overview: inspect repository boom"}}}},
		{"missing", ViewState{Repos: []overview.Repo{{Name: "ghost", Missing: true}}}},
		{"runs", ViewState{Repos: runsRepos}},
		{"mixed", ViewState{Repos: mixed}},
		{"empty", ViewState{}},
		{"run-focus", ViewState{Repos: runsRepos, Focus: FocusRuns, RunCursor: RunPos{Repo: 0, Run: 1}}},
		{"open-run", ViewState{Repos: runsRepos, OpenRunID: "11111111111111111111"}},
		{"help", ViewState{Repos: mixed, Help: true}},
	}
	for _, fx := range fixtures {
		for _, width := range []int{60, 70, 100, 140} {
			plain := RenderOverviewPlain(width, fx.view)
			colored := RenderOverview(width, fx.view)
			if plain != stripEscapes(colored) {
				t.Errorf("%s/%d: color mode altered visible content", fx.name, width)
			}
			if len(fx.view.Repos) > 0 && !strings.Contains(colored, "\x1b[38;5;") {
				t.Errorf("%s/%d: colored render emitted no escapes", fx.name, width)
			}
			assertWidth(t, plain, width)
		}
	}
}

// expectedCaptionRow rebuilds the dim column-caption row from the same
// fitRunes geometry the rows use, so the pinned bytes can never silently
// drift from the layout widths: blank icon cell (two runes), FLOW(16),
// STAGE(20), STATE(9), and the trailing AGE label.
func expectedCaptionRow() string {
	return "  " + " " + fitRunes("COMMIT", 7) + fitRunes("FLOW", 16) + fitRunes("STAGE", 20) + fitRunes("STATE", 9) + "AGE"
}

// TestOverviewActivityCaptionRowAnchors pins slice 14's caption contract:
// exactly one caption row renders immediately under the ACTIVITY heading in
// the real-data path, anchored to the same columns the run rows use — flow
// content starts where FLOW starts, the stage value where STAGE does, the
// state word where STATE does, and the age where AGE does. The mock
// dashboard stays byte-frozen without captions.
func TestOverviewActivityCaptionRowAnchors(t *testing.T) {
	want := expectedCaptionRow()
	repos := []overview.Repo{{Name: "solo",
		Runs: []presence.RunSummary{run("abcdef1234567890abcd", agentrun.StateRunning, 7, time.Time{})}}}
	// Stacked width: activityBlock needs the pane lines uninterleaved from
	// the tree, and 70 columns still fit the whole caption row.
	act := activityBlock(t, RenderOverviewPlain(70, ViewState{Repos: repos}))
	lines := strings.Split(act, "\n")
	trim := func(l string) string { return strings.TrimRight(l, " ") }
	if got := trim(lines[0]); got != want {
		t.Fatalf("first ACTIVITY line = %q, want the caption row %q", got, want)
	}
	count := 0
	for _, l := range lines {
		if trim(l) == want {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d caption rows rendered, want exactly one:\n%s", count, act)
	}

	row := trim(lines[1])
	caption := trim(lines[0])
	// Column positions are compared in RUNE space: icons like "⠹" are
	// multi-byte, so byte offsets would lie about the visible grid.
	runeIndex := func(s, sub string) int {
		i := strings.Index(s, sub)
		if i < 0 {
			return -1
		}
		return utf8.RuneCountInString(s[:i])
	}
	for _, anchor := range []struct{ captionMarker, rowMarker string }{
		{"FLOW", "abcdef123456"},
		{"STAGE", "rev 7"},
		{"STATE", "RUNNING"},
	} {
		if got, expect := runeIndex(caption, anchor.captionMarker), runeIndex(row, anchor.rowMarker); got != expect {
			t.Errorf("column %q at rune %d but row content %q at rune %d:\ncaption %q\nrow    %q",
				anchor.captionMarker, got, anchor.rowMarker, expect, caption, row)
		}
	}
	if got, expect := utf8.RuneCountInString(caption)-len("AGE"), utf8.RuneCountInString(row)-1; got != expect {
		t.Errorf("trailing AGE column at rune %d but row age at rune %d:\ncaption %q\nrow    %q",
			got, expect, caption, row)
	}

	for _, w := range []int{60, 80, 100, 140} {
		if strings.Contains(DashboardPlain(w), "FLOW") {
			t.Errorf("mock dashboard(%d) must stay caption-free (byte-frozen goldens)", w)
		}
	}
}

// TestOverviewActivityFlowUsesOperationWithIdFallback pins the flow-column
// rule: a run with an admitted operation shows it (clamped to the 16-rune
// flow width), while a run without one keeps today's 12-rune id prefix.
func TestOverviewActivityFlowUsesOperationWithIdFallback(t *testing.T) {
	repos := []overview.Repo{{Name: "solo", Runs: []presence.RunSummary{
		run("11111111111111111111", agentrun.StateRunning, 1, time.Time{}),
		runWithOperation("22222222222222222222", agentrun.StateSucceeded, 2, time.Time{}, "gate pre-push"),
	}}}
	act := activityBlock(t, RenderOverviewPlain(80, ViewState{Repos: repos}))
	assertContains(t, act, "gate pre-push")
	if strings.Contains(act, "2222222") {
		t.Errorf("the operation label must replace the id prefix:\n%s", act)
	}
	assertContains(t, act, "111111111111")
	if strings.Contains(act, "1111111111111") {
		t.Errorf("the id fallback must stay at %d runes:\n%s", maxRunFlowRunes, act)
	}

	longLabel := "review security-and-beyond"
	repos[0].Runs = append(repos[0].Runs,
		runWithOperation("33333333333333333333", agentrun.StateFailed, 3, time.Time{}, longLabel))
	act = activityBlock(t, RenderOverviewPlain(80, ViewState{Repos: repos}))
	assertContains(t, act, truncateRunes(longLabel, maxOperationFlowRunes))
	if strings.Contains(act, longLabel) {
		t.Errorf("a long operation must clamp to the %d-rune flow width:\n%s", maxOperationFlowRunes, act)
	}
}

// TestFilterReposNarrowsRunMatches verifies that a run-field query keeps the
// matching repository but removes unrelated runs from the ACTIVITY pane.
func TestFilterReposNarrowsRunMatches(t *testing.T) {
	repos := []overview.Repo{
		liveRepoWithRuns("alpha",
			runWithOperation("logic-run", agentrun.StateRunning, 1, time.Time{}, "review logic"),
			runWithOperation("style-run", agentrun.StateSucceeded, 2, time.Time{}, "review style")),
		liveRepoWithRuns("beta",
			runWithOperation("other-run", agentrun.StateSucceeded, 3, time.Time{}, "review tests")),
	}

	filtered := ProjectOverview(repos, "logic")
	if len(filtered) != 1 || filtered[0].Name != "alpha" {
		t.Fatalf("filter returned repositories %#v, want only alpha", filtered)
	}
	if len(filtered[0].Runs) != 1 || filtered[0].Runs[0].RunID != "logic-run" {
		t.Fatalf("filtered alpha runs = %#v, want only logic-run", filtered[0].Runs)
	}
}

// TestFilterReposKeepsDescendantsForRepositoryMatch verifies that matching a
// repository identity is a broad match and does not hide its run history.
func TestFilterReposKeepsDescendantsForRepositoryMatch(t *testing.T) {
	repos := []overview.Repo{liveRepoWithRuns("alpha",
		runWithOperation("logic-run", agentrun.StateRunning, 1, time.Time{}, "review logic"),
		runWithOperation("style-run", agentrun.StateSucceeded, 2, time.Time{}, "review style"))}

	filtered := ProjectOverview(repos, "alpha")
	if len(filtered) != 1 || len(filtered[0].Runs) != 2 {
		t.Fatalf("repository match narrowed descendants: %#v", filtered)
	}
}

// TestFilterReposNarrowsWorktreeMatches verifies that a worktree query keeps
// the matching child and only runs explicitly attributed to that child.
func TestFilterReposNarrowsWorktreeMatches(t *testing.T) {
	repo := liveRepoWithRuns("project",
		runWithOperation("main-run", agentrun.StateSucceeded, 1, time.Time{}, "review main"),
		runWithOperation("feature-run", agentrun.StateRunning, 2, time.Time{}, "review feature"))
	repo.Worktrees = []inventory.Worktree{wt("main", true), wt("feature", false)}
	repo.Runs[0].Worktree = repo.Worktrees[0].Path
	repo.Runs[1].Worktree = repo.Worktrees[1].Path

	filtered := ProjectOverview([]overview.Repo{repo}, "feature")
	if len(filtered) != 1 || len(filtered[0].Worktrees) != 1 ||
		filtered[0].Worktrees[0].Branch != "feature" {
		t.Fatalf("filtered worktrees = %#v, want only feature", filtered)
	}
	if len(filtered[0].Runs) != 1 || filtered[0].Runs[0].RunID != "feature-run" {
		t.Fatalf("filtered worktree runs = %#v, want only feature-run", filtered[0].Runs)
	}
}

// TestOverviewActivityFiltersBySelectedWorktree verifies that selecting a
// worktree excludes unrelated and legacy-unattributed runs from ACTIVITY.
func TestOverviewActivityFiltersBySelectedWorktree(t *testing.T) {
	repo := liveRepoWithRuns("project",
		runWithOperation("main-run", agentrun.StateSucceeded, 1, time.Time{}, "review main"),
		runWithOperation("feature-run", agentrun.StateRunning, 2, time.Time{}, "review feature"),
		run("legacy-run", agentrun.StateSucceeded, 3, time.Time{}))
	repo.Worktrees = []inventory.Worktree{wt("main", true), wt("feature", false)}
	repo.Runs[0].Worktree = repo.Worktrees[0].Path
	repo.Runs[1].Worktree = repo.Worktrees[1].Path

	act := activityBlock(t, RenderOverviewPlain(80, ViewState{
		Repos:      []overview.Repo{repo},
		TreeCursor: TreePos{Repo: 0, Worktree: 1},
	}))
	assertContains(t, act, "review feature")
	for _, hidden := range []string{"main-run", "review main", "legacy-run"} {
		if strings.Contains(act, hidden) {
			t.Errorf("selected worktree leaked %q into ACTIVITY:\n%s", hidden, act)
		}
	}
}

// TestOverviewActivityDistinguishesDefaultAndFirstWorktree pins the cursor
// sentinel: a zero-value ViewState shows all runs, while an explicit first
// worktree selection filters to its attributed runs.
func TestOverviewActivityDistinguishesDefaultAndFirstWorktree(t *testing.T) {
	repo := liveRepoWithRuns("project",
		runWithOperation("main-run", agentrun.StateSucceeded, 1, time.Time{}, "review main"),
		runWithOperation("feature-run", agentrun.StateRunning, 2, time.Time{}, "review feature"))
	repo.Worktrees = []inventory.Worktree{wt("main", true), wt("feature", false)}
	repo.Runs[0].Worktree = repo.Worktrees[0].Path
	repo.Runs[1].Worktree = repo.Worktrees[1].Path

	allRuns := activityBlock(t, RenderOverviewPlain(80, ViewState{Repos: []overview.Repo{repo}}))
	assertContains(t, allRuns, "review main", "review feature")

	firstWorktree := activityBlock(t, RenderOverviewPlain(80, ViewState{
		Repos:      []overview.Repo{repo},
		TreeCursor: TreePos{Repo: 0, Worktree: 0}, TreeCursorSet: true,
	}))
	assertContains(t, firstWorktree, "review main")
	if strings.Contains(firstWorktree, "review feature") {
		t.Errorf("explicit first worktree leaked another worktree run:\n%s", firstWorktree)
	}
}

// TestRenderOverviewFilterBar makes the filter mode observable: the query and
// an editing cursor are visible while typing, and the cursor disappears after
// the query is committed.
func TestRenderOverviewFilterBar(t *testing.T) {
	repos := []overview.Repo{liveRepoWithRuns("alpha",
		runWithOperation("logic-run", agentrun.StateRunning, 1, time.Time{}, "review logic"))}

	editing := RenderOverviewPlain(80, ViewState{Repos: repos, Filter: "logic", Filtering: true})
	assertContains(t, editing, " / FILTER: logic▏")

	committed := RenderOverviewPlain(80, ViewState{Repos: repos, Filter: "logic"})
	assertContains(t, committed, " / FILTER: logic")
	if strings.Contains(committed, "logic▏") {
		t.Errorf("committed filter still shows the editing cursor:\n%s", committed)
	}
}

// TestRenderOverviewHeaderCounts pins the summary arithmetic on a mixed
// snapshot: one live daemon, two attention repos, and zero active — since
// Slice 10 active counts non-terminal runs and this fixture carries none,
// regardless of its dirty worktree.
func TestRenderOverviewHeaderCounts(t *testing.T) {
	repos := []overview.Repo{
		liveRepo("alpha", wt("main", false)),
		stoppedRepo("beta", wt("dev", true)),
		{Name: "gamma", Error: "probe failed"},
		{Name: "delta", Missing: true},
	}
	header := strings.Split(RenderOverviewPlain(120, ViewState{Repos: repos}), "\n")[1]
	assertContains(t, header, "● 1 daemons ", "· 0 active ", "· 2 attention")
}

// TestRenderOverviewSelectionClamping pins the selection rule on the right
// pane: an in-range selected drives LOCATION to that repository, negative
// falls back to the first repository, past-the-end falls back to the last,
// and an empty registry keeps the dash placeholder without panicking. The
// renders use a stacked width so the LOCATION block is isolated from the
// tree pane's own repository names.
func TestRenderOverviewSelectionClamping(t *testing.T) {
	repos := []overview.Repo{
		liveRepo("alpha", wt("main", true)),
		stoppedRepo("beta", wt("dev", true)),
		stoppedRepo("gamma"),
	}
	tests := []struct {
		name     string
		selected int
		want     string
	}{
		{"first repository", 0, "alpha"},
		{"in range selects that repository", 1, "beta"},
		{"last repository", 2, "gamma"},
		{"negative falls back to first", -3, "alpha"},
		{"past the end falls back to last", 9, "gamma"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dash := RenderOverviewPlain(70, ViewState{Repos: repos, RepoCursor: tt.selected})
			loc := locationBlock(t, dash)
			if !strings.Contains(loc, tt.want) {
				t.Errorf("LOCATION block missing %q:\n%s", tt.want, loc)
			}
			for _, r := range repos {
				if r.Name != tt.want && strings.Contains(loc, r.Name) {
					t.Errorf("LOCATION block leaked %q while showing %q:\n%s", r.Name, tt.want, loc)
				}
			}
		})
	}
	dash := RenderOverviewPlain(70, ViewState{Repos: nil, RepoCursor: -7})
	assertContains(t, dash, "no repositories registered")
}

// locationBlock extracts the LOCATION field lines from a plain render:
// everything between the LOCATION header and the inner double rule that
// closes the block.
func locationBlock(t *testing.T, dash string) string {
	t.Helper()
	lines := strings.Split(dash, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, " LOCATION") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("render has no LOCATION header:\n%s", dash)
	}
	var block []string
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "══") {
			break
		}
		block = append(block, lines[i])
	}
	return strings.Join(block, "\n")
}

// activityBlock extracts the ACTIVITY rows from a plain render: everything
// between the ACTIVITY header and the major rule that closes the block. It
// requires a stacked width (below 84 columns) so the pane lines are not
// interleaved with the tree. Both heading shapes match because the focused
// pane prefixes its marker.
func activityBlock(t *testing.T, dash string) string {
	t.Helper()
	lines := strings.Split(dash, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, " ACTIVITY") || strings.HasPrefix(l, " ▸ ACTIVITY") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("render has no ACTIVITY header:\n%s", dash)
	}
	var block []string
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "══") {
			break
		}
		block = append(block, lines[i])
	}
	return strings.Join(block, "\n")
}

// TestOverviewActivityRunStateMapping covers every LifecycleState in the
// approved table: the running family spins blue as RUNNING,
// awaiting_decision pauses yellow as DECISION, succeeded passes green,
// failed/timed_out/unavailable fail red, and canceled/terminated settle dim
// as CANCELED. Icons inherit their state color.
func TestOverviewActivityRunStateMapping(t *testing.T) {
	tests := []struct {
		state agentrun.LifecycleState
		icon  string
		word  string
		color Color
	}{
		{agentrun.StateCreated, "⠹", "RUNNING", Blue},
		{agentrun.StateQueued, "⠹", "RUNNING", Blue},
		{agentrun.StateAdmitted, "⠹", "RUNNING", Blue},
		{agentrun.StateRunning, "⠹", "RUNNING", Blue},
		{agentrun.StateTerminating, "⠹", "RUNNING", Blue},
		{agentrun.StateAwaitingDecision, "⏸", "DECISION", Yellow},
		{agentrun.StateSucceeded, "✓", "PASSED", Green},
		{agentrun.StateFailed, "✗", "FAILED", Red},
		{agentrun.StateTimedOut, "✗", "FAILED", Red},
		{agentrun.StateUnavailable, "✗", "FAILED", Red},
		{agentrun.StateCanceled, "○", "CANCELED", Dim},
		{agentrun.StateTerminated, "○", "CANCELED", Dim},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			repos := []overview.Repo{{Name: "solo",
				Runs: []presence.RunSummary{run("abcdef1234567890abcd", tt.state, 7, time.Time{})}}}
			act := activityBlock(t, RenderOverviewPlain(70, ViewState{Repos: repos}))
			assertContains(t, act, tt.icon, tt.word, "abcdef123456", "rev 7")
			colored := RenderOverview(70, ViewState{Repos: repos})
			if !strings.Contains(colored, colorPrefix(tt.color, " "+tt.icon)) {
				t.Errorf("icon %q must inherit color %d:\n%s", tt.icon, tt.color, colored)
			}
			if plain := RenderOverviewPlain(70, ViewState{Repos: repos}); plain != stripEscapes(colored) {
				t.Error("color mode altered visible content")
			}
		})
	}
}

// TestRenderOverviewMixedRepoReplacesSummaryWithRuns pins the row layout on a
// repository carrying both worktrees and runs: its runs replace the LIVE
// summary row entirely, while the tree pane keeps rendering the worktrees.
func TestRenderOverviewMixedRepoReplacesSummaryWithRuns(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	pinClock(t, now)
	repos := []overview.Repo{liveRepo("vas.sentinel", wt("main", true), wt("feature", false))}
	repos[0].Runs = []presence.RunSummary{
		run("aaaaaaaaaaaaaaaaaaaa", agentrun.StateRunning, 4, now.Add(-2*time.Minute)),
		run("bbbbbbbbbbbbbbbbbbbb", agentrun.StateAwaitingDecision, 6, time.Time{}),
	}
	dash := RenderOverviewPlain(70, ViewState{Repos: repos})
	assertContains(t, dash, "clean", "dirty") // tree pane untouched
	act := activityBlock(t, dash)
	assertContains(t, act, "aaaaaaaaaaaa", "RUNNING", "rev 4", "02:00",
		"bbbbbbbbbbbb", "DECISION", "rev 6", "-")
	for _, leaked := range []string{"LIVE", "vas.sentinel", "worktrees"} {
		if strings.Contains(act, leaked) {
			t.Errorf("runs must replace the summary row; ACTIVITY leaked %q:\n%s", leaked, act)
		}
	}
}

// TestRenderOverviewStoppedRepoRunsReplaceSummary pins the STOPPED half of
// the suppression rule: a stopped repository carrying runs shows its runs
// instead of the red STOPPED summary row.
func TestRenderOverviewStoppedRepoRunsReplaceSummary(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	pinClock(t, now)
	repos := []overview.Repo{stoppedRepo("halted", wt("main", true))}
	repos[0].Runs = []presence.RunSummary{
		run("dddddddddddddddddddd", agentrun.StateFailed, 9, now.Add(-time.Second)),
	}
	act := activityBlock(t, RenderOverviewPlain(70, ViewState{Repos: repos}))
	assertContains(t, act, "dddddddddddd", "FAILED")
	for _, leaked := range []string{"STOPPED", "halted"} {
		if strings.Contains(act, leaked) {
			t.Errorf("runs must replace the STOPPED summary row; ACTIVITY leaked %q:\n%s", leaked, act)
		}
	}
}

// TestRenderOverviewDegradedRepoKeepsSummaryWithoutInventedRuns pins both
// halves of the degraded rule: without runs exactly one summary row renders
// (nothing invented), and runs a degraded snapshot does carry, they append
// after the kept ATTENTION row.
func TestRenderOverviewDegradedRepoKeepsSummaryWithoutInventedRuns(t *testing.T) {
	t.Run("no runs means exactly one summary row", func(t *testing.T) {
		degraded := overview.Repo{Name: "broken", Error: "overview: inspect repository boom"}
		act := activityBlock(t, RenderOverviewPlain(70, ViewState{Repos: []overview.Repo{degraded}}))
		assertContains(t, act, "ATTENTION")
		if strings.Count(act, "ATTENTION") != 1 || strings.Contains(act, "rev ") {
			t.Errorf("degraded repo must render one invented-free row:\n%s", act)
		}
	})
	t.Run("carried runs append after the summary row", func(t *testing.T) {
		degraded := overview.Repo{Name: "broken", Error: "overview: inspect repository boom",
			Runs: []presence.RunSummary{run("cccccccccccccccccccc", agentrun.StateSucceeded, 3, time.Time{})}}
		act := activityBlock(t, RenderOverviewPlain(70, ViewState{Repos: []overview.Repo{degraded}}))
		assertContains(t, act, "ATTENTION", "PASSED", "cccccccccccc", "rev 3")
	})
}

// TestRenderOverviewHeaderCountsNonTerminalRunsOnly pins the header arithmetic:
// active counts lifecycle states without a terminal class across all
// repositories' runs. Note terminated carries no terminal class yet (the
// canceled settlement stays authoritative), so it still counts as active.
func TestRenderOverviewHeaderCountsNonTerminalRunsOnly(t *testing.T) {
	repos := []overview.Repo{
		{Name: "a", Runs: []presence.RunSummary{
			run("11111111111111111111", agentrun.StateRunning, 1, time.Time{}),
			run("22222222222222222222", agentrun.StateSucceeded, 2, time.Time{}),
		}},
		{Name: "b", Runs: []presence.RunSummary{
			run("33333333333333333333", agentrun.StateFailed, 3, time.Time{}),
			run("44444444444444444444", agentrun.StateCanceled, 4, time.Time{}),
			run("55555555555555555555", agentrun.StateTerminated, 5, time.Time{}),
		}},
		{Name: "c"},
	}
	header := strings.Split(RenderOverviewPlain(120, ViewState{Repos: repos}), "\n")[1]
	assertContains(t, header, "● 0 daemons ", "· 2 active ", "· 0 attention")
}

// TestRenderOverviewEmptyRunsRepoBehaviorUnchanged pins that repositories
// without runs keep the exact pre-Slice-10 rows: classic LIVE/STOPPED
// summaries with worktree counts, and nothing run-shaped anywhere.
func TestRenderOverviewEmptyRunsRepoBehaviorUnchanged(t *testing.T) {
	repos := []overview.Repo{liveRepo("alpha", wt("main", true)), stoppedRepo("beta", wt("dev", true))}
	dash := RenderOverviewPlain(70, ViewState{Repos: repos})
	act := activityBlock(t, dash)
	assertContains(t, act, "alpha", "LIVE", "beta", "STOPPED", "1 worktree")
	if strings.Contains(act, "rev ") {
		t.Errorf("repositories without runs must not grow run rows:\n%s", act)
	}
	header := strings.Split(dash, "\n")[1]
	assertContains(t, header, "· 0 active ")
}

// TestFormatAgeCompactRules pins the compact clock: mm:ss below one hour,
// h:mm:ss afterwards, and clamping for negative durations.
func TestFormatAgeCompactRules(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{"zero", 0, "00:00"},
		{"fifty nine seconds", 59 * time.Second, "00:59"},
		{"one minute", time.Minute, "01:00"},
		{"just below an hour", 59*time.Minute + 59*time.Second, "59:59"},
		{"one hour flips format", time.Hour, "1:00:00"},
		{"hour minute second", time.Hour + time.Minute + time.Second, "1:01:01"},
		{"ten hours", 10 * time.Hour, "10:00:00"},
		{"negative clamps to zero", -5 * time.Second, "00:00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatAge(tt.d); got != tt.want {
				t.Fatalf("formatAge(%v) = %q, want %q", tt.d, got, tt.want)
			}
		})
	}
}

// runsNavRepos builds the shared navigation fixture: two live repositories
// carrying runs around a runless one, so the flat run walk crosses
// repository boundaries and skips the gap. Ids are full-length (20 runes)
// because the detail line must show the complete identifier.
func runsNavRepos() []overview.Repo {
	return []overview.Repo{
		liveRepoWithRuns("alpha",
			run("aaaaaaaaaaaaaaaaaaaa", agentrun.StateRunning, 4, time.Time{}),
			run("bbbbbbbbbbbbbbbbbbbb", agentrun.StateAwaitingDecision, 6, time.Time{})),
		liveRepo("middle"),
		liveRepoWithRuns("gamma",
			run("cccccccccccccccccccc", agentrun.StateSucceeded, 3, time.Time{})),
	}
}

// TestVisibleRunsRenderOrder pins the walk contract: run rows only, in the
// exact order the ACTIVITY pane draws them — repo by repo, stored order
// inside each repository, summary-only repositories skipped.
func TestVisibleRunsRenderOrder(t *testing.T) {
	got := VisibleRuns(ViewState{Repos: runsNavRepos()})
	want := []RunPos{{Repo: 0, Run: 0}, {Repo: 0, Run: 1}, {Repo: 2, Run: 0}}
	if len(got) != len(want) {
		t.Fatalf("VisibleRuns = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("VisibleRuns[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	if got := VisibleRuns(ViewState{}); len(got) != 0 {
		t.Errorf("VisibleRuns on an empty state = %v, want none", got)
	}
}

// TestOverviewHeadingFocusMarkers pins the heading affordance: the focused
// pane's heading gains a purple "▸" marker; unfocused headings keep the
// exact historical bytes.
func TestOverviewHeadingFocusMarkers(t *testing.T) {
	repos := runsNavRepos()
	t.Run("tree focus marks REPOSITORIES purple", func(t *testing.T) {
		colored := RenderOverview(100, ViewState{Repos: repos})
		assertContains(t, colored, colorPrefix(Purple, " ▸ REPOSITORIES"))
		if strings.Contains(colored, colorPrefix(Purple, " ▸ ACTIVITY")) {
			t.Errorf("unfocused ACTIVITY heading must not carry the marker:\n%s", colored)
		}
	})
	t.Run("runs focus marks ACTIVITY purple", func(t *testing.T) {
		colored := RenderOverview(100, ViewState{Repos: repos, Focus: FocusRuns})
		assertContains(t, colored, colorPrefix(Purple, " ▸ ACTIVITY"))
		if strings.Contains(colored, colorPrefix(Purple, " ▸ REPOSITORIES")) {
			t.Errorf("unfocused REPOSITORIES heading must not carry the marker:\n%s", colored)
		}
	})
	t.Run("unfocused headings keep the exact historical bytes", func(t *testing.T) {
		dash := RenderOverviewPlain(70, ViewState{Repos: repos, Focus: FocusRuns})
		assertContains(t, dash, "\n REPOSITORIES")
		assertContains(t, dash, " ▸ ACTIVITY")
		if strings.Contains(dash, "▸ REPOSITORIES") {
			t.Errorf("unfocused tree heading must stay unmarked:\n%s", dash)
		}
	})
}

// TestOverviewTreeCursorGlyph pins the tree affordance: only the repository
// under the cursor swaps its expand glyph for "▸" and only while the tree
// owns focus; expanded rows otherwise keep "▾" and child lines never change.
func TestOverviewTreeCursorGlyph(t *testing.T) {
	repos := []overview.Repo{liveRepo("alpha"), liveRepo("beta")}
	dash := RenderOverviewPlain(70, ViewState{Repos: repos, TreeCursor: TreePos{Repo: 1, Worktree: -1}})
	assertContains(t, dash, " ▸ alpha", " ▸ beta")
	other := RenderOverviewPlain(70, ViewState{Repos: repos, TreeCursor: TreePos{Repo: 1, Worktree: -1}, Focus: FocusRuns})
	assertContains(t, other, " ▸ alpha", " ▾ beta")
	withTrees := []overview.Repo{stoppedRepo("multi", wt("main", true), wt("feature", false))}
	child := RenderOverviewPlain(70, ViewState{Repos: withTrees, TreeCursor: TreePos{Repo: 0, Worktree: -1}})
	assertContains(t, child, "├─ main", "└─ feature")
}

// TestOverviewRunCursorPrefixOnlyFocusedRow pins the runs affordance: exactly
// one ACTIVITY row — the one at RunCursor, and only while the runs pane owns
// focus — prefixes "▸" in purple over its icon column.
func TestOverviewRunCursorPrefixOnlyFocusedRow(t *testing.T) {
	repos := runsNavRepos()
	view := ViewState{Repos: repos, Focus: FocusRuns, RunCursor: RunPos{Repo: 2, Run: 0}}
	act := activityBlock(t, RenderOverviewPlain(80, view))
	assertContains(t, act, "cccccccccccc")
	prefixed := 0
	for _, l := range strings.Split(act, "\n") {
		if strings.HasPrefix(l, "▸ ") {
			prefixed++
		}
	}
	if prefixed != 1 {
		t.Errorf("%d rows carry the focus prefix, want exactly one:\n%s", prefixed, act)
	}
	treeFocus := RenderOverviewPlain(80, ViewState{Repos: repos})
	for _, l := range strings.Split(activityBlock(t, treeFocus), "\n") {
		if strings.HasPrefix(l, "▸ ") {
			t.Fatalf("tree focus must leave every run row unprefixed:\n%s", treeFocus)
		}
	}
	colored := RenderOverview(80, view)
	if !strings.Contains(colored, colorPrefix(Purple, "▸")) {
		t.Errorf("the focus prefix must be purple:\n%s", colored)
	}
}

// TestOverviewOpenRunDetailLine pins the inline expansion: the row whose
// full run id matches OpenRunID appends exactly one dim detail line with the
// full id, state word, revision, UTC timestamp, and compact age; unknown or
// empty ids render nothing extra.
func TestOverviewOpenRunDetailLine(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	pinClock(t, now)
	repos := runsNavRepos()
	repos[0].Runs[0].UpdatedAt = now.Add(-2 * time.Minute)
	dash := RenderOverviewPlain(80, ViewState{Repos: repos, OpenRunID: "aaaaaaaaaaaaaaaaaaaa"})
	want := "   └─ aaaaaaaaaaaaaaaaaaaa · RUNNING · rev 4 · 2023-11-14T22:11:20Z · 02:00"
	assertContains(t, dash, want)
	if n := strings.Count(dash, "   └─"); n != 1 {
		t.Errorf("%d detail lines rendered, want exactly one:\n%s", n, dash)
	}
	for _, id := range []string{"", "zzzzzzzzzzzzzzzzzzzz"} {
		quiet := RenderOverviewPlain(80, ViewState{Repos: repos, OpenRunID: id})
		if strings.Contains(quiet, "   └─") {
			t.Errorf("OpenRunID %q must render no detail line:\n%s", id, quiet)
		}
	}
}

// TestOverviewHelpBlockReplacesPaneContent pins the help overlay: the KEYS
// block replaces the LOCATION+ACTIVITY content behind the same inner double
// rule, lists every navigation key, and keeps the width discipline.
func TestOverviewHelpBlockReplacesPaneContent(t *testing.T) {
	dash := RenderOverviewPlain(70, ViewState{Repos: runsNavRepos(), Help: true})
	assertContains(t, dash, " KEYS",
		"↑↓", "navigate panes",
		"tab", "focus",
		"enter", "open/close run",
		"abort", "retry",
		"/", "filter",
		"?", "help", "quit")
	if strings.Contains(dash, "LOCATION") || strings.Contains(dash, "ACTIVITY") {
		t.Errorf("help must replace the pane content wholesale:\n%s", dash)
	}
	lines := strings.Split(dash, "\n")
	keysIdx := -1
	for i, l := range lines {
		if strings.HasPrefix(l, " KEYS") {
			keysIdx = i
			break
		}
	}
	if keysIdx < 0 {
		t.Fatalf("no KEYS heading:\n%s", dash)
	}
	innerRule := false
	for i := 0; i < keysIdx; i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "══") {
			innerRule = true
		}
	}
	if !innerRule {
		t.Errorf("the inner double rule must survive above KEYS:\n%s", dash)
	}
	assertWidth(t, dash, 70)
}

// TestRunRowSanitizesOperationLabels pins the render-boundary hygiene rule:
// control characters inside an operation label (operator-supplied command
// text) never reach the row bytes.
func TestRunRowSanitizesOperationLabels(t *testing.T) {
	row := runRow(presence.RunSummary{
		RunID: "aaaaaaaaaaaaaaaaaaaa", State: agentrun.StateRunning,
		Operation: "validate go\ttest\n./... \x1b[31m--race",
	})
	if strings.ContainsAny(row.flow, "\n\t\x1b") {
		t.Errorf("control characters leaked into the flow column: %q", row.flow)
	}
	if row.flow != "validate gotest." {
		t.Errorf("sanitized label truncated unexpectedly: %q", row.flow)
	}
}
