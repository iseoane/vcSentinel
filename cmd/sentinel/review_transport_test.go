package main

import (
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
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

func (a *fakeRestrictedAgent) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.EjecutarRevision(prompt, sha, paths)
}

func (*fakeRestrictedAgent) ReviewToolPolicy() reviewcontract.ToolPolicy {
	return reviewcontract.DefaultToolPolicy()
}

// agentWithoutRevision satisfies AuditorAgente only: it lacks the restricted
// reviewer capability the durable transport requires.
type agentWithoutRevision struct{}

func (agentWithoutRevision) EjecutarPrompt(string) (string, error) { return "", nil }

type policyRecordingRestrictedAgent struct {
	policy reviewcontract.ToolPolicy
	calls  int
}

func (a *policyRecordingRestrictedAgent) EjecutarPrompt(string) (string, error) {
	return `{"dim":"logic","verdict":"ok"}`, nil
}

func (a *policyRecordingRestrictedAgent) EjecutarRevision(string, string, []string) (string, error) {
	return `{"dim":"logic","verdict":"ok"}`, nil
}

func (a *policyRecordingRestrictedAgent) ReviewWithPolicy(_ string, _ string, _ []string, policy reviewcontract.ToolPolicy) (string, error) {
	a.policy = policy
	a.calls++
	return `{"dim":"logic","verdict":"ok"}`, nil
}

func TestDurableReviewTransportForwardsResolvedToolPolicy(t *testing.T) {
	cfg := config.Config{}
	cfg.Review.EvidenceAdmission = true
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	agent := &policyRecordingRestrictedAgent{}
	contract, err := reviewcontract.Lookup(reviewcontract.DimensionLogic)
	if err != nil {
		t.Fatal(err)
	}

	result := review.AuditarCommit(func(review.ReviewBundle, string) (review.AuditorAgente, string, error) {
		return agent, "normal", nil
	}, 1, review.OpcionesAuditoria{
		SHA:             "sha-policy",
		Bundles:         []review.ReviewBundle{{Name: "quality", Dimensions: []string{review.DimLogic}, Priority: review.PriorityRequired, Cost: 1}},
		ReviewTransport: durableReviewTransport(cfg, worktree, "sha-policy", []string{"x.go"}),
	})

	if result.Veredicto != review.VerdictOK || agent.calls != 1 || agent.policy != contract.ToolPolicy {
		t.Fatalf("result=%+v calls=%d policy=%#v, want one durable policy-aware call with %#v", result, agent.calls, agent.policy, contract.ToolPolicy)
	}
}

// TestDurableReviewTransportFailsHonestWithoutGitDir pins the ticket 13
// (R11) cutover completion: with no git common dir there is no legacy path to
// degrade to, so the factory still returns a non-nil transport whose calls
// fail with an explicit unavailability error each dimension surfaces as
// unavailable evidence.
func TestDurableReviewTransportFailsHonestWithoutGitDir(t *testing.T) {
	cfg := config.Config{}
	cfg.Review.EvidenceAdmission = true
	transport := durableReviewTransport(cfg, t.TempDir(), "sha", nil)
	if transport == nil {
		t.Fatal("transport = nil, want an always-failing honest transport")
	}

	output, invocation, err := transport("quality", "logic", "the prompt", &fakeRestrictedAgent{})
	if err == nil || !strings.Contains(err.Error(), "durable review transport unavailable") {
		t.Fatalf("err = %v, want an explicit unavailability error", err)
	}
	if output != "" || invocation != "" {
		t.Fatalf("output/invocation = %q/%q, want empty on the failed seam", output, invocation)
	}
}

func TestDurableReviewTransportRoutesThroughRealStore(t *testing.T) {
	cfg := config.Config{}
	// Ticket 07: production defaults admission on (config default), so the
	// strict wiring test pins it explicitly instead of relying on the zero
	// value of a hand-built Config.
	cfg.Review.EvidenceAdmission = true
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	transport := durableReviewTransport(cfg, worktree, "sha-abc", []string{"x.go"})
	agent := &fakeRestrictedAgent{response: `{"dim":"logic","verdict":"ok"}`}

	output, invocation, err := transport("quality", "logic", "the prompt", agent)
	if err != nil {
		t.Fatalf("transport() error = %v", err)
	}
	want := "the prompt|{\"dim\":\"logic\",\"verdict\":\"ok\"}"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
	if invocation == "" {
		t.Fatal("invocation = empty, want the durable evidence identity of the admitted run")
	}
	if _, _, err := transport("quality", "logic", "the prompt again", agent); err != nil {
		t.Fatalf("second routed call error = %v, want salted candidate to prevent collision", err)
	}
}

func TestDurableReviewTransportRejectsNonRestrictedAgent(t *testing.T) {
	cfg := config.Config{}
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	transport := durableReviewTransport(cfg, worktree, "sha-def", nil)
	if transport == nil {
		t.Fatal("transport = nil, want wired")
	}
	soloPrompt := agentWithoutRevision{}

	_, _, err := transport("quality", "logic", "prompt", soloPrompt)
	if err == nil || !strings.Contains(err.Error(), "restricted reviewer capability") {
		t.Fatalf("err = %v, want restricted-capability requirement preserved", err)
	}
}

// TestDurableReviewTransportLenientWhenAdmissionDisabled proves the ticket 07
// cutover wiring: with review.evidence_admission=false the durable transport
// still routes but admits output unverified with an
// empty invocation identity, exactly like the pre-R6 closure.
func TestDurableReviewTransportLenientWhenAdmissionDisabled(t *testing.T) {
	cfg := config.Config{}
	cfg.Review.EvidenceAdmission = false
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	transport := durableReviewTransport(cfg, worktree, "sha-lenient", []string{"x.go"})
	if transport == nil {
		t.Fatal("transport = nil, want wired when durable runs stays enabled")
	}
	agent := &fakeRestrictedAgent{response: `{"dim":"logic","verdict":"ok"}`}

	output, invocation, err := transport("quality", "logic", "the prompt", agent)
	if err != nil {
		t.Fatalf("transport() error = %v, want lenient routing to succeed", err)
	}
	want := "the prompt|{\"dim\":\"logic\",\"verdict\":\"ok\"}"
	if output != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
	if invocation != "" {
		t.Fatalf("invocation = %q, want empty identity in lenient mode (zero Evidence)", invocation)
	}
}

func TestDurableReviewTransportSanitizesBoundPaths(t *testing.T) {
	cfg := config.Config{}
	cfg.Review.EvidenceAdmission = true
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	transport := durableReviewTransport(cfg, worktree, "sha-jda1", []string{
		"ok.go", "-flag.txt", "../evil.go", "/abs/x.go", "win\\sub.go", "bad\nline.go", "..", "c:\\drive.go",
	})
	if transport == nil {
		t.Fatal("transport = nil, want wired")
	}
	agent := &fakeRestrictedAgent{response: `{"dim":"logic","verdict":"ok"}`}

	if _, _, err := transport("quality", "logic", "prompt", agent); err != nil {
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
