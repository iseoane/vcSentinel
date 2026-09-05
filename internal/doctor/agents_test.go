package doctor

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

const probeBudgetForTest = 60 * time.Second

const acpxFirstYML = `version: "2.0"
active_agent: "auto"
agents:
  nightly:
    kind: "acpx"
    agent: "opencode"
    model: "opencode/big-pickle"
    reasoning_effort: "default"
  claude:
    model: "claude-5-sonnet"
    reasoning_effort: "high"
`

func TestHealthyTreeReportsAllOK(t *testing.T) {
	isolateHome(t)
	worktree := t.TempDir()
	writeProjectYML(t, worktree, twoAgentYML)
	common := t.TempDir()
	writeHook(t, common, "/usr/local/bin/sentinel")
	stageBinaries(t, "claude", "opencode", "rg")
	env := stubEnv(t, common, map[string]string{"claude": "ok", "opencode": "ok"}, nil)
	rep := Run(worktree, Options{CurrentVersion: "0.2.0", Env: env})
	assertUniqueRows(t, rep)
	for _, c := range rep.Checks {
		if !c.OK {
			t.Errorf("check %s/%s not OK: %s", c.Section, c.Name, c.Detail)
		}
		if c.Remedy != "" {
			t.Errorf("check %s/%s OK but carries a remedy", c.Section, c.Name)
		}
	}
	for _, want := range [][2]string{
		{"config", "strict_load"},
		{"agents", "active_agent"}, {"agents", "claude resolves"}, {"agents", "claude answers"},
		{"agents", "opencode resolves"}, {"agents", "opencode answers"},
		{"search", "rg"}, {"hook", "pre-commit"},
	} {
		findCheck(t, rep, want[0], want[1])
	}
	if got := findCheck(t, rep, "agents", "opencode answers").Detail; !strings.Contains(got, "ok") {
		t.Errorf("answers detail = %q, want the probe answer", got)
	}
	if n := rep.WarnCount(); n != 0 {
		t.Errorf("WarnCount = %d, want 0", n)
	}
	if got := rep.Text(); !strings.Contains(got, "WARN: 0") {
		t.Errorf("report text misses the zero-warning summary:\n%s", got)
	}
}

func TestMissingAgentBinarySkipsAnswer(t *testing.T) {
	isolateHome(t)
	worktree := t.TempDir()
	writeProjectYML(t, worktree, twoAgentYML)
	common := t.TempDir()
	writeHook(t, common, "/usr/local/bin/sentinel")
	stageBinaries(t, "claude", "rg")
	env := stubEnv(t, common, map[string]string{"claude": "ok"}, nil)
	rep := Run(worktree, Options{Env: env})
	assertUniqueRows(t, rep)
	if got := findCheck(t, rep, "agents", "opencode resolves"); got.OK {
		t.Errorf("opencode resolves OK while its binary is off PATH")
	} else if !strings.Contains(got.Remedy, "never installs") {
		t.Errorf("resolves remedy = %q, want the never-installs note", got.Remedy)
	}
	if got := findCheck(t, rep, "agents", "opencode answers"); got.OK {
		t.Errorf("opencode answers OK while it does not resolve")
	} else if !strings.Contains(got.Detail, "does not resolve") {
		t.Errorf("answers detail = %q, want the skip reason", got.Detail)
	}
	if got := findCheck(t, rep, "agents", "active_agent"); !got.OK {
		t.Errorf("active_agent not OK when auto still resolves claude: %s", got.Detail)
	} else if !strings.Contains(got.Detail, "claude") {
		t.Errorf("active_agent detail = %q, want the resolved agent", got.Detail)
	}
}

func TestProbeErrorReportsProviderText(t *testing.T) {
	isolateHome(t)
	worktree := t.TempDir()
	writeProjectYML(t, worktree, twoAgentYML)
	common := t.TempDir()
	writeHook(t, common, "/usr/local/bin/sentinel")
	stageBinaries(t, "claude", "opencode", "rg")
	env := stubEnv(t, common, map[string]string{"claude": "ok"},
		map[string]error{"opencode": errors.New("provider reported: Authentication required")})
	rep := Run(worktree, Options{Env: env})
	if got := findCheck(t, rep, "agents", "opencode resolves"); !got.OK {
		t.Errorf("opencode resolves not OK while its binary is on PATH: %s", got.Detail)
	}
	if got := findCheck(t, rep, "agents", "opencode answers"); got.OK {
		t.Errorf("opencode answers OK on a provider error")
	} else if !strings.Contains(got.Detail, "Authentication required") {
		t.Errorf("answers detail = %q, want the provider-reported cause", got.Detail)
	}
}

func TestAcpxAgentResolvesViaLauncher(t *testing.T) {
	isolateHome(t)
	worktree := t.TempDir()
	writeProjectYML(t, worktree, acpxFirstYML)
	common := t.TempDir()
	writeHook(t, common, "/usr/local/bin/sentinel")
	stageBinaries(t, "npx", "node", "rg")
	env := stubEnv(t, common, map[string]string{"nightly": "ok"}, nil)
	rep := Run(worktree, Options{Env: env})
	assertUniqueRows(t, rep)
	if got := findCheck(t, rep, "agents", "nightly resolves"); !got.OK {
		t.Errorf("nightly resolves not OK while npx/node are on PATH: %s", got.Detail)
	}
	if got := findCheck(t, rep, "agents", "nightly answers"); !got.OK {
		t.Errorf("nightly answers not OK on a live probe: %s", got.Detail)
	}
	// Auto resolution must use the same per-family logic: nightly is valid
	// even though no "nightly" binary exists on PATH.
	if got := findCheck(t, rep, "agents", "active_agent"); !got.OK {
		t.Errorf("active_agent not OK with a resolvable acpx agent first: %s", got.Detail)
	} else if !strings.Contains(got.Detail, "nightly") {
		t.Errorf("active_agent detail = %q, want nightly", got.Detail)
	}
}

func TestMissingSearchBinaryWarnsForOpenCode(t *testing.T) {
	isolateHome(t)
	worktree := t.TempDir()
	writeProjectYML(t, worktree, twoAgentYML)
	common := t.TempDir()
	writeHook(t, common, "/usr/local/bin/sentinel")
	stageBinaries(t, "claude", "opencode")
	env := stubEnv(t, common, map[string]string{"claude": "ok", "opencode": "ok"}, nil)
	if got := findCheck(t, Run(worktree, Options{Env: env}), "search", "rg"); got.OK {
		t.Errorf("search OK without rg while opencode is configured")
	}
}

func TestNeedsExternalSearch(t *testing.T) {
	cli := func() config.AgentConfig { return config.AgentConfig{Model: "m"} }
	acpx := func(token string) config.AgentConfig {
		return config.AgentConfig{Model: "m", Kind: config.AgentKindACPX, ACPAgent: token}
	}
	cases := []struct {
		name   string
		cfg    config.Config
		need   bool
		whyHas string
	}{
		{"only claude needs nothing", config.Config{AgentOrder: []string{"claude"}, Agents: map[string]config.AgentConfig{"claude": cli()}}, false, "embeds ripgrep"},
		{"opencode CLI needs rg", config.Config{AgentOrder: []string{"opencode"}, Agents: map[string]config.AgentConfig{"opencode": cli()}}, true, "opencode"},
		{"acpx opencode needs rg", config.Config{AgentOrder: []string{"nightly"}, Agents: map[string]config.AgentConfig{"nightly": acpx("opencode")}}, true, "nightly"},
		{"acpx claude needs nothing", config.Config{AgentOrder: []string{"nightly"}, Agents: map[string]config.AgentConfig{"nightly": acpx("claude")}}, false, "embeds ripgrep"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			need, why := needsExternalSearch(tc.cfg)
			if need != tc.need {
				t.Errorf("need = %v, want %v (%s)", need, tc.need, why)
			}
			if !strings.Contains(why, tc.whyHas) {
				t.Errorf("why = %q, want %q", why, tc.whyHas)
			}
		})
	}
}

func TestProbeTimeoutIsNotAProviderFailure(t *testing.T) {
	isolateHome(t)
	worktree := t.TempDir()
	writeProjectYML(t, worktree, twoAgentYML)
	common := t.TempDir()
	writeHook(t, common, "/usr/local/bin/sentinel")
	stageBinaries(t, "claude", "opencode", "rg")
	env := stubEnv(t, common, map[string]string{"claude": "ok"},
		map[string]error{"opencode": &ProbeTimeout{Agent: "opencode", Budget: probeBudgetForTest}})
	rep := Run(worktree, Options{Env: env})
	if got := findCheck(t, rep, "agents", "opencode answers"); got.OK {
		t.Errorf("opencode answers OK on a probe timeout")
	} else if !strings.Contains(got.Detail, "within") {
		t.Errorf("answers detail = %q, want the timeout bound", got.Detail)
	} else if strings.Contains(got.Remedy, "authentication") {
		t.Errorf("timeout remedy = %q, must not send the operator to credentials", got.Remedy)
	} else if !strings.Contains(got.Remedy, "wedged") {
		t.Errorf("timeout remedy = %q, want the wedged-agent guidance", got.Remedy)
	}
}
