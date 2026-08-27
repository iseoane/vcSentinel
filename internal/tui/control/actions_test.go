package control

// Run-actions coverage pairing with slice 12: the a/r gating matrix over real
// Update transitions, the pending-marker lifecycle, and the result-message
// semantics (stale results ignored whole, rejections overwrite Err, success
// stays silent). Everything runs synchronously through Update — no terminal,
// no sleeps.

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/bubbletea"

	"github.com/ISeoane-Quental/vas.sentinel/internal/inventory"
	"github.com/ISeoane-Quental/vas.sentinel/internal/overview"
	"github.com/ISeoane-Quental/vas.sentinel/internal/presence"
	"github.com/ISeoane-Quental/vas.sentinel/internal/tui/art"
)

// spyActions records every hook invocation in dispatch order and answers with
// a command yielding the matching action-result message, staging s.err as the
// rejection when set.
type spyActions struct {
	calls []string
	err   error
}

func (s *spyActions) Abort(repoPath, runID string) tea.Cmd {
	return s.record(KindAbort, repoPath, runID)
}

func (s *spyActions) Retry(repoPath, runID string) tea.Cmd {
	return s.record(KindRetry, repoPath, runID)
}

func (s *spyActions) record(kind, repoPath, runID string) tea.Cmd {
	s.calls = append(s.calls, kind+" "+repoPath+" "+runID)
	err := s.err
	return func() tea.Msg {
		return actionResultMsg{Kind: kind, RepoPath: repoPath, RunID: runID, Err: err}
	}
}

// actionRepos builds one healthy repository plus disabled, missing, and
// degraded twins, each carrying exactly one run row so the gating matrix can
// park the cursor on every kind of row.
func actionRepos() []overview.Repo {
	disabled := repoWithRuns("disabled", "bbbbbbbbbbbbbbbbbbbb")
	disabled.Enabled = false
	missing := repoWithRuns("missing", "cccccccccccccccccccc")
	missing.Missing = true
	degraded := repoWithRuns("degraded", "dddddddddddddddddddd")
	degraded.Error = "probe failed"
	return []overview.Repo{
		repoWithRuns("healthy", "aaaaaaaaaaaaaaaaaaaa"),
		disabled,
		missing,
		degraded,
	}
}

// focused builds a live model with actions wired, runs focus, and the runs
// cursor parked on visible[start]; start -1 plants a stale off-walk cursor.
func focused(repos []overview.Repo, acts RunActions, start int) Model {
	m := NewLive(repos, (&recorder{}).refresh, time.Second, acts)
	m.focus = art.FocusRuns
	if start >= 0 {
		m.runCursor = art.VisibleRuns(art.ViewState{Repos: repos})[start]
	} else {
		m.runCursor = art.RunPos{Repo: 9, Run: 9}
	}
	return m
}

// TestRunActionGatingMatrix drives every gate cell: a/r only fire when help
// is closed, the runs pane owns focus, the cursor rests on a currently
// visible run row, its repository is enabled/non-missing/error-free, and an
// actions hook is wired. Every unmet gate is a pure no-op.
func TestRunActionGatingMatrix(t *testing.T) {
	repos := actionRepos()
	visible := art.VisibleRuns(art.ViewState{Repos: repos})
	if len(visible) != 4 {
		t.Fatalf("fixture walk = %v, want one row per fixture repository", visible)
	}
	tests := []struct {
		name      string
		start     int  // cursor position into the fixture walk (-1: stale)
		focusRuns bool // runs pane owns focus
		helpOpen  bool
		key       string
		wantCall  string // "" proves the hooks were never contacted
	}{
		{
			name:  "abort fires on a healthy repository's run",
			start: 0, focusRuns: true, key: "a",
			wantCall: "abort " + filepath.Join("/tmp", "healthy") + " aaaaaaaaaaaaaaaaaaaa",
		},
		{
			name:  "retry fires on a healthy repository's run",
			start: 0, focusRuns: true, key: "r",
			wantCall: "retry " + filepath.Join("/tmp", "healthy") + " aaaaaaaaaaaaaaaaaaaa",
		},
		{
			name:  "disabled repository never dispatches",
			start: 1, focusRuns: true, key: "a",
			wantCall: "",
		},
		{
			name:  "missing repository never dispatches",
			start: 2, focusRuns: true, key: "r",
			wantCall: "",
		},
		{
			name:  "degraded repository never dispatches",
			start: 3, focusRuns: true, key: "a",
			wantCall: "",
		},
		{
			name:  "tree focus never dispatches",
			start: 0, focusRuns: false, key: "a",
			wantCall: "",
		},
		{
			name:  "a stale off-walk cursor never dispatches",
			start: -1, focusRuns: true, key: "a",
			wantCall: "",
		},
		{
			name:  "an open help overlay swallows the key",
			start: 0, focusRuns: true, helpOpen: true, key: "a",
			wantCall: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &spyActions{}
			m := focused(repos, spy, tt.start)
			if !tt.focusRuns {
				m.focus = art.FocusTree
			}
			m.help = tt.helpOpen

			next, cmd := m.Update(keyMsg(tt.key))
			after := next.(Model)

			if tt.wantCall == "" {
				if len(spy.calls) != 0 {
					t.Fatalf("hooks were contacted: %v", spy.calls)
				}
				if cmd != nil {
					t.Errorf("%s scheduled %v, want a pure no-op", tt.key, cmd)
				}
				if _, _, _, ok := after.PendingAction(); ok {
					t.Errorf("rejected %s left a pending marker behind", tt.key)
				}
				if tt.helpOpen && !after.HelpVisible() {
					// Existing contract: the swallowed key closes help first.
				} else if tt.helpOpen {
					t.Errorf("the swallowed %s did not close help", tt.key)
				}
				return
			}
			if len(spy.calls) != 1 || spy.calls[0] != tt.wantCall {
				t.Fatalf("hook calls = %v, want exactly [%s]", spy.calls, tt.wantCall)
			}
			if cmd == nil {
				t.Fatalf("%s dispatched without returning the hook command", tt.key)
			}
			wantKind, wantPath, wantRun := KindAbort, filepath.Join("/tmp", "healthy"), "aaaaaaaaaaaaaaaaaaaa"
			if tt.key == "r" {
				wantKind = KindRetry
			}
			kind, repoPath, runID, ok := after.PendingAction()
			if !ok {
				t.Fatal("dispatch did not raise the pending marker")
			}
			if kind != wantKind || repoPath != wantPath || runID != wantRun {
				t.Errorf("pending = (%q,%q,%q), want (%q,%q,%q)",
					kind, repoPath, runID, wantKind, wantPath, wantRun)
			}
			// The handed-out command must be the hook's own: invoking it
			// yields the matching completion message.
			msg := cmd()
			result, ok := msg.(actionResultMsg)
			if !ok {
				t.Fatalf("hook command yielded %T, want actionResultMsg", msg)
			}
			if result.Kind != kind || result.RepoPath != repoPath || result.RunID != runID {
				t.Errorf("completion = %+v, want it to echo the pending marker", result)
			}
		})
	}
}

func TestAggregateGateRootActionsAreDisabled(t *testing.T) {
	const reason = "gate root is an aggregate; abort and retry are unavailable"
	repo := repoWithRuns("gate", "gate-root")
	repo.Runs[0].ControlDisabledReason = reason
	for _, key := range []string{"a", "r"} {
		spy := &spyActions{}
		m := focused([]overview.Repo{repo}, spy, 0)
		next, cmd := m.Update(keyMsg(key))
		after := next.(Model)
		if cmd != nil || len(spy.calls) != 0 {
			t.Fatalf("aggregate %s dispatched cmd=%v calls=%v", key, cmd, spy.calls)
		}
		if _, _, _, ok := after.PendingAction(); ok || after.Err() != reason {
			t.Fatalf("aggregate %s feedback = pending=%v err=%q", key, ok, after.Err())
		}
	}

	spy := &spyActions{}
	ordinary := focused([]overview.Repo{repoWithRuns("ordinary", "ordinary-run")}, spy, 0)
	if _, cmd := ordinary.Update(keyMsg("a")); cmd == nil || len(spy.calls) != 1 {
		t.Fatalf("ordinary run did not dispatch: cmd=%v calls=%v", cmd, spy.calls)
	}
}

// TestFilteredRunActionsTargetVisibleRows proves that the action target uses
// the same filtered coordinates as the ACTIVITY rows. It covers broad
// repository matches and narrowed run matches for both actions.
func TestFilteredRunActionsTargetVisibleRows(t *testing.T) {
	alpha := repoWithRuns("alpha", "alpha-run")
	alpha.Runs[0].Operation = "review alpha"
	alpha.Runs = append(alpha.Runs, presence.RunSummary{
		RunID: "target-run", State: "running", Operation: "review target",
	})
	beta := repoWithRuns("beta", "beta-run")
	repos := []overview.Repo{alpha, beta}

	tests := []struct {
		name     string
		filter   string
		action   string
		wantCall string
	}{
		{"abort targets the filtered repository", "beta", "a", "abort " + filepath.Join("/tmp", "beta") + " beta-run"},
		{"retry targets the filtered repository", "beta", "r", "retry " + filepath.Join("/tmp", "beta") + " beta-run"},
		{"abort targets the filtered run", "target", "a", "abort " + filepath.Join("/tmp", "alpha") + " target-run"},
		{"retry targets the filtered run", "target", "r", "retry " + filepath.Join("/tmp", "alpha") + " target-run"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := &spyActions{}
			m := NewLive(repos, (&recorder{}).refresh, time.Second, spy)
			m = pressKeys(t, m, "/", tt.filter, "enter", "tab")
			_, cmd := m.Update(keyMsg(tt.action))
			if cmd == nil {
				t.Fatalf("filtered %s did not dispatch an action", tt.action)
			}
			if len(spy.calls) != 1 || spy.calls[0] != tt.wantCall {
				t.Fatalf("action calls = %v, want [%s]", spy.calls, tt.wantCall)
			}
		})
	}
}

// TestRunActionsFollowSelectedWorktree proves that changing the tree
// selection reanchors the run cursor before an action is dispatched. The
// hidden run must never receive the action when ACTIVITY shows only the
// selected worktree's run.
func TestRunActionsFollowSelectedWorktree(t *testing.T) {
	r := repoWithRuns("project", "main-run", "feature-run")
	r.Worktrees = []inventory.Worktree{
		{Path: filepath.Join("/tmp", "project", "main"), Branch: "main", Clean: true},
		{Path: filepath.Join("/tmp", "project", "feature"), Branch: "feature", Clean: false},
	}
	r.Runs[0].Worktree = r.Worktrees[0].Path
	r.Runs[1].Worktree = r.Worktrees[1].Path
	spy := &spyActions{}
	m := NewLive([]overview.Repo{r}, (&recorder{}).refresh, time.Second, spy)
	m = pressKeys(t, m, "down", "down", "tab")
	_, cmd := m.Update(keyMsg("a"))
	if cmd == nil {
		t.Fatal("action on the selected worktree did not dispatch")
	}
	wantCall := "abort " + filepath.Join("/tmp", "project") + " feature-run"
	if len(spy.calls) != 1 || spy.calls[0] != wantCall {
		t.Fatalf("action calls = %v, want the selected worktree run", spy.calls)
	}
}

func TestRunActionsPreserveSelectedRunAcrossRefresh(t *testing.T) {
	const runID = "shared-run"
	left, right := repo("left"), repo("right")
	left.Runs = []presence.RunSummary{{RunID: runID, State: "running"}}
	right.Runs = []presence.RunSummary{{RunID: runID, State: "running"}}
	spy := &spyActions{}
	m := NewLive([]overview.Repo{left, right}, (&recorder{}).refresh, time.Second, spy)
	m.focus, m.runCursor = art.FocusRuns, art.RunPos{Repo: 0, Run: 0}
	fresh := left
	fresh.Runs = append([]presence.RunSummary{{RunID: "newer", State: "running"}}, left.Runs...)
	m = update(t, m, snapshotMsg{repos: []overview.Repo{fresh, right}})
	_, cmd := m.Update(keyMsg("a"))
	want := "abort " + filepath.Join("/tmp", "left") + " " + runID
	if cmd == nil || len(spy.calls) != 1 || spy.calls[0] != want {
		t.Fatalf("refresh action = cmd %v calls %v, want [%s]", cmd, spy.calls, want)
	}
}

// TestRunFilterClearsWorktreeScope proves a new global query does not leave
// ACTIVITY constrained to a previously selected, unrelated worktree.
func TestRunFilterClearsWorktreeScope(t *testing.T) {
	r := repoWithRuns("project", "main-run", "target-run")
	r.Worktrees = []inventory.Worktree{
		{Path: filepath.Join("/tmp", "project", "main"), Branch: "main", Clean: true},
		{Path: filepath.Join("/tmp", "project", "feature"), Branch: "feature", Clean: false},
	}
	r.Runs[0].Worktree = r.Worktrees[0].Path
	r.Runs[1].Worktree = r.Worktrees[1].Path
	spy := &spyActions{}
	m := NewLive([]overview.Repo{r}, (&recorder{}).refresh, time.Second, spy)
	m = pressKeys(t, m, "down", "down", "/", "target", "enter", "tab")
	_, cmd := m.Update(keyMsg("a"))
	if cmd == nil || len(spy.calls) != 1 {
		t.Fatalf("filtered action cmd=%v calls=%v, want the matching run", cmd, spy.calls)
	}
	want := "abort " + filepath.Join("/tmp", "project") + " target-run"
	if spy.calls[0] != want {
		t.Fatalf("filtered action call = %q, want %q", spy.calls[0], want)
	}
}

// TestRunActionNilActionsArePureNoOps pins the disabled-seam contract: with a
// nil interface both keys are inert on live models, and static New models
// (which never carry actions) behave identically.
func TestRunActionNilActionsArePureNoOps(t *testing.T) {
	repos := actionRepos()
	live := focused(repos, nil, 0)
	for _, key := range []string{"a", "r"} {
		next, cmd := live.Update(keyMsg(key))
		if cmd != nil {
			t.Fatalf("nil-actions %s scheduled %v", key, cmd)
		}
		if _, _, _, ok := next.(Model).PendingAction(); ok {
			t.Fatalf("nil-actions %s raised a pending marker", key)
		}
	}
	static := New(repos)
	static.focus = art.FocusRuns
	next, cmd := static.Update(keyMsg("a"))
	if cmd != nil {
		t.Fatalf("static model a scheduled %v", cmd)
	}
	if _, _, _, ok := next.(Model).PendingAction(); ok {
		t.Fatal("static model raised a pending marker")
	}
}

// TestRunActionResultLifecycle pins the completion semantics: only the
// message matching the latest dispatch clears the marker, rejections land on
// Err with overwrite semantics, and success stays silent.
func TestRunActionResultLifecycle(t *testing.T) {
	const (
		runA = "aaaaaaaaaaaaaaaaaaaa"
		runB = "bbbbbbbbbbbbbbbbbbbb"
	)
	path := filepath.Join("/tmp", "healthy")

	t.Run("matching failure clears pending and lands on Err", func(t *testing.T) {
		m := focused(actionRepos(), &spyActions{}, 0)
		m = update(t, m, keyMsg("a"))
		m = update(t, m, actionResultMsg{Kind: KindAbort, RepoPath: path, RunID: runA,
			Err: errors.New("host refused")})
		if _, _, _, ok := m.PendingAction(); ok {
			t.Error("the matching result did not clear the pending marker")
		}
		if got := m.Err(); got != "host refused" {
			t.Errorf("Err() = %q, want the rejection text", got)
		}
	})
	t.Run("success clears quietly without touching Err", func(t *testing.T) {
		m := focused(actionRepos(), &spyActions{}, 0)
		m = update(t, m, snapshotMsg{err: errors.New("planted refresh failure")})
		m = update(t, m, keyMsg("r"))
		m = update(t, m, actionResultMsg{Kind: KindRetry, RepoPath: path, RunID: runA})
		if _, _, _, ok := m.PendingAction(); ok {
			t.Error("a successful result did not clear the pending marker")
		}
		if got := m.Err(); got != "planted refresh failure" {
			t.Errorf("silent success rewrote Err(): got %q, want the untouched refresh error", got)
		}
	})
	t.Run("action failure overwrites a prior refresh error", func(t *testing.T) {
		m := focused(actionRepos(), &spyActions{}, 0)
		m = update(t, m, snapshotMsg{err: errors.New("planted refresh failure")})
		m = update(t, m, keyMsg("a"))
		m = update(t, m, actionResultMsg{Kind: KindAbort, RepoPath: path, RunID: runA,
			Err: errors.New("abort rejected")})
		if got := m.Err(); got != "abort rejected" {
			t.Errorf("Err() = %q, want the newest action feedback to win", got)
		}
	})
	t.Run("stale mismatched results are ignored whole", func(t *testing.T) {
		m := focused(actionRepos(), &spyActions{}, 0)
		m = update(t, m, keyMsg("a"))
		kind, repoPath, runID, ok := m.PendingAction()
		if !ok {
			t.Fatal("dispatch lost its marker before the stale results arrived")
		}
		stales := []actionResultMsg{
			{Kind: KindAbort, RepoPath: path, RunID: runB, Err: errors.New("wrong run")},
			{Kind: KindRetry, RepoPath: path, RunID: runA, Err: errors.New("wrong kind")},
			{Kind: KindAbort, RepoPath: filepath.Join("/tmp", "elsewhere"), RunID: runA, Err: errors.New("wrong repo")},
		}
		for _, stale := range stales {
			m = update(t, m, stale)
			gotKind, gotPath, gotRun, stillOK := m.PendingAction()
			if !stillOK || gotKind != kind || gotPath != repoPath || gotRun != runID {
				t.Fatalf("stale result %+v disturbed the marker: got (%q,%q,%q,%t)",
					stale, gotKind, gotPath, gotRun, stillOK)
			}
			if m.Err() != "" {
				t.Fatalf("stale result leaked its error into Err(): %q", m.Err())
			}
		}
	})
	t.Run("moving the cursor after dispatch does not clear pending", func(t *testing.T) {
		m := focused(actionRepos(), &spyActions{}, 0)
		m = update(t, m, keyMsg("a"))
		m = update(t, m, keyMsg("j")) // walks onto the next visible row
		if m.RunCursorPosition() == (art.RunPos{Repo: 0, Run: 0}) {
			t.Fatal("fixture setup failed: the cursor did not move")
		}
		gotKind, _, gotRun, ok := m.PendingAction()
		if !ok || gotKind != KindAbort || gotRun != runA {
			t.Fatalf("cursor movement disturbed the marker: (%q,%q,%t)", gotKind, gotRun, ok)
		}
		m = update(t, m, actionResultMsg{Kind: KindAbort, RepoPath: path, RunID: runA})
		if _, _, _, ok := m.PendingAction(); ok {
			t.Error("only the matching result may clear the marker")
		}
	})
	t.Run("a snapshot swap between dispatch and result keeps pending", func(t *testing.T) {
		m := focused(actionRepos(), &spyActions{}, 0)
		m = update(t, m, keyMsg("a"))
		m = update(t, m, snapshotMsg{repos: []overview.Repo{repo("fresh")}})
		if _, _, _, ok := m.PendingAction(); !ok {
			t.Fatal("a snapshot swap cleared a still-unresolved marker")
		}
		m = update(t, m, actionResultMsg{Kind: KindAbort, RepoPath: path, RunID: runA})
		if _, _, _, ok := m.PendingAction(); ok {
			t.Error("the matching result stopped clearing the marker after a swap")
		}
	})
	t.Run("a second action cannot replace a pending dispatch", func(t *testing.T) {
		spy := &spyActions{}
		m := focused(actionRepos(), spy, 0)
		m = update(t, m, keyMsg("a"))
		next, cmd := m.Update(keyMsg("r"))
		if cmd != nil || len(spy.calls) != 1 {
			t.Fatalf("second action dispatched cmd=%v calls=%v, want no dispatch", cmd, spy.calls)
		}
		kind, repoPath, runID, ok := next.(Model).PendingAction()
		if !ok || kind != KindAbort || repoPath != path || runID != runA {
			t.Fatalf("second action replaced pending marker: (%q,%q,%q,%t)", kind, repoPath, runID, ok)
		}
	})
}

// TestActionResultConstructorIsConsumable pins the exported production seam:
// the command built through ActionResult yields exactly the private message
// Update consumes, so external implementations cannot drift off-contract.
func TestActionResultConstructorIsConsumable(t *testing.T) {
	msg := ActionResult(KindRetry, filepath.Join("/tmp", "healthy"), "aaaaaaaaaaaaaaaaaaaa",
		errors.New("retry rejected"))()
	result, ok := msg.(actionResultMsg)
	if !ok {
		t.Fatalf("ActionResult yielded %T, want actionResultMsg", msg)
	}
	if result.Kind != KindRetry || result.RepoPath != filepath.Join("/tmp", "healthy") ||
		result.RunID != "aaaaaaaaaaaaaaaaaaaa" || result.Err == nil {
		t.Fatalf("ActionResult payload = %+v, want the constructed identity plus error", result)
	}
	m := focused(actionRepos(), &spyActions{}, 0)
	m = update(t, m, keyMsg("r")) // raise the marker the retry result will match
	m = update(t, m, msg)
	if _, _, _, ok := m.PendingAction(); ok {
		t.Error("constructor-built result did not clear the matching marker")
	}
	if got := m.Err(); got != "retry rejected" {
		t.Errorf("Err() = %q, want the carried rejection", got)
	}
}
