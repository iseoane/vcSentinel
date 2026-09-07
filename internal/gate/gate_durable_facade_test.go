// Facade harness and compatibility pins for the real durable gate
// orchestration (R9 slice 2, sole execution path since ticket 13 R11). This
// file owns the byte-level facade contract: the facade table pins the exact
// State/Messages shapes per terminal class (including the refuted-CRITICAL
// NEEDS_USER_REVIEW case), and the infrastructure text pins fix the exact
// strings that have no pre-cutover equivalent to compare against. The CLI
// contract (stdout text, exit codes) is produced from Result
// State/Messages by cmd/sentinel, so these tests pin the facade at its
// source.
package gate

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewexec"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// countedTransportFactory mirrors the production construction path of
// `sentinel review` (durableReviewTransport in cmd/sentinel): a REAL
// reviewexec.DurableTransport over a fresh store per audit, with the gate's
// root run ID threaded into its run policy as the persisted parent linkage.
// The counter seam records how often the factory itself was invoked — it must
// be zero whenever validation failed.
func countedTransportFactory(t *testing.T, sha string, invocations *int) func(agentrun.Identity) review.ReviewTransport {
	t.Helper()
	return func(rootRunID agentrun.Identity) review.ReviewTransport {
		*invocations++
		backing := store.NewStore(filepath.Join(t.TempDir(), "review-common"))
		transport := reviewexec.NewDurableTransport(backing, store.RunPolicy{ID: "policy:test-gate-review", ParentRunID: string(rootRunID)}, sha, nil)
		return func(bundleName, dimension, prompt string, agent review.AgentReviewer) (string, string, error) {
			restricted, ok := agent.(reviewexec.RestrictedReviewer)
			if !ok {
				return "", "", review.ErrRestrictedRequired
			}
			output, evidence, err := transport.Run(restricted, bundleName+"/"+dimension, prompt)
			if err != nil {
				return "", "", err
			}
			return output, evidence.InvocationID, nil
		}
	}
}

// durableOptions overrides an already-wired base Options set with a fresh
// temp-dir store and a counted transport factory, so tests can assert on
// factory invocations against fixtures built by baseOptions.
func durableOptions(t *testing.T, base Options, transports *int) Options {
	t.Helper()
	durable := base
	durable.Stage = "pre-push"
	durable.CandidateSHA = base.ReviewOptions.SHA
	durable.DurableStore = store.NewStore(filepath.Join(t.TempDir(), "gate-common"))
	durable.DurableReviewTransportFactory = countedTransportFactory(t, base.ReviewOptions.SHA, transports)
	return durable
}

// TestGateFacadePinsTerminalClasses pins the compatibility facade table:
// success, multi-command validation failure, review block (confirmed and
// refuted CRITICAL), and infrastructure shapes. These literals are what the
// removed legacy orchestration produced for equivalent inputs; the durable
// orchestration must keep rendering them byte-for-byte through the shared
// helpers (validationNotRunMessage, validationFailedMessages,
// translateVerdict).
func TestGateFacadePinsTerminalClasses(t *testing.T) {
	blockJSON := `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"confirmable risk"}]}`
	refutationNegative := `{"refuted":false,"reason":"the risk remains"}`

	cases := []struct {
		name string
		base func(t *testing.T) Options
		// state is the expected terminal state. When exact is true the
		// joined messages must equal the pin byte-for-byte; otherwise each
		// entry must be contained (used when the facade embeds generated
		// finding identities). Every fixture audits exactly one dimension,
		// so the summary line is deterministic.
		state    string
		exact    bool
		messages []string
	}{
		{
			name: "success renders the PASS facade",
			base: func(t *testing.T) Options {
				calls := 0
				opts := baseOptions(t, cfgWithProfile("lint", "echo ok"), selectedExecutor(nil), countingFactory(&calls, `{"dim":"logic","verdict":"ok","findings":[]}`, nil))
				opts.RunValidation = runProfileWithoutCandidate
				return opts
			},
			state: StatePass,
			exact: true,
			messages: []string{
				"✅ Validation and semantic review green.",
				"\n🔎 Review of 01234567: ok\n  logic     ok          profile-test",
			},
		},
		{
			name: "multi-command validation failure renders the VALIDATION_FAILED facade",
			base: func(t *testing.T) Options {
				calls := 0
				opts := baseOptions(t, cfgWithTwoCapabilities(), selectedExecutor(map[string]validation.ValidationRun{
					"echo test": {Exit: 1, Output: "real output of the failed command"},
				}), countingFactory(&calls, "", nil))
				opts.RunValidation = runProfileWithoutCandidate
				return opts
			},
			state: StateValidationFailed,
			exact: true,
			messages: []string{
				"❌ Validation FAILED: semantic review not executed.",
				"  ✖ test (echo test):\nreal output of the failed command",
			},
		},
		{
			name: "confirmed CRITICAL review renders the CODE_REVIEW_FAILED facade",
			base: func(t *testing.T) Options {
				calls := 0
				opts := baseOptions(t, cfgWithProfile("lint", "echo ok"), selectedExecutor(nil), countingFactory(&calls, blockJSON, nil))
				opts.RunValidation = runProfileWithoutCandidate
				opts.RefuterFactory = func() (review.AgentReviewer, string, error) {
					return &fakeReviewer{output: refutationNegative}, "cheap", nil
				}
				return opts
			},
			state: StateCodeReviewFailed,
			messages: []string{
				"❌ Semantic review confirmed CRITICAL findings.",
				"🔎 Review of 01234567: block",
				"Confirmed CRITICAL finding evidence:",
				`    description: "confirmable risk"`,
			},
		},
		{
			name: "refuted CRITICAL renders the NEEDS_USER_REVIEW facade",
			base: func(t *testing.T) Options {
				calls := 0
				agent := &sequentialReviewer{answers: []string{blockJSON}, calls: &calls}
				factory := func(_ review.ReviewBundle, _ string) (review.AgentReviewer, string, error) {
					return agent, "profile-test", nil
				}
				opts := baseOptions(t, cfgWithProfile("lint", "echo ok"), selectedExecutor(nil), factory)
				opts.RunValidation = runProfileWithoutCandidate
				opts.RefuterFactory = func() (review.AgentReviewer, string, error) {
					return &fakeReviewer{output: `{"refuted":true,"reason":"the final code already handles this case","sha":"0123456789abcdef","file":"a.go","line_start":1,"line_end":1,"evidence":"final code handles this case"}`}, "cheap", nil
				}
				opts.ReviewOptions.ReadSnapshotContent = func(sha, file string) (string, error) {
					if sha != "0123456789abcdef" || file != "a.go" {
						t.Fatalf("snapshot read sha=%q file=%q", sha, file)
					}
					return "final code handles this case", nil
				}
				return opts
			},
			state: StateNeedsUserReview,
			exact: true,
			messages: []string{
				"❓ Semantic review refuted a CRITICAL finding and requires human attention.",
				"\n🔎 Review of 01234567: warn\n  logic     warn        profile-test",
			},
		},
		{
			name: "validation orchestration failure renders the infrastructure facade",
			base: func(t *testing.T) Options {
				calls := 0
				opts := baseOptions(t, cfgWithProfile("lint", "echo ok"), nil, countingFactory(&calls, "", nil))
				opts.RunValidation = func(profile string, scope []string, o validation.RunOptions) ([]validation.ValidationRun, error) {
					return nil, errAgentUnavailableTest
				}
				return opts
			},
			state: StateReviewInfrastructureError,
			exact: true,
			messages: []string{
				"Could not run validation: agent unavailable",
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			result := RunGate(testCase.base(t))
			if result.State != testCase.state {
				t.Fatalf("state = %q, expected %q (%v)", result.State, testCase.state, result.Messages)
			}
			joined := strings.Join(result.Messages, "\n")
			if testCase.exact {
				if want := strings.Join(testCase.messages, "\n"); joined != want {
					t.Fatalf("facade drifted:\n got  %q\n want %q", joined, want)
				}
				return
			}
			for _, fragment := range testCase.messages {
				if !strings.Contains(joined, fragment) {
					t.Fatalf("facade lost %q:\n got %q", fragment, joined)
				}
			}
		})
	}
}

// TestGateDurableInfrastructurePinsTexts fixes the exact infrastructure-only
// facade strings that have no green-path equivalent to pin elsewhere.
func TestGateDurableInfrastructurePinsTexts(t *testing.T) {
	t.Run("plan failure keeps the slice-1 prefix verbatim", func(t *testing.T) {
		opts := baseOptions(t, cfgWithProfile("lint", "echo ok"), nil, nil)
		opts.Stage = ""
		opts.CandidateSHA = "abc123def456"
		opts.DurableStore = store.NewStore(filepath.Join(t.TempDir(), "gate-common"))

		result := RunGate(opts)

		want := "gate durable run plan failed before wiring: gate: invalid durable run plan (stage): must be a non-empty lifecycle stage"
		if result.State != StateReviewInfrastructureError || result.Messages[0] != want {
			t.Fatalf("plan-failure facade drifted:\n got  %q / %q\n want %q", result.State, result.Messages[0], want)
		}
	})

	t.Run("missing store names the seam explicitly", func(t *testing.T) {
		opts := baseOptions(t, cfgWithProfile("lint", "echo ok"), nil, nil)
		opts.Stage = "pr"
		opts.CandidateSHA = "abc123def456"
		opts.DurableStore = nil

		result := RunGate(opts)

		want := "gate: durable runs require an injected store"
		if result.State != StateReviewInfrastructureError || result.Messages[0] != want {
			t.Fatalf("missing-store facade drifted:\n got  %q / %q\n want %q", result.State, result.Messages[0], want)
		}
	})
}
