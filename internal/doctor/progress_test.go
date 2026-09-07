package doctor

import (
	"errors"
	"slices"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/graph"
)

// TestRunAnnouncesProgressBeforeLongSteps pins the visible-progress contract:
// Run emits one "probing <name>…" announcement before each configured agent's
// probe (in yml declaration order) and "checking for updates…" before the
// release feed is contacted, so the 2x60s probes read as progress instead of
// silence. A nil Progress is the default for embedders and must stay silent
// and panic-free.
func TestRunAnnouncesProgressBeforeLongSteps(t *testing.T) {
	isolateHome(t)
	worktree := t.TempDir()
	writeProjectYML(t, worktree, twoAgentYML)
	common := t.TempDir()
	writeHook(t, common, "/usr/local/bin/sentinel")
	stageBinaries(t, "claude", "opencode", "rg")
	env := stubEnv(t, common, map[string]string{"claude": "ok", "opencode": "ok"}, nil)
	env.LatestRelease = func() (string, error) { return "v0.2.0", nil }

	var got []string
	Run(worktree, Options{CurrentVersion: "0.2.0", CheckUpdates: true, Env: env, Progress: func(msg string) {
		got = append(got, msg)
	}})
	want := []string{
		"probing claude…",
		"probing opencode…",
		"checking for updates…",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("progress announcements = %q, want %q", got, want)
	}
}

func TestRunNilProgressNeverPanics(t *testing.T) {
	isolateHome(t)
	worktree := t.TempDir()
	writeProjectYML(t, worktree, twoAgentYML)
	common := t.TempDir()
	writeHook(t, common, "/usr/local/bin/sentinel")
	stageBinaries(t, "claude", "opencode", "rg")
	env := stubEnv(t, common, map[string]string{"claude": "ok", "opencode": "ok"}, nil)
	env.LatestRelease = func() (string, error) { return "", errors.New("no network") }
	env.Codegraph = func(string) []graph.Condition { return nil }

	rep := Run(worktree, Options{CurrentVersion: "0.2.0", CheckUpdates: true, Env: env})
	if rep.WarnCount() == 0 {
		t.Fatalf("expected the unreachable release feed to surface as a warning, got %+v", rep.Checks)
	}
}
