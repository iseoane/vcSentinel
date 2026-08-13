package agentadapter

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestReviewCommandOpenCodeRestrictsToolsAndSteps(t *testing.T) {
	adapter := CLIAdapter{BinaryName: "opencode"}
	args, env, err := adapter.reviewCommand(ReviewRequest{
		Prompt:       "audit",
		Paths:        []string{"internal/review/engine.go", "internal/planning/context.go"},
		SnapshotDir:  "/snapshot",
		MaxToolCalls: 7,
	})
	if err != nil {
		t.Fatalf("reviewCommand() error = %v", err)
	}

	if expected := []string{"run", "--pure", "--agent", "reviewer", "--dir", "/snapshot"}; !reflect.DeepEqual(args, expected) {
		t.Fatalf("args = %v, expected %v", args, expected)
	}

	configText := env["OPENCODE_CONFIG_CONTENT"]
	var config struct {
		Agent map[string]struct {
			Steps      int            `json:"steps"`
			Permission map[string]any `json:"permission"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(configText), &config); err != nil {
		t.Fatalf("review configuration is invalid JSON: %v", err)
	}
	reviewer := config.Agent["reviewer"]
	if reviewer.Steps != 7 {
		t.Errorf("steps = %d, expected 7", reviewer.Steps)
	}
	if got := reviewer.Permission["*"]; got != "deny" {
		t.Errorf("default permission = %v, expected deny", got)
	}
	for _, tool := range []string{"bash", "edit", "write", "webfetch"} {
		if got := reviewer.Permission[tool].(map[string]any)["*"]; got != "deny" {
			t.Errorf("%s permission = %v, expected deny", tool, got)
		}
	}
	for _, path := range []string{"internal/review/engine.go", "internal/planning/context.go"} {
		for _, tool := range []string{"read", "grep", "glob"} {
			if got := reviewer.Permission[tool].(map[string]any)[path]; got != "allow" {
				t.Errorf("%s permission for %q = %v, expected allow", tool, path, got)
			}
		}
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
	t.Setenv("HOME", "/host/home")

	env := reviewEnvironment("generated", t.TempDir())
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
	if values["OPENCODE_DISABLE_PROJECT_CONFIG"][0] != "1" || values["OPENCODE_PURE"][0] != "1" {
		t.Fatalf("review isolation is incomplete: %v", values)
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
