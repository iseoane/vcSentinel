package control

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbletea"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/overview"
	"github.com/ISeoane-Quental/vas.sentinel/internal/presence"
	"github.com/ISeoane-Quental/vas.sentinel/internal/tui/art"
)

// Update-transition and View coverage pairing with control.go. The style
// mirrors internal/tui: drive Update directly with constructed messages, no
// teatest, no terminal.

// repo builds a minimal stopped snapshot entry; only the identity fields the
// LOCATION block renders are needed.
func repo(name string) overview.Repo {
	return overview.Repo{Name: name, Path: filepath.Join("/tmp", name), Enabled: true,
		Origin: "git@github.com:org/" + name}
}

// repoWithRuns builds a repository whose snapshot carries durable-run
// summaries with fixed ids; state and revision only feed rendering.
func repoWithRuns(name string, ids ...string) overview.Repo {
	r := repo(name)
	for _, id := range ids {
		r.Runs = append(r.Runs, presence.RunSummary{RunID: id, State: agentrun.StateRunning, Revision: 1})
	}
	return r
}

// navRepos builds the shared dual-pane registry: two repositories carrying
// runs around one runless repository, so the flat run walk crosses
// repository boundaries and skips the gap.
func navRepos() []overview.Repo {
	return []overview.Repo{
		repoWithRuns("alpha", "aaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbb"),
		repo("middle"),
		repoWithRuns("gamma", "cccccccccccccccccccc"),
	}
}

// keyMsg builds a tea.KeyMsg the way a real terminal would deliver it.
func keyMsg(key string) tea.KeyMsg {
	switch key {
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+c":
		return tea.KeyMsg{Type: tea.KeyCtrlC}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case "shift+tab":
		return tea.KeyMsg{Type: tea.KeyShiftTab}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEscape}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}

// update drives one message through m and returns the next model.
func update(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(Model)
}

// pressKeys drives m through a sequence of key presses.
func pressKeys(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		m = update(t, m, keyMsg(k))
	}
	return m
}

func TestNewDefaults(t *testing.T) {
	repos := []overview.Repo{repo("alpha")}
	m := New(repos)
	if m.Width() != 80 {
		t.Errorf("initial width = %d, want the documented default 80", m.Width())
	}
	if m.Selected() != 0 {
		t.Errorf("initial selection = %d, want the first repository", m.Selected())
	}
	if m.Quitting() {
		t.Errorf("a fresh model must not be quitting")
	}
	if got := m.Repos(); len(got) != 1 || got[0].Name != "alpha" {
		t.Errorf("Repos() = %v, want the constructor snapshot untouched", got)
	}
}

// TestInitSchedulesNothing pins this slice's non-goal: no refresh loop means
// Init returns no command.
func TestInitSchedulesNothing(t *testing.T) {
	if cmd := New(nil).Init(); cmd != nil {
		t.Fatalf("Init scheduled %v, want nothing", cmd)
	}
}

func TestUpdateKeyRouting(t *testing.T) {
	repos := []overview.Repo{repo("alpha"), repo("beta"), repo("gamma")}
	single := []overview.Repo{repo("only")}
	tests := []struct {
		name   string
		setup  func(*testing.T) Model
		key    string
		assert func(t *testing.T, before Model, after Model, cmd tea.Cmd)
	}{
		{
			name:  "down moves the cursor",
			setup: func(t *testing.T) Model { return New(repos) },
			key:   "down",
			assert: func(t *testing.T, before Model, after Model, cmd tea.Cmd) {
				want := before.Selected() + 1
				if after.Selected() != want {
					t.Errorf("selected = %d, want %d", after.Selected(), want)
				}
			},
		},
		{
			name:  "j moves the cursor",
			setup: func(t *testing.T) Model { return New(repos) },
			key:   "j",
			assert: func(t *testing.T, before Model, after Model, cmd tea.Cmd) {
				if after.Selected() != 1 {
					t.Errorf("selected = %d, want 1", after.Selected())
				}
			},
		},
		{
			name:  "k moves the cursor up",
			setup: func(t *testing.T) Model { return pressKeys(t, New(repos), "down", "down") },
			key:   "k",
			assert: func(t *testing.T, before Model, after Model, cmd tea.Cmd) {
				want := before.Selected() - 1
				if after.Selected() != want {
					t.Errorf("selected = %d, want %d", after.Selected(), want)
				}
			},
		},
		{
			name:  "up clamps at the top",
			setup: func(t *testing.T) Model { return New(repos) },
			key:   "up",
			assert: func(t *testing.T, before Model, after Model, cmd tea.Cmd) {
				if after.Selected() != 0 {
					t.Errorf("selected = %d, want clamped at 0", after.Selected())
				}
			},
		},
		{
			name:  "down clamps at the bottom",
			setup: func(t *testing.T) Model { return pressKeys(t, New(repos), "down", "down") },
			key:   "down",
			assert: func(t *testing.T, before Model, after Model, cmd tea.Cmd) {
				if after.Selected() != len(repos)-1 {
					t.Errorf("selected = %d, want clamped at %d", after.Selected(), len(repos)-1)
				}
			},
		},
		{
			name:  "single repository ignores down",
			setup: func(t *testing.T) Model { return New(single) },
			key:   "down",
			assert: func(t *testing.T, before Model, after Model, cmd tea.Cmd) {
				if after.Selected() != 0 {
					t.Errorf("selected = %d, want 0 on a single-repository registry", after.Selected())
				}
			},
		},
		{
			name:  "single repository ignores up",
			setup: func(t *testing.T) Model { return New(single) },
			key:   "up",
			assert: func(t *testing.T, before Model, after Model, cmd tea.Cmd) {
				if after.Selected() != 0 {
					t.Errorf("selected = %d, want 0 on a single-repository registry", after.Selected())
				}
			},
		},
		{
			name:  "empty registry ignores down",
			setup: func(t *testing.T) Model { return New(nil) },
			key:   "down",
			assert: func(t *testing.T, before Model, after Model, cmd tea.Cmd) {
				if after.Selected() != 0 {
					t.Errorf("selected = %d, want no move on an empty registry", after.Selected())
				}
			},
		},
		{
			name:  "q flags quitting and returns the quit command",
			setup: func(t *testing.T) Model { return New(repos) },
			key:   "q",
			assert: func(t *testing.T, before Model, after Model, cmd tea.Cmd) {
				if !after.Quitting() {
					t.Errorf("q did not flag quitting")
				}
				if cmd == nil {
					t.Fatalf("q returned no command, want tea.Quit")
				}
				if msg := cmd(); msg == nil {
					t.Errorf("the q command produced no message")
				}
			},
		},
		{
			name:  "ctrl+c flags quitting and returns the quit command",
			setup: func(t *testing.T) Model { return New(repos) },
			key:   "ctrl+c",
			assert: func(t *testing.T, before Model, after Model, cmd tea.Cmd) {
				if !after.Quitting() {
					t.Errorf("ctrl+c did not flag quitting")
				}
				if cmd == nil {
					t.Errorf("ctrl+c returned no command, want tea.Quit")
				}
			},
		},
		{
			name:  "unknown key leaves the whole model untouched",
			setup: func(t *testing.T) Model { return pressKeys(t, New(repos), "down") },
			key:   "z",
			assert: func(t *testing.T, before Model, after Model, cmd tea.Cmd) {
				if after.Width() != before.Width() || after.Selected() != before.Selected() ||
					after.Quitting() != before.Quitting() ||
					len(after.Repos()) != len(before.Repos()) {
					t.Errorf("unknown key changed the model: width %d→%d selected %d→%d quitting %v→%v repos %d→%d",
						before.Width(), after.Width(), before.Selected(), after.Selected(),
						before.Quitting(), after.Quitting(), len(before.Repos()), len(after.Repos()))
				}
				if cmd != nil {
					t.Errorf("unmapped key z scheduled %v", cmd)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.setup(t)
			before := m
			next, cmd := m.Update(keyMsg(tt.key))
			tt.assert(t, before, next.(Model), cmd)
		})
	}
}

// TestFilterInputIsVisibleAndDoesNotTriggerShortcuts pins the filter input
// contract: slash opens an observable query bar, printable shortcut-looking
// runes stay in the query, and the query narrows the rendered repositories.
func TestFilterInputIsVisibleAndDoesNotTriggerShortcuts(t *testing.T) {
	m := New([]overview.Repo{repo("alpha"), repo("beta")})
	m = update(t, m, keyMsg("/"))
	if !m.filtering {
		t.Fatal("slash did not enter filtering mode")
	}
	if view := stripANSI(m.View()); !strings.Contains(view, " / FILTER: ▏") {
		t.Fatalf("empty filter bar is not visible:\n%s", view)
	}

	m = update(t, m, keyMsg("q"))
	m = update(t, m, keyMsg("?"))
	if m.Quitting() || m.HelpVisible() {
		t.Fatalf("filter runes triggered global shortcuts: quitting=%v help=%v", m.Quitting(), m.HelpVisible())
	}
	if view := stripANSI(m.View()); !strings.Contains(view, " / FILTER: q?▏") {
		t.Fatalf("typed filter is not visible:\n%s", view)
	}

	m = update(t, m, keyMsg("backspace"))
	if !strings.Contains(stripANSI(m.View()), " / FILTER: q▏") {
		t.Fatalf("backspace did not remove one query rune:\n%s", m.View())
	}

	m = update(t, m, keyMsg("esc"))
	if m.filtering {
		t.Fatal("escape did not leave filtering mode")
	}
}

// TestFilterInputNarrowsView verifies the control model passes the active
// query through to the renderer instead of only storing it internally.
func TestFilterInputNarrowsView(t *testing.T) {
	m := New([]overview.Repo{repo("alpha"), repo("beta")})
	m = pressKeys(t, m, "/", "beta")
	out := stripANSI(m.View())
	if !strings.Contains(out, "beta") {
		t.Fatalf("filtered view lost the matching repository:\n%s", out)
	}
	if strings.Contains(out, "alpha") {
		t.Fatalf("filtered view still renders the non-matching repository:\n%s", out)
	}
}

func TestUpdateWindowResize(t *testing.T) {
	tests := []struct {
		name  string
		width int
		want  int
	}{
		{"tiny terminal floors at 40", 20, 40},
		{"just below the floor", 39, 40},
		{"exactly the floor", 40, 40},
		{"honest width is kept", 100, 100},
		{"wide terminal is kept", 120, 120},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := update(t, New(nil), tea.WindowSizeMsg{Width: tt.width, Height: 24})
			if m.Width() != tt.want {
				t.Errorf("WindowSizeMsg(width=%d) left width = %d, want %d", tt.width, m.Width(), tt.want)
			}
		})
	}
}

// stripANSI removes ANSI escape sequences so assertions match visible runes.
func stripANSI(s string) string {
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

// locationBlock extracts the LOCATION field lines from a View render made at
// a stacked width (<84 columns): everything between the LOCATION header and
// the inner double rule that closes the block. Stacked layout keeps the
// right-pane content isolated from the tree pane's own repository names.
func locationBlock(t *testing.T, view string) string {
	t.Helper()
	lines := strings.Split(view, "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, " LOCATION") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("view has no LOCATION header:\n%s", view)
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

// TestViewRendersSelectedRepository drives the cursor to the second
// repository and asserts its name and path drive the LOCATION block instead
// of any other repository's.
func TestViewRendersSelectedRepository(t *testing.T) {
	m := pressKeys(t, New([]overview.Repo{repo("alpha"), repo("beta"), repo("gamma")}), "down")
	m = update(t, m, tea.WindowSizeMsg{Width: 70, Height: 24}) // stacked layout
	loc := locationBlock(t, stripANSI(m.View()))
	if !strings.Contains(loc, "beta") {
		t.Errorf("LOCATION block missing the selected repository \"beta\":\n%s", loc)
	}
	if !strings.Contains(loc, filepath.Join("/tmp", "beta")) {
		t.Errorf("LOCATION block missing the selected beta path:\n%s", loc)
	}
	for _, other := range []string{"alpha", "gamma"} {
		if strings.Contains(loc, other) {
			t.Errorf("LOCATION block leaked unselected %q:\n%s", other, loc)
		}
	}
}

// TestViewEmptyRegistryRendersEmptyState proves View never panics on an
// empty registry (the renderer clamps out-of-range selections), shows the
// approved empty state, and honors the default width before any window-size
// message arrives.
func TestViewEmptyRegistryRendersEmptyState(t *testing.T) {
	m := New(nil)
	out := stripANSI(m.View())
	if !strings.Contains(out, "no repositories registered") {
		t.Errorf("empty registry view missing the empty-state line:\n%s", out)
	}
	for i, line := range strings.Split(out, "\n") {
		if utf8.RuneCountInString(line) > DefaultWidth {
			t.Errorf("default-width line %d overflows %d columns: %q", i, DefaultWidth, line)
		}
	}
}

// Live refresh loop coverage pairing with control.go. Everything runs
// synchronously: bubbletea commands are plain funcs, so tests call them
// directly instead of waiting on timers — no wall-clock sleeps anywhere.

// scheduledMsg marks a scheduler invocation that made it into a batch, so
// tests can prove both tick effects (reschedule plus refresh) exist.
type scheduledMsg struct{}

// recorder captures what the live loop did: scheduler invocations with their
// intervals and collector invocations with a canned result, pinning the exact
// one-tick/one-refresh/one-reschedule contract without sleeping.
type recorder struct {
	schedules []time.Duration
	refreshes int
	repos     []overview.Repo
	err       error
}

// schedule records the requested cadence and returns a marker command so the
// test can count it among the batch's concrete effects.
func (r *recorder) schedule(d time.Duration) tea.Cmd {
	r.schedules = append(r.schedules, d)
	return func() tea.Msg { return scheduledMsg{} }
}

// refresh returns the canned snapshot, recording the call.
func (r *recorder) refresh() ([]overview.Repo, error) {
	r.refreshes++
	return r.repos, r.err
}

// collectEffects executes cmd synchronously and returns every produced
// message, unwrapping tea.Batch into its member commands.
func collectEffects(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var msgs []tea.Msg
	for _, c := range batch {
		if c != nil {
			msgs = append(msgs, c())
		}
	}
	return msgs
}

// snapshotOf extracts the single snapshot message among collected effects,
// failing when there is none or more than one.
func snapshotOf(t *testing.T, msgs []tea.Msg) snapshotMsg {
	t.Helper()
	var found []snapshotMsg
	for _, msg := range msgs {
		if snap, ok := msg.(snapshotMsg); ok {
			found = append(found, snap)
		}
	}
	switch len(found) {
	case 1:
		return found[0]
	case 0:
		t.Fatalf("no snapshot message among %d collected effects", len(msgs))
	default:
		t.Fatalf("collected %d snapshot messages, want exactly one", len(found))
	}
	return snapshotMsg{}
}

// TestNewLivePanicsOnNilRefresh pins the documented constructor contract: a
// live loop with nothing to collect is a programming error.
func TestNewLivePanicsOnNilRefresh(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewLive with a nil refresh must panic by contract")
		}
	}()
	_ = NewLive(nil, nil, time.Second, nil)
}

// TestNewLiveClampsNonPositiveInterval pins the defensive cadence: callers
// pass a positive interval, but zero or negative values fall back to exactly
// DefaultRefreshInterval instead of producing a degenerate ticker.
func TestNewLiveClampsNonPositiveInterval(t *testing.T) {
	rec := &recorder{}
	for _, in := range []time.Duration{0, -time.Millisecond, -time.Hour} {
		m := NewLive(nil, rec.refresh, in, nil)
		if m.interval != DefaultRefreshInterval {
			t.Errorf("NewLive(interval=%v) kept %v, want the documented default %v",
				in, m.interval, DefaultRefreshInterval)
		}
	}
	m := NewLive(nil, rec.refresh, 5*time.Second, nil)
	if m.interval != 5*time.Second {
		t.Errorf("positive interval rewritten to %v, want 5s kept verbatim", m.interval)
	}
}

// TestNewLiveInitSchedulesFirstTick proves Init arms the loop: a non-nil
// first-tick command through the default tea.Tick scheduler (never executed,
// so no timer ever fires) and no scheduler contact before Update runs.
func TestNewLiveInitSchedulesFirstTick(t *testing.T) {
	rec := &recorder{}
	m := NewLive([]overview.Repo{repo("alpha")}, rec.refresh, time.Second, nil)
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("live Init scheduled nothing, want the first tick")
	}
	if len(rec.schedules) != 0 {
		t.Errorf("Init contacted the scheduler directly (%v), want scheduling deferred to ticks", rec.schedules)
	}
}

// TestStaticModelIgnoresLiveLoopMessages pins New's passivity under the new
// message types: Init stays nil and a stray tick neither panics nor mutates
// anything, because static models never enabled the loop.
func TestStaticModelIgnoresLiveLoopMessages(t *testing.T) {
	m := New([]overview.Repo{repo("alpha")})
	if cmd := m.Init(); cmd != nil {
		t.Fatalf("static Init scheduled %v, want nothing", cmd)
	}
	next, cmd := m.Update(tickMsg{})
	if cmd != nil {
		t.Fatalf("tick on a static model scheduled %v, want nothing", cmd)
	}
	static := next.(Model)
	if static.Width() != m.Width() || static.Selected() != m.Selected() ||
		static.Quitting() || len(static.Repos()) != 1 || static.Err() != "" {
		t.Errorf("tick mutated a static model")
	}
}

// TestUnknownMessageIgnored pins the default branch against regression now
// that Update routes more message types.
func TestUnknownMessageIgnored(t *testing.T) {
	repos := []overview.Repo{repo("alpha")}
	before := pressKeys(t, New(repos), "down")
	next, cmd := before.Update(struct{ opaque int }{opaque: 7})
	if cmd != nil {
		t.Errorf("unknown message scheduled %v", cmd)
	}
	after := next.(Model)
	if after.Width() != before.Width() || after.Selected() != before.Selected() ||
		after.Quitting() || len(after.Repos()) != 1 {
		t.Errorf("unknown message mutated the model")
	}
}

// TestTickDrivesExactlyOneRefreshAndReschedule pins the cycle contract: one
// tick maps to exactly one reschedule at the model's interval plus exactly
// one snapshot collection, both delivered as batch effects whose results the
// runtime may deliver in any order.
func TestTickDrivesExactlyOneRefreshAndReschedule(t *testing.T) {
	rec := &recorder{repos: []overview.Repo{repo("fresh")}}
	interval := 750 * time.Millisecond
	m := NewLive([]overview.Repo{repo("stale")}, rec.refresh, interval, nil)
	m.schedule = rec.schedule

	next, cmd := m.Update(tickMsg{})
	live := next.(Model)
	if cmd == nil {
		t.Fatal("tick scheduled nothing, want reschedule plus refresh")
	}
	if len(rec.schedules) != 1 || rec.schedules[0] != interval {
		t.Fatalf("schedule calls = %v, want exactly one at %v", rec.schedules, interval)
	}
	if rec.refreshes != 0 {
		t.Fatalf("refresh ran %d times inside Update, want deferred to the command", rec.refreshes)
	}

	effects := collectEffects(t, cmd)
	var snapshots, reschedules int
	for _, msg := range effects {
		switch msg.(type) {
		case snapshotMsg:
			snapshots++
		case scheduledMsg:
			reschedules++
		default:
			t.Errorf("tick batch produced unexpected effect %T", msg)
		}
	}
	if snapshots != 1 || reschedules != 1 {
		t.Fatalf("tick produced %d snapshots and %d reschedules, want exactly one of each", snapshots, reschedules)
	}
	if rec.refreshes != 1 {
		t.Fatalf("refresh ran %d times for one tick, want exactly once", rec.refreshes)
	}

	live = update(t, live, snapshotOf(t, effects))
	if got := live.Repos(); len(got) != 1 || got[0].Name != "fresh" {
		t.Errorf("Repos() = %v, want the refreshed snapshot", got)
	}
	if live.Err() != "" {
		t.Errorf("Err() = %q, want empty after a healthy refresh", live.Err())
	}
}

// TestTickDoesNotOverlapRefreshes pins the serialized collection contract: a
// slow first refresh keeps the cadence scheduled but a second tick cannot
// launch an older competing snapshot that might overwrite a newer one.
func TestTickDoesNotOverlapRefreshes(t *testing.T) {
	rec := &recorder{repos: []overview.Repo{repo("fresh")}}
	m := NewLive([]overview.Repo{repo("stale")}, rec.refresh, time.Second, nil)
	m.schedule = rec.schedule

	firstNext, firstCmd := m.Update(tickMsg{})
	first := firstNext.(Model)
	firstEffects := collectEffects(t, firstCmd)
	if rec.refreshes != 1 {
		t.Fatalf("first tick ran %d refreshes, want one", rec.refreshes)
	}

	secondNext, secondCmd := first.Update(tickMsg{})
	second := secondNext.(Model)
	secondEffects := collectEffects(t, secondCmd)
	if rec.refreshes != 1 {
		t.Fatalf("overlapping tick ran %d refreshes, want the in-flight one only", rec.refreshes)
	}
	if len(secondEffects) != 1 {
		t.Fatalf("overlapping tick produced %d effects, want only the reschedule", len(secondEffects))
	}
	if _, ok := secondEffects[0].(scheduledMsg); !ok {
		t.Fatalf("overlapping tick effect = %T, want scheduledMsg", secondEffects[0])
	}

	firstSnapshot := snapshotOf(t, firstEffects)
	second = update(t, second, firstSnapshot)
	_, thirdCmd := second.Update(tickMsg{})
	thirdEffects := collectEffects(t, thirdCmd)
	if rec.refreshes != 2 {
		t.Fatalf("next tick ran %d refreshes, want two after the first completed", rec.refreshes)
	}
	thirdSnapshot := snapshotOf(t, thirdEffects)
	if len(thirdSnapshot.repos) != 1 || thirdSnapshot.repos[0].Name != "fresh" {
		t.Fatalf("next tick snapshot = %#v, want the refreshed registry", thirdSnapshot.repos)
	}
}

// TestSnapshotMsgTransitions drives every snapshot outcome: success replaces
// the registry and clears any recorded error while clamping the cursor back
// into range with the same >= 0 semantics as key navigation; failure keeps
// the last good snapshot untouched and records the error text.
func TestSnapshotMsgTransitions(t *testing.T) {
	alpha, beta, gamma := repo("alpha"), repo("beta"), repo("gamma")
	fresh := []overview.Repo{repo("fresh")}
	tests := []struct {
		name       string
		repos      []overview.Repo // initial registry
		downs      int             // cursor presses before the snapshot lands
		priorErr   bool            // plant a failing snapshot before msg
		msg        snapshotMsg
		wantRepos  []string // wanted names after msg
		wantSelect int
		wantErr    string
	}{
		{
			name:       "success replaces repos and clears err",
			repos:      []overview.Repo{alpha},
			msg:        snapshotMsg{repos: fresh},
			wantRepos:  []string{"fresh"},
			wantSelect: 0,
			wantErr:    "",
		},
		{
			name:       "success clamps the cursor after the registry shrinks",
			repos:      []overview.Repo{alpha, beta, gamma},
			downs:      2,
			msg:        snapshotMsg{repos: fresh},
			wantRepos:  []string{"fresh"},
			wantSelect: 0,
			wantErr:    "",
		},
		{
			name:       "success clamps to the last repository without overshoot",
			repos:      []overview.Repo{alpha, beta, gamma},
			downs:      2,
			msg:        snapshotMsg{repos: []overview.Repo{repo("one"), repo("two")}},
			wantRepos:  []string{"one", "two"},
			wantSelect: 1,
			wantErr:    "",
		},
		{
			name:       "success onto an empty registry parks the cursor at the top",
			repos:      []overview.Repo{alpha, beta},
			downs:      1,
			msg:        snapshotMsg{},
			wantRepos:  nil,
			wantSelect: 0,
			wantErr:    "",
		},
		{
			name:       "failure keeps the last good snapshot and records the error",
			repos:      []overview.Repo{alpha, beta},
			downs:      1,
			msg:        snapshotMsg{err: errors.New("collector offline")},
			wantRepos:  []string{"alpha", "beta"},
			wantSelect: 1,
			wantErr:    "collector offline",
		},
		{
			name:       "a later success clears a planted error",
			repos:      []overview.Repo{alpha},
			priorErr:   true,
			msg:        snapshotMsg{repos: fresh},
			wantRepos:  []string{"fresh"},
			wantSelect: 0,
			wantErr:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewLive(tt.repos, (&recorder{}).refresh, time.Second, nil)
			for i := 0; i < tt.downs; i++ {
				m = update(t, m, keyMsg("down"))
			}
			if tt.priorErr {
				m = update(t, m, snapshotMsg{err: errors.New("planted failure")})
			}
			m = update(t, m, tt.msg)

			got := m.Repos()
			if len(got) != len(tt.wantRepos) {
				t.Fatalf("Repos() has %d entries (%v), want %d", len(got), got, len(tt.wantRepos))
			}
			for i, want := range tt.wantRepos {
				if got[i].Name != want {
					t.Errorf("Repos()[%d].Name = %q, want %q", i, got[i].Name, want)
				}
			}
			if m.Selected() != tt.wantSelect {
				t.Errorf("selected = %d, want %d", m.Selected(), tt.wantSelect)
			}
			if m.Err() != tt.wantErr {
				t.Errorf("Err() = %q, want %q", m.Err(), tt.wantErr)
			}
		})
	}
}

// TestUpdateFocusSwitching pins tab/shift+tab and the tree-side enter rule:
// focus moves between panes, an empty registry clamps the switch to a no-op,
// and enter hands focus to the runs pane without touching the selection.
func TestUpdateFocusSwitching(t *testing.T) {
	repos := navRepos()
	tests := []struct {
		name       string
		setup      func(*testing.T) Model
		key        string
		wantFocus  art.FocusPane
		wantSelect int
	}{
		{
			name:       "tab switches focus to the runs pane",
			setup:      func(t *testing.T) Model { return New(repos) },
			key:        "tab",
			wantFocus:  art.FocusRuns,
			wantSelect: 0,
		},
		{
			name:       "shift+tab returns focus to the tree",
			setup:      func(t *testing.T) Model { return pressKeys(t, New(repos), "tab") },
			key:        "shift+tab",
			wantFocus:  art.FocusTree,
			wantSelect: 0,
		},
		{
			name:       "tab is a clamped no-op on an empty registry",
			setup:      func(t *testing.T) Model { return New(nil) },
			key:        "tab",
			wantFocus:  art.FocusTree,
			wantSelect: 0,
		},
		{
			name:       "shift+tab on an empty registry stays in the tree",
			setup:      func(t *testing.T) Model { return New(nil) },
			key:        "shift+tab",
			wantFocus:  art.FocusTree,
			wantSelect: 0,
		},
		{
			name:       "enter in the tree moves focus to runs without changing selection",
			setup:      func(t *testing.T) Model { return pressKeys(t, New(repos), "down") },
			key:        "enter",
			wantFocus:  art.FocusRuns,
			wantSelect: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := tt.setup(t)
			next, cmd := m.Update(keyMsg(tt.key))
			if cmd != nil {
				t.Errorf("%s scheduled %v, want nothing", tt.key, cmd)
			}
			after := next.(Model)
			if after.Focus() != tt.wantFocus {
				t.Errorf("focus = %v, want %v", after.Focus(), tt.wantFocus)
			}
			if after.Selected() != tt.wantSelect {
				t.Errorf("selected = %d, want %d", after.Selected(), tt.wantSelect)
			}
		})
	}
}

// TestUpdateRunCursorWalksRenderOrder pins the runs-pane walk: up/down/j/k
// move over VisibleRuns of the current snapshot in render order — across
// repository boundaries, skipping summary-only repos — with hard clamps at
// both ends, and focus stays on the runs pane throughout.
func TestUpdateRunCursorWalksRenderOrder(t *testing.T) {
	repos := navRepos()
	visible := art.VisibleRuns(art.ViewState{Repos: repos})
	if len(visible) != 3 {
		t.Fatalf("fixture walk = %v, want three run rows", visible)
	}
	tests := []struct {
		name  string
		start int      // index into visible the model is parked on
		keys  []string // presses after parking
		want  art.RunPos
	}{
		{"j crosses into the next repository's runs", 0, []string{"j"}, visible[1]},
		{"j skips the runless middle repository", 1, []string{"j"}, visible[2]},
		{"j clamps at the last visible run", 2, []string{"j"}, visible[2]},
		{"k walks back one row", 2, []string{"k"}, visible[1]},
		{"k clamps at the first visible run", 0, []string{"k"}, visible[0]},
		{"up arrow equals k", 1, []string{"up"}, visible[0]},
		{"down arrow equals j", 1, []string{"down"}, visible[2]},
		{"a longer sequence lands where arithmetic says", 0, []string{"j", "j", "k"}, visible[1]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(repos)
			m.focus = art.FocusRuns
			m.runCursor = visible[tt.start]
			m = pressKeys(t, m, tt.keys...)
			if got := m.RunCursorPosition(); got != tt.want {
				t.Errorf("cursor = %v, want %v", got, tt.want)
			}
			if m.Focus() != art.FocusRuns {
				t.Errorf("walking must not change focus, got %v", m.Focus())
			}
		})
	}
}

// TestUpdateRunCursorNoOpsWithoutRuns pins the empty-walk contract: with no
// navigable rows every direction key and enter are inert no-ops.
func TestUpdateRunCursorNoOpsWithoutRuns(t *testing.T) {
	m := New([]overview.Repo{repo("plain"), repo("bare")})
	m.focus = art.FocusRuns
	m = pressKeys(t, m, "j", "j", "k", "k")
	if m.RunCursorPosition() != (art.RunPos{}) {
		t.Errorf("cursor moved on a runless registry: %v", m.RunCursorPosition())
	}
	m = update(t, m, keyMsg("enter"))
	if m.OpenRun() != "" || m.RunCursorPosition() != (art.RunPos{}) {
		t.Errorf("enter on an empty walk mutated the model: open=%q cursor=%v",
			m.OpenRun(), m.RunCursorPosition())
	}
	empty := New(nil)
	empty.focus = art.FocusRuns
	empty = pressKeys(t, empty, "j", "k")
	if empty.RunCursorPosition() != (art.RunPos{}) {
		t.Errorf("cursor moved on a nil registry: %v", empty.RunCursorPosition())
	}
}

// TestUpdateEnterTogglesOpenRun pins enter semantics inside the runs pane:
// it opens, closes, re-opens another run, movement clears it, a clamped
// move does not, and a stale cursor neither panics nor opens anything.
func TestUpdateEnterTogglesOpenRun(t *testing.T) {
	repos := navRepos()
	visible := art.VisibleRuns(art.ViewState{Repos: repos})
	build := func(start int) Model {
		m := New(repos)
		m.focus = art.FocusRuns
		m.runCursor = visible[start]
		return m
	}
	t.Run("enter opens the run under the cursor", func(t *testing.T) {
		m := update(t, build(0), keyMsg("enter"))
		if got := m.OpenRun(); got != "aaaaaaaaaaaaaaaaaaaa" {
			t.Errorf("open run = %q, want the first alpha run", got)
		}
	})
	t.Run("enter again closes the same run", func(t *testing.T) {
		m := pressKeys(t, build(1), "enter", "enter")
		if got := m.OpenRun(); got != "" {
			t.Errorf("open run = %q, want cleared by the second enter", got)
		}
	})
	t.Run("enter opens another run after moving", func(t *testing.T) {
		m := pressKeys(t, build(0), "enter", "j", "j", "enter")
		if got := m.OpenRun(); got != "cccccccccccccccccccc" {
			t.Errorf("open run = %q, want the gamma run", got)
		}
	})
	t.Run("moving clears an open run", func(t *testing.T) {
		m := pressKeys(t, build(0), "enter", "j")
		if got := m.OpenRun(); got != "" {
			t.Errorf("moving left %q open, want cleared", got)
		}
		if m.RunCursorPosition() != visible[1] {
			t.Errorf("cursor = %v, want %v", m.RunCursorPosition(), visible[1])
		}
	})
	t.Run("a clamped move that cannot move keeps the detail open", func(t *testing.T) {
		m := pressKeys(t, build(0), "enter", "k")
		if got := m.OpenRun(); got != "aaaaaaaaaaaaaaaaaaaa" {
			t.Errorf("clamped move cleared %q, want it kept", got)
		}
	})
	t.Run("a stale cursor re-anchors without opening anything", func(t *testing.T) {
		m := New(repos)
		m.focus = art.FocusRuns
		m.runCursor = art.RunPos{Repo: 9, Run: 9}
		m.openRunID = "stale"
		m = pressKeys(t, m, "j")
		if m.OpenRun() != "" {
			t.Errorf("re-anchor kept a stale detail open: %q", m.OpenRun())
		}
		if m.RunCursorPosition() != visible[1] {
			t.Errorf("cursor = %v, want the re-anchored walk position %v", m.RunCursorPosition(), visible[1])
		}
	})
}

// TestUpdateHelpToggleAndSwallow pins the help overlay state machine: "?"
// toggles, any other key closes help first and is otherwise swallowed, and
// q/ctrl+c still quit from help.
func TestUpdateHelpToggleAndSwallow(t *testing.T) {
	repos := navRepos()
	t.Run("? toggles help on and off", func(t *testing.T) {
		m := pressKeys(t, New(repos), "?")
		if !m.HelpVisible() {
			t.Fatalf("? did not open help")
		}
		m = pressKeys(t, m, "?")
		if m.HelpVisible() {
			t.Errorf("the second ? did not close help")
		}
	})
	for _, key := range []string{"down", "up", "k", "j", "tab", "enter"} {
		t.Run("help swallows "+key, func(t *testing.T) {
			m := pressKeys(t, New(repos), "down", "?")
			m = pressKeys(t, m, key)
			if m.HelpVisible() {
				t.Errorf("%s did not close help", key)
			}
			if m.Selected() != 1 || m.Focus() != art.FocusTree ||
				m.RunCursorPosition() != (art.RunPos{}) || m.OpenRun() != "" {
				t.Errorf("%s leaked through help: selected=%d focus=%v cursor=%v open=%q",
					key, m.Selected(), m.Focus(), m.RunCursorPosition(), m.OpenRun())
			}
		})
	}
	for _, key := range []string{"q", "ctrl+c"} {
		t.Run("quit works while help is open via "+key, func(t *testing.T) {
			m := pressKeys(t, New(repos), "?")
			next, cmd := m.Update(keyMsg(key))
			after := next.(Model)
			if !after.Quitting() {
				t.Errorf("%s did not flag quitting from help", key)
			}
			if cmd == nil || cmd() == nil {
				t.Errorf("%s returned no quit command from help", key)
			}
		})
	}
	t.Run("quit works from runs focus via q", func(t *testing.T) {
		m := pressKeys(t, New(repos), "enter")
		if m.Focus() != art.FocusRuns {
			t.Fatalf("enter did not move focus to the runs pane")
		}
		next, cmd := m.Update(keyMsg("q"))
		after := next.(Model)
		if !after.Quitting() {
			t.Errorf("q did not flag quitting from runs focus")
		}
		if cmd == nil || cmd() == nil {
			t.Errorf("q returned no quit command from runs focus")
		}
	})
}

// TestViewRendersNavigationStates drives real models through Update and pins
// what each navigation state paints: focused headings and row prefix, the
// open-run detail line, and the KEYS overlay replacing pane content.
func TestViewRendersNavigationStates(t *testing.T) {
	repos := navRepos()
	stacked := tea.WindowSizeMsg{Width: 80, Height: 24} // below 84: stacked layout
	t.Run("runs focus marks its heading and the focused row", func(t *testing.T) {
		m := New(repos)
		m.focus = art.FocusRuns
		m = update(t, m, stacked)
		out := stripANSI(m.View())
		assertContains := func(got string, wants ...string) {
			t.Helper()
			for _, want := range wants {
				if !strings.Contains(got, want) {
					t.Fatalf("view missing %q:\n%s", want, got)
				}
			}
		}
		assertContains(out, " ▸ ACTIVITY", "\n▸ ")
		if strings.Contains(out, " ▸ REPOSITORIES") {
			t.Errorf("unfocused tree heading carried the marker:\n%s", out)
		}
	})
	t.Run("open run appends the full-id detail line", func(t *testing.T) {
		m := New(repos)
		m.focus = art.FocusRuns
		m.openRunID = "aaaaaaaaaaaaaaaaaaaa"
		m = update(t, m, stacked)
		out := stripANSI(m.View())
		if !strings.Contains(out, "   └─ aaaaaaaaaaaaaaaaaaaa · RUNNING · rev 1") ||
			!strings.Contains(out, "0001-01-01T00:00:00Z") {
			t.Errorf("view missing the open-run detail line:\n%s", out)
		}
	})
	t.Run("help replaces the pane content with the KEYS block", func(t *testing.T) {
		m := New(repos)
		m.help = true
		m = update(t, m, stacked)
		out := stripANSI(m.View())
		if !strings.Contains(out, " KEYS") || !strings.Contains(out, "navigate panes") {
			t.Errorf("help view missing the KEYS block:\n%s", out)
		}
		if strings.Contains(out, "LOCATION") || strings.Contains(out, "ACTIVITY") {
			t.Errorf("help view leaked pane content:\n%s", out)
		}
	})
}
