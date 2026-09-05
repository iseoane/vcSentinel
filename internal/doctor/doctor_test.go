package doctor

import (
	"errors"
	"github.com/ISeoane-Quental/vas.sentinel/internal/graph"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
}

func writeProjectYML(t *testing.T, worktree, body string) {
	t.Helper()
	dir := filepath.Join(worktree, ".vas_sentinel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "vassentinel.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const twoAgentYML = `version: "2.0"
active_agent: "auto"
agents:
  claude:
    model: "claude-5-sonnet"
    reasoning_effort: "high"
  opencode:
    model: "deepseek-v4-flash-free"
    reasoning_effort: "default"
`

func okCodegraph() []graph.Condition {
	var out []graph.Condition
	for _, name := range []string{graph.CondBinary, graph.CondIndexDir, graph.CondHead, graph.CondWorktreeClean, graph.CondIndexInitialized, graph.CondProjectPath, graph.CondPendingChanges, graph.CondWorktreeMatch} {
		out = append(out, graph.Condition{Name: name, OK: true, Detail: name + " ok"})
	}
	return out
}

// stageBinaries replaces PATH with a directory holding only the given
// executables. Replacement (not prepend) keeps real host binaries such as
// opencode or rg from leaking into resolution assertions.
func stageBinaries(t *testing.T, names ...string) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := writeExecutable(dir, name); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

// writeExecutable creates an executable stub binary. LookPath only needs it
// to exist and execute; the probe itself is stubbed.
func writeExecutable(dir, name string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755)
}

// stubEnv builds an Env with canned codegraph conditions, a common dir, and
// a probe answering per agent. The probe fails the test on an empty prompt:
// the contract is a real minimal prompt, never an empty one.
func stubEnv(t *testing.T, commonDir string, answers map[string]string, failures map[string]error) Env {
	t.Helper()
	return Env{
		GitCommonDir: func(string) (string, error) { return commonDir, nil },
		Codegraph:    func(string) []graph.Condition { return okCodegraph() },
		Probe: func(agent, prompt string) (string, error) {
			if prompt == "" {
				t.Errorf("probe for %s sent an empty prompt", agent)
			}
			if err, ok := failures[agent]; ok {
				return "", err
			}
			if ans, ok := answers[agent]; ok {
				return ans, nil
			}
			return "", errors.New("unexpected probe for " + agent)
		},
	}
}

func writeHook(t *testing.T, commonDir, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(commonDir, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "#!/bin/sh\n\"" + target + "\" check --staged\n"
	if err := os.WriteFile(filepath.Join(commonDir, "hooks", "pre-commit"), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func findCheck(t *testing.T, rep Report, section, name string) Check {
	t.Helper()
	for _, c := range rep.Checks {
		if c.Section == section && c.Name == name {
			return c
		}
	}
	t.Fatalf("check %s/%s missing in %+v", section, name, rep.Checks)
	return Check{}
}

// assertUniqueRows pins exactly one row per section/name: a placeholder plus
// a real row would let first-match hide a failure.
func assertUniqueRows(t *testing.T, rep Report) {
	t.Helper()
	seen := map[string]int{}
	for _, c := range rep.Checks {
		seen[c.Section+"/"+c.Name]++
	}
	for key, n := range seen {
		if n != 1 {
			t.Errorf("check %s appears %d times, want exactly once", key, n)
		}
	}
}

func TestStrictFailureStopsAtConfig(t *testing.T) {
	isolateHome(t)
	worktree := t.TempDir()
	writeProjectYML(t, worktree, "active_agent: [broken\n")
	rep := Run(worktree, Options{})
	cfg := findCheck(t, rep, "config", "strict_load")
	if cfg.OK {
		t.Errorf("strict_load OK on a broken yml")
	}
	if cfg.Remedy == "" {
		t.Errorf("strict_load carries no remedy")
	}
	for _, c := range rep.Checks {
		if c.Section == "agents" {
			t.Fatalf("agent checks ran on an unloadable config: %+v", c)
		}
	}
}

func TestCoreSectionsReportOK(t *testing.T) {
	isolateHome(t)
	worktree := t.TempDir()
	writeProjectYML(t, worktree, twoAgentYML)
	common := t.TempDir()
	writeHook(t, common, "/usr/local/bin/sentinel")
	env := stubEnv(t, common, nil, nil)
	rep := Run(worktree, Options{CurrentVersion: "0.2.0", Env: env})
	assertUniqueRows(t, rep)
	for _, c := range rep.Checks {
		if c.Section == "agents" || c.Section == "search" {
			t.Fatalf("core report holds %s/%s, want environment sections only", c.Section, c.Name)
		}
		if !c.OK {
			t.Errorf("check %s/%s not OK: %s", c.Section, c.Name, c.Detail)
		}
	}
	findCheck(t, rep, "config", "strict_load")
	findCheck(t, rep, "codegraph", "binary")
	findCheck(t, rep, "hook", "pre-commit")
}

func TestHookPointingInsideRepoWarnsTrap(t *testing.T) {
	isolateHome(t)
	worktree := t.TempDir()
	writeProjectYML(t, worktree, twoAgentYML)
	common := t.TempDir()
	writeHook(t, common, filepath.Join(worktree, "bin", "0.2.0", "sentinel"))
	stageBinaries(t, "claude", "opencode", "rg")
	env := stubEnv(t, common, map[string]string{"claude": "ok", "opencode": "ok"}, nil)
	rep := Run(worktree, Options{Env: env})
	if got := findCheck(t, rep, "hook", "pre-commit"); got.OK {
		t.Errorf("hook OK while pointing at bin/<version>/")
	} else if !strings.Contains(got.Detail, "bin/") || !strings.Contains(got.Remedy, "sentinel init") {
		t.Errorf("hook finding = %+v, want the T0.0 trap plus the init command", got)
	}
}
func TestMissingHookPrintsInitCommand(t *testing.T) {
	isolateHome(t)
	worktree := t.TempDir()
	writeProjectYML(t, worktree, twoAgentYML)
	stageBinaries(t, "claude", "opencode", "rg")
	env := stubEnv(t, t.TempDir(), map[string]string{"claude": "ok", "opencode": "ok"}, nil)
	rep := Run(worktree, Options{Env: env})
	if got := findCheck(t, rep, "hook", "pre-commit"); got.OK {
		t.Errorf("hook OK without a hook file")
	} else if !strings.Contains(got.Remedy, "sentinel init") {
		t.Errorf("hook remedy = %q, want the exact init command", got.Remedy)
	}
}

func TestUpdatesFlagComparesAgainstLatest(t *testing.T) {
	isolateHome(t)
	worktree := t.TempDir()
	writeProjectYML(t, worktree, twoAgentYML)
	common := t.TempDir()
	writeHook(t, common, "/usr/local/bin/sentinel")
	stageBinaries(t, "claude", "opencode", "rg")
	env := stubEnv(t, common, map[string]string{"claude": "ok", "opencode": "ok"}, nil)

	plain := Run(worktree, Options{CurrentVersion: "0.2.0", Env: env})
	for _, c := range plain.Checks {
		if c.Section == "updates" {
			t.Fatalf("updates section present without --check-updates")
		}
	}

	env.LatestRelease = func() (string, error) { return "v9.9.9", nil }
	rep := Run(worktree, Options{CurrentVersion: "0.2.0", CheckUpdates: true, Env: env})
	if got := findCheck(t, rep, "updates", "latest_release"); got.OK {
		t.Errorf("updates OK while v9.9.9 is newer than 0.2.0")
	} else if !strings.Contains(got.Remedy, "sentinel upgrade") {
		t.Errorf("updates remedy = %q, want the exact upgrade command", got.Remedy)
	}

	env.LatestRelease = func() (string, error) { return "", errors.New("no network") }
	rep = Run(worktree, Options{CurrentVersion: "0.2.0", CheckUpdates: true, Env: env})
	if got := findCheck(t, rep, "updates", "latest_release"); got.OK {
		t.Errorf("updates OK on a network failure")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		current, latest string
		want            int
	}{
		{"0.2.0", "v0.2.0", 0},
		{"0.2.0", "v0.3.0", -1},
		{"0.3.0", "v0.2.0", 1},
		{"0.2.0", "0.2.0", 0},
		{"dev", "v0.2.0", 0},
	}
	for _, tc := range cases {
		if got := compareVersions(tc.current, tc.latest); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.current, tc.latest, got, tc.want)
		}
	}
}
