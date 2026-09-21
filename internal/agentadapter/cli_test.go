package agentadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/config"
)

func TestPrepareCommitCommandOpenCodeIsolatesExecution(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not get the current directory: %v", err)
	}
	adapter := CLIAdapter{
		BinaryName: "opencode",
		Config:     config.AgentConfig{Model: "openai/gpt-5.6-sol", ReasoningEffort: "high"},
	}

	cmd, cleanup, err := adapter.prepareCommitCommand(context.Background(), "feat(test): mensaje")
	if err != nil {
		t.Fatalf("prepareCommitCommand returned an error: %v", err)
	}

	wantArgs := []string{"opencode", "run", "--pure", "--agent", "title", "--format", "json", "--model", "openai/gpt-5.6-sol", "--variant", "high", "--dir", cmd.Dir}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("args = %v, want %v", cmd.Args, wantArgs)
	}
	if cmd.Dir == "" {
		t.Fatal("OpenCode must run in a neutral directory")
	}
	if samePath(cmd.Dir, cwd) {
		t.Fatalf("cmd.Dir = %q, it must differ from the repository cwd %q", cmd.Dir, cwd)
	}
	data, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		t.Fatalf("could not read stdin: %v", err)
	}
	if string(data) != "feat(test): mensaje" {
		t.Fatalf("stdin = %q, want the full prompt", data)
	}

	cleanup()
	if _, err := os.Stat(cmd.Dir); !os.IsNotExist(err) {
		t.Fatalf("the isolated directory still exists after cleanup: %v", err)
	}
}

// TestPrepareClaudeCommitCommandIsolatesExecution mirrors
// TestPrepareCommitCommandOpenCodeIsolatesExecution: claude must run outside
// the repository, with every tool disabled and --safe-mode set.
func TestPrepareClaudeCommitCommandIsolatesExecution(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not get the current directory: %v", err)
	}
	adapter := CLIAdapter{
		BinaryName: "claude",
		Config:     config.AgentConfig{Model: "claude-sonnet-5", ReasoningEffort: "high"},
	}

	cmd, cleanup, err := adapter.prepareClaudeCommitCommand(context.Background(), "feat(test): mensaje")
	if err != nil {
		t.Fatalf("prepareClaudeCommitCommand returned an error: %v", err)
	}

	wantArgs := []string{"claude", "-p", "--safe-mode", "--tools", "", "--model", "claude-sonnet-5", "--effort", "high"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("args = %v, want %v", cmd.Args, wantArgs)
	}
	if cmd.Dir == "" {
		t.Fatal("claude must run in a neutral directory")
	}
	if samePath(cmd.Dir, cwd) {
		t.Fatalf("cmd.Dir = %q, it must differ from the repository cwd %q", cmd.Dir, cwd)
	}
	data, err := io.ReadAll(cmd.Stdin)
	if err != nil {
		t.Fatalf("could not read stdin: %v", err)
	}
	if string(data) != "feat(test): mensaje" {
		t.Fatalf("stdin = %q, want the full prompt", data)
	}

	cleanup()
	if _, err := os.Stat(cmd.Dir); !os.IsNotExist(err) {
		t.Fatalf("the isolated directory still exists after cleanup: %v", err)
	}
}

type agentCapture struct {
	Args          []string `json:"args"`
	Dir           string   `json:"dir"`
	Stdin         string   `json:"stdin"`
	Home          string   `json:"home"`
	OpenCodeAuth  string   `json:"opencode_auth"`
	ProviderState string   `json:"provider_state"`
}

func samePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && filepath.Clean(absA) == filepath.Clean(absB)
}

func readAgentCapture(t *testing.T, path string) agentCapture {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read the agent capture: %v", err)
	}
	var capture agentCapture
	if err := json.Unmarshal(data, &capture); err != nil {
		t.Fatalf("invalid capture: %v", err)
	}
	return capture
}

func TestGetCommitMessageOpenCodeWithoutDiffFailsWithoutInvokingProcess(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("VCSENTINEL_TEST_CAPTURE", capturePath)
	t.Setenv("VCSENTINEL_TEST_OUTPUT", "feat(adapter): conservar contexto del repositorio")
	adapter := CLIAdapter{BinaryName: compileAgentBinary(t, "opencode"), Timeout: 10 * time.Second}

	if _, err := adapter.GetCommitMessage([]string{"internal/git/plan.go"}, "backend", 1); err == nil {
		t.Fatal("OpenCode without the consented micro-diff must fail closed")
	}
	if _, err := os.Stat(capturePath); !os.IsNotExist(err) {
		t.Fatalf("OpenCode was invoked without the consented micro-diff: %v", err)
	}
}

// TestGetCommitMessageClaudeWithoutDiffFailsWithoutInvokingProcess mirrors
// TestGetCommitMessageOpenCodeWithoutDiffFailsWithoutInvokingProcess: claude
// must also be blocked from generating a commit message without a micro-diff.
func TestGetCommitMessageClaudeWithoutDiffFailsWithoutInvokingProcess(t *testing.T) {
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("VCSENTINEL_TEST_CAPTURE", capturePath)
	t.Setenv("VCSENTINEL_TEST_OUTPUT", "feat(adapter): conservar contexto del repositorio")
	adapter := CLIAdapter{BinaryName: compileAgentBinary(t, "claude"), Timeout: 10 * time.Second}

	if _, err := adapter.GetCommitMessage([]string{"internal/git/plan.go"}, "backend", 1); err == nil {
		t.Fatal("claude without the consented micro-diff must fail closed")
	}
	if _, err := os.Stat(capturePath); !os.IsNotExist(err) {
		t.Fatalf("claude was invoked without the consented micro-diff: %v", err)
	}
}

// TestGetCommitMessageWithDiffClaudeRunsIsolated mirrors
// TestGetCommitMessageWithDiffOpenCodeRunsIsolated: claude runs isolated
// outside the repository and its plain-text output goes straight through
// validateCommitMessage, without any NDJSON parsing.
func TestGetCommitMessageWithDiffClaudeRunsIsolated(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not get the current directory: %v", err)
	}
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("VCSENTINEL_TEST_CAPTURE", capturePath)
	t.Setenv("VCSENTINEL_TEST_OUTPUT", "fix(adapter): usar el micro diff aislado")
	adapter := CLIAdapter{
		BinaryName: compileAgentBinary(t, "claude"),
		Config:     config.AgentConfig{Model: "claude-sonnet-5", ReasoningEffort: "high"},
		Timeout:    10 * time.Second,
	}
	microDiff := "diff --git a/x.go b/x.go\n+func corregida() {}"

	message, err := adapter.GetCommitMessageWithDiff([]string{"x.go"}, "backend", 1, microDiff)
	if err != nil {
		t.Fatalf("GetCommitMessageWithDiff returned an error: %v", err)
	}
	if message != "fix(adapter): usar el micro diff aislado" {
		t.Fatalf("message = %q", message)
	}
	capture := readAgentCapture(t, capturePath)
	if samePath(capture.Dir, cwd) {
		t.Fatalf("isolated cwd = %q, it must not be the repository", capture.Dir)
	}
	wantArgs := []string{"-p", "--safe-mode", "--tools", "", "--model", "claude-sonnet-5", "--effort", "high"}
	if !reflect.DeepEqual(capture.Args, wantArgs) {
		t.Fatalf("args = %v, want %v", capture.Args, wantArgs)
	}
	if !strings.Contains(capture.Stdin, microDiff) {
		t.Fatalf("stdin does not contain the real micro-diff: %q", capture.Stdin)
	}
	if _, err := os.Stat(capture.Dir); !os.IsNotExist(err) {
		t.Fatalf("the isolated cwd still exists after the run: %v", err)
	}
}

func TestGetCommitMessageWithDiffOpenCodeRunsIsolated(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("could not get the current directory: %v", err)
	}
	capturePath := filepath.Join(t.TempDir(), "capture.json")
	t.Setenv("VCSENTINEL_TEST_CAPTURE", capturePath)
	t.Setenv("VCSENTINEL_TEST_OUTPUT", "{\"type\":\"step_start\"}\n{\"type\":\"text\",\"part\":{\"text\":\"fix(adapter): usar el micro diff aislado\"}}\n{\"type\":\"step_finish\"}\n")
	adapter := CLIAdapter{
		BinaryName: compileAgentBinary(t, "opencode"),
		Config:     config.AgentConfig{Model: "openai/gpt-5.6-sol", ReasoningEffort: "high"},
		Timeout:    10 * time.Second,
	}
	microDiff := "diff --git a/x.go b/x.go\n+func corregida() {}"

	message, err := adapter.GetCommitMessageWithDiff([]string{"x.go"}, "backend", 1, microDiff)
	if err != nil {
		t.Fatalf("GetCommitMessageWithDiff returned an error: %v", err)
	}
	if message != "fix(adapter): usar el micro diff aislado" {
		t.Fatalf("message = %q", message)
	}
	capture := readAgentCapture(t, capturePath)
	if samePath(capture.Dir, cwd) {
		t.Fatalf("isolated cwd = %q, it must not be the repository", capture.Dir)
	}
	wantArgs := []string{"run", "--pure", "--agent", "title", "--format", "json", "--model", "openai/gpt-5.6-sol", "--variant", "high", "--dir", capture.Dir}
	if !reflect.DeepEqual(capture.Args, wantArgs) {
		t.Fatalf("args = %v, want %v", capture.Args, wantArgs)
	}
	if !strings.Contains(capture.Stdin, microDiff) {
		t.Fatalf("stdin does not contain the real micro-diff: %q", capture.Stdin)
	}
	if _, err := os.Stat(capture.Dir); !os.IsNotExist(err) {
		t.Fatalf("the isolated cwd still exists after the run: %v", err)
	}
}

// TestGetCommitMessageWithDiffClaudePropagatesStderrDetail checks that a
// claude process failure keeps the stderr detail in the returned error
// instead of discarding it silently.
func TestGetCommitMessageWithDiffClaudePropagatesStderrDetail(t *testing.T) {
	t.Setenv("VCSENTINEL_TEST_FAIL", "authentication expired")
	adapter := CLIAdapter{
		BinaryName: compileAgentBinary(t, "claude"),
		Timeout:    10 * time.Second,
	}

	_, err := adapter.GetCommitMessageWithDiff([]string{"x.go"}, "backend", 1, "diff")
	if err == nil {
		t.Fatal("a claude process error was expected")
	}
	if !strings.Contains(err.Error(), "authentication expired") {
		t.Fatalf("error = %q, want it to include the stderr detail", err)
	}
}

// TestGetCommitMessageWithDiffOpenCodePropagatesStderrDetail mirrors
// TestGetCommitMessageWithDiffClaudePropagatesStderrDetail for opencode.
func TestGetCommitMessageWithDiffOpenCodePropagatesStderrDetail(t *testing.T) {
	t.Setenv("VCSENTINEL_TEST_FAIL", "rate limit exceeded")
	adapter := CLIAdapter{
		BinaryName: compileAgentBinary(t, "opencode"),
		Timeout:    10 * time.Second,
	}

	_, err := adapter.GetCommitMessageWithDiff([]string{"x.go"}, "backend", 1, "diff")
	if err == nil {
		t.Fatal("an opencode process error was expected")
	}
	if !strings.Contains(err.Error(), "rate limit exceeded") {
		t.Fatalf("error = %q, want it to include the stderr detail", err)
	}
}

func TestValidateCommitMessage(t *testing.T) {
	tc := []struct {
		name   string
		output string
		valid  bool
	}{
		{name: "conventional", output: "feat(slice): group by cohesion", valid: true},
		{name: "multiline", output: "I reviewed the changes\nfeat(slice): group by cohesion", valid: false},
		{name: "generic text", output: "I reviewed the changes", valid: false},
		{name: "empty", output: "  ", valid: false},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			message, err := validateCommitMessage(tc.output)
			if (err == nil) != tc.valid {
				t.Fatalf("validateCommitMessage(%q) error = %v", tc.output, err)
			}
			if tc.valid && message != strings.TrimSpace(tc.output) {
				t.Fatalf("message = %q, want %q", message, strings.TrimSpace(tc.output))
			}
		})
	}
}

func TestExtractOpenCodeCommitMessage(t *testing.T) {
	tc := []struct {
		name   string
		output string
		want   string
		valid  bool
	}{
		{
			name:   "one text among events",
			output: "{\"type\":\"step_start\"}\n{\"type\":\"text\",\"part\":{\"text\":\"feat(slice): describe the change\"}}\n{\"type\":\"step_finish\"}\n",
			want:   "feat(slice): describe the change",
			valid:  true,
		},
		{name: "malformed json", output: "{\"type\":\"text\"", valid: false},
		{name: "concatenated objects on one line", output: "{\"type\":\"step_start\"}{\"type\":\"text\",\"part\":{\"text\":\"feat: change\"}}\n", valid: false},
		{name: "blank intermediate line", output: "{\"type\":\"step_start\"}\n\n{\"type\":\"text\",\"part\":{\"text\":\"feat: change\"}}\n", valid: false},
		{name: "blank content", output: "   \n", valid: false},
		{name: "no text", output: "{\"type\":\"step_finish\"}\n", valid: false},
		{
			name:   "text without payload",
			output: "{\"type\":\"text\",\"part\":{}}\n",
			valid:  false,
		},
		{
			name:   "conflicting texts",
			output: "{\"type\":\"text\",\"part\":{\"text\":\"feat: first\"}}\n{\"type\":\"text\",\"part\":{\"text\":\"fix: second\"}}\n",
			valid:  false,
		},
		{
			name:   "multiline payload",
			output: "{\"type\":\"text\",\"part\":{\"text\":\"explanation\\nfeat: change\"}}\n",
			valid:  false,
		},
		{
			name:   "non-conventional payload",
			output: "{\"type\":\"text\",\"part\":{\"text\":\"change without format\"}}\n",
			valid:  false,
		},
	}

	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			message, err := extractOpenCodeCommitMessage(tc.output)
			if (err == nil) != tc.valid {
				t.Fatalf("extractOpenCodeCommitMessage error = %v", err)
			}
			if message != tc.want {
				t.Fatalf("message = %q, want %q", message, tc.want)
			}
		})
	}
}

func TestExtractOpenCodeCommitMessageAcceptsLargeBoundedJSONLLine(t *testing.T) {
	output := fmt.Sprintf("{\"type\":\"step_start\",\"padding\":%q}\n{\"type\":\"text\",\"part\":{\"text\":\"feat(slice): validar jsonl grande\"}}\n", strings.Repeat("x", 70*1024))
	message, err := extractOpenCodeCommitMessage(output)
	if err != nil || message != "feat(slice): validar jsonl grande" {
		t.Fatalf("message = %q, err = %v", message, err)
	}
}

func TestExtractOpenCodeCommitMessageRejectsJSONLLineOverLimit(t *testing.T) {
	output := fmt.Sprintf("{\"type\":\"step_start\",\"padding\":%q}\n", strings.Repeat("x", 1024*1024))
	if _, err := extractOpenCodeCommitMessage(output); err == nil {
		t.Fatal("a JSONL line above the explicit limit was accepted")
	}
}

func TestGetCommitMessageWithDiffIncludesDiff(t *testing.T) {
	prompt := buildAgentPromptWithDiff("backend", 2, []string{"internal/git/plan.go"}, "diff --git a/x b/x\n+linea", "es")
	if !strings.Contains(prompt, "diff --git a/x b/x\n+linea") {
		t.Fatalf("the prompt does not contain the micro-diff: %q", prompt)
	}
}

func TestGetCommitMessageOpenCodeRejectsNonConventionalOutput(t *testing.T) {
	binary := compileAgentBinary(t, "opencode")
	adapter := CLIAdapter{BinaryName: binary, Timeout: 10 * time.Second}

	if _, err := adapter.GetCommitMessage([]string{"internal/git/plan.go"}, "backend", 1); err == nil {
		t.Fatal("an error was expected when the full prompt comes back as the output")
	}
}

// compileSleeper compiles the testdata/sleeper helper to a temporary
// executable and returns its path. Tests using it are skipped in -short mode.
func compileSleeper(t *testing.T) string {
	t.Helper()
	return compileAgentBinary(t, "sleeper")
}

// compileAgentBinary compiles the testdata/sleeper helper to a temporary
// binary with the given name (e.g. "opencode" or "claude") and returns its
// path. Naming the binary after the active agent activates stdin transport in
// the adapter without depending on a real agent. Tests using it are skipped
// in -short mode.
func compileAgentBinary(t *testing.T, name string) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skips compiling the helper in -short mode")
	}

	exe := filepath.Join(t.TempDir(), name)
	if filepath.Ext(exe) == "" && os.PathSeparator == '\\' {
		exe += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", exe, ".")
	cmd.Dir = filepath.Join("testdata", "sleeper")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("could not compile the helper %s: %v\n%s", name, err, output)
	}
	return exe
}

// longPrompt returns a prompt of more than 40,000 characters, above the
// 32,767 limit of the Windows command line, to verify that stdin transport
// does not depend on the length.
func longPrompt() string {
	return strings.Repeat("[1]", 15_000)
}

func TestRunCommandReturnsOutput(t *testing.T) {
	sleeper := compileSleeper(t)
	adapter := CLIAdapter{BinaryName: sleeper}

	output, err := adapter.runCommandWithTimeout("prompt de prueba", 10*time.Second)
	if err != nil {
		t.Fatalf("runCommandWithTimeout returned an error: %v", err)
	}
	if output != "prompt de prueba" {
		t.Errorf("output = %q, want %q", output, "prompt de prueba")
	}
}

func TestRunCommandKillsOnTimeout(t *testing.T) {
	sleeper := compileSleeper(t)
	adapter := CLIAdapter{BinaryName: sleeper}

	start := time.Now()
	// The helper interprets the first numeric argument as sleep seconds:
	// "3" sleeps 3 s, far above the 150 ms timeout.
	_, err := adapter.runCommandWithTimeout("3", 150*time.Millisecond)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a timeout error was expected, none was returned")
	}
	if elapsed > 2*time.Second {
		t.Errorf("the process was not cut off: it took %v to return", elapsed)
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("the error arrived too early (%v), it looks like a pre-timeout failure", elapsed)
	}
}

func TestRunCommandTimeoutCarriesEarlyStderr(t *testing.T) {
	sleeper := compileSleeper(t)
	adapter := CLIAdapter{BinaryName: sleeper}

	// The helper writes the refusal to stderr BEFORE sleeping past the
	// budget: a timed-out probe must keep the agent's own words instead of
	// collapsing to a bare "signal: killed".
	t.Setenv("VCSENTINEL_TEST_STDERR_EARLY", "Error: The usage limit has been reached")
	t.Setenv("VCSENTINEL_TEST_SLEEP", "30")

	_, err := adapter.runCommandWithTimeout("probe", 300*time.Millisecond)
	if err == nil {
		t.Fatal("a timeout error was expected, none was returned")
	}
	if !strings.Contains(err.Error(), "The usage limit has been reached") {
		t.Fatalf("error = %q, want it to carry the agent's early stderr", err)
	}
	var cmdFail *CommandFailure
	if !errors.As(err, &cmdFail) {
		t.Fatalf("error = %T (%v), want *CommandFailure", err, err)
	}
	if !cmdFail.TimedOut {
		t.Errorf("TimedOut = false, want true for a budget kill")
	}
}

// TestRunCommandStdinOpenCode verifies opencode receives the prompt via
// stdin (the helper is named "opencode" to activate that path) and that a
// prompt above the Windows line limit arrives complete, without truncation.
func TestRunCommandStdinOpenCode(t *testing.T) {
	binary := compileAgentBinary(t, "opencode")
	adapter := CLIAdapter{
		BinaryName: binary,
		Config:     config.AgentConfig{Model: "deepseek-v4-flash-free", ReasoningEffort: "default"},
	}

	prompt := longPrompt()
	output, err := adapter.runCommandWithTimeout(prompt, 10*time.Second)
	if err != nil {
		t.Fatalf("runCommandWithTimeout returned an error: %v", err)
	}
	if output != prompt {
		t.Errorf("output of length %d, want %d (the prompt travels via stdin without truncation)", len(output), len(prompt))
	}
}

// TestRunCommandStdinClaude verifies the same stdin transport for claude,
// with its usual model/effort configuration.
func TestRunCommandStdinClaude(t *testing.T) {
	binary := compileAgentBinary(t, "claude")
	adapter := CLIAdapter{
		BinaryName: binary,
		Config:     config.AgentConfig{Model: "claude-3-5-sonnet", ReasoningEffort: "high"},
	}

	prompt := longPrompt()
	output, err := adapter.runCommandWithTimeout(prompt, 10*time.Second)
	if err != nil {
		t.Fatalf("runCommandWithTimeout returned an error: %v", err)
	}
	if output != prompt {
		t.Errorf("output of length %d, want %d (the prompt travels via stdin without truncation)", len(output), len(prompt))
	}
}

// TestRunCommandUnknownBinaryUsesArgument verifies that a binary outside the
// known ones (claude/opencode) keeps the argument transport: the "sleeper"
// helper receives the prompt as "-p <prompt>" and returns it verbatim.
func TestRunCommandUnknownBinaryUsesArgument(t *testing.T) {
	sleeper := compileSleeper(t)
	adapter := CLIAdapter{BinaryName: sleeper}

	// Long prompt but below the 32,767 Windows limit: the unknown case keeps
	// traveling as an argument and must not be truncated.
	prompt := strings.Repeat("x", 20_000)
	output, err := adapter.runCommandWithTimeout(prompt, 10*time.Second)
	if err != nil {
		t.Fatalf("runCommandWithTimeout returned an error: %v", err)
	}
	if output != prompt {
		t.Errorf("output of length %d, want %d (the argument transport is kept)", len(output), len(prompt))
	}
}

// TestPromptCommandDecidesStdin verifies the transport decision without
// running any binary: opencode/claude use stdin; anything else passes the
// prompt as the "-p" argument. The opencode invocation carries --pure like
// the sibling review and title invocations (no external plugins) and never
// --format json: this path parses trimmed plain text, not an event stream.
func TestPromptCommandDecidesStdin(t *testing.T) {
	tc := []struct {
		name     string
		binary   string
		model    string
		prompt   string
		wantArgs []string
		viaStdin bool
	}{
		{name: "opencode uses run and stdin", binary: "opencode", wantArgs: []string{"run", "--pure"}, viaStdin: true},
		{name: "opencode with extension is detected", binary: "opencode.exe", wantArgs: []string{"run", "--pure"}, viaStdin: true},
		{name: "opencode with a configured model", binary: "opencode", model: "deepseek-v4-flash-free", wantArgs: []string{"run", "--pure", "--model", "deepseek-v4-flash-free"}, viaStdin: true},
		{name: "claude uses -p and stdin", binary: "claude", wantArgs: []string{"-p"}, viaStdin: true},
		{name: "unknown uses -p with the prompt as argument", binary: "sleeper", prompt: "prompt de prueba", wantArgs: []string{"-p", "prompt de prueba"}, viaStdin: false},
	}
	for _, tc := range tc {
		t.Run(tc.name, func(t *testing.T) {
			adapter := CLIAdapter{BinaryName: tc.binary, Config: config.AgentConfig{Model: tc.model}}
			args, viaStdin := adapter.promptCommand(tc.prompt)
			if !reflect.DeepEqual(args, tc.wantArgs) {
				t.Errorf("promptCommand(%q) args = %v, want %v", tc.prompt, args, tc.wantArgs)
			}
			if viaStdin != tc.viaStdin {
				t.Errorf("promptCommand(%q) viaStdin = %v, want %v", tc.prompt, viaStdin, tc.viaStdin)
			}
		})
	}
}
