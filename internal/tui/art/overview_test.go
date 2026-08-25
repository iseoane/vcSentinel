package art

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ISeoane-Quental/vas.sentinel/internal/inventory"
	"github.com/ISeoane-Quental/vas.sentinel/internal/overview"
	"github.com/ISeoane-Quental/vas.sentinel/internal/presence"
)

// Fixtures build snapshot values directly (no git needed). colorPrefix
// matches painted spans whose text spanLine right-pads before wrapping.
func wt(branch string, clean bool) inventory.Worktree {
	return inventory.Worktree{Path: "/tmp/repo/" + branch, Branch: branch, Clean: clean}
}

func liveRepo(name string, worktrees ...inventory.Worktree) overview.Repo {
	return overview.Repo{Name: name, Path: "/tmp/" + name, Enabled: true,
		Origin: "git@github.com:org/" + name, Worktrees: worktrees,
		Daemon: presence.Presence{Live: true, PID: 4321}}
}

func stoppedRepo(name string, worktrees ...inventory.Worktree) overview.Repo {
	return overview.Repo{Name: name, Path: "/tmp/" + name, Enabled: true, Worktrees: worktrees}
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
	dash := RenderOverviewPlain(100, []overview.Repo{liveRepo("vas.sentinel", wt("main", true))}, 0)
	assertContains(t, dash, "SENTINEL CONTROL CENTER", "REPOSITORIES", "LOCATION",
		"ACTIVITY", "vas.sentinel", "● live", "main", "clean",
		"git@github.com:org/vas.sentinel", "pid 4321",
		"1 clean · 0 dirty · 1 worktrees", "navigate")
	assertWidth(t, dash, 100)
}

func TestRenderOverviewMultiWorktree(t *testing.T) {
	repos := []overview.Repo{stoppedRepo("multi", wt("main", true), wt("feature", false))}
	dash := RenderOverviewPlain(100, repos, 0)
	assertContains(t, dash, "├─", "└─", " dirty", "· 1 active")
	if !strings.Contains(RenderOverview(100, repos, 0), colorPrefix(Blue, " dirty")) {
		t.Error("dirty child must be blue")
	}
}

func TestRenderOverviewDegradedRepoShowsError(t *testing.T) {
	degraded := overview.Repo{Name: "broken", Error: "overview: inspect repository boom"}
	dash := RenderOverviewPlain(100, []overview.Repo{degraded}, 0)
	// Pane width truncates the one-line error: assert on the prefix that
	// survives both the tree child line and the ACTIVITY stage column.
	if n := strings.Count(dash, "overview: inspect re"); n < 2 {
		t.Errorf("error text must reach tree (%d hits) and activity stage:\n%s", n, dash)
	}
	assertContains(t, dash, "ATTENTION", "⚠", "· 1 attention")
	if !strings.Contains(RenderOverview(100, []overview.Repo{degraded}, 0), colorPrefix(Yellow, " ● attention")) {
		t.Error("attention state must be yellow")
	}
}

func TestRenderOverviewMissingDirFlagged(t *testing.T) {
	dash := RenderOverviewPlain(100, []overview.Repo{{Name: "ghost", Missing: true}}, 0)
	assertContains(t, dash, "● missing", "· 1 attention")
}

func TestRenderOverviewDaemonColors(t *testing.T) {
	colored := RenderOverview(100, []overview.Repo{liveRepo("a"), stoppedRepo("b")}, 0)
	assertContains(t, colored, colorPrefix(Rose, " ● live"), colorPrefix(Red, " ● stopped"),
		colorPrefix(Rose, " ●"), colorPrefix(Red, " ○"),
		colorPrefix(Rose, "LIVE"), colorPrefix(Red, "STOPPED"))
	dash := RenderOverviewPlain(100, []overview.Repo{liveRepo("a"), stoppedRepo("b")}, 0)
	assertContains(t, dash, "LIVE", "STOPPED")
	if strings.Contains(dash, "HEALTHY") {
		t.Errorf("a stopped repository must not render as HEALTHY:\n%s", dash)
	}
}

func TestRenderOverviewEmptyRegistry(t *testing.T) {
	dash := RenderOverviewPlain(100, nil, 0)
	assertContains(t, dash, "no repositories registered", "REPOSITORIES",
		"LOCATION", "ACTIVITY", "navigate", "● 0 daemons")
	assertWidth(t, dash, 100)
}

func TestRenderOverviewStacksBelow84(t *testing.T) {
	dash := RenderOverviewPlain(70, []overview.Repo{
		liveRepo("vas.sentinel", wt("main", true)), stoppedRepo("second"),
	}, 0)
	assertContains(t, dash, "REPOSITORIES", "LOCATION", "ACTIVITY", "vas.sentinel", "second")
	assertWidth(t, dash, 70)
	if strings.Contains(dash, " │ ") {
		t.Error("stacked layout must not draw the pane separator")
	}
}

// TestRenderOverviewPlainMatchesColoredRuneParity pins the color contract:
// stripping escapes from a colored render yields the plain render for every
// fixture and both layout modes.
func TestRenderOverviewPlainMatchesColoredRuneParity(t *testing.T) {
	fixtures := map[string][]overview.Repo{
		"healthy":  {liveRepo("vas.sentinel", wt("main", true))},
		"multi":    {stoppedRepo("multi", wt("main", true), wt("feature", false))},
		"degraded": {{Name: "broken", Error: "overview: inspect repository boom"}},
		"missing":  {{Name: "ghost", Missing: true}},
		"mixed": {
			liveRepo("alpha", wt("main", false)),
			stoppedRepo("beta", wt("dev", true), wt("spike", false)),
			{Name: "gamma", Error: "probe failed"},
			{Name: "delta", Missing: true},
		},
		"empty": nil,
	}
	for name, repos := range fixtures {
		for _, width := range []int{60, 70, 100, 140} {
			plain := RenderOverviewPlain(width, repos, 0)
			colored := RenderOverview(width, repos, 0)
			if plain != stripEscapes(colored) {
				t.Errorf("%s/%d: color mode altered visible content", name, width)
			}
			if len(repos) > 0 && !strings.Contains(colored, "\x1b[38;5;") {
				t.Errorf("%s/%d: colored render emitted no escapes", name, width)
			}
		}
	}
}

// TestRenderOverviewHeaderCounts pins the summary arithmetic on a mixed
// snapshot: one live daemon, one dirty worktree, two attention repos.
func TestRenderOverviewHeaderCounts(t *testing.T) {
	repos := []overview.Repo{
		liveRepo("alpha", wt("main", false)),
		stoppedRepo("beta", wt("dev", true)),
		{Name: "gamma", Error: "probe failed"},
		{Name: "delta", Missing: true},
	}
	header := strings.Split(RenderOverviewPlain(120, repos, 0), "\n")[1]
	assertContains(t, header, "● 1 daemons ", "· 1 active ", "· 2 attention")
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
			dash := RenderOverviewPlain(70, repos, tt.selected)
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
	dash := RenderOverviewPlain(70, nil, -7)
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
