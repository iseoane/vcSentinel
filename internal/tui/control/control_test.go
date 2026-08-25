package control

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/bubbletea"

	"github.com/ISeoane-Quental/vas.sentinel/internal/overview"
)

// Update-transition and View coverage pairing with control.go. The style
// mirrors internal/tui: drive Update directly with constructed messages, no
// teatest, no terminal.

// repo builds a minimal stopped snapshot entry; only the identity fields the
// LOCATION block renders are needed.
func repo(name string) overview.Repo {
	return overview.Repo{Name: name, Path: "/tmp/" + name, Enabled: true,
		Origin: "git@github.com:org/" + name}
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
			name:  "q flags quitting without scheduling anything",
			setup: func(t *testing.T) Model { return New(repos) },
			key:   "q",
			assert: func(t *testing.T, before Model, after Model, cmd tea.Cmd) {
				if !after.Quitting() {
					t.Errorf("q did not flag quitting")
				}
				if cmd != nil {
					t.Errorf("q scheduled %v, want nothing", cmd)
				}
			},
		},
		{
			name:  "ctrl+c flags quitting",
			setup: func(t *testing.T) Model { return New(repos) },
			key:   "ctrl+c",
			assert: func(t *testing.T, before Model, after Model, cmd tea.Cmd) {
				if !after.Quitting() {
					t.Errorf("ctrl+c did not flag quitting")
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
	if !strings.Contains(loc, "/tmp/beta") {
		t.Errorf("LOCATION block missing the selected path \"/tmp/beta\":\n%s", loc)
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
