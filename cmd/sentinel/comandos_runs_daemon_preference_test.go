package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/daemon"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// daemonProbeAgent stands in for the local agent chain and records every
// prompt it is asked to execute. A healthy-daemon run must never reach it:
// silence here proves the command routed through the remote host instead of
// a fresh in-process one.
type daemonProbeAgent struct {
	mu      sync.Mutex
	prompts []string
}

func (a *daemonProbeAgent) ObtenerMensajeCommit([]string, string, int) (string, error) {
	return "", nil
}

func (a *daemonProbeAgent) EjecutarPrompt(prompt string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.prompts = append(a.prompts, prompt)
	return "probe output", nil
}

func (a *daemonProbeAgent) recorded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.prompts...)
}

// daemonSideAdapter is the adapter behind the test daemon's controller. It
// records the raw admission prompts that reach the SERVER side: exactly one
// entry carrying the operator prompt is the unique marker that `runs start`
// admitted through the daemon rather than locally.
type daemonSideAdapter struct {
	mu      sync.Mutex
	prompts []string
}

func (a *daemonSideAdapter) Execute(_ context.Context, job agentrun.LogicalJob, _ agentrun.InvocationEnvelope, _ string) (execution.AdapterResult, error) {
	a.mu.Lock()
	a.prompts = append(a.prompts, string(job.Request().Prompt()))
	a.mu.Unlock()
	return execution.AdapterResult{Output: "daemon-side"}, nil
}

func (a *daemonSideAdapter) recorded() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.prompts...)
}

var (
	_ agentadapterPrompt = (*daemonProbeAgent)(nil)
)

// agentadapterPrompt keeps the fake bound to the prompt seam without
// importing agentadapter for a single assertion.
type agentadapterPrompt interface {
	EjecutarPrompt(string) (string, error)
}

// startRunsTestDaemon binds the platform default endpoint for the worktree's
// real daemon directory, serves a controller over the shared common-dir
// store, and persists endpoint.json so runs-command discovery resolves it.
// It skips when the platform cannot provide its default transport.
func startRunsTestDaemon(t *testing.T, worktree string) *daemonSideAdapter {
	t.Helper()
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	dir := daemon.Dir(commonDir)
	ep := daemon.DefaultEndpoint(dir)
	listener, err := daemon.Listen(ep)
	if err != nil {
		if ep.Network == "unix" {
			t.Skipf("unix domain sockets unavailable: %v", err)
		}
		t.Fatalf("listen %s endpoint: %v", ep.Network, err)
	}
	adapter := &daemonSideAdapter{}
	srv := daemon.NewServer(execution.NewController(store.NuevoStore(commonDir), adapter), daemon.FingerprintRepository(commonDir))
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(srv.Close)
	t.Cleanup(func() { _ = listener.Close() })

	dialable := ep
	if ep.Network == "tcp" {
		dialable.Address = listener.Addr().String()
	}
	owner := daemon.Owner{
		PID: os.Getpid(), StartedAt: time.Now(),
		Host: "preference-test", ProtocolRevision: daemon.ProtocolRevision,
	}
	if err := daemon.SaveEndpoint(dir, dialable, owner); err != nil {
		t.Fatalf("persist endpoint.json: %v", err)
	}
	return adapter
}

func endpointRecordPath(t *testing.T, worktree string) string {
	t.Helper()
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(daemon.Dir(commonDir), "endpoint.json")
}

// TestRunsStartPrefersAHealthyDaemonEndpoint proves the preference with a
// unique marker: against a live daemon announcing endpoint.json, `runs start`
// admits through the REMOTE host — the operator prompt reaches the server's
// adapter and never the local agent — and the durable request record carries
// exactly the canonical identities of that prompt.
func TestRunsStartPrefersAHealthyDaemonEndpoint(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	localAgent := &daemonProbeAgent{}
	replaceRunsAgent(t, localAgent)
	serverAdapter := startRunsTestDaemon(t, worktree)

	var out bytes.Buffer
	const markerPrompt = "daemon-preference-marker"
	if code := executeRunsStart(&out, worktree, []string{"--prompt", markerPrompt, "--json"}); code != runExitSuccess {
		t.Fatalf("executeRunsStart exit = %d, output:\n%s", code, out.String())
	}
	decoded := decodeRunsJSON(t, out.String())
	runID, _ := decoded["run_id"].(string)
	if runID == "" {
		t.Fatalf("start output lost its run id:\n%s", out.String())
	}

	if got := localAgent.recorded(); len(got) != 0 {
		t.Fatalf("local agent executed %d prompts (%q), want silence: the run must be admitted remotely", len(got), got)
	}
	// Adapter execution is asynchronous on the daemon side, so poll bounded
	// instead of asserting immediately after admission returns.
	deadline := time.Now().Add(2 * time.Second)
	var got []string
	for {
		got = serverAdapter.recorded()
		if len(got) == 1 && got[0] == markerPrompt {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server-side adapter recorded %d prompt(s) %q within the deadline, want exactly the operator prompt %q", len(got), got, markerPrompt)
		}
		time.Sleep(5 * time.Millisecond)
	}

	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	durable, err := store.NuevoStore(commonDir).ReadExecutionRequest(runID)
	if err != nil {
		t.Fatal(err)
	}
	wantPromptID := string(agentrun.PromptIdentity(agentrun.Prompt(markerPrompt)))
	if durable.PromptID != wantPromptID {
		t.Fatalf("durable prompt identity = %s, want %s (identity must survive transport)", durable.PromptID, wantPromptID)
	}
}

// TestRunsStartWithoutEndpointRecordKeepsTodayBehavior pins the fallback:
// with no endpoint.json on disk everything behaves byte-identically to the
// pre-daemon flow — success exit code and the local agent doing the work.
func TestRunsStartWithoutEndpointRecordKeepsTodayBehavior(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	localAgent := &daemonProbeAgent{}
	replaceRunsAgent(t, localAgent)

	var out bytes.Buffer
	if code := executeRunsStart(&out, worktree, []string{"--prompt", "in-process-marker", "--json"}); code != runExitSuccess {
		t.Fatalf("executeRunsStart exit = %d, output:\n%s", code, out.String())
	}
	got := localAgent.recorded()
	if len(got) != 1 || !strings.Contains(got[0], "in-process-marker") {
		t.Fatalf("local agent saw %q, want exactly the in-process admission", got)
	}
}

// TestRunsStartWithCorruptEndpointRecordFallsBackSilently pins that ANY
// discovery failure — here an unreadable endpoint.json — degrades into the
// clean in-process path without ever surfacing a daemon error to the
// operator.
func TestRunsStartWithCorruptEndpointRecordFallsBackSilently(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	localAgent := &daemonProbeAgent{}
	replaceRunsAgent(t, localAgent)

	recordPath := endpointRecordPath(t, worktree)
	if err := os.MkdirAll(filepath.Dir(recordPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath, []byte("{not json at all"), 0600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if code := executeRunsStart(&out, worktree, []string{"--prompt", "fallback-marker", "--json"}); code != runExitSuccess {
		t.Fatalf("executeRunsStart exit = %d, output:\n%s", code, out.String())
	}
	if strings.Contains(out.String(), "daemon") {
		t.Fatalf("fallback leaked daemon diagnostics:\n%s", out.String())
	}
	got := localAgent.recorded()
	if len(got) != 1 || !strings.Contains(got[0], "fallback-marker") {
		t.Fatalf("local agent saw %q, want the silent in-process fallback", got)
	}
}
