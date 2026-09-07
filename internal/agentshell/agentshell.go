// Package agentshell centralizes the logic shared between internal/ops and
// internal/validation to delegate verification/validation to an agent with a
// free shell: running one command through the system shell returning the exit
// code + combined output, and parsing the "tested: ..." contract the agent
// returns when it finishes. Before this extraction, both packages had a
// nearly identical copy of this code (one of them byte for byte); living here
// keeps a behavior change (for example, how an exit code is detected) from
// having to be replicated by hand in both places.
//
// Deliberately free of dependencies on internal/ops and internal/validation:
// that way both can import this package without creating a cycle.
package agentshell

import (
	"errors"
	"os/exec"
	"runtime"
	"strings"
)

// Run launches command through the system shell (cmd /c on Windows, sh -c
// elsewhere) inside worktree and returns the exit code and the combined
// output (stdout+stderr). It is the superset of what the two callers need:
// internal/validation uses the output (fails_when=output_not_empty and the
// evidence of findings); internal/ops only needs the exit code and discards
// the output at its call site.
//
// Commands come from the user's vassentinel.yml: running them through a shell
// is the design (trust equivalent to the yml itself); they are not sanitized
// here.
func Run(worktree, command string) (exit int, output string, err error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/c", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	if worktree != "" {
		cmd.Dir = worktree
	}
	outputBytes, err := cmd.CombinedOutput()
	output = string(outputBytes)
	if err == nil {
		return 0, output, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), output, nil
	}
	return -1, output, err
}

// ParseTestedContract extracts the commands from the "tested: ..." line of a
// delegated agent's output and rejects unavailable / a missing contract.
func ParseTestedContract(output string) ([]string, error) {
	if strings.Contains(strings.ToLower(output), "unavailable") {
		return nil, errors.New("the agent could not run the tests (unavailable)")
	}
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		idx := strings.Index(trimmed, "tested:")
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(trimmed[idx+len("tested:"):])
		var commands []string
		for _, c := range strings.Split(rest, ";") {
			c = strings.TrimSpace(c)
			if c != "" {
				commands = append(commands, c)
			}
		}
		if len(commands) == 0 {
			return nil, errors.New("empty tested contract")
		}
		return commands, nil
	}
	return nil, errors.New("the output does not contain a tested contract")
}
