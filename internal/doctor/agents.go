package doctor

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// probePrompt is the minimal real prompt each configured agent must answer.
// It asks for one word so a healthy agent spends almost nothing, while an
// authentication, network, or model failure surfaces the provider's own
// error instead of a later review timeout.
const probePrompt = "Reply with exactly: ok"

// ProbeTimeout reports that the agent did not answer the probe prompt within
// the probe's own budget. It is not a provider refusal: authentication,
// network, and model errors arrive as ordinary errors through the adapter,
// usually fast. A call still running when the budget expires means the agent
// is slow or wedged, and the remedy must say that instead of sending the
// operator to check credentials.
type ProbeTimeout struct {
	Agent  string
	Budget time.Duration
}

func (e *ProbeTimeout) Error() string {
	return fmt.Sprintf("%s did not answer the probe prompt within %s", e.Agent, e.Budget)
}

// resolves reports whether the agent's launch path exists on PATH, per
// family: the CLI binary itself, or the npx/node spawn chain for acpx.
func resolves(name string, agent config.AgentConfig) (string, bool) {
	if agent.Kind == config.AgentKindACPX {
		npx, err := exec.LookPath("npx")
		if err != nil {
			return "", false
		}
		if _, err := exec.LookPath("node"); err != nil {
			return "", false
		}
		return npx, true
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	return path, true
}

func checkOneAgent(name string, agent config.AgentConfig, opts Options, add func(string, string, bool, string, string)) {
	if _, ok := resolves(name, agent); !ok {
		want := fmt.Sprintf("install the %s CLI so exec.LookPath finds it on PATH (doctor never installs)", name)
		if agent.Kind == config.AgentKindACPX {
			want = "install npx and node so exec.LookPath finds the acpx spawn chain on PATH (doctor never installs)"
		}
		add("agents", name+" resolves", false, fmt.Sprintf("%s does not resolve on PATH", describeTarget(name, agent)), want)
		add("agents", name+" answers", false, fmt.Sprintf("skipped: %s does not resolve", name), "")
		return
	}
	add("agents", name+" resolves", true, fmt.Sprintf("%s resolves on PATH", describeTarget(name, agent)), "")
	answer, err := opts.Env.Probe(name, probePrompt)
	if err != nil {
		var timeout *ProbeTimeout
		if errors.As(err, &timeout) {
			add("agents", name+" answers", false, fmt.Sprintf("%s did not answer the probe prompt within %s (the probe's own budget, not a provider refusal)", name, timeout.Budget), fmt.Sprintf("raise the probe bound if %s is slow, or check whether the agent process is wedged; doctor never changes credentials", name))
			return
		}
		add("agents", name+" answers", false, fmt.Sprintf("%s does not answer the probe prompt: %v", name, err), fmt.Sprintf("resolve the provider-reported failure for %s (authentication, network, or model access); doctor never changes credentials", name))
		return
	}
	first, _, _ := strings.Cut(strings.TrimSpace(answer), "\n")
	if len(first) > 120 {
		first = first[:120]
	}
	add("agents", name+" answers", true, fmt.Sprintf("%s answers the probe prompt: %s", name, first), "")
}

func describeTarget(name string, agent config.AgentConfig) string {
	if agent.Kind == config.AgentKindACPX {
		return fmt.Sprintf("acpx agent %q (token %s)", name, agent.ACPAgent)
	}
	return name
}

// resolveActive reports which agent active_agent selects, using the same
// per-family resolution as the agent rows. Auto follows the declaration
// order in the yml: the first agent that resolves wins.
func resolveActive(cfg config.Config) (string, string) {
	if cfg.ActiveAgent == "" || cfg.ActiveAgent == "auto" {
		for _, name := range cfg.AgentOrder {
			agent, known := cfg.Agents[name]
			if !known {
				continue
			}
			if _, ok := resolves(name, agent); ok {
				return name, fmt.Sprintf("auto resolves to %s", name)
			}
		}
		return "", "auto resolves to nothing: no configured agent resolves on PATH"
	}
	agent, known := cfg.Agents[cfg.ActiveAgent]
	if !known {
		return "", fmt.Sprintf("%q is not a configured agent", cfg.ActiveAgent)
	}
	if _, ok := resolves(cfg.ActiveAgent, agent); ok {
		return cfg.ActiveAgent, fmt.Sprintf("%q resolves on PATH", cfg.ActiveAgent)
	}
	return "", fmt.Sprintf("%q does not resolve on PATH", cfg.ActiveAgent)
}

// needsExternalSearch reports whether any configured reviewer shells out to
// an external ripgrep: Claude Code embeds it, OpenCode does not
// (docs/reingenieria/f9-observabilidad.md). acpx runtimes with a non-claude
// token cannot be judged statically, so they count as needing it: a wrong
// warning is cheaper than a ten-minute timeout.
func needsExternalSearch(cfg config.Config) (bool, string) {
	var cli, acpx []string
	for _, name := range cfg.AgentOrder {
		agent, known := cfg.Agents[name]
		if !known {
			continue
		}
		if agent.Kind == config.AgentKindACPX {
			if agent.ACPAgent != "claude" {
				acpx = append(acpx, name)
			}
			continue
		}
		base := strings.ToLower(strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)))
		if base == "opencode" {
			cli = append(cli, name)
		}
	}
	switch {
	case len(cli) > 0:
		return true, fmt.Sprintf("%s shells out to an external rg", strings.Join(cli, ", "))
	case len(acpx) > 0:
		return true, fmt.Sprintf("%s runs over acpx, whose search backend cannot be verified statically", strings.Join(acpx, ", "))
	default:
		return false, "no configured reviewer needs external search (Claude embeds ripgrep)"
	}
}

func checkSearch(cfg config.Config, add func(string, string, bool, string, string)) {
	path, err := exec.LookPath("rg")
	needed, why := needsExternalSearch(cfg)
	if err != nil {
		if !needed {
			add("search", "rg", true, fmt.Sprintf("rg absent and unneeded: %s", why), "")
			return
		}
		add("search", "rg", false, fmt.Sprintf("rg not found on PATH and %s", why), "install ripgrep so exec.LookPath finds rg on PATH (doctor never installs)")
		return
	}
	add("search", "rg", true, fmt.Sprintf("rg resolves to %s", path), "")
}
