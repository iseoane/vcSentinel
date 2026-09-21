package agentadapter

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/config"
)

// Behavior tests for the model probe path (RunPrompt): the probe
// invocation must request the configured model via --model, just like
// reviewCommand, because environment variables (OPENCODE_MODEL) are not
// enough with real OpenCode. The fake testdata/sleeper binary captures the
// arguments without invoking a real agent.

func argsWithModel(t *testing.T, capture agentCapture, expectedModel string) {
	index := -1
	t.Helper()
	for i, arg := range capture.Args {
		if arg == "--model" {
			index = i
			break
		}
	}
	if index == -1 {
		t.Fatalf("probe invocation does not request a model: args = %v, expected --model %s", capture.Args, expectedModel)
	}
	if index+1 >= len(capture.Args) {
		t.Fatalf("--model has no value: args = %v", capture.Args)
	}
	if capture.Args[index+1] != expectedModel {
		t.Fatalf("requested model = %q, expected %q (args = %v)", capture.Args[index+1], expectedModel, capture.Args)
	}
}

func TestPromptProbeRequestsConfiguredModelOpenCode(t *testing.T) {
	const model = "opencode-go/glm-5.3-flash"
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("VCSENTINEL_TEST_CAPTURE", capturePath)
	adapter := CLIAdapter{
		BinaryName: compileAgentBinary(t, "opencode"),
		Config:     config.AgentConfig{Model: model, ReasoningEffort: "low"},
		Timeout:    10 * time.Second,
	}

	output, err := adapter.RunPrompt("what model are you?")
	if err != nil {
		t.Fatalf("RunPrompt returned an error: %v", err)
	}
	if output != "what model are you?" {
		t.Fatalf("output = %q, expected the prompt echoed via stdin", output)
	}

	capture := readAgentCapture(t, capturePath)
	if len(capture.Args) == 0 || capture.Args[0] != "run" {
		t.Fatalf("args = %v, expected it to start with the run subcommand", capture.Args)
	}
	argsWithModel(t, capture, model)
	if capture.Stdin != "what model are you?" {
		t.Fatalf("stdin = %q, expected the full prompt (stdin transport intact)", capture.Stdin)
	}
}

func TestPromptProbeRequestsConfiguredModelClaude(t *testing.T) {
	const model = "claude-sonnet-5"
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("VCSENTINEL_TEST_CAPTURE", capturePath)
	adapter := CLIAdapter{
		BinaryName: compileAgentBinary(t, "claude"),
		Config:     config.AgentConfig{Model: model, ReasoningEffort: "high"},
		Timeout:    10 * time.Second,
	}

	output, err := adapter.RunPrompt("what model are you?")
	if err != nil {
		t.Fatalf("RunPrompt returned an error: %v", err)
	}
	if output != "what model are you?" {
		t.Fatalf("output = %q, expected the prompt echoed via stdin", output)
	}

	capture := readAgentCapture(t, capturePath)
	if len(capture.Args) == 0 || capture.Args[0] != "-p" {
		t.Fatalf("args = %v, expected them to start with -p", capture.Args)
	}
	argsWithModel(t, capture, model)
	if capture.Stdin != "what model are you?" {
		t.Fatalf("stdin = %q, expected the full prompt (stdin transport intact)", capture.Stdin)
	}
}

func TestPromptProbeWithoutModelAddsNoFlag(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("VCSENTINEL_TEST_CAPTURE", capturePath)
	adapter := CLIAdapter{
		BinaryName: compileAgentBinary(t, "opencode"),
		Timeout:    10 * time.Second,
	}

	if _, err := adapter.RunPrompt("prompt"); err != nil {
		t.Fatalf("RunPrompt returned an error: %v", err)
	}

	capture := readAgentCapture(t, capturePath)
	for _, arg := range capture.Args {
		if arg == "--model" {
			t.Fatalf("without a configured model the invocation must not carry --model: args = %v", capture.Args)
		}
	}
	if len(capture.Args) == 0 || capture.Args[0] != "run" {
		t.Fatalf("args = %v, expected it to start with run", capture.Args)
	}
	if capture.Stdin != "prompt" {
		t.Fatalf("stdin = %q, expected the full prompt", capture.Stdin)
	}
}
