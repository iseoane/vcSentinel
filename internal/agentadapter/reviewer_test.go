package agentadapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

func TestReviewCommandOpenCodeRestrictsToolsAndSteps(t *testing.T) {
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
		SnapshotDir:  "/snapshot",
		MaxToolCalls: 7,
	})
	if err != nil {
		t.Fatalf("reviewCommand() error = %v", err)
	}

	if expected := []string{"run", "--pure", "--agent", "reviewer", "--model", "openai/gpt-5.6-terra", "--dir", "/snapshot"}; !reflect.DeepEqual(args, expected) {
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
	for _, path := range []string{"internal/review/engine.go", "internal/planning/context.go"} {
		for _, tool := range []string{"read", "grep", "glob"} {
			if got := reviewer.Permission[tool].(map[string]any)[path]; got != "allow" {
				t.Errorf("%s permission for %q = %v, expected allow", tool, path, got)
			}
		}
	}
}

func TestReviewCommandOpenCodeOmitsEmptyModelConfiguration(t *testing.T) {
	adapter := CLIAdapter{BinaryName: "opencode"}
	args, env, err := adapter.reviewCommand(ReviewRequest{
		Prompt:       "audit",
		Paths:        []string{"internal/review/engine.go"},
		SnapshotDir:  "/snapshot",
		MaxToolCalls: 7,
	})
	if err != nil {
		t.Fatalf("reviewCommand() error = %v", err)
	}

	if expected := []string{"run", "--pure", "--agent", "reviewer", "--dir", "/snapshot"}; !reflect.DeepEqual(args, expected) {
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
	if got := permission["read"].(map[string]any)["internal/review/engine.go"]; got != "allow" {
		t.Errorf("read permission = %v, expected allow", got)
	}
}

func TestReviewCommandOpenCodeFiltersUnsafePaths(t *testing.T) {
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
		SnapshotDir: "/snapshot",
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
	for _, tool := range []string{"read", "grep", "glob"} {
		permissions := config.Agent["reviewer"].Permission[tool].(map[string]any)
		if got := permissions["internal/review/engine.go"]; got != "allow" {
			t.Errorf("%s normalized permission = %v, expected allow", tool, got)
		}
		for _, unsafe := range []string{"/etc/passwd", "../outside.go", "nested/../../outside.go", "C:/outside.go", "D:\\outside.go", "unsafe\x00path", "unsafe\rpath", "unsafe\npath", "star*.go", "double**.go", "question?.go", "class[ab].go", "brace{a,b}.go", "-option.go"} {
			if got := permissions[unsafe]; got != nil {
				t.Errorf("%s permission for unsafe path %q = %v, expected absent", tool, unsafe, got)
			}
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

	env := reviewEnvironment("generated", t.TempDir(), "openai/gpt-5.6-terra")
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
	if dataHome := values["XDG_DATA_HOME"]; !reflect.DeepEqual(dataHome, []string{"/host/data"}) {
		t.Fatalf("XDG_DATA_HOME = %v, expected exactly the inherited authentication directory", dataHome)
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

func TestReviewCommandClaudeFailsWithoutPathConfinedToolPermissions(t *testing.T) {
	adapter := CLIAdapter{BinaryName: "claude"}
	_, _, err := adapter.reviewCommand(ReviewRequest{Prompt: "audit", MaxToolCalls: 7})

	if err == nil {
		t.Fatal("reviewCommand() error = nil, expected unavailable path confinement error")
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
