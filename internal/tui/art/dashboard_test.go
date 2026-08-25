package art

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestGoldenDashboard80(t *testing.T)  { golden(t, "dashboard_80", DashboardPlain(80)) }
func TestGoldenDashboard100(t *testing.T) { golden(t, "dashboard_100", DashboardPlain(100)) }
func TestGoldenDashboard140(t *testing.T) { golden(t, "dashboard_140", DashboardPlain(140)) }

func TestDashboardWidths(t *testing.T) {
	for _, w := range []int{60, 80, 100, 140} {
		dash := DashboardPlain(w)
		for i, line := range strings.Split(dash, "\n") {
			if utf8.RuneCountInString(line) > w {
				t.Errorf("dashboard(%d) line %d overflows: %d runes: %q", w, i, utf8.RuneCountInString(line), line)
			}
		}
	}
}

// TestDashboardColoredKeepsRuneContent pins the contract that color mode
// changes only escapes: stripping them yields the plain layout byte-for-byte.
func TestDashboardColoredKeepsRuneContent(t *testing.T) {
	plain := DashboardPlain(100)
	colored := stripEscapes(Dashboard(100))
	if plain != colored {
		t.Fatalf("color mode altered visible content:\n--- plain ---\n%s\n--- colored ---\n%s", plain, colored)
	}
	if !strings.Contains(Dashboard(100), "\x1b[38;5;") {
		t.Error("colored dashboard emitted no ANSI escapes")
	}
}

func TestDashboardShowsContractSections(t *testing.T) {
	dash := DashboardPlain(100)
	for _, want := range []string{
		"SENTINEL CONTROL CENTER", "REPOSITORIES", "LOCATION", "ACTIVITY",
		"vas.sentinel", "tui-control-cent", "Origin", "navigate",
	} {
		if !strings.Contains(dash, want) {
			t.Errorf("dashboard missing contract section %q", want)
		}
	}
	if !strings.Contains(dash, "════") {
		t.Error("dashboard missing double-line separators")
	}
}

// TestDashboardTreeColumnsAlign pins the spec-review fix: worktree state
// columns start at the same rune column because names are truncated, not
// allowed to push the column right.
func TestDashboardTreeColumnsAlign(t *testing.T) {
	dash := DashboardPlain(100)
	var columns []int
	for _, line := range strings.Split(dash, "\n") {
		stateIdx := -1
		for _, state := range []string{"clean", "2 runs", "1 attention"} {
			if i := strings.Index(line, state); i >= 0 && (stateIdx < 0 || i < stateIdx) {
				stateIdx = i
			}
		}
		if stateIdx >= 0 && strings.Contains(line[:stateIdx], "─") {
			columns = append(columns, runeLen(line[:stateIdx]))
		}
	}
	if len(columns) < 3 {
		t.Fatalf("expected 3 worktree rows, got %d", len(columns))
	}
	for i := 1; i < len(columns); i++ {
		if columns[i] != columns[0] {
			t.Fatalf("worktree state columns misaligned: %v", columns)
		}
	}
}
