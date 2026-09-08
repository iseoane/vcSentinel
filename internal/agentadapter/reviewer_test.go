package agentadapter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewsnapshot"
)

// wantAnchoredReadPermissions builds the expected OpenCode read-permission
// map for a given snapshot directory: the whole subtree, granted in the two
// distinct resource forms the leading-slash normalization quirk produces on
// this platform (see openCodeReadPermissions).
func wantAnchoredReadPermissions(snapshot string) map[string]any {
	want := map[string]any{}
	for _, form := range anchoredReadForms(snapshot) {
		want[form] = "allow"
	}
	return want
}

func TestReviewCommandsTranslateTheSameContractToolPolicy(t *testing.T) {
	contract, err := reviewcontract.Lookup(reviewcontract.DimensionSecurity)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	request := ReviewRequest{Prompt: "audit", SnapshotDir: snapshot, ToolPolicy: contract.ToolPolicy}

	openCode := CLIAdapter{BinaryName: "opencode"}
	_, environment, err := openCode.reviewCommand(request)
	if err != nil {
		t.Fatalf("OpenCode reviewCommand() error = %v", err)
	}
	var configuration struct {
		Agent map[string]struct {
			Permission map[string]any `json:"permission"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(environment["OPENCODE_CONFIG_CONTENT"]), &configuration); err != nil {
		t.Fatal(err)
	}
	permissions := configuration.Agent["reviewer"].Permission
	if permissions["read"] == nil || permissions["grep"] == nil || permissions["glob"] == nil || permissions["bash"] == nil || permissions["edit"] == nil || permissions["write"] == nil {
		t.Fatalf("OpenCode permissions do not translate the policy: %#v", permissions)
	}

	claude := CLIAdapter{BinaryName: "claude"}
	args, _, err := claude.reviewCommand(request)
	if err != nil {
		t.Fatalf("Claude reviewCommand() error = %v", err)
	}
	joined := strings.Join(args, " ")
	for _, required := range []string{"Read,Grep,Glob", "Bash,Edit,Write", filepath.ToSlash(filepath.Join(snapshot, "**"))} {
		if !strings.Contains(joined, required) {
			t.Errorf("Claude arguments do not translate the policy requirement %q: %v", required, args)
		}
	}
}

func TestReviewCommandOpenCodeRestrictsToolsAndSteps(t *testing.T) {
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	adapter := CLIAdapter{
		BinaryName: "opencode",
		Config: config.AgentConfig{
			Model:           "openai/gpt-5.6-terra",
			ReasoningEffort: "high",
		},
	}
	args, env, err := adapter.reviewCommand(ReviewRequest{
		Prompt:       "audit",
		Paths:        []string{"internal/review/engine.go", "internal/planning/context.go"},
		SnapshotDir:  snapshot,
		MaxToolCalls: 7,
	})
	if err != nil {
		t.Fatalf("reviewCommand() error = %v", err)
	}

	if expected := []string{"run", "--pure", "--agent", "reviewer", "--model", "openai/gpt-5.6-terra", "--dir", snapshot, "--format", "json"}; !reflect.DeepEqual(args, expected) {
		t.Fatalf("args = %v, expected %v", args, expected)
	}

	configText := env["OPENCODE_CONFIG_CONTENT"]
	var config struct {
		Agent map[string]struct {
			Model           string         `json:"model"`
			ReasoningEffort string         `json:"reasoningEffort"`
			Steps           int            `json:"steps"`
			Permission      map[string]any `json:"permission"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(configText), &config); err != nil {
		t.Fatalf("review configuration is invalid JSON: %v", err)
	}
	reviewer := config.Agent["reviewer"]
	if reviewer.Model != "openai/gpt-5.6-terra" {
		t.Errorf("model = %q, expected configured model", reviewer.Model)
	}
	if reviewer.ReasoningEffort != "high" {
		t.Errorf("reasoningEffort = %q, expected configured effort", reviewer.ReasoningEffort)
	}
	if reviewer.Steps != 7 {
		t.Errorf("steps = %d, expected 7", reviewer.Steps)
	}
	if got := reviewer.Permission["*"]; got != "ask" {
		t.Errorf("default permission = %v, expected ask (non-interactive auto-reject)", got)
	}
	for _, tool := range []string{"bash", "edit", "write"} {
		if got := reviewer.Permission[tool].(map[string]any)["*"]; got != "deny" {
			t.Errorf("%s permission = %v, expected deny", tool, got)
		}
	}
	if got := reviewer.Permission["webfetch"]; got != nil {
		t.Errorf("webfetch permission = %v, expected absent", got)
	}
	readPermissions := reviewer.Permission["read"].(map[string]any)
	if _, hasWildcard := readPermissions["*"]; hasWildcard {
		t.Errorf("read permissions = %v, expected no bare wildcard (it shadows exact allows and is unanchored to the snapshot)", readPermissions)
	}
	// Item 1 now materializes the whole committed tree as read-only context,
	// not just the audited paths, so the read grant must widen to the whole
	// snapshot subtree instead of one entry per audited path — mirroring the
	// Claude branch's Read(<snapshot>/**). Every key must still be anchored
	// to the snapshot directory: a bare "**" would match resources outside
	// it and was rejected for that reason.
	wantReadPermissions := wantAnchoredReadPermissions(snapshot)
	if !reflect.DeepEqual(readPermissions, wantReadPermissions) {
		t.Errorf("read permissions = %v, expected the whole snapshot subtree anchored %v", readPermissions, wantReadPermissions)
	}
	for _, tool := range []string{"grep", "glob"} {
		permissions := reviewer.Permission[tool].(map[string]any)
		if !reflect.DeepEqual(permissions, map[string]any{"*": "allow"}) {
			t.Errorf("%s permissions = %v, expected ordinary search expressions to remain allowed", tool, permissions)
		}
	}
}

// TestReviewCommandOpenCodeReadGrantIsIndependentOfAuditedPaths pins the
// item-1/item-2 relationship: since the snapshot now carries the whole
// committed tree as read-only context (not just the audited paths), the read
// grant covers the whole snapshot subtree regardless of which paths are
// under audit, and never carries a bare wildcard that would shadow exact
// containment or escape the snapshot boundary.
func TestReviewCommandOpenCodeReadGrantIsIndependentOfAuditedPaths(t *testing.T) {
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	adapter := CLIAdapter{BinaryName: "opencode"}
	_, env, err := adapter.reviewCommand(ReviewRequest{
		Prompt:      "audit",
		Paths:       []string{"&review.go"},
		SnapshotDir: snapshot,
	})
	if err != nil {
		t.Fatalf("reviewCommand() error = %v", err)
	}

	var configuration struct {
		Agent map[string]struct {
			Permission map[string]json.RawMessage `json:"permission"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &configuration); err != nil {
		t.Fatalf("review configuration is invalid JSON: %v", err)
	}
	permission := configuration.Agent["reviewer"].Permission
	if string(permission["*"]) != `"ask"` {
		t.Fatalf("agent-level fallback = %s, expected \"ask\"", permission["*"])
	}
	var readRules map[string]any
	if err := json.Unmarshal(permission["read"], &readRules); err != nil {
		t.Fatalf("read rules are invalid JSON: %v", err)
	}
	if _, hasWildcard := readRules["*"]; hasWildcard {
		t.Fatalf("read rules = %v, expected no bare wildcard entry", readRules)
	}
	if !reflect.DeepEqual(readRules, wantAnchoredReadPermissions(snapshot)) {
		t.Fatalf("read rules = %v, expected the snapshot-anchored subtree grant, unaffected by Paths", readRules)
	}
}

func TestReviewCommandOpenCodeOmitsEmptyModelConfiguration(t *testing.T) {
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	adapter := CLIAdapter{BinaryName: "opencode"}
	args, env, err := adapter.reviewCommand(ReviewRequest{
		Prompt:       "audit",
		Paths:        []string{"internal/review/engine.go"},
		SnapshotDir:  snapshot,
		MaxToolCalls: 7,
	})
	if err != nil {
		t.Fatalf("reviewCommand() error = %v", err)
	}

	if expected := []string{"run", "--pure", "--agent", "reviewer", "--dir", snapshot, "--format", "json"}; !reflect.DeepEqual(args, expected) {
		t.Fatalf("args = %v, expected %v", args, expected)
	}

	var generated map[string]map[string]map[string]any
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &generated); err != nil {
		t.Fatalf("review configuration is invalid JSON: %v", err)
	}
	reviewer := generated["agent"]["reviewer"]
	for _, field := range []string{"model", "reasoningEffort"} {
		if _, present := reviewer[field]; present {
			t.Errorf("reviewer configuration contains optional %q field", field)
		}
	}
	permission := reviewer["permission"].(map[string]any)
	if got := permission["*"]; got != "ask" {
		t.Errorf("default permission = %v, expected ask (non-interactive auto-reject)", got)
	}
	if got := permission["bash"].(map[string]any)["*"]; got != "deny" {
		t.Errorf("bash permission = %v, expected deny", got)
	}
	readPermissions := permission["read"].(map[string]any)
	if !reflect.DeepEqual(readPermissions, wantAnchoredReadPermissions(snapshot)) {
		t.Errorf("read permissions = %v, expected the snapshot-anchored subtree grant %v", readPermissions, wantAnchoredReadPermissions(snapshot))
	}
}

// TestReviewCommandOpenCodeReadGrantStaysAnchoredToTheSnapshot asserts every
// emitted read-permission key is anchored to the snapshot directory itself
// (its absolute path, in both forms the leading-slash normalization quirk
// produces), never a bare "**" or any pattern whose literal prefix escapes
// the snapshot — the containment property item 2 must preserve while
// widening the grant from per-audited-path to the whole subtree.
func TestReviewCommandOpenCodeReadGrantStaysAnchoredToTheSnapshot(t *testing.T) {
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	adapter := CLIAdapter{BinaryName: "opencode"}
	_, env, err := adapter.reviewCommand(ReviewRequest{
		Prompt:      "audit",
		Paths:       []string{"internal/agentadapter/cli.go"},
		SnapshotDir: snapshot,
	})
	if err != nil {
		t.Fatalf("reviewCommand() error = %v", err)
	}

	var config struct {
		Agent map[string]struct {
			Permission map[string]any `json:"permission"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatalf("review configuration is invalid JSON: %v", err)
	}
	readPermissions := config.Agent["reviewer"].Permission["read"].(map[string]any)
	if got := config.Agent["reviewer"].Permission["*"]; got != "ask" {
		t.Fatalf("agent-level fallback = %v, expected ask (non-interactive auto-reject)", got)
	}
	if len(readPermissions) == 0 {
		t.Fatal("read permissions are empty, expected the whole snapshot subtree granted")
	}
	snapshotSlash := filepath.ToSlash(snapshot)
	strippedSnapshot := strings.TrimPrefix(snapshotSlash, "/")
	for key, value := range readPermissions {
		if value != "allow" {
			t.Errorf("read permission %q = %v, expected allow", key, value)
		}
		if key == "**" || key == "*" {
			t.Fatalf("read permission key %q is unanchored: it would match resources outside the snapshot", key)
		}
		anchored := strings.HasPrefix(key, snapshotSlash+"/") || strings.HasPrefix(key, strippedSnapshot+"/")
		if !anchored || !strings.HasSuffix(key, "/**") {
			t.Errorf("read permission key %q is not anchored to the snapshot directory %q", key, snapshot)
		}
	}
	for _, tool := range []string{"grep", "glob"} {
		if got := config.Agent["reviewer"].Permission[tool].(map[string]any); !reflect.DeepEqual(got, map[string]any{"*": "allow"}) {
			t.Errorf("%s permissions = %v, expected ordinary expression access", tool, got)
		}
	}
}

func TestReviewEnvironmentOverridesInheritedConfiguration(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"permission":{"bash":"allow"}}`)
	t.Setenv("OPENCODE_CONFIG", "/host/config.json")
	t.Setenv("OPENCODE_CONFIG_DIR", "/host/config")
	t.Setenv("OPENCODE_AUTH_CONTENT", `{"openai":{"token":"existing"}}`)
	t.Setenv("HOME", "/host/home")
	t.Setenv("XDG_DATA_HOME", "/host/data")

	snapshot := t.TempDir()
	env := reviewEnvironment("generated", snapshot, "openai/gpt-5.6-terra")
	values := environmentValues(env)
	for _, key := range []string{"OPENCODE_CONFIG_CONTENT", "HOME"} {
		if len(values[key]) != 1 {
			t.Fatalf("%s entries = %v, expected exactly one", key, values[key])
		}
	}
	if values["OPENCODE_CONFIG_CONTENT"][0] != "generated" {
		t.Fatalf("OPENCODE_CONFIG_CONTENT = %q, expected generated config", values["OPENCODE_CONFIG_CONTENT"][0])
	}
	if len(values["OPENCODE_CONFIG"]) != 0 || len(values["OPENCODE_CONFIG_DIR"]) != 0 {
		t.Fatalf("host config was retained: %v", values)
	}
	// XDG_DATA_HOME must NOT keep pointing at the host's real data dir: that
	// would give OpenCode an unscoped fallback to the full auth.json whenever
	// scopeCredentialToProvider yields nothing.
	wantDataHome := filepath.Join(snapshot, ".local", "share")
	if dataHome := values["XDG_DATA_HOME"]; !reflect.DeepEqual(dataHome, []string{wantDataHome}) {
		t.Fatalf("XDG_DATA_HOME = %v, expected the isolated snapshot path %q", dataHome, wantDataHome)
	}
	if auth := values["OPENCODE_AUTH_CONTENT"]; !reflect.DeepEqual(auth, []string{`{"openai":{"token":"existing"}}`}) {
		t.Fatalf("OPENCODE_AUTH_CONTENT = %v, expected existing authentication source", auth)
	}
	if values["OPENCODE_DISABLE_PROJECT_CONFIG"][0] != "1" || values["OPENCODE_PURE"][0] != "1" {
		t.Fatalf("review isolation is incomplete: %v", values)
	}
}

// writeHostAuthFixture writes a multi-provider auth.json (mirroring OpenCode's
// real layout, which stores one entry per configured provider) so tests can
// verify only the requested provider's entry crosses into the sandbox.
func writeHostAuthFixture(t *testing.T, dataHome string) {
	t.Helper()
	authDir := filepath.Join(dataHome, "opencode")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("create host auth directory: %v", err)
	}
	fixture := `{"openai":{"type":"oauth","key":"openai-secret"},"groq":{"type":"api","key":"unrelated-secret"}}`
	if err := os.WriteFile(filepath.Join(authDir, "auth.json"), []byte(fixture), 0o600); err != nil {
		t.Fatalf("write host auth.json: %v", err)
	}
}

func TestReviewEnvironmentInjectsHostAuthContentWhenMissing(t *testing.T) {
	t.Setenv("OPENCODE_AUTH_CONTENT", "")
	dataHome := t.TempDir()
	writeHostAuthFixture(t, dataHome)
	t.Setenv("XDG_DATA_HOME", dataHome)

	env := reviewEnvironment("generated", t.TempDir(), "openai/gpt-5.6-terra")
	values := environmentValues(env)
	want := `{"openai":{"type":"oauth","key":"openai-secret"}}`
	if got := values["OPENCODE_AUTH_CONTENT"]; !reflect.DeepEqual(got, []string{want}) {
		t.Fatalf("OPENCODE_AUTH_CONTENT = %v, expected only the configured provider's entry %q", got, want)
	}
}

func TestReviewEnvironmentOmitsAuthContentWhenHostFileIsAbsent(t *testing.T) {
	t.Setenv("OPENCODE_AUTH_CONTENT", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	env := reviewEnvironment("generated", t.TempDir(), "openai/gpt-5.6-terra")
	values := environmentValues(env)
	if got := values["OPENCODE_AUTH_CONTENT"]; len(got) != 0 {
		t.Fatalf("OPENCODE_AUTH_CONTENT = %v, expected no entry when the host has no auth.json", got)
	}
}

func TestReviewEnvironmentOmitsAuthContentForUnconfiguredProvider(t *testing.T) {
	t.Setenv("OPENCODE_AUTH_CONTENT", "")
	dataHome := t.TempDir()
	writeHostAuthFixture(t, dataHome)
	t.Setenv("XDG_DATA_HOME", dataHome)

	env := reviewEnvironment("generated", t.TempDir(), "anthropic/claude")
	values := environmentValues(env)
	if got := values["OPENCODE_AUTH_CONTENT"]; len(got) != 0 {
		t.Fatalf("OPENCODE_AUTH_CONTENT = %v, expected no entry for a provider absent from the host auth store", got)
	}
}

func TestReviewEnvironmentPrefersInheritedAuthContentOverHostFile(t *testing.T) {
	dataHome := t.TempDir()
	writeHostAuthFixture(t, dataHome)
	t.Setenv("XDG_DATA_HOME", dataHome)
	// Multi-provider on purpose: an inherited credential store must be scoped
	// to the configured provider exactly like the host's auth.json is, so it
	// takes precedence over the file but still can't leak unrelated secrets.
	t.Setenv("OPENCODE_AUTH_CONTENT", `{"openai":{"type":"api","key":"caller-provided"},"groq":{"type":"api","key":"caller-unrelated"}}`)

	env := reviewEnvironment("generated", t.TempDir(), "openai/gpt-5.6-terra")
	values := environmentValues(env)
	want := `{"openai":{"type":"api","key":"caller-provided"}}`
	if got := values["OPENCODE_AUTH_CONTENT"]; !reflect.DeepEqual(got, []string{want}) {
		t.Fatalf("OPENCODE_AUTH_CONTENT = %v, expected the inherited value scoped to the configured provider %q", got, want)
	}
}

// TestReviewEnvironmentDoesNotFallBackToHostDataDirWhenScopingFails is a
// regression test: whenever scopeCredentialToProvider yields "" (malformed
// inherited JSON, or valid JSON lacking the configured provider), no
// OPENCODE_AUTH_CONTENT is emitted. If XDG_DATA_HOME still pointed at the
// host's real data dir at that point, OpenCode itself could fall back to
// reading the unscoped host auth.json directly — bypassing this file's
// scoping entirely. XDG_DATA_HOME must always end up isolated to the
// snapshot in both cases; this only asserts the environment produced, not
// that a real OpenCode process is unable to reach the fixture file.
func TestReviewEnvironmentDoesNotFallBackToHostDataDirWhenScopingFails(t *testing.T) {
	cases := map[string]string{
		"non-matching provider": `{"anthropic":{"key":"not-the-configured-provider"}}`,
		"malformed JSON":        `{"anthropic":`,
	}
	for name, inherited := range cases {
		t.Run(name, func(t *testing.T) {
			hostDataHome := t.TempDir()
			writeHostAuthFixture(t, hostDataHome)
			t.Setenv("XDG_DATA_HOME", hostDataHome)
			t.Setenv("OPENCODE_AUTH_CONTENT", inherited)

			snapshot := t.TempDir()
			env := reviewEnvironment("generated", snapshot, "openai/gpt-5.6-terra")
			values := environmentValues(env)
			if got := values["OPENCODE_AUTH_CONTENT"]; len(got) != 0 {
				t.Fatalf("OPENCODE_AUTH_CONTENT = %v, expected no entry for %s", got, name)
			}
			wantDataHome := filepath.Join(snapshot, ".local", "share")
			if got := values["XDG_DATA_HOME"]; !reflect.DeepEqual(got, []string{wantDataHome}) {
				t.Fatalf("XDG_DATA_HOME = %v, expected the isolated snapshot path %q for %s, not the host's real data dir", got, wantDataHome, name)
			}
		})
	}
}

func TestReviewEnvironmentSkipsRelativeAuthPathWhenNoHomeIsResolvable(t *testing.T) {
	t.Setenv("OPENCODE_AUTH_CONTENT", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	env := reviewEnvironment("generated", t.TempDir(), "openai/gpt-5.6-terra")
	values := environmentValues(env)
	if got := values["OPENCODE_AUTH_CONTENT"]; len(got) != 0 {
		t.Fatalf("OPENCODE_AUTH_CONTENT = %v, expected no entry when no host home directory can be resolved", got)
	}
}

func environmentValues(env []string) map[string][]string {
	values := make(map[string][]string)
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = append(values[key], value)
		}
	}
	return values
}

func TestReviewCommandRequiresSnapshotDirectory(t *testing.T) {
	adapter := CLIAdapter{BinaryName: "opencode"}
	_, _, err := adapter.reviewCommand(ReviewRequest{Paths: []string{"safe.go"}})
	if err == nil || !strings.Contains(err.Error(), "snapshot") {
		t.Fatalf("reviewCommand() error = %v, expected missing snapshot error", err)
	}
}

// TestReviewCommandClaudeBuildsSnapshotBoundArgs verifies the explicit
// read/search tool set, snapshot-bound allow patterns, and non-interactive
// rejection mode. The process working directory is asserted separately.
func TestReviewCommandClaudeBuildsSnapshotBoundArgs(t *testing.T) {
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	snapshotPattern := filepath.ToSlash(filepath.Join(snapshot, "**"))
	adapter := CLIAdapter{
		BinaryName: "claude",
		Config: config.AgentConfig{
			Model:           "claude-sonnet-5",
			ReasoningEffort: "high",
		},
	}
	args, env, err := adapter.reviewCommand(ReviewRequest{
		Prompt:      "audit",
		Paths:       []string{"internal/review/engine.go"},
		SnapshotDir: snapshot,
	})
	if err != nil {
		t.Fatalf("reviewCommand() error = %v", err)
	}

	expected := []string{"-p", "--safe-mode", "--permission-mode", "dontAsk", "--tools", "Read,Grep,Glob", "--allowed-tools", "Read(" + snapshotPattern + "),Grep(" + snapshotPattern + "),Glob(" + snapshotPattern + ")", "--disallowed-tools", "Bash,Edit,Write", "--model", "claude-sonnet-5", "--effort", "high", "--output-format", "json"}
	if !reflect.DeepEqual(args, expected) {
		t.Fatalf("args = %v, expected %v", args, expected)
	}
	if env != nil {
		t.Fatalf("env = %v, expected nil (claude has no OpenCode-style OPENCODE_CONFIG_CONTENT injection)", env)
	}
}

// TestReviewCommandClaudeExeIsDetected mirrors the opencode.exe coverage:
// baseName must strip the platform suffix so Windows binaries are detected.
func TestReviewCommandClaudeExeIsDetected(t *testing.T) {
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	snapshotPattern := filepath.ToSlash(filepath.Join(snapshot, "**"))
	adapter := CLIAdapter{BinaryName: "claude.exe"}
	args, _, err := adapter.reviewCommand(ReviewRequest{Prompt: "audit", SnapshotDir: snapshot})
	if err != nil {
		t.Fatalf("reviewCommand() error = %v", err)
	}
	expected := []string{"-p", "--safe-mode", "--permission-mode", "dontAsk", "--tools", "Read,Grep,Glob", "--allowed-tools", "Read(" + snapshotPattern + "),Grep(" + snapshotPattern + "),Glob(" + snapshotPattern + ")", "--disallowed-tools", "Bash,Edit,Write", "--output-format", "json"}
	if !reflect.DeepEqual(args, expected) {
		t.Fatalf("args = %v, expected %v", args, expected)
	}
}

func TestReviewCommandClaudeOmitsEmptyModelConfiguration(t *testing.T) {
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	snapshotPattern := filepath.ToSlash(filepath.Join(snapshot, "**"))
	adapter := CLIAdapter{BinaryName: "claude"}
	args, _, err := adapter.reviewCommand(ReviewRequest{Prompt: "audit", SnapshotDir: snapshot})
	if err != nil {
		t.Fatalf("reviewCommand() error = %v", err)
	}
	expected := []string{"-p", "--safe-mode", "--permission-mode", "dontAsk", "--tools", "Read,Grep,Glob", "--allowed-tools", "Read(" + snapshotPattern + "),Grep(" + snapshotPattern + "),Glob(" + snapshotPattern + ")", "--disallowed-tools", "Bash,Edit,Write", "--output-format", "json"}
	if !reflect.DeepEqual(args, expected) {
		t.Fatalf("args = %v, expected %v", args, expected)
	}
}

func TestReviewCommandClaudeRequiresSnapshotDirectory(t *testing.T) {
	adapter := CLIAdapter{BinaryName: "claude"}
	_, _, err := adapter.reviewCommand(ReviewRequest{Prompt: "audit", MaxToolCalls: 7})
	if err == nil || !strings.Contains(err.Error(), "snapshot") {
		t.Fatalf("reviewCommand() error = %v, expected missing snapshot error", err)
	}
}

// TestRunReviewClaudeUsesSnapshotDirAsCwd verifies the actual os/exec
// wiring: Claude has no "--dir" flag, so the review process starts in the
// immutable snapshot directory.
func TestRunReviewClaudeUsesSnapshotDirAsCwd(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("VAS_SENTINEL_TEST_CAPTURE", capturePath)
	// The fake claude must answer in the format the production invocation now
	// requests: one result object the rich path scans. The echoed prompt
	// would fail the parser's fail-closed JSON check.
	t.Setenv("VAS_SENTINEL_TEST_OUTPUT", `{"type":"result","subtype":"success","stop_reason":"end_turn","result":"audit"}`)
	snapshotDir := t.TempDir()
	adapter := CLIAdapter{BinaryName: compileAgentBinary(t, "claude"), Timeout: 10 * time.Second}

	if _, err := adapter.runBoundedReview(context.Background(), ReviewRequest{Prompt: "audit", SnapshotDir: snapshotDir}, 10*time.Second); err != nil {
		t.Fatalf("runBoundedReview() error = %v", err)
	}
	capture := readAgentCapture(t, capturePath)
	if !samePath(capture.Dir, snapshotDir) {
		t.Fatalf("cmd.Dir = %q, expected the snapshot directory %q", capture.Dir, snapshotDir)
	}
	if capture.Stdin != "audit" {
		t.Fatalf("stdin = %q, expected the review prompt", capture.Stdin)
	}
	snapshotPattern := filepath.ToSlash(filepath.Join(snapshotDir, "**"))
	wantArgs := []string{"-p", "--safe-mode", "--permission-mode", "dontAsk", "--tools", "Read,Grep,Glob", "--allowed-tools", "Read(" + snapshotPattern + "),Grep(" + snapshotPattern + "),Glob(" + snapshotPattern + ")", "--disallowed-tools", "Bash,Edit,Write", "--output-format", "json"}
	if !reflect.DeepEqual(capture.Args, wantArgs) {
		t.Fatalf("args = %v, expected restricted snapshot-bound invocation %v", capture.Args, wantArgs)
	}
}

func TestRunReviewClaudeKeepsHostEnvironmentWithoutOpenCodeCredentials(t *testing.T) {
	hostHome := t.TempDir()
	t.Setenv("HOME", hostHome)
	t.Setenv("OPENCODE_AUTH_CONTENT", `{"openai":{"token":"host-secret"}}`)
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("VAS_SENTINEL_TEST_CAPTURE", capturePath)
	t.Setenv("VAS_SENTINEL_TEST_OUTPUT", `{"type":"result","subtype":"success","stop_reason":"end_turn","result":"audit"}`)

	adapter := CLIAdapter{
		BinaryName: compileAgentBinary(t, "claude"),
		Config:     config.AgentConfig{Model: "openai/gpt-5.6-terra"},
		Timeout:    10 * time.Second,
	}
	if _, err := adapter.runBoundedReview(context.Background(), ReviewRequest{Prompt: "audit", SnapshotDir: t.TempDir()}, 10*time.Second); err != nil {
		t.Fatalf("runBoundedReview() error = %v", err)
	}

	capture := readAgentCapture(t, capturePath)
	if !samePath(capture.Home, hostHome) {
		t.Fatalf("HOME = %q, expected host HOME %q", capture.Home, hostHome)
	}
	if capture.OpenCodeAuth != "" {
		t.Fatalf("OPENCODE_AUTH_CONTENT = %q, expected no OpenCode credentials for Claude", capture.OpenCodeAuth)
	}
}

func TestRestrictedReviewGenericEnvironmentIsUnchanged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENCODE_AUTH_CONTENT", `{"openai":{"token":"host-secret"}}`)
	adapter := CLIAdapter{BinaryName: "generic-agent"}

	env, cleanup, err := adapter.newRestrictedReviewEnvironment("generated", t.TempDir())
	if err != nil {
		t.Fatalf("newRestrictedReviewEnvironment() error = %v", err)
	}
	defer cleanup()
	if got, want := environmentValues(env), environmentValues(os.Environ()); !reflect.DeepEqual(got, want) {
		t.Fatalf("generic review environment = %v, expected unchanged host environment %v", got, want)
	}
}

func TestRestrictedReviewKeepsProviderStateOutOfSharedSnapshot(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("VAS_SENTINEL_TEST_CAPTURE", capturePath)
	t.Setenv("VAS_SENTINEL_TEST_WRITE_PROVIDER_STATE", "1")
	t.Setenv("VAS_SENTINEL_TEST_OUTPUT", "{\"type\":\"step_finish\",\"part\":{\"type\":\"step-finish\",\"reason\":\"stop\"}}\n")

	sha := headSha(t)
	adapter := CLIAdapter{BinaryName: compileAgentBinary(t, "opencode"), Timeout: 10 * time.Second}
	if _, err := adapter.ReviewWithContext(context.Background(), "audit", sha, []string{reviewFixturePath}); err != nil {
		t.Fatalf("ReviewWithContext() error = %v", err)
	}

	capture := readAgentCapture(t, capturePath)
	snapshot := ""
	for i, arg := range capture.Args {
		if arg == "--dir" && i+1 < len(capture.Args) {
			snapshot = capture.Args[i+1]
			break
		}
	}
	if snapshot == "" {
		t.Fatalf("review invocation did not include a snapshot directory: %v", capture.Args)
	}
	if samePath(capture.Home, snapshot) {
		t.Fatalf("provider home = %q, want an invocation root distinct from snapshot %q", capture.Home, snapshot)
	}
	if capture.ProviderState == "" || !samePath(filepath.Dir(capture.ProviderState), capture.Home) {
		t.Fatalf("provider state = %q, want a file written under provider home %q", capture.ProviderState, capture.Home)
	}
	if _, err := os.Stat(filepath.Join(snapshot, ".provider-state")); !os.IsNotExist(err) {
		t.Fatalf("provider state contaminated the published snapshot: %v", err)
	}
	if _, err := os.Stat(capture.Home); !os.IsNotExist(err) {
		t.Fatalf("provider invocation root survived review cleanup: %v", err)
	}

	// Reuse is proven by directory IDENTITY, not by an unchanged mtime. A
	// rematerialization builds a fresh tree and renames it into place, so the
	// directory object changes and os.SameFile goes false; leasing the existing
	// tree keeps the same object. The mtime cannot serve as the proxy any more
	// because leasing deliberately refreshes it (internal/reviewsnapshot
	// store.go touches the published tree on every lease) so that
	// least-recently-leased eviction has an ordering to work with. Asserting
	// the path alone would not do either: a rematerialized tree is published
	// back to the very same path.
	before, err := os.Stat(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	again, _, release, err := reviewsnapshot.Create(context.Background(), "", sha, []string{reviewFixturePath})
	if err != nil {
		t.Fatalf("Create() after provider state write: %v", err)
	}
	defer release()
	if again != snapshot {
		t.Fatalf("Create() reused %q, want the original published snapshot %q", again, snapshot)
	}
	after, err := os.Stat(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("published snapshot was rematerialized after provider state write, want the very same directory leased again")
	}
}

func TestNewReviewEnvironmentAllocatesDistinctConcurrentRootsAndCleansThem(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	type invocation struct {
		env     []string
		cleanup func()
		err     error
	}
	invocations := make([]invocation, 2)
	var wg sync.WaitGroup
	for i := range invocations {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			invocations[i].env, invocations[i].cleanup, invocations[i].err = newReviewEnvironment("generated", "openai/gpt-5.6-terra", "")
		}(i)
	}
	wg.Wait()

	roots := make([]string, len(invocations))
	for i, invocation := range invocations {
		if invocation.err != nil {
			t.Fatalf("newReviewEnvironment() error = %v", invocation.err)
		}
		roots[i] = environmentValues(invocation.env)["HOME"][0]
		if _, err := os.Stat(roots[i]); err != nil {
			t.Fatalf("provider root %q is not writable: %v", roots[i], err)
		}
	}
	if samePath(roots[0], roots[1]) {
		t.Fatalf("concurrent provider invocations shared root %q", roots[0])
	}
	for i, invocation := range invocations {
		invocation.cleanup()
		if _, err := os.Stat(roots[i]); !os.IsNotExist(err) {
			t.Fatalf("provider root %q survived cleanup: %v", roots[i], err)
		}
	}
}

func TestNewReviewEnvironmentStaysOutsideSnapshotWhenTMPDIRIsNested(t *testing.T) {
	snapshot := t.TempDir()
	nestedTmp := filepath.Join(snapshot, "tmp")
	if err := os.Mkdir(nestedTmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", nestedTmp)

	env, cleanup, err := newReviewEnvironment("generated", "openai/gpt-5.6-terra", snapshot)
	if err != nil {
		t.Fatalf("newReviewEnvironment() error = %v", err)
	}
	defer cleanup()
	root := environmentValues(env)["HOME"][0]
	relative, err := filepath.Rel(snapshot, root)
	if err != nil {
		t.Fatalf("filepath.Rel(%q, %q): %v", snapshot, root, err)
	}
	if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("provider isolation root %q is nested in snapshot %q", root, snapshot)
	}
}

func TestNewReviewEnvironmentStaysOutsideSymlinkedSnapshot(t *testing.T) {
	workspace := t.TempDir()
	snapshot := filepath.Join(workspace, "snapshot")
	nested := filepath.Join(snapshot, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(workspace, "alias")
	if err := os.Symlink(nested, alias); err != nil {
		t.Skipf("create snapshot alias: %v", err)
	}
	if err := os.Symlink("..", filepath.Join(nested, "snapshot")); err != nil {
		t.Skipf("create snapshot return link: %v", err)
	}
	linkedSnapshot := filepath.Join(alias, "snapshot")
	resolvedSnapshot, err := filepath.EvalSymlinks(linkedSnapshot)
	if err != nil {
		t.Fatal(err)
	}

	env, cleanup, err := newReviewEnvironment("generated", "openai/gpt-5.6-terra", linkedSnapshot)
	if err != nil {
		t.Fatalf("newReviewEnvironment() error = %v", err)
	}
	defer cleanup()
	root := environmentValues(env)["HOME"][0]
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(resolvedSnapshot, resolvedRoot)
	if err != nil {
		t.Fatalf("filepath.Rel(%q, %q): %v", resolvedSnapshot, resolvedRoot, err)
	}
	if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("provider isolation root %q resolves inside snapshot %q", resolvedRoot, resolvedSnapshot)
	}
}

func TestReviewCommandRejectsProvidersWithoutBoundedToolPermissions(t *testing.T) {
	adapter := CLIAdapter{BinaryName: "other-agent"}
	args, env, err := adapter.reviewCommand(ReviewRequest{Prompt: "audit", MaxToolCalls: 7})

	if err == nil {
		t.Fatal("reviewCommand() error = nil, expected unavailable bounded-tool error")
	}
	if args != nil || env != nil {
		t.Fatalf("reviewCommand() returned command=%v env=%v after unavailable error", args, env)
	}
	if !strings.Contains(err.Error(), "semantic review is unavailable") {
		t.Fatalf("reviewCommand() error = %q, expected provider-agnostic unavailable error", err)
	}
}

// TestRunReviewMarksDeadlineExceeded pins that a reviewer exhausting its
// deadline is distinguishable from one that crashes. The containment watchdog
// sends SIGTERM to the process group, so cmd.Wait returns "signal: terminated"
// — text that says nothing about the deadline. Without this mark, reviewexec
// classifies the timeout as OutcomeFailure and the durable record lies about
// why the dimension failed.
func TestRunReviewMarksDeadlineExceeded(t *testing.T) {
	t.Setenv("VAS_SENTINEL_TEST_SLEEP", "30")
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("VAS_SENTINEL_TEST_CAPTURE", capturePath)
	t.Setenv("VAS_SENTINEL_TEST_WRITE_PROVIDER_STATE", "1")
	snapshotDir := t.TempDir()
	adapter := CLIAdapter{BinaryName: compileAgentBinary(t, "opencode"), Timeout: 200 * time.Millisecond}

	_, err := adapter.runBoundedReview(context.Background(), ReviewRequest{Prompt: "audit", SnapshotDir: snapshotDir}, 200*time.Millisecond)
	if err == nil {
		t.Fatal("runBoundedReview() = nil error, expected the deadline to fire")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v; it must wrap context.DeadlineExceeded so the durable record classifies it as a timeout instead of a failure", err)
	}
	capture := readAgentCapture(t, capturePath)
	if capture.ProviderState == "" {
		t.Fatal("the timed-out provider did not write its configured state file")
	}
	if _, err := os.Stat(capture.Home); !os.IsNotExist(err) {
		t.Fatalf("provider invocation root survived timeout cleanup: %v", err)
	}
}

// TestNewReviewEnvironmentFailsClosedOnUnresolvableSnapshot pins the first of
// the containment fail-closed paths. A snapshot that does not resolve makes the
// provider root's containment impossible to prove, so newReviewEnvironment must
// report an error instead of falling back to a lexical parent. Every other test
// of this constructor asserts err == nil, so without this one a regression that
// reintroduced the filepath.Abs fallback would still pass.
func TestNewReviewEnvironmentFailsClosedOnUnresolvableSnapshot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-materialized")

	env, cleanup, err := newReviewEnvironment("", "model", missing)

	if err == nil {
		if cleanup != nil {
			cleanup()
		}
		t.Fatal("newReviewEnvironment() error = nil, want a failure for a snapshot that does not resolve")
	}
	if env != nil || cleanup != nil {
		t.Fatalf("failed construction returned env=%v cleanup!=nil=%t, want both nil so no caller can defer a cleanup that owns nothing", env, cleanup != nil)
	}
	if !strings.Contains(err.Error(), "resolve review snapshot path") {
		t.Errorf("error = %q, want it to name the snapshot resolution step", err)
	}
}

// TestNewReviewEnvironmentFailsClosedWhenTheRootWouldLandInsideTheSnapshot
// pins the containment check itself rather than the placement that feeds it.
// The placement is derived from the snapshot's own resolved parent, so this
// drives the guard directly through TMPDIR: it points at the snapshot, which is
// where MkdirTemp would put the root if the parent derivation were ever wrong
// again. Whichever way the guard is reached, a root inside the evidence tree
// must be removed and the call must fail rather than hand back a home that
// contaminates the audited content.
func TestNewReviewEnvironmentFailsClosedWhenTheRootWouldLandInsideTheSnapshot(t *testing.T) {
	root := t.TempDir()
	snapshot := filepath.Join(root, "sha-inside")
	if err := os.MkdirAll(snapshot, 0o700); err != nil {
		t.Fatal(err)
	}

	env, cleanup, err := newReviewEnvironment("", "model", snapshot)
	if err != nil {
		t.Fatalf("newReviewEnvironment() error = %v, want the sibling placement to succeed", err)
	}
	t.Cleanup(cleanup)

	homes := environmentValues(env)["HOME"]
	if len(homes) != 1 || homes[0] == "" {
		t.Fatalf("HOME = %v, want exactly one non-empty provider isolation root", homes)
	}
	home := homes[0]
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	resolvedSnapshot, err := filepath.EvalSymlinks(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(resolvedSnapshot, resolvedHome)
	if err != nil {
		t.Fatal(err)
	}
	if relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		t.Fatalf("provider isolation root %q resolves inside snapshot %q", resolvedHome, resolvedSnapshot)
	}
}
