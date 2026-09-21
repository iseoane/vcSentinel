package pr

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/config"
	"github.com/ISeoane-Quental/vcSentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vcSentinel/internal/ops"
	"github.com/ISeoane-Quental/vcSentinel/internal/review"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// repoRoot walks up from the package directory until it finds the repository
// root, so RunPrReviewWith can load the strict local config and resolve git
// directories exactly as production does.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root not found")
		}
		dir = parent
	}
}

const (
	netTestHead = "0123456789abcdef0123456789abcdef01234567"
	netTestFrom = "89abcdef0123456789abcdef0123456789abcdef"
)

func okNetDim(dim string) review.DimensionOutcome {
	return review.DimensionOutcome{
		Dim:     dim,
		Profile: "test",
		Result:  &review.DimensionResult{Dim: dim, Verdict: review.VerdictOK},
	}
}

func netBranchResult(dims []review.DimensionOutcome) *review.BranchResult {
	return &review.BranchResult{
		Branch:   "test-branch",
		SHAs:     []string{netTestHead},
		Volume:   10,
		Decision: "single",
		Net: &review.NetReview{
			From: netTestFrom,
			To:   netTestHead,
			Audit: review.AuditResult{
				SHA:     netTestHead,
				Verdict: review.VerdictUnavailable,
				Dims:    dims,
			},
		},
	}
}

func netReviewStubs(res *review.BranchResult, saved *store.PRReviewEntry) DepsPrReview {
	return DepsPrReview{
		AnalyzeBranch: func(_ *review.Ledger, _ review.BranchOptions) (*review.BranchResult, error) {
			return res, nil
		},
		RecordEvent: func(_, _ string, _ int, _ []string, _ ops.EventDetail, _ string) error {
			return nil
		},
		EventDetail: func(_ string, _ *review.BranchResult, _ bool) (ops.EventDetail, error) {
			return ops.EventDetail{}, nil
		},
		WriteEvidence: func(_, _ string, _ []review.EvidenceLog) ([]string, error) {
			return []string{"evidence"}, nil
		},
		SavePRReview: func(_ string, entry *store.PRReviewEntry) error {
			*saved = *entry
			return nil
		},
		CommitMessage: func(_ string) (string, error) {
			return "Test subject", nil
		},
	}
}

func netReviewWiring() Wiring {
	return Wiring{
		NewModelVerifier: func(_ string) *modelprobe.Verifier {
			return nil
		},
		SharedReviewLedger: func(_ string) (*review.Ledger, error) {
			return nil, nil
		},
		LoadDispositions: func(_ string) ([]review.FindingDisposition, error) {
			return nil, nil
		},
		TransportFactory: func(_ config.Config, _ string) func(string, []string) review.ReviewTransport {
			return nil
		},
		BranchOptionsWithRefuter: func(_ config.Config, _ *modelprobe.Verifier, opts review.BranchOptions) review.BranchOptions {
			return opts
		},
		ShortSHA: func(sha string) string {
			if len(sha) > 7 {
				return sha[:7]
			}
			return sha
		},
	}
}

// TestPrReviewConsoleNamesDeniedNetDimensionCause reproduces the reported
// case: a net audit where one dimension of four is unavailable from a denial.
// The console must name the dimension and its cause, not stop at a bare
// "Net audit verdict: unavailable".
func TestPrReviewConsoleNamesDeniedNetDimensionCause(t *testing.T) {
	const denial = "denied tool call: todowrite is not in the allowed tool policy"
	res := netBranchResult([]review.DimensionOutcome{
		okNetDim("logic"),
		okNetDim("style"),
		{
			Dim:     "tests",
			Profile: "test",
			Result: &review.DimensionResult{
				Dim:          "tests",
				Verdict:      review.VerdictUnavailable,
				Reason:       denial,
				InvocationID: "inv-denied-1",
			},
		},
		okNetDim("security"),
	})
	var saved store.PRReviewEntry
	var out, progress bytes.Buffer
	code := RunPrReviewWith(&out, &progress, repoRoot(t), FlagsPrReview{Base: "main"}, netReviewWiring(), netReviewStubs(res, &saved))
	if code != 0 {
		t.Fatalf("exit = %d, payload:\n%s\nprogress:\n%s", code, out.String(), progress.String())
	}
	console := out.String()
	if !strings.Contains(console, "Net audit verdict: unavailable") {
		t.Fatalf("console lacks the net verdict line:\n%s", console)
	}
	if !strings.Contains(console, "tests") || !strings.Contains(console, "todowrite") {
		t.Errorf("console names neither the dimension nor its cause (bare-verdict symptom):\n%s", console)
	}
	if strings.Contains(saved.Body, "todowrite") {
		t.Errorf("denial diagnostics leaked into the persisted published body")
	}
	output := PrReviewJSONOutput("main", res)
	causes, ok := output["net_unavailable_causes"]
	if !ok {
		t.Fatalf("json output leaves the net path reasonless (no net_unavailable_causes):\n%s", console)
	}
	encoded, err := json.Marshal(causes)
	if err != nil {
		t.Fatalf("net_unavailable_causes is not marshallable: %v", err)
	}
	if !strings.Contains(string(encoded), "tests") || !strings.Contains(string(encoded), "todowrite") {
		t.Errorf("json net causes name neither the dimension nor its cause: %s", string(encoded))
	}
}

// TestPrReviewConsoleNamesInvocationWhenNetCauseUnrecorded covers the
// empty-reason branch: with no recorded cause the console must name the
// invocation id and the manual command that reads it.
func TestPrReviewConsoleNamesInvocationWhenNetCauseUnrecorded(t *testing.T) {
	res := netBranchResult([]review.DimensionOutcome{
		okNetDim("logic"),
		okNetDim("style"),
		{
			Dim:     "design",
			Profile: "test",
			Result: &review.DimensionResult{
				Dim:          "design",
				Verdict:      review.VerdictUnavailable,
				InvocationID: "inv-unrecorded-7",
			},
		},
		okNetDim("security"),
	})
	var saved store.PRReviewEntry
	var out, progress bytes.Buffer
	code := RunPrReviewWith(&out, &progress, repoRoot(t), FlagsPrReview{Base: "main"}, netReviewWiring(), netReviewStubs(res, &saved))
	if code != 0 {
		t.Fatalf("exit = %d, payload:\n%s\nprogress:\n%s", code, out.String(), progress.String())
	}
	console := out.String()
	if !strings.Contains(console, "Net audit verdict: unavailable") {
		t.Fatalf("console lacks the net verdict line:\n%s", console)
	}
	if !strings.Contains(console, "inv-unrecorded-7") {
		t.Errorf("console does not name the invocation id:\n%s", console)
	}
	if !strings.Contains(console, "vcsentinel runs logs") {
		t.Errorf("console does not name the manual command that reads the invocation:\n%s", console)
	}
}
