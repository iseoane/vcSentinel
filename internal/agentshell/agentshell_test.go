package agentshell

import (
	"runtime"
	"strings"
	"testing"
)

// combinedOutputCommand returns, depending on the operating system, a command
// that writes one line to stdout and another to stderr: it serves to check
// that Run returns the combined output (stdout+stderr), not just one of the
// two.
func combinedOutputCommand() string {
	if runtime.GOOS == "windows" {
		return "echo stdout-line && echo stderr-line 1>&2"
	}
	return "echo stdout-line; echo stderr-line >&2"
}

// exitCodeCommand returns a command that terminates with the given exit code
// (0 or non-zero), valid both in cmd and in sh.
func exitCodeCommand(code int) string {
	if code == 0 {
		if runtime.GOOS == "windows" {
			return "exit 0"
		}
		return "exit 0"
	}
	if runtime.GOOS == "windows" {
		return "exit 1"
	}
	return "exit 1"
}

// TestRun_CombinedOutput: Run must return stdout+stderr in a single string,
// together with exit 0 and no execution error.
func TestRun_CombinedOutput(t *testing.T) {
	exit, output, err := Run("", combinedOutputCommand())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
	if !strings.Contains(output, "stdout-line") || !strings.Contains(output, "stderr-line") {
		t.Errorf("output = %q, want it to contain both lines (stdout+stderr)", output)
	}
}

// TestRun_NonZeroExitCode: a command that fails with exit 1 is reflected in
// the returned exit code, without that being an execution error.
func TestRun_NonZeroExitCode(t *testing.T) {
	exit, _, err := Run("", exitCodeCommand(1))
	if err != nil {
		t.Fatalf("Run must not treat a non-zero exit code as an execution error: %v", err)
	}
	if exit != 1 {
		t.Errorf("exit = %d, want 1", exit)
	}
}

// TestParseTestedContract_MultipleCommands: extracts the commands from the
// "tested: ..." line separated by ;.
func TestParseTestedContract_MultipleCommands(t *testing.T) {
	commands, err := ParseTestedContract("Done.\ntested: go test ./...; go build ./...")
	if err != nil {
		t.Fatalf("ParseTestedContract failed: %v", err)
	}
	if len(commands) != 2 || commands[0] != "go test ./..." || commands[1] != "go build ./..." {
		t.Errorf("commands = %#v, want [go test ./... go build ./...]", commands)
	}
}

// TestParseTestedContract_Unavailable: rejects the "unavailable" output.
func TestParseTestedContract_Unavailable(t *testing.T) {
	if _, err := ParseTestedContract("could not run anything\nunavailable"); err == nil {
		t.Error("ParseTestedContract accepted unavailable")
	}
}

// TestParseTestedContract_EmptyContract: rejects a tested contract with no
// commands after the colon.
func TestParseTestedContract_EmptyContract(t *testing.T) {
	if _, err := ParseTestedContract("tested: "); err == nil {
		t.Error("ParseTestedContract accepted an empty tested contract")
	}
}

// TestParseTestedContract_NoContract: rejects an output with no tested line.
func TestParseTestedContract_NoContract(t *testing.T) {
	if _, err := ParseTestedContract("reply without contract"); err == nil {
		t.Error("ParseTestedContract accepted an output without a contract")
	}
}
