package agentadapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/process"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewsnapshot"
)

// CommandTimeout is the limit of one call to the agent (300 s). Phase 1 makes
// it configurable via review.timeout in vassentinel.yml; this is the fallback
// when an adapter does not set Timeout.
const CommandTimeout = 300 * time.Second

var commitMessagePattern = regexp.MustCompile(`^(build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test)(\([a-zA-Z0-9._/-]+\))?!?: .+$`)

type CLIAdapter struct {
	BinaryName string
	Config     config.AgentConfig
	// CommitLanguage sets the language of the generated commit messages. If
	// empty, DefaultLanguage is used.
	CommitLanguage string
	// Timeout is the per-call limit; if 0, CommandTimeout is used.
	Timeout time.Duration

	// activeTree holds the restricted reviewer's currently owned process
	// tree, or nil when no review child is running. The execution controller
	// reads it at abort time to escalate against the whole tree.
	activeTree atomic.Pointer[process.Tree]
}

// OwnedTree reports the live owned review process tree, or nil.
func (c *CLIAdapter) OwnedTree() *process.Tree {
	return c.activeTree.Load()
}

// ReviewRequest limits an agent review to read-only exploration of planned paths.
type ReviewRequest struct {
	Prompt       string
	SHA          string
	Paths        []string
	SnapshotDir  string
	MaxToolCalls int
	ToolPolicy   reviewcontract.ToolPolicy
}

// defaultReviewToolCalls is the OpenCode agent-configuration "Steps" turn
// budget applied to a restricted review when the caller supplies none.
//
// It is PROVISIONAL and unmeasured. It was raised from 8 to 16 believing that
// budget exhaustion caused the truncated reviews; captured provider streams
// then DISPROVED that. A denied tool call kills the turn: across 11
// invocations of one review, the 3 that recorded a permission rejection all
// ended "tool-calls" — at 4 and 5 turns out of 16, nowhere near the budget —
// and the 8 without a rejection all ended "stop". The real fix is that the
// snapshot now carries the whole committed tree so the reviewer is not denied
// the context it needs; this value merely stopped being the suspect.
//
// docs/issues/actionable.md, "Recalibrate or retire the OpenCode
// reviewer turn budget", tracks replacing this guess with a measurement.
// The Claude branch of reviewCommand intentionally ignores this value: its
// own comment there explains there is no confirmed flag to cap Claude Code's
// turn count.
const defaultReviewToolCalls = 16

// RunPrompt runs the binary with an arbitrary prompt and returns the output.
// It is the audit engine's public path to the agent.
func (c *CLIAdapter) RunPrompt(prompt string) (string, error) {
	return c.runCommand(prompt)
}

// RunReview runs a semantic review with the bounded tool profile.
// The legacy contract carries no context: the review runs under the adapter's
// own timeout budget only. Context-carrying callers go through
// ReviewWithContext so cooperative cancellation reaches the provider process.
func (c *CLIAdapter) RunReview(prompt, sha string, paths []string) (string, error) {
	return c.ReviewWithPolicy(prompt, sha, paths, reviewcontract.DefaultToolPolicy())
}

// ReviewWithPolicy translates one provider-neutral contract policy
// into this provider's command and configuration syntax.
func (c *CLIAdapter) ReviewWithPolicy(prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error) {
	return c.reviewWithContextPolicy(context.Background(), prompt, sha, paths, policy)
}

func (c *CLIAdapter) GetCommitMessage(paths []string, layer string, batchNum int) (string, error) {
	if c.isOpenCode() || c.isClaude() {
		return "", fmt.Errorf("%s requires the consented micro-diff path to generate commit messages", c.baseName())
	}
	output, err := c.runCommand(buildAgentPrompt(layer, batchNum, paths, c.commitLanguage()))
	if err != nil {
		return "", err
	}
	return validateCommitMessage(output)
}

// GetCommitMessageWithDiff incorporates the prepared change into the prompt so
// the agent does not need to read the repository during message generation.
func (c *CLIAdapter) GetCommitMessageWithDiff(paths []string, layer string, batchNum int, diff string) (string, error) {
	prompt := buildAgentPromptWithDiff(layer, batchNum, paths, diff, c.commitLanguage())
	return c.runCommitMessageCommand(prompt)
}

// ProposeRefactorPlan asks the agent for a split plan for a massive code
// file. Implements AdapterRefactor.
func (c *CLIAdapter) ProposeRefactorPlan(filePath string) (string, error) {
	return c.runCommand(buildRefactorPrompt(filePath))
}

// ApplyRefactorPlan orders the agent to run the refactor plan directly on the
// working tree, without committing. Implements AdapterRefactor.
func (c *CLIAdapter) ApplyRefactorPlan(filePath string, plan string) (string, error) {
	return c.runCommand(buildApplyRefactorPrompt(filePath, plan))
}

// runCommand runs the agent's binary with the given prompt and returns the
// complete (trimmed) standard output. For opencode and claude the prompt
// travels via stdin (see promptCommand); for the other binaries it is passed
// as the "-p" argument. The process is cut off with CommandTimeout if the
// agent does not answer: an agent waiting for interactive input must not
// hang the audit.
func (c *CLIAdapter) runCommand(prompt string) (string, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = CommandTimeout
	}
	return c.runCommandWithTimeout(prompt, timeout)
}

func (c *CLIAdapter) runCommitMessageCommand(prompt string) (string, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = CommandTimeout
	}
	if !c.isOpenCode() && !c.isClaude() {
		output, err := c.runCommandWithTimeout(prompt, timeout)
		if err != nil {
			return "", err
		}
		return validateCommitMessage(output)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if c.isClaude() {
		cmd, cleanup, err := c.prepareClaudeCommitCommand(ctx, prompt)
		if err != nil {
			return "", err
		}
		defer cleanup()

		var out, stderr bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			if detail := strings.TrimSpace(stderr.String()); detail != "" {
				return "", fmt.Errorf("generate commit message with claude: %w: %s", err, detail)
			}
			return "", err
		}
		// Claude Code's plain-text "-p" output already IS the final message
		// (unlike OpenCode's "--format json" NDJSON stream), so it goes
		// straight through the same format check as the unrestricted path.
		return validateCommitMessage(out.String())
	}

	cmd, cleanup, err := c.prepareCommitCommand(ctx, prompt)
	if err != nil {
		return "", err
	}
	defer cleanup()

	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return "", fmt.Errorf("generate commit message with opencode: %w: %s", err, detail)
		}
		return "", err
	}
	return extractOpenCodeCommitMessage(out.String())
}

// prepareCommitCommand runs OpenCode outside the repository and without
// external plugins. The micro-diff already travels in the prompt, so no
// context is lost.
func (c *CLIAdapter) prepareCommitCommand(ctx context.Context, prompt string) (*exec.Cmd, func(), error) {
	dir, err := os.MkdirTemp("", "vas-sentinel-commit-")
	if err != nil {
		return nil, nil, fmt.Errorf("create neutral directory for opencode: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	args := []string{"run", "--pure", "--agent", "title", "--format", "json"}
	if c.Config.Model != "" {
		args = append(args, "--model", c.Config.Model)
	}
	if c.Config.ReasoningEffort != "" {
		args = append(args, "--variant", c.Config.ReasoningEffort)
	}
	args = append(args, "--dir", dir)
	cmd := exec.CommandContext(ctx, c.BinaryName, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("OPENCODE_MODEL=%s", c.Config.Model),
		fmt.Sprintf("OPENCODE_REASONING_EFFORT=%s", c.Config.ReasoningEffort),
	)
	cmd.Stdin = strings.NewReader(prompt)
	return cmd, cleanup, nil
}

// prepareClaudeCommitCommand runs Claude Code in an empty temporary working
// directory with declarative CLI permission flags. The micro-diff already
// travels complete in the prompt. Tests verify the generated arguments and
// working directory only: local `claude --help` documents these flags, but no
// integration test proves a live path matcher or an OS filesystem sandbox.
func (c *CLIAdapter) prepareClaudeCommitCommand(ctx context.Context, prompt string) (*exec.Cmd, func(), error) {
	dir, err := os.MkdirTemp("", "vas-sentinel-commit-")
	if err != nil {
		return nil, nil, fmt.Errorf("create neutral directory for claude: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	args := []string{"-p", "--safe-mode", "--tools", ""}
	if c.Config.Model != "" {
		args = append(args, "--model", c.Config.Model)
	}
	if c.Config.ReasoningEffort != "" {
		args = append(args, "--effort", c.Config.ReasoningEffort)
	}
	cmd := exec.CommandContext(ctx, c.BinaryName, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(prompt)
	return cmd, cleanup, nil
}

func extractOpenCodeCommitMessage(output string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	message := ""
	texts := 0
	lines := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			return "", fmt.Errorf("invalid opencode JSONL output: empty line")
		}
		lines++
		var event struct {
			Type string `json:"type"`
			Part struct {
				Text string `json:"text"`
			} `json:"part"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			return "", fmt.Errorf("invalid opencode JSONL output: %w", err)
		}
		if event.Type != "text" {
			continue
		}
		texts++
		if texts > 1 {
			return "", fmt.Errorf("opencode returned multiple text events")
		}
		message = event.Part.Text
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("invalid opencode JSONL output: %w", err)
	}
	if lines == 0 {
		return "", fmt.Errorf("empty opencode JSONL output")
	}
	if texts != 1 {
		return "", fmt.Errorf("opencode returned no text event")
	}
	return validateCommitMessage(message)
}

func validateCommitMessage(output string) (string, error) {
	message := strings.TrimSpace(output)
	if strings.ContainsAny(message, "\r\n") || !commitMessagePattern.MatchString(message) {
		return "", fmt.Errorf("agent did not return a single-line Conventional Commit")
	}
	return message, nil
}

// runCommandWithTimeout is the parameterized variant of runCommand;
// it lets tests shorten the wait without touching the production constant.
func (c *CLIAdapter) runCommandWithTimeout(prompt string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args, viaStdin := c.promptCommand(prompt)
	cmd := exec.CommandContext(ctx, c.BinaryName, args...)
	env := os.Environ()

	if c.isClaude() {
		env = append(env, fmt.Sprintf("CLAUDE_CODE_MODEL=%s", c.Config.Model))
		env = append(env, fmt.Sprintf("CLAUDE_CODE_REASONING=%s", c.Config.ReasoningEffort))
	} else if c.isOpenCode() {
		env = append(env, fmt.Sprintf("OPENCODE_MODEL=%s", c.Config.Model))
		env = append(env, fmt.Sprintf("OPENCODE_REASONING_EFFORT=%s", c.Config.ReasoningEffort))
	}
	cmd.Env = env

	var out bytes.Buffer
	cmd.Stdout = &out
	// opencode and claude read the prompt from stdin; the other binaries
	// receive it as an argument. Passing the prompt via stdin avoids the
	// 32,767-character limit of the Windows command line.
	if viaStdin {
		cmd.Stdin = strings.NewReader(prompt)
	}
	if err := cmd.Run(); err != nil {
		return "", err
	}

	return strings.TrimSpace(out.String()), nil
}

func (c *CLIAdapter) reviewCommand(request ReviewRequest) ([]string, map[string]string, error) {
	policy := request.ToolPolicy
	if policy == (reviewcontract.ToolPolicy{}) {
		policy = reviewcontract.DefaultToolPolicy()
	}
	if !policy.AllowRead || !policy.AllowSearch || !policy.RequireImmutableSnapshot || policy.AllowMutation || policy.AllowShell || policy.AllowNetwork {
		return nil, nil, fmt.Errorf("semantic review requires the canonical read/search-only immutable snapshot tool policy")
	}
	maxToolCalls := request.MaxToolCalls
	if maxToolCalls <= 0 {
		maxToolCalls = defaultReviewToolCalls
	}
	if c.isClaude() {
		if request.SnapshotDir == "" {
			return nil, nil, fmt.Errorf("semantic review requires an immutable snapshot directory")
		}
		snapshotPattern := filepath.ToSlash(filepath.Join(request.SnapshotDir, "**"))
		allowedTools := strings.Join([]string{
			"Read(" + snapshotPattern + ")",
			"Grep(" + snapshotPattern + ")",
			"Glob(" + snapshotPattern + ")",
		}, ",")
		// These flags declaratively select read/search tools, snapshot patterns,
		// and non-interactive permission handling. Tests assert only the emitted
		// command shape; they do not establish an OS sandbox or live matching.
		// There is no confirmed Claude Code flag/env var to cap tool-call
		// count (OpenCode's "Steps"), so maxToolCalls is intentionally unused
		// here; do not invent one.
		args := []string{"-p", "--safe-mode", "--permission-mode", "dontAsk", "--tools", "Read,Grep,Glob", "--allowed-tools", allowedTools, "--disallowed-tools", "Bash,Edit,Write"}
		if c.Config.Model != "" {
			args = append(args, "--model", c.Config.Model)
		}
		if c.Config.ReasoningEffort != "" {
			args = append(args, "--effort", c.Config.ReasoningEffort)
		}
		// --output-format json switches stdout from the human-rendered answer
		// to a single result object that scanClaudeReview normalizes: the same
		// observable answer (probe-verified text parity), plus the wire usage
		// and the terminal stop reason. The flag is valid with --print, which
		// "-p" is. Probed live against Claude Code 2.1.263 with the production
		// reviewer invocation; the redacted capture is tracked as
		// testdata/claude/usage-probe.json.
		args = append(args, "--output-format", "json")
		return args, nil, nil
	}
	if !c.isOpenCode() {
		return nil, nil, fmt.Errorf("semantic review is unavailable: path-confined tool permissions are not configured for this provider")
	}
	if request.SnapshotDir == "" {
		return nil, nil, fmt.Errorf("semantic review requires an immutable snapshot directory")
	}
	permissions := map[string]any{
		"bash":  map[string]string{"*": "deny"},
		"edit":  map[string]string{"*": "deny"},
		"write": map[string]string{"*": "deny"},
		"read":  openCodeReadPermissions(request.SnapshotDir),
		// Grep and Glob receive user-supplied search expressions, not the paths
		// found by those searches. Restricting them to audited filenames would
		// reject ordinary expressions while contributing no snapshot containment.
		"grep": map[string]string{"*": "allow"},
		"glob": map[string]string{"*": "allow"},
	}
	// The agent-level fallback is "ask", not "deny": live probing (OpenCode
	// 1.18.23) showed deny dominance — once any matching rule denies, later
	// exact allows never win, so an agent-level deny would block every
	// admitted read. In a non-interactive run "ask" auto-rejects, which keeps
	// the boundary fail-closed (unmatched tools, external reads, inherited MCP
	// servers) without shadowing the explicit read allows. The read map itself
	// carries no wildcard for the same shadowing reason.
	permission := make(map[string]any, len(permissions)+1)
	permission["*"] = "ask"
	for tool, rules := range permissions {
		permission[tool] = rules
	}
	configuration := struct {
		Agent map[string]struct {
			Model           string         `json:"model,omitempty"`
			ReasoningEffort string         `json:"reasoningEffort,omitempty"`
			Steps           int            `json:"steps"`
			Permission      map[string]any `json:"permission"`
		} `json:"agent"`
	}{Agent: map[string]struct {
		Model           string         `json:"model,omitempty"`
		ReasoningEffort string         `json:"reasoningEffort,omitempty"`
		Steps           int            `json:"steps"`
		Permission      map[string]any `json:"permission"`
	}{"reviewer": {Model: c.Config.Model, ReasoningEffort: c.Config.ReasoningEffort, Steps: maxToolCalls, Permission: permission}}}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		panic(fmt.Sprintf("review configuration cannot be serialized: %v", err))
	}
	args := []string{"run", "--pure", "--agent", "reviewer"}
	if c.Config.Model != "" {
		args = append(args, "--model", c.Config.Model)
	}
	args = append(args, "--dir", request.SnapshotDir)
	// --format json switches stdout from the human-rendered answer to the
	// NDJSON event stream scanOpenCodeReview normalizes: same observable
	// answer (the ordered text parts, trimmed once), plus the wire usage and
	// the terminal stop reason. Probed live against OpenCode 1.18.29 with the
	// production reviewer invocation; the redacted capture is tracked as
	// testdata/opencode/usage-probe.ndjson.
	args = append(args, "--format", "json")
	return args, map[string]string{"OPENCODE_CONFIG_CONTENT": string(encoded)}, nil
}

type openCodeReadPermissionRules struct {
	allowed []string
}

// anchoredReadForms returns the resource forms that grant read access to the
// whole snapshot subtree, every one anchored to the snapshot directory
// itself. Item 1 materializes the whole committed tree into the snapshot as
// read-only context (not just the audited paths that used to drive this
// function), so the grant widens from one entry per audited path to the
// whole subtree — mirroring the Claude branch's Read(<snapshot>/**).
//
// Live probing (OpenCode 1.18.23) showed the permission resource is
// normalized inconsistently: absolute calls may be evaluated with the
// leading slash stripped. This keeps that normalization discipline for the
// subtree pattern instead of dropping it, plus the platform-native
// separator form for Windows parity (identical to the slash form on POSIX,
// where it collapses via dedup). A bare "**" is deliberately never emitted:
// under globstar semantics it is unanchored and could match resources
// outside the snapshot (an external path, a sibling temp directory), which
// would turn "widen read inside the immutable snapshot" into "grant read
// everywhere" — exactly what this change must not do.
//
// Determination (branch audit, item 3): a review of this branch flagged that
// granting read ONLY through these snapshot-anchored, absolute-path forms
// could stop matching a RELATIVE read — the reviewer runs with its working
// directory set to the snapshot (see reviewCommand's --dir wiring), and a
// relative call would fall through to the "*": "ask" fallback, auto-reject,
// and kill the turn, exactly the defect this branch exists to fix. The
// captured provider streams from a full 31-invocation audit of this branch
// show OpenCode issuing every read with an absolute path (e.g. filePath:
// /tmp/vas-sentinel-review-<id>/internal/...), which these anchored forms
// already match, with zero truncations and zero denials across those 31
// invocations. The warning is real in principle — OpenCode's own docs do not
// guarantee absolute-only read paths — but not observed in practice.
// Enumerating every relative form for this repository's tree was rejected:
// it would add on the order of 1800 permission entries, all to cover a case
// the evidence above does not show occurring. A bare "**" was rejected for
// the reason already documented above: it destroys the snapshot containment
// that is the entire point of this restricted profile. Net: no behavior
// change from this determination. If it is wrong, the observable symptom is
// a denied read of a path that is genuinely inside the snapshot — that is
// the signal to revisit this determination, not a truncated turn in
// general, since other causes of truncation exist (see TruncatedTurnError).
func anchoredReadForms(snapshot string) []string {
	slashAbsolute := filepath.ToSlash(filepath.Clean(snapshot)) + "/**"
	nativeAbsolute := filepath.Clean(snapshot) + string(filepath.Separator) + "**"
	candidates := []string{
		slashAbsolute,
		strings.TrimPrefix(slashAbsolute, "/"),
		nativeAbsolute,
	}
	seen := make(map[string]bool, len(candidates))
	forms := make([]string, 0, len(candidates))
	for _, form := range candidates {
		if !seen[form] {
			seen[form] = true
			forms = append(forms, form)
		}
	}
	return forms
}

func openCodeReadPermissions(snapshot string) openCodeReadPermissionRules {
	return openCodeReadPermissionRules{allowed: anchoredReadForms(snapshot)}
}

func (rules openCodeReadPermissionRules) MarshalJSON() ([]byte, error) {
	var encoded bytes.Buffer
	encoded.WriteByte('{')
	for i, resource := range rules.allowed {
		if i > 0 {
			encoded.WriteByte(',')
		}
		key, err := json.Marshal(resource)
		if err != nil {
			return nil, err
		}
		encoded.Write(key)
		encoded.WriteString(`:"allow"`)
	}
	encoded.WriteByte('}')
	return encoded.Bytes(), nil
}

// newRestrictedReviewEnvironment isolates only OpenCode's writable provider
// state. Other providers retain their host environment and credential model.
func (c *CLIAdapter) newRestrictedReviewEnvironment(configuration, snapshot string) ([]string, func(), error) {
	if c.isOpenCode() {
		return newReviewEnvironment(configuration, c.Config.Model, snapshot)
	}
	if !c.isClaude() {
		return os.Environ(), func() {}, nil
	}
	env := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && key == "OPENCODE_AUTH_CONTENT" {
			continue
		}
		env = append(env, entry)
	}
	return env, func() {}, nil
}

// newReviewEnvironment gives one OpenCode invocation its own writable state
// directory. The snapshot remains the provider's working directory and read
// target; HOME and XDG state must never share the published evidence tree.
func newReviewEnvironment(configuration, model, snapshot string) ([]string, func(), error) {
	parent := ""
	if snapshot != "" {
		absoluteSnapshot, err := filepath.Abs(snapshot)
		if err != nil {
			return nil, nil, fmt.Errorf("resolve review snapshot path: %w", err)
		}
		parent = filepath.Dir(absoluteSnapshot)
	}
	isolationRoot, err := os.MkdirTemp(parent, reviewsnapshot.ProviderStatePrefix)
	if err != nil {
		return nil, nil, fmt.Errorf("create provider isolation root: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(isolationRoot) }
	return reviewEnvironment(configuration, isolationRoot, model), cleanup, nil
}

func reviewEnvironment(configuration, isolationRoot, model string) []string {
	blocked := map[string]bool{
		"OPENCODE_CONFIG": true, "OPENCODE_CONFIG_CONTENT": true, "OPENCODE_CONFIG_DIR": true,
		"OPENCODE_TEST_HOME": true, "OPENCODE_PURE": true, "OPENCODE_DISABLE_PROJECT_CONFIG": true,
		"HOME": true, "USERPROFILE": true, "XDG_CONFIG_HOME": true,
		"XDG_STATE_HOME": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true,
		"OPENCODE_AUTH_CONTENT": true,
	}
	env := make([]string, 0, len(os.Environ())+10)
	authContent := ""
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if key == "OPENCODE_AUTH_CONTENT" {
			authContent = value
		}
		if !blocked[key] {
			env = append(env, entry)
		}
	}
	env = append(env,
		"OPENCODE_CONFIG_CONTENT="+configuration,
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"OPENCODE_PURE=1",
		"OPENCODE_TEST_HOME="+isolationRoot,
		"HOME="+isolationRoot,
		"XDG_CONFIG_HOME="+filepath.Join(isolationRoot, ".config"),
		"XDG_STATE_HOME="+filepath.Join(isolationRoot, ".local", "state"),
		"XDG_CACHE_HOME="+filepath.Join(isolationRoot, ".cache"),
		// XDG_DATA_HOME is isolated too, not just left blocked: if it were
		// inherited unblocked (or simply absent) it would still resolve to
		// the host's real auth.json directory, giving OpenCode an unscoped
		// fallback whenever scopeCredentialToProvider below yields nothing
		// (malformed or non-matching credentials). Pointing it at the empty
		// provider root closes that fallback path.
		"XDG_DATA_HOME="+filepath.Join(isolationRoot, ".local", "share"),
	)
	// Isolating HOME strands OpenCode's real auth.json (it lives under the
	// host's data dir). Whatever the credential source — an inherited
	// OPENCODE_AUTH_CONTENT or the host's auth.json — scopeCredentialToProvider
	// is applied uniformly below, so the sandboxed reviewer only ever sees the
	// one provider entry this review actually uses, never a whole
	// multi-provider credential store.
	if authContent == "" {
		authContent = hostAuthContent()
	}
	if scoped := scopeCredentialToProvider(authContent, model); scoped != "" {
		env = append(env, "OPENCODE_AUTH_CONTENT="+scoped)
	}
	env = append(env, "USERPROFILE="+isolationRoot)
	return env
}

// hostAuthContentPath resolves the real OpenCode auth.json path outside the
// isolated sandbox, following the same XDG data dir convention OpenCode uses.
// Returns "" if no host home directory can be resolved: a relative fallback
// would read auth.json from whatever the process's cwd happens to be, which
// under review may be the audited project itself.
func hostAuthContentPath() string {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home := os.Getenv("HOME")
		if home == "" {
			home = os.Getenv("USERPROFILE")
		}
		if home == "" {
			return ""
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "opencode", "auth.json")
}

// hostAuthContent reads the host's auth.json, or "" if it cannot be resolved
// or read.
func hostAuthContent() string {
	path := hostAuthContentPath()
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// scopeCredentialToProvider parses a multi-provider credential payload
// (auth.json's layout, keyed by provider name) and returns a JSON object
// containing only the entry for model's provider (the segment before "/").
// Applied the same way regardless of where the credential came from —
// inherited OPENCODE_AUTH_CONTENT or the host's auth.json — so neither source
// can bypass provider scoping. Any parse failure or missing entry returns "":
// fail closed rather than let an unscoped credential reach the sandbox.
func scopeCredentialToProvider(raw, model string) string {
	if raw == "" {
		return ""
	}
	provider, _, ok := strings.Cut(model, "/")
	if !ok || provider == "" {
		return ""
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &all); err != nil {
		return ""
	}
	entry, ok := all[provider]
	if !ok {
		return ""
	}
	scoped, err := json.Marshal(map[string]json.RawMessage{provider: entry})
	if err != nil {
		return ""
	}
	return string(scoped)
}

// promptCommand builds the invocation arguments based on the binary and
// whether the prompt travels via stdin: opencode uses the "run" subcommand and
// claude "-p", both reading the prompt from stdin (no length limit); any other
// binary receives the prompt as the "-p" argument (previous behavior). The
// opencode invocation carries --pure (no external plugins), matching the
// sibling review and commit-message invocations, and never --format json:
// runCommandWithTimeout parses this output as trimmed plain text, not as an
// event stream. When a model is configured, "--model <model>" is appended,
// matching the model-flag part of the review invocations: environment
// variables (OPENCODE_MODEL, CLAUDE_CODE_MODEL) alone are not enough for the
// binary to resolve the desired model. The identity probe keeps the default
// reasoning effort; effort propagation is out of scope for the probe.
func (c *CLIAdapter) promptCommand(prompt string) ([]string, bool) {
	if c.isOpenCode() {
		args := []string{"run", "--pure"}
		if c.Config.Model != "" {
			args = append(args, "--model", c.Config.Model)
		}
		return args, true
	}
	if c.isClaude() {
		args := []string{"-p"}
		if c.Config.Model != "" {
			args = append(args, "--model", c.Config.Model)
		}
		return args, true
	}
	return []string{"-p", prompt}, false
}

func (c *CLIAdapter) isClaude() bool {
	return c.baseName() == "claude"
}

func (c *CLIAdapter) isOpenCode() bool {
	return c.baseName() == "opencode"
}

// baseName extracts the binary's name without path or extension, to tolerate
// full paths or platform suffixes (e.g. "opencode.exe").
func (c *CLIAdapter) baseName() string {
	return strings.TrimSuffix(filepath.Base(c.BinaryName), filepath.Ext(c.BinaryName))
}

// buildAgentPrompt assembles the commit-message prompt. The language is set
// explicitly and with an example (T0.13): without stating it, the model chose
// at random and mixed languages within the same run.
func buildAgentPrompt(layer string, batchNum int, files []string, language string) string {
	filesJoined := strings.Join(files, " ")
	instruction, example := instructionAndExample(language)
	return fmt.Sprintf(
		"Analiza estos archivos modificados de la capa [%s] (Lote #%d): %s. Genera un mensaje de commit semántico bajo el estándar Conventional Commits. %s Ejemplo del formato esperado: %s. Devuelve ÚNICAMENTE la línea del mensaje, sin marcas de markdown ni comillas.",
		layer, batchNum, filesJoined, instruction, example,
	)
}

func buildAgentPromptWithDiff(layer string, batchNum int, files []string, diff string, language string) string {
	return fmt.Sprintf("%s\n\nMicro-diff preparado:\n%s", buildAgentPrompt(layer, batchNum, files, language), diff)
}

func buildRefactorPrompt(path string) string {
	return fmt.Sprintf(
		"Analiza el archivo %s. Propón un plan detallado para dividirlo en archivos más pequeños y cohesivos, respetando el principio de responsabilidad única (SRP). Devuelve el plan en texto plano: qué archivos crear, qué contenido debería moverse a cada uno y el orden sugerido. Sin marcas de markdown.",
		path,
	)
}

func buildApplyRefactorPrompt(path string, plan string) string {
	return fmt.Sprintf(
		"Aplica el siguiente plan de refactorización sobre el archivo %s:\n%s\nRealiza los cambios directamente en el working tree: crea, mueve y edita los archivos necesarios. NO hagas commits ni ejecutes git. Devuelve un resumen breve de los archivos creados, modificados o eliminados. Sin marcas de markdown.",
		path, plan,
	)
}
