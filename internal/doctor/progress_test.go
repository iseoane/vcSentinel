package doctor

import (
	"errors"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/graph"
)

// TestRunAnnouncesProgressBeforeLongSteps pins the visible-progress contract:
// Run emits each long step's announcement before the blocking call it
// describes — one "probing <name>…" before each configured agent's probe and
// "checking for updates…" before the release feed is contacted — so the 2x60s
// probes read as progress instead of silence. The assertions are containment
// over the writer state at the moment each blocking call starts, not the full
// line sequence: a future benign announcement must not fail this test. A nil
// Progress is the default for embedders and must stay silent and panic-free.
func TestRunAnnouncesProgressBeforeLongSteps(t *testing.T) {
	isolateHome(t)
	worktree := t.TempDir()
	writeProjectYML(t, worktree, twoAgentYML)
	common := t.TempDir()
	writeHook(t, common, "/usr/local/bin/vcsentinel")
	stageBinaries(t, "claude", "opencode", "rg")
	env := stubEnv(t, common, map[string]string{"claude": "ok", "opencode": "ok"}, nil)

	var got []string
	writerAt := map[string]string{}
	baseProbe := env.Probe
	env.Probe = func(agent, prompt string) (string, error) {
		writerAt["probe "+agent] = strings.Join(got, "\n")
		return baseProbe(agent, prompt)
	}
	env.LatestRelease = func() (string, error) {
		writerAt["update check"] = strings.Join(got, "\n")
		return "v0.2.0", nil
	}
	Run(worktree, Options{CurrentVersion: "0.2.0", CheckUpdates: true, Env: env, Progress: func(msg string) {
		got = append(got, msg)
	}})
	for _, agent := range []string{"claude", "opencode"} {
		if !strings.Contains(writerAt["probe "+agent], "probing "+agent+"…") {
			t.Fatalf("writer held %q when the %s probe started, want its flushed announcement", writerAt["probe "+agent], agent)
		}
	}
	if !strings.Contains(writerAt["update check"], "checking for updates…") {
		t.Fatalf("writer held %q when the release feed was contacted, want the flushed announcement", writerAt["update check"])
	}
}

func TestRunNilProgressNeverPanics(t *testing.T) {
	isolateHome(t)
	worktree := t.TempDir()
	writeProjectYML(t, worktree, twoAgentYML)
	common := t.TempDir()
	writeHook(t, common, "/usr/local/bin/vcsentinel")
	stageBinaries(t, "claude", "opencode", "rg")
	env := stubEnv(t, common, map[string]string{"claude": "ok", "opencode": "ok"}, nil)
	env.LatestRelease = func() (string, error) { return "", errors.New("no network") }
	env.Codegraph = func(string) []graph.Condition { return nil }

	rep := Run(worktree, Options{CurrentVersion: "0.2.0", CheckUpdates: true, Env: env})
	if rep.WarnCount() == 0 {
		t.Fatalf("expected the unreachable release feed to surface as a warning, got %+v", rep.Checks)
	}
}
