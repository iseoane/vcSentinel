//go:build linux

package reviewexec

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/config"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/process"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// These proofs cover the full slice-3 chain with real configuration parsing:
// a project vcsentinel.yml sets review.cancellation_escalation, the config
// loader honors it, the production wiring shape turns it into an
// EscalationPolicy, and the transport's worker context carries exactly the
// Disabled semantics proven at controller level by ticket 08 slice 2: no
// whole-tree signal capability armed, direct-child exec kill switch active.
// Linux-only like the rest of the process-tree integration coverage.

func requireLinuxConfigIntegration(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("cancellation escalation wiring integration requires Linux")
	}
}

// writeProjectConfig writes a minimal project vcsentinel.yml into worktree.
func writeProjectConfig(t *testing.T, worktree, yamlBody string) {
	t.Helper()
	directory := filepath.Join(worktree, ".vcsentinel")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "vcsentinel.yml")
	if err := os.WriteFile(path, []byte(yamlBody), 0600); err != nil {
		t.Fatal(err)
	}
}

// probeWaitForFile polls until path exists and is non-empty. It returns an
// error instead of failing the test, so the caller can register survivor
// containment before any failure path runs.
func probeWaitForFile(path string) (string, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return strings.TrimSpace(string(data)), nil
		}
		if time.Now().After(deadline) {
			return "", err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// probePidAlive reports whether a process with that pid still exists.
func probePidAlive(pid int) bool {
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return err == nil
}

// treeProbeReviewer observes the worker context the transport stamps from its
// escalation policy and proves the direct-child kill switch with real
// processes: a TERM-trapping shell child dies when a derived context cancels,
// while its sleeping grandchild survives untouched.
type treeProbeReviewer struct {
	t              *testing.T
	gpidFile       string
	directChildPid int
	grandchildPid  int
	waited         bool
	wholeTreeArmed bool
}

func (r *treeProbeReviewer) RunReview(prompt, _ string, _ []string) (string, error) {
	return prompt + "|legacy", nil
}

func (r *treeProbeReviewer) ReviewWithContext(ctx context.Context, prompt, _ string, _ []string) (string, error) {
	r.wholeTreeArmed = process.WholeTreeTermination(ctx)

	childContext, cancelChild := context.WithCancel(ctx)
	defer cancelChild()
	script := `trap '' TERM
sleep 30 & printf %s $! > ` + r.gpidFile + `
wait`
	cmd := exec.CommandContext(childContext, "sh", "-c", script)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	r.directChildPid = cmd.Process.Pid
	raw, waitErr := probeWaitForFile(r.gpidFile)
	if waitErr != nil {
		return "", waitErr
	}
	gpid, parseErr := strconv.Atoi(raw)
	if parseErr != nil {
		return "", parseErr
	}
	// Containment first: the moment the survivor's identity is known, its
	// cleanup is registered — before any later assertion can fail and leave
	// the sleep 30 grandchild running unowned.
	grandchildPid := gpid
	r.t.Cleanup(func() {
		if proc, findErr := os.FindProcess(grandchildPid); findErr == nil {
			_ = proc.Kill()
		}
	})
	r.grandchildPid = gpid

	// The stamped-policy kill switch: canceling a derived context must kill
	// the direct child even though it traps-and-ignores TERM.
	cancelChild()
	_ = cmd.Wait() // any exit — including a signal death from the kill switch — confirms the child is gone
	r.waited = true
	return prompt + "|ok", nil
}

func TestCancellationEscalationFalseFromYamlKeepsDirectChildOnlyKill(t *testing.T) {
	requireLinuxConfigIntegration(t)

	home := t.TempDir()
	worktree := t.TempDir()
	backingDir := t.TempDir()
	t.Setenv("HOME", home)
	writeProjectConfig(t, worktree, "version: \"2.0\"\nreview:\n  cancellation_escalation: false\n")

	cfg, err := config.LoadStrictLocalConfig(worktree)
	if err != nil {
		t.Fatalf("strict config load = %v", err)
	}
	if cfg.Review.CancellationEscalation {
		t.Fatalf("config = {CancellationEscalation:%t}, want false honored from project yaml",
			cfg.Review.CancellationEscalation)
	}

	reviewer := &treeProbeReviewer{t: t, gpidFile: filepath.Join(backingDir, "grandchild-pid")}
	backing := store.NewStore(backingDir)
	transport := NewDurableTransport(backing, store.RunPolicy{ID: "policy:review"}, "sha-abc123", nil,
		WithEvidenceAdmission(cfg.Review.EvidenceAdmission),
		WithCancellationEscalation(execution.EscalationPolicy{Disabled: !cfg.Review.CancellationEscalation}))

	output, evidence, runErr := transport.Run(reviewer, "bundle/logic", "the prompt")
	if runErr != nil {
		t.Fatalf("Run() error = %v; cooperative completion must keep working in disabled mode", runErr)
	}
	if output != "the prompt|ok" || evidence.InvocationID == "" {
		t.Fatalf("output/evidence = %q/%+v, want admitted output with durable invocation identity", output, evidence)
	}
	if reviewer.wholeTreeArmed {
		t.Fatal("worker context armed whole-tree termination; cancellation_escalation=false must disarm it end to end")
	}
	if !reviewer.waited || probePidAlive(reviewer.directChildPid) {
		t.Fatalf("direct child %d survived; the exec kill switch stamped by the Disabled policy must stay active",
			reviewer.directChildPid)
	}
	if reviewer.grandchildPid == 0 || !probePidAlive(reviewer.grandchildPid) {
		t.Fatalf("grandchild %d died under disabled escalation; that proves an illegal whole-tree signal", reviewer.grandchildPid)
	}
	// Survivor containment is already registered inside the reviewer, the
	// moment the grandchild pid became known — no failure above can leak it.

	page, eventsErr := backing.ReadEvents(evidence.RunID, 0, 128)
	if eventsErr != nil {
		t.Fatal(eventsErr)
	}
	for _, frame := range page.Events {
		if frame.To == agentrun.StateTerminating || frame.To == agentrun.StateTerminated {
			t.Fatalf("disabled-mode run appended escalation frame %s->%s; zero escalation frames expected", frame.From, frame.To)
		}
	}
}

// TestCancellationEscalationDefaultStaysArmed is the positive control of the
// same wiring chain: with no key in yaml the default keeps whole-tree
// capability armed on every worker context the transport builds.
func TestCancellationEscalationDefaultStaysArmed(t *testing.T) {
	requireLinuxConfigIntegration(t)

	home := t.TempDir()
	worktree := t.TempDir()
	backingDir := t.TempDir()
	t.Setenv("HOME", home)
	writeProjectConfig(t, worktree, "version: \"2.0\"\n")

	cfg, err := config.LoadStrictLocalConfig(worktree)
	if err != nil {
		t.Fatalf("strict config load = %v", err)
	}
	if !cfg.Review.CancellationEscalation {
		t.Fatal("absent review.cancellation_escalation must default to true")
	}

	reviewer := &treeProbeReviewer{t: t, gpidFile: filepath.Join(backingDir, "grandchild-pid")}
	transport := NewDurableTransport(store.NewStore(backingDir), store.RunPolicy{ID: "policy:review"}, "sha-def456", nil,
		WithEvidenceAdmission(cfg.Review.EvidenceAdmission),
		WithCancellationEscalation(execution.EscalationPolicy{Disabled: !cfg.Review.CancellationEscalation}))

	if _, evidence, runErr := transport.Run(reviewer, "bundle/logic", "the prompt"); runErr != nil {
		t.Fatalf("Run() error = %v", runErr)
	} else if evidence.InvocationID == "" {
		t.Fatal("default-enabled run must still admit output with durable invocation identity")
	}
	if !reviewer.wholeTreeArmed {
		t.Fatal("default configuration left whole-tree termination disarmed; escalation capability must stay armed")
	}
	// Survivor containment is already registered inside the reviewer, the
	// moment the grandchild pid became known.
}
