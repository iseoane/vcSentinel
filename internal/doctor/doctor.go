// Package doctor preflights the local environment a review depends on so a
// missing tool fails fast here instead of surfacing as a review timeout.
//
// It is advisory only: Run reports and the command exits 0 like check. It
// never gates anything and never installs — every warning prints what the
// operator should run instead. Binary resolution always uses exec.LookPath,
// never a shell probe: a shell function or alias can answer `command -v`
// while the review subprocess, which inherits no shell functions, still
// fails. Version detection is local; comparing against the latest published
// release is a network call and stays behind Options.CheckUpdates.
//
// This checklist does not contradict internal/review/diagnostico.go: on the
// failure path the honest report is whatever the provider said failed, while
// here the checklist itself is the deliverable a human asked for. Keep the
// two separate.
package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ISeoane-Quental/vcSentinel/internal/config"
	"github.com/ISeoane-Quental/vcSentinel/internal/git"
	"github.com/ISeoane-Quental/vcSentinel/internal/graph"
	"github.com/ISeoane-Quental/vcSentinel/internal/setup"
)

// Check is one preflight finding. OK checks carry no remedy, and unknown
// checks (a prober that never ran) carry neither a verdict nor a remedy.
type Check struct {
	Section string
	Name    string
	OK      bool
	Detail  string
	Remedy  string
	Unknown bool
}

// Report is the full advisory preflight result. It is always produced;
// there is no error return because every failure is a finding.
type Report struct {
	Checks []Check
}

// WarnCount counts the failed checks needing operator attention. Unknown
// checks are not failures and stay out of this count.
func (r Report) WarnCount() int {
	n := 0
	for _, c := range r.Checks {
		if !c.OK && !c.Unknown {
			n++
		}
	}
	return n
}

// UnknownCount counts the checks no prober observed.
func (r Report) UnknownCount() int {
	n := 0
	for _, c := range r.Checks {
		if c.Unknown {
			n++
		}
	}
	return n
}

// Text renders the human-readable report. Unknown or missing pieces are
// reported as warnings, never as zeros or silent omissions.
func (r Report) Text() string {
	var b strings.Builder
	b.WriteString("Doctor: preflight of the review environment (advisory, exit 0)\n")
	section := ""
	for _, c := range r.Checks {
		if c.Section != section {
			section = c.Section
			fmt.Fprintf(&b, "\n[%s]\n", section)
		}
		mark := "ok"
		if c.Unknown {
			mark = "UNKNOWN"
		} else if !c.OK {
			mark = "WARN"
		}
		fmt.Fprintf(&b, "  %s %s: %s\n", mark, c.Name, c.Detail)
		if c.Remedy != "" {
			fmt.Fprintf(&b, "       remedy: %s\n", c.Remedy)
		}
	}
	fmt.Fprintf(&b, "\nWARN: %d\nUNKNOWN: %d\n", r.WarnCount(), r.UnknownCount())
	return b.String()
}

// Env carries the seams Run needs. LookPath is deliberately not a seam:
// resolution must exercise the real PATH behavior, and tests stage fake
// binaries on PATH instead.
type Env struct {
	GitCommonDir  func(path string) (string, error)
	LatestRelease func() (string, error)
	Codegraph     func(root string) []graph.Condition
	// Probe sends the minimal real prompt to one configured agent through
	// the adapter built for its kind. Tests stub it; production wires the
	// agentadapter family constructor with a short budget.
	Probe func(agent, prompt string) (string, error)
}

// Options tunes one preflight run.
type Options struct {
	// CurrentVersion is the running binary's version for --check-updates.
	CurrentVersion string
	// CheckUpdates enables the network comparison against the latest
	// published release. Off by default.
	// Progress receives one line before each long step (each agent probe,
	// the update check) so the operator sees motion instead of silence.
	// Nil is the default: callers that stream nothing stay safe.
	CheckUpdates bool
	Progress     func(string)
	Env          Env
}

func (o *Options) withDefaults() {
	if o.Env.GitCommonDir == nil {
		o.Env.GitCommonDir = git.GetGitCommonDir
	}
	if o.Env.LatestRelease == nil {
		o.Env.LatestRelease = setup.LatestReleaseTag
	}
	if o.Env.Codegraph == nil {
		o.Env.Codegraph = graph.PreflightCodeGraph
	}
	if o.Env.Probe == nil {
		o.Env.Probe = func(agent, _ string) (string, error) {
			return "", fmt.Errorf("probe for %s is not wired", agent)
		}
	}
}

// Run preflights the review environment of the worktree at worktreePath and
// always returns a report.
func Run(worktreePath string, opts Options) Report {
	opts.withDefaults()
	var rep Report
	add := func(section, name string, ok bool, detail, remedy string) {
		if ok {
			remedy = ""
		}
		rep.Checks = append(rep.Checks, Check{Section: section, Name: name, OK: ok, Detail: detail, Remedy: remedy})
	}

	cfg, err := config.LoadStrictLocalConfig(worktreePath)
	if err != nil {
		add("config", "strict_load", false, fmt.Sprintf("vassentinel.yml fails strict load: %v", err), "fix the yml so it loads strictly; agent checks are skipped until it does")
		return rep
	}
	add("config", "strict_load", true, fmt.Sprintf("yml loads strictly (%d agents, active_agent %q)", len(cfg.Agents), cfg.ActiveAgent), "")

	for _, name := range cfg.AgentOrder {
		agent, known := cfg.Agents[name]
		if !known {
			continue
		}
		checkOneAgent(name, agent, opts, add)
	}
	if resolved, detail := resolveActive(cfg); resolved == "" {
		add("agents", "active_agent", false, detail, "install one configured agent binary so it resolves on PATH (doctor never installs)")
	} else {
		add("agents", "active_agent", true, detail, "")
	}
	checkSearch(cfg, add)
	for _, cond := range opts.Env.Codegraph(worktreePath) {
		add("codegraph", cond.Name, cond.OK, cond.Detail, codegraphRemedy(cond))
		rep.Checks[len(rep.Checks)-1].Unknown = cond.Unknown
	}
	checkHook(worktreePath, opts, add)
	if opts.CheckUpdates {
		opts.announce("checking for updates…")
		checkUpdates(opts, add)
	}
	return rep
}

func codegraphRemedy(cond graph.Condition) string {
	if cond.OK || cond.Unknown {
		return ""
	}
	switch cond.Name {
	case graph.CondBinary:
		return "install the CodeGraph CLI so exec.LookPath finds codegraph on PATH (doctor never installs)"
	case graph.CondIndexDir:
		return "index this repository with the CodeGraph CLI"
	case graph.CondHead:
		return "run inside a git worktree with a resolvable HEAD"
	case graph.CondWorktreeClean:
		return "commit or stash before reviewing so context is computed on the audited tree"
	case graph.CondIndexInitialized:
		return "initialize the codegraph index for this repository"
	case graph.CondProjectPath:
		return "re-index codegraph for this worktree path"
	case graph.CondPendingChanges:
		return "let codegraph consume its pending changes before reviewing"
	case graph.CondWorktreeMatch:
		return "re-sync the codegraph index with this worktree"
	default:
		return ""
	}
}

func checkHook(worktreePath string, opts Options, add func(string, string, bool, string, string)) {
	common, err := opts.Env.GitCommonDir(worktreePath)
	if err != nil {
		add("hook", "pre-commit", false, fmt.Sprintf("cannot locate the git common dir: %v", err), "run inside a git worktree")
		return
	}
	raw, err := os.ReadFile(filepath.Join(common, "hooks", "pre-commit"))
	if err != nil {
		add("hook", "pre-commit", false, "no pre-commit hook installed", "run sentinel init to install the hook")
		return
	}
	target, ok := hookTarget(string(raw))
	if !ok {
		add("hook", "pre-commit", false, "hook content is not the sentinel shape", "run sentinel init to reinstall the hook")
		return
	}
	if isVersionedBinTarget(target) {
		add("hook", "pre-commit", false, fmt.Sprintf("hook points at %s under bin/<version>/, which changes every release (the T0.0 trap), leaving a stale hook behind", target), "reinstall the hook from a stable binary with sentinel init")
		return
	}
	add("hook", "pre-commit", true, fmt.Sprintf("hook points at stable binary %s", target), "")
}

// isVersionedBinTarget reports the T0.0 trap shape: a hook binary installed
// as <anything>/bin/<version>/sentinel, where the version directory changes
// on every release. Installed locations (GOPATH/bin, /usr/local/bin,
// ~/.vas_sentinel/bin) have a non-version parent and are stable.
func isVersionedBinTarget(target string) bool {
	parts := strings.Split(strings.Trim(target, "/"), "/")
	if len(parts) < 3 {
		return false
	}
	parent, grandparent := parts[len(parts)-2], parts[len(parts)-3]
	return grandparent == "bin" && looksLikeVersion(parent)
}

// looksLikeVersion accepts dot-separated all-numeric segments such as 0.2.0.
func looksLikeVersion(segment string) bool {
	parts := strings.Split(segment, ".")
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// hookTarget extracts the quoted binary path from the generated hook shape:
// #!/bin/sh\n"<binary>" check --staged\n.
func hookTarget(body string) (string, bool) {
	lines := strings.Split(body, "\n")
	if len(lines) < 2 || lines[0] != "#!/bin/sh" {
		return "", false
	}
	rest := strings.TrimSpace(lines[1])
	if !strings.HasPrefix(rest, "\"") {
		return "", false
	}
	end := strings.Index(rest[1:], "\"")
	if end < 0 {
		return "", false
	}
	target := rest[1 : 1+end]
	if target == "" || !strings.HasSuffix(rest[1+end+1:], "check --staged") {
		return "", false
	}
	return filepath.ToSlash(target), true
}

func checkUpdates(opts Options, add func(string, string, bool, string, string)) {
	latest, err := opts.Env.LatestRelease()
	if err != nil {
		add("updates", "latest_release", false, fmt.Sprintf("could not reach the release feed: %v; staying on %s", err, opts.CurrentVersion), "retry with network access")
		return
	}
	switch cmp := compareVersions(opts.CurrentVersion, latest); {
	case cmp < 0:
		add("updates", "latest_release", false, fmt.Sprintf("%s is running, %s is published", opts.CurrentVersion, latest), "run sentinel upgrade")
	case cmp > 0:
		add("updates", "latest_release", true, fmt.Sprintf("%s is running, newer than published %s", opts.CurrentVersion, latest), "")
	default:
		add("updates", "latest_release", true, fmt.Sprintf("%s matches published %s", opts.CurrentVersion, latest), "")
	}
}

// announce reports one progress line when the caller wired a callback. The
// announcement precedes the blocking call it describes, never trails it.
func (o Options) announce(msg string) {
	if o.Progress != nil {
		o.Progress(msg)
	}
}

// compareVersions orders two versions after stripping one leading "v".
// Unparseable input compares equal: a development build must not report a
// phantom update.
func compareVersions(current, latest string) int {
	norm := func(v string) []int {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		var out []int
		for _, part := range strings.Split(v, ".") {
			n, err := strconv.Atoi(part)
			if err != nil {
				return nil
			}
			out = append(out, n)
		}
		return out
	}
	a, b := norm(current), norm(latest)
	if a == nil || b == nil {
		return 0
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}
