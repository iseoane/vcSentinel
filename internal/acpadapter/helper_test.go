package acpadapter

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	helperEnvVar    = "GO_WANT_ACP_HELPER_PROCESS"
	helperModeEnv   = "ACP_HELPER_MODE"
	helperModeOK    = "end-turn"
	helperModeTrap  = "exit0-cancelled"
	helperModeSleep = "sleep-long"
	helperModeFlood = "oversized-output"

	// helperExpectEnv optionally overrides the pinned argument tail
	// (space-separated) so non-default configurations — revision mode with
	// --cwd, custom timeouts — can pin their exact command-line contract.
	helperExpectEnv = "ACP_HELPER_EXPECT_TAIL"

	// helperArgFileEnv optionally points at a file where the helper records
	// its received argument tail, one argument per line, for post-run order
	// assertions.
	helperArgFileEnv = "ACP_HELPER_ARGFILE"
)

// legacyHelperTail is the strict default contract pinned by the original
// spawn tests: global flags first, then the agent token, then exec, then the
// prompt.
var legacyHelperTail = []string{"--format", "json", "--json-strict", "--timeout", "300",
	"claude", "exec", "say PROBE"}

// TestHelperProcess is the child process for the spawn tests. The parent runs
// this same test binary as the acpx launcher, so the helper can pin the exact
// command-line contract before emitting its canned transcript. Env-selected
// modes cover the slice-2 scenarios: a long-running child for
// cancellation/ownership tests and an oversized-output flood for the
// MaxOutputBytes cap test.
func TestHelperProcess(t *testing.T) {
	if os.Getenv(helperEnvVar) != "1" {
		t.Skip("helper process only")
	}
	args := os.Args
	dashdash := -1
	for i, arg := range args {
		if arg == "--" {
			dashdash = i
			break
		}
	}
	var tail []string
	if dashdash >= 0 {
		tail = args[dashdash+1:]
	}
	pinTail := true
	want := legacyHelperTail
	switch expect := os.Getenv(helperExpectEnv); {
	case expect == "-":
		// Scenario tests whose tail embeds a generated snapshot directory
		// skip exact pinning; their contract is asserted post-run through
		// the argument-recording file instead.
		pinTail = false
	case expect != "":
		want = strings.Fields(expect)
	}
	if pinTail && (dashdash < 0 || !slices.Equal(tail, want)) {
		fmt.Fprintf(os.Stderr, "helper got args %v (dashdash=%d), want tail %v\n", args, dashdash, want)
		os.Exit(2)
	}
	if argFile := os.Getenv(helperArgFileEnv); argFile != "" {
		if err := os.WriteFile(argFile, []byte(strings.Join(tail, "\n")), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "helper cannot record args: %v\n", err)
			os.Exit(4)
		}
	}
	switch os.Getenv(helperModeEnv) {
	case helperModeOK:
		fmt.Print(strings.Join([]string{
			`{"jsonrpc":"2.0","id":0,"result":{"configOptions":[{"id":"model","currentValue":"test-model"}]}}`,
			`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"HELLO FROM FAKE ACPX"}}}}`,
			`{"jsonrpc":"2.0","id":7,"result":{"stopReason":"end_turn","usage":{"inputTokens":1,"outputTokens":2}}}`,
			"",
		}, "\n"))
	case helperModeTrap:
		// Both exit codes are 0 live; classification must still be
		// cancellation because the terminal stopReason says so.
		fmt.Print(`{"jsonrpc":"2.0","id":8,"result":{"stopReason":"cancelled"}}` + "\n")
	case helperModeSleep:
		// Long-running stand-in for a stuck backend: the parent must end
		// this child through context cancellation plus tree termination,
		// never by waiting it out.
		time.Sleep(60 * time.Second)
	case helperModeFlood:
		// Continuous oversized stdout used to drive the drain past a small
		// MaxOutputBytes cap while the child is still running; the parent is
		// expected to stop reading and terminate this process.
		line := strings.Repeat("x", 1024) + "\n"
		for i := 0; i < 4096; i++ {
			fmt.Fprint(os.Stdout, line)
		}
	default:
		fmt.Fprintln(os.Stderr, "unknown helper mode:", os.Getenv(helperModeEnv))
		os.Exit(3)
	}
	os.Exit(0)
}

// spawnHelperConfig builds an adapter over the fake acpx child with the
// standard launcher guard, letting each test mutate the configuration — extra
// ChildEnv entries, budgets — before construction.
func spawnHelperConfig(t *testing.T, mutate func(*Config)) *AcpxAdapter {
	t.Helper()
	cfg := Config{
		Launcher: []string{os.Args[0], "-test.run=^TestHelperProcess$", "--"},
		Agent:    "claude",
		Effort:   "high",
		Model:    "fallback-model",
		ChildEnv: []string{helperEnvVar + "=1", helperModeEnv + "=" + helperModeOK},
	}
	if mutate != nil {
		mutate(&cfg)
	}
	a, err := NewAcpx(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func spawnHelper(t *testing.T, mode string) (*AcpxAdapter, error) {
	t.Helper()
	return spawnHelperConfig(t, func(cfg *Config) {
		cfg.ChildEnv = append(cfg.ChildEnv, helperModeEnv+"="+mode)
	}), nil
}
