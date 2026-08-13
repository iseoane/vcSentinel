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
		Paths:        []string{"internal/review", "internal/planning/context.go"},
		MaxToolCalls: 7,
	})
	if err != nil {
		t.Fatalf("reviewCommand() error = %v", err)
	}

	if expected := []string{"run", "--agent", "reviewer"}; !reflect.DeepEqual(args, expected) {
		t.Fatalf("args = %v, expected %v", args, expected)
	}

	configText := env["OPENCODE_CONFIG_CONTENT"]
	var config struct {
		Agent map[string]struct {
			Steps      int                       `json:"steps"`
			Permission map[string]map[string]any `json:"permission"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(configText), &config); err != nil {
		t.Fatalf("review configuration is invalid JSON: %v", err)
	}
	reviewer := config.Agent["reviewer"]
	if reviewer.Steps != 7 {
		t.Errorf("steps = %d, expected 7", reviewer.Steps)
	}
	for _, tool := range []string{"bash", "edit", "write", "webfetch"} {
		if got := reviewer.Permission[tool]["*"]; got != "deny" {
			t.Errorf("%s permission = %v, expected deny", tool, got)
		}
	}
	for _, path := range []string{"internal/review", "internal/planning/context.go"} {
		for _, tool := range []string{"read", "grep", "glob"} {
			if got := reviewer.Permission[tool][path]; got != "allow" {
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
		},
	})
	if err != nil {
		t.Fatalf("reviewCommand() error = %v", err)
	}

	var config struct {
		Agent map[string]struct {
			Permission map[string]map[string]any `json:"permission"`
		} `json:"agent"`
	}
	if err := json.Unmarshal([]byte(env["OPENCODE_CONFIG_CONTENT"]), &config); err != nil {
		t.Fatalf("review configuration is invalid JSON: %v", err)
	}
	for _, tool := range []string{"read", "grep", "glob"} {
		permissions := config.Agent["reviewer"].Permission[tool]
		if got := permissions["internal/review/engine.go"]; got != "allow" {
			t.Errorf("%s normalized permission = %v, expected allow", tool, got)
		}
		for _, unsafe := range []string{"/etc/passwd", "../outside.go", "nested/../../outside.go", "C:/outside.go", "D:\\outside.go", "unsafe\x00path", "unsafe\rpath", "unsafe\npath"} {
			if got := permissions[unsafe]; got != nil {
				t.Errorf("%s permission for unsafe path %q = %v, expected absent", tool, unsafe, got)
			}
		}
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
