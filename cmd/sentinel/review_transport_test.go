package main

import (
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// initGitRepo makes the temp directory a real repository so
// git.ObtenerGitCommonDir resolves during transport wiring.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v: %s", dir, err, out)
	}
}

type fakeRestrictedAgent struct {
	response string
	mu       sync.Mutex
	gotPaths []string
}

func (a *fakeRestrictedAgent) EjecutarPrompt(string) (string, error) { return a.response, nil }

func (a *fakeRestrictedAgent) EjecutarRevision(prompt, _ string, paths []string) (string, error) {
	a.mu.Lock()
	a.gotPaths = append([]string(nil), paths...)
	a.mu.Unlock()
	return prompt + "|" + a.response, nil
}

// agentWithoutRevision satisfies AuditorAgente only: it lacks the restricted
// reviewer capability the durable transport requires.
type agentWithoutRevision struct{}

func (agentWithoutRevision) EjecutarPrompt(string) (string, error) { return "", nil }

func TestDurableReviewTransportDisabledReturnsNil(t *testing.T) {
	cfg := config.Config{}
	cfg.Review.DurableRuns = false
	if transport := durableReviewTransport(cfg, t.TempDir(), "sha", nil); transport != nil {
		t.Fatal("transport = non-nil, want nil when review.durable_runs is disabled")
	}
}

func TestDurableReviewTransportRoutesThroughRealStore(t *testing.T) {
	cfg := config.Config{}
	cfg.Review.DurableRuns = true
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	transport := durableReviewTransport(cfg, worktree, "sha-abc", []string{"x.go"})
	if transport == nil {
		t.Fatal("transport = nil, want wired when review.durable_runs is enabled")
	}
	agent := &fakeRestrictedAgent{response: `{"dim":"logic","verdict":"ok"}`}

	output, err := transport("quality", "logic", "the prompt", agent)
	if err != nil {
		t.Fatalf("transport() error = %v", err)
	}
	want := "the prompt|{\"dim\":\"logic\",\"verdict\":\"ok\"}"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
	if _, err := transport("quality", "logic", "the prompt again", agent); err != nil {
		t.Fatalf("second routed call error = %v, want salted candidate to prevent collision", err)
	}
}

func TestDurableReviewTransportRejectsNonRestrictedAgent(t *testing.T) {
	cfg := config.Config{}
	cfg.Review.DurableRuns = true
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	transport := durableReviewTransport(cfg, worktree, "sha-def", nil)
	if transport == nil {
		t.Fatal("transport = nil, want wired")
	}
	soloPrompt := agentWithoutRevision{}

	_, err := transport("quality", "logic", "prompt", soloPrompt)
	if err == nil || !strings.Contains(err.Error(), "restricted reviewer capability") {
		t.Fatalf("err = %v, want restricted-capability requirement preserved", err)
	}
}

func TestDurableReviewTransportSanitizesBoundPaths(t *testing.T) {
	cfg := config.Config{}
	cfg.Review.DurableRuns = true
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	transport := durableReviewTransport(cfg, worktree, "sha-jda1", []string{
		"ok.go", "-flag.txt", "../evil.go", "/abs/x.go", "win\\sub.go", "bad\nline.go", "..", "c:\\drive.go",
	})
	if transport == nil {
		t.Fatal("transport = nil, want wired")
	}
	agent := &fakeRestrictedAgent{response: `{"dim":"logic","verdict":"ok"}`}

	if _, err := transport("quality", "logic", "prompt", agent); err != nil {
		t.Fatalf("transport() error = %v", err)
	}
	agent.mu.Lock()
	got := agent.gotPaths
	agent.mu.Unlock()
	want := []string{"ok.go", "win/sub.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reviewer paths = %q, want sanitized %q (JD-A1: dash, parent-relative, absolute, drive-letter, and control-character names must be dropped or normalized)", got, want)
	}
}
