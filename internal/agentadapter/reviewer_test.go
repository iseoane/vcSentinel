package agentadapter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
)

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
	snapshotPattern := filepath.ToSlash(filepath.Join(snapshot, "**"))
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

	if expected := []string{"run", "--pure", "--agent", "reviewer", "--model", "openai/gpt-5.6-terra", "--dir", snapshot}; !reflect.DeepEqual(args, expected) {
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
	if got := reviewer.Permission["*"]; got != "deny" {
		t.Errorf("default permission = %v, expected deny", got)
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
	if !reflect.DeepEqual(readPermissions, map[string]any{"*": "deny", snapshotPattern: "allow"}) {
		t.Errorf("read permissions = %v, expected only the absolute snapshot pattern", readPermissions)
	}
	for _, tool := range []string{"grep", "glob"} {
		permissions := reviewer.Permission[tool].(map[string]any)
		if !reflect.DeepEqual(permissions, map[string]any{"*": "allow"}) {
			t.Errorf("%s permissions = %v, expected ordinary search expressions to remain allowed", tool, permissions)
		}
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

	if expected := []string{"run", "--pure", "--agent", "reviewer", "--dir", snapshot}; !reflect.DeepEqual(args, expected) {
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
	if got := permission["*"]; got != "deny" {
		t.Errorf("default permission = %v, expected deny", got)
	}
	if got := permission["bash"].(map[string]any)["*"]; got != "deny" {
		t.Errorf("bash permission = %v, expected deny", got)
	}
	wantPattern := filepath.ToSlash(filepath.Join(snapshot, "**"))
	if got := permission["read"].(map[string]any)[wantPattern]; got != "allow" {
		t.Errorf("read permission = %v, expected allow for the snapshot pattern", got)
	}
}

func TestReviewCommandOpenCodeDoesNotUseAuditedPathsAsPermissionPatterns(t *testing.T) {
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	adapter := CLIAdapter{BinaryName: "opencode"}
	_, env, err := adapter.reviewCommand(ReviewRequest{
		Prompt: "audit",
		Paths: []string{
			"internal\\review\\engine.go",
			"/etc/passwd",
			"../outside.go",
			"nested/../../outside.go",
			"C:/outside.go",
			"D:\\outside.go",
			"unsafe\x00path",
			"unsafe\rpath",
			"unsafe\npath",
			"star*.go",
			"double**.go",
			"question?.go",
			"class[ab].go",
			"brace{a,b}.go",
			"-option.go",
		},
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
	wantPattern := filepath.ToSlash(filepath.Join(snapshot, "**"))
	if !reflect.DeepEqual(readPermissions, map[string]any{"*": "deny", wantPattern: "allow"}) {
		t.Fatalf("read permissions = %v, expected only the snapshot pattern", readPermissions)
	}
	for _, auditedPath := range []string{"internal/review/engine.go", "/etc/passwd", "../outside.go", "star*.go"} {
		if _, exists := readPermissions[auditedPath]; exists {
			t.Errorf("read permissions unexpectedly include audited path %q", auditedPath)
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
	casos := map[string]string{
		"non-matching provider": `{"anthropic":{"key":"not-the-configured-provider"}}`,
		"malformed JSON":        `{"anthropic":`,
	}
	for nombre, inherited := range casos {
		t.Run(nombre, func(t *testing.T) {
			hostDataHome := t.TempDir()
			writeHostAuthFixture(t, hostDataHome)
			t.Setenv("XDG_DATA_HOME", hostDataHome)
			t.Setenv("OPENCODE_AUTH_CONTENT", inherited)

			snapshot := t.TempDir()
			env := reviewEnvironment("generated", snapshot, "openai/gpt-5.6-terra")
			values := environmentValues(env)
			if got := values["OPENCODE_AUTH_CONTENT"]; len(got) != 0 {
				t.Fatalf("OPENCODE_AUTH_CONTENT = %v, expected no entry for %s", got, nombre)
			}
			wantDataHome := filepath.Join(snapshot, ".local", "share")
			if got := values["XDG_DATA_HOME"]; !reflect.DeepEqual(got, []string{wantDataHome}) {
				t.Fatalf("XDG_DATA_HOME = %v, expected the isolated snapshot path %q for %s, not the host's real data dir", got, wantDataHome, nombre)
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

	expected := []string{"-p", "--safe-mode", "--permission-mode", "dontAsk", "--tools", "Read,Grep,Glob", "--allowed-tools", "Read(" + snapshotPattern + "),Grep(" + snapshotPattern + "),Glob(" + snapshotPattern + ")", "--disallowed-tools", "Bash,Edit,Write", "--model", "claude-sonnet-5", "--effort", "high"}
	if !reflect.DeepEqual(args, expected) {
		t.Fatalf("args = %v, expected %v", args, expected)
	}
	if env != nil {
		t.Fatalf("env = %v, expected nil (claude has no OpenCode-style OPENCODE_CONFIG_CONTENT injection)", env)
	}
}

// TestReviewCommandClaudeExeIsDetected mirrors the opencode.exe coverage:
// nombreBase must strip the platform suffix so Windows binaries are detected.
func TestReviewCommandClaudeExeIsDetected(t *testing.T) {
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	snapshotPattern := filepath.ToSlash(filepath.Join(snapshot, "**"))
	adapter := CLIAdapter{BinaryName: "claude.exe"}
	args, _, err := adapter.reviewCommand(ReviewRequest{Prompt: "audit", SnapshotDir: snapshot})
	if err != nil {
		t.Fatalf("reviewCommand() error = %v", err)
	}
	expected := []string{"-p", "--safe-mode", "--permission-mode", "dontAsk", "--tools", "Read,Grep,Glob", "--allowed-tools", "Read(" + snapshotPattern + "),Grep(" + snapshotPattern + "),Glob(" + snapshotPattern + ")", "--disallowed-tools", "Bash,Edit,Write"}
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
	expected := []string{"-p", "--safe-mode", "--permission-mode", "dontAsk", "--tools", "Read,Grep,Glob", "--allowed-tools", "Read(" + snapshotPattern + "),Grep(" + snapshotPattern + "),Glob(" + snapshotPattern + ")", "--disallowed-tools", "Bash,Edit,Write"}
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

// TestEjecutarRevisionClaudeUsesSnapshotDirAsCwd verifies the actual os/exec
// wiring: Claude has no "--dir" flag, so the review process starts in the
// immutable snapshot directory.
func TestEjecutarRevisionClaudeUsesSnapshotDirAsCwd(t *testing.T) {
	capturaRuta := filepath.Join(t.TempDir(), "captura.json")
	t.Setenv("VAS_SENTINEL_TEST_CAPTURE", capturaRuta)
	snapshotDir := t.TempDir()
	adapter := CLIAdapter{BinaryName: compilarAgenteConNombre(t, "claude"), Timeout: 10 * time.Second}

	if _, err := adapter.ejecutarRevision(context.Background(), ReviewRequest{Prompt: "audit", SnapshotDir: snapshotDir}, 10*time.Second); err != nil {
		t.Fatalf("ejecutarRevision() error = %v", err)
	}
	captura := leerCapturaAgente(t, capturaRuta)
	if !mismaRuta(captura.Dir, snapshotDir) {
		t.Fatalf("cmd.Dir = %q, expected the snapshot directory %q", captura.Dir, snapshotDir)
	}
	if captura.Stdin != "audit" {
		t.Fatalf("stdin = %q, expected the review prompt", captura.Stdin)
	}
	snapshotPattern := filepath.ToSlash(filepath.Join(snapshotDir, "**"))
	wantArgs := []string{"-p", "--safe-mode", "--permission-mode", "dontAsk", "--tools", "Read,Grep,Glob", "--allowed-tools", "Read(" + snapshotPattern + "),Grep(" + snapshotPattern + "),Glob(" + snapshotPattern + ")", "--disallowed-tools", "Bash,Edit,Write"}
	if !reflect.DeepEqual(captura.Args, wantArgs) {
		t.Fatalf("args = %v, expected restricted snapshot-bound invocation %v", captura.Args, wantArgs)
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
