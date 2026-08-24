package art

import (
	"testing"
	"unicode/utf8"
)

func TestGoldenDashboard80(t *testing.T)  { golden(t, "dashboard_80", Dashboard(80)) }
func TestGoldenDashboard100(t *testing.T) { golden(t, "dashboard_100", Dashboard(100)) }
func TestGoldenDashboard140(t *testing.T) { golden(t, "dashboard_140", Dashboard(140)) }

func TestDashboardWidths(t *testing.T) {
	for _, w := range []int{60, 80, 100, 140} {
		dash := Dashboard(w)
		for i, line := range splitLines(dash) {
			if utf8.RuneCountInString(line) > w {
				t.Errorf("dashboard(%d) line %d overflows: %d runes", w, i, utf8.RuneCountInString(line))
			}
		}
	}
}

func splitLines(s string) []string {
	lines := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
