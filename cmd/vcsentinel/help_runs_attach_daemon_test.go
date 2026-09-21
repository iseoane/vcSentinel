package main

// Dedicated -h/--help coverage for the two durable-runs surfaces that postdate
// ticket 15 (D2/D3): 'runs attach' must answer with its own text instead of
// dying at flag parsing, and 'runs daemon' must answer with its own text
// instead of degrading to the generic runs usage.

import (
	"bytes"
	"strings"
	"testing"
)

// TestHandleHelpServesDedicatedAttach: 'runs attach --help' and '-h' resolve
// attach's dedicated text on stdout, empty stderr, before reaching the flag
// parser that used to reject it as an unknown flag.
func TestHandleHelpServesDedicatedAttach(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		out, errOut, served := capturedHandleHelp(t, "runs", []string{"attach", flag})
		if !served {
			t.Fatalf("runs attach %s: help did not intercept; the handler would keep rejecting the flag", flag)
		}
		if !strings.Contains(out, runsAttachUsage) || !strings.Contains(out, "--follow") {
			t.Errorf("runs attach %s: the output is not attach's dedicated help:\n%s", flag, out)
		}
		if errOut != "" {
			t.Errorf("runs attach %s: stderr must stay empty, received %q", flag, errOut)
		}
	}
}

// TestHandleHelpServesDedicatedDaemon: both 'runs daemon --help' and the
// third-level invocation 'runs daemon start --help' serve daemon's dedicated
// text (start|status|stop), never the generic 'vcsentinel runs' header.
func TestHandleHelpServesDedicatedDaemon(t *testing.T) {
	scenarios := [][]string{
		{"daemon", "--help"},
		{"daemon", "-h"},
		{"daemon", "start", "--help"},
		{"daemon", "status", "-h"},
		{"daemon", "stop", "--help"},
	}
	for _, args := range scenarios {
		out, errOut, served := capturedHandleHelp(t, "runs", args)
		if !served {
			t.Fatalf("runs %v: help was supposed to intercept with daemon's text", args)
		}
		if !strings.Contains(out, runsDaemonUsage) || !strings.Contains(out, "orphaned-runs") {
			t.Errorf("runs %v: the output is not daemon's dedicated help:\n%s", args, out)
		}
		if strings.Contains(out, "vcsentinel runs <subcommand>") || out == runsUsage {
			t.Errorf("runs %v: served runs' generic help instead of daemon's:\n%s", args, out)
		}
		if errOut != "" {
			t.Errorf("runs %v: stderr must stay empty, received %q", args, errOut)
		}
	}
}

// TestHelpAttachWinsOverInvalidFlag: the help request wins over any other
// flag line, including ones the parser would reject. It is checked at the two
// layers: the central interception and attach handler's own guard.
func TestHelpAttachWinsOverInvalidFlag(t *testing.T) {
	args := []string{"attach", "--older-than", "720h", "--help"}
	out, errOut, served := capturedHandleHelp(t, "runs", args)
	if !served {
		t.Fatalf("runs %v: the central help was supposed to intercept", args)
	}
	if !strings.Contains(out, runsAttachUsage) {
		t.Errorf("runs %v: the central interception did not serve attach's help:\n%s", args, out)
	}
	if errOut != "" {
		t.Errorf("runs %v: stderr must stay empty, received %q", args, errOut)
	}

	var handlerOut bytes.Buffer
	code := executeRunsAttach(&handlerOut, t.TempDir(), []string{"--older-than", "720h", "--help"})
	if code != runExitSuccess {
		t.Errorf("executeRunsAttach with --help mixed in: exit %d, want 0", code)
	}
	if handlerOut.String() != runsAttachHelp {
		t.Errorf("executeRunsAttach with --help mixed in: did not serve the dedicated text:\n%s", handlerOut.String())
	}

	var hOut bytes.Buffer
	if code := executeRunsAttach(&hOut, t.TempDir(), []string{"-h"}); code != runExitSuccess {
		t.Errorf("executeRunsAttach -h: exit %d, want 0", code)
	}
}

// TestAttachWithoutFlagsTakesNormalRoute: without a help request, attach
// enters its normal execution. In a temporary directory without a repository
// the first step (resolving the git common dir) fails with the infrastructure
// error: that proves the help guard did not fire and the real flow started.
func TestAttachWithoutFlagsTakesNormalRoute(t *testing.T) {
	for _, args := range [][]string{nil, {"--run", "r1"}} {
		var out, errOut bytes.Buffer
		if _, _, served := capturedHandleHelp(t, "runs", append([]string{"attach"}, args...)); served {
			t.Errorf("runs attach %v: intercepted without a help request", args)
		}
		code := executeRunsAttach(&out, t.TempDir(), args)
		if code != runExitInfrastructure {
			t.Errorf("runs attach %v: exit %d, want %d (deterministic failure outside a repository)", args, code, runExitInfrastructure)
		}
		if !strings.HasPrefix(out.String(), "❌") {
			t.Errorf("runs attach %v: the normal route was supposed to report the infrastructure failure, printed:\n%s", args, out.String())
		}
		if strings.Contains(out.String(), "Purpose:") {
			t.Errorf("runs attach %v: the normal route printed help text:\n%s", args, out.String())
		}
		if errOut.Len() != 0 {
			t.Errorf("runs attach %v: errOut captured without reason: %q", args, errOut.String())
		}
	}
}

// TestDaemonSubcommandsRejectExtraFlags: daemon's subcommands accept no flags
// by contract; only the help request is served, and it arrives from the
// central interception (resolved to the 'runs daemon' key), never from
// dedicated handlers — like all its siblings in the package, where no handler
// intercepts help itself.
func TestDaemonSubcommandsRejectExtraFlags(t *testing.T) {
	var out bytes.Buffer
	if code := executeRunsDaemon(&out, t.TempDir(), []string{"status", "--json"}); code != runExitUsage {
		t.Errorf("daemon status --json: exit %d, want %d", code, runExitUsage)
	}
	if !strings.Contains(out.String(), "❌ Unknown flag for 'vcsentinel runs daemon status': --json") {
		t.Errorf("daemon status --json: the rejection is not the expected contract error:\n%s", out.String())
	}
	out.Reset()
	if code := executeRunsDaemon(&out, t.TempDir(), nil); code != runExitUsage {
		t.Errorf("daemon without subcommand: exit %d, want %d", code, runExitUsage)
	}
	if !strings.Contains(out.String(), runsDaemonUsage) {
		t.Errorf("daemon without subcommand: the rejection does not quote the shared usage line:\n%s", out.String())
	}
}
