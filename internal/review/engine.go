package review

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/acpadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/process"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
	"github.com/ISeoane-Quental/vas.sentinel/internal/risk"
)

// AgentReviewer is the interface the engine uses to talk to the agent.
// CLIAdapter implements it; tests inject doubles.
type AgentReviewer interface {
	RunPrompt(prompt string) (string, error)
}

type restrictedToolReviewer interface {
	RunReview(prompt, sha string, paths []string) (string, error)
}

type policyAwareReviewer interface {
	ReviewWithPolicy(prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error)
}

type contextualPolicyAwareReviewer interface {
	ReviewWithContextAndPolicy(ctx context.Context, prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error)
}

// policyBoundReviewer carries the resolved dimension policy through a
// transport that predates the contract registry. It deliberately exposes the
// same policy-aware method as providers so the durable adapter can require and
// invoke it without importing this package.
type policyBoundReviewer struct {
	AgentReviewer
	policy reviewcontract.ToolPolicy
}

func bindPolicy(agent AgentReviewer, policy reviewcontract.ToolPolicy) policyBoundReviewer {
	return policyBoundReviewer{AgentReviewer: agent, policy: policy}
}

func (a policyBoundReviewer) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	reviewer, ok := a.AgentReviewer.(policyAwareReviewer)
	if !ok {
		return "", missingRestrictedCapability(a.AgentReviewer)
	}
	return reviewer.ReviewWithPolicy(prompt, sha, paths, a.policy)
}

// missingRestrictedCapability names the adapter that cannot review. Without
// this the rejection was the bare "restricted reviewer capability is required",
// repeated once per dimension: six identical messages that never say which
// adapter failed, why, or what to do. The real case is configuring
// `kind: acpx`, whose adapter implements neither ReviewWithPolicy nor any
// tool-permission surface.
func missingRestrictedCapability(agent AgentReviewer) error {
	return fmt.Errorf("%w: the configured agent %T cannot review under a tool policy; configure a CLI agent (claude or opencode) for review", ErrRestrictedRequired, agent)
}

func (a policyBoundReviewer) RunReview(prompt, sha string, paths []string) (string, error) {
	return a.ReviewWithPolicy(prompt, sha, paths, a.policy)
}

func (a policyBoundReviewer) ReviewWithContextAndPolicy(ctx context.Context, prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	if reviewer, ok := a.AgentReviewer.(contextualPolicyAwareReviewer); ok {
		return reviewer.ReviewWithContextAndPolicy(ctx, prompt, sha, paths, a.policy)
	}
	return a.ReviewWithPolicy(prompt, sha, paths, a.policy)
}

// ReviewWithContextAndPolicyResult forwards a rich result through the policy
// binding. It only ever reaches a method that carries the resolved policy: the
// policy-free rich seam would silently drop the tool restrictions the bundle
// resolved, and with them the ErrRestrictedRequired gate that refuses an agent
// which cannot review under a policy at all. Reviewers without a policy-aware
// rich method fall back to the enforcing string path.
func (a policyBoundReviewer) ReviewWithContextAndPolicyResult(ctx context.Context, prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (acpadapter.Result, error) {
	if reviewer, ok := a.AgentReviewer.(resultPolicyAwareReviewer); ok {
		return reviewer.ReviewWithContextAndPolicyResult(ctx, prompt, sha, paths, a.policy)
	}
	output, err := a.ReviewWithContextAndPolicy(ctx, prompt, sha, paths, a.policy)
	return acpadapter.Result{Output: output}, err
}

// resultPolicyAwareReviewer is the rich reviewer seam that accepts the
// resolved tool policy. It is deliberately the only rich method the policy
// binding will call.
type resultPolicyAwareReviewer interface {
	ReviewWithContextAndPolicyResult(ctx context.Context, prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (acpadapter.Result, error)
}

func (a policyBoundReviewer) ReviewToolPolicy() reviewcontract.ToolPolicy { return a.policy }

// EffectiveAgent forwards the wrapped agent's effective-responder report so
// per-finding producer stamping keeps working through the policy binding the
// durable transport requires.
func (a policyBoundReviewer) EffectiveAgent() (agentadapter.EffectiveAgent, bool) {
	reporter, ok := a.AgentReviewer.(agentadapter.ReportsEffectiveAgent)
	if !ok {
		return agentadapter.EffectiveAgent{}, false
	}
	return reporter.EffectiveAgent()
}

// OwnedTree forwards the wrapped reviewer's live process tree so durable
// cancellation can escalate against the whole tree despite policy binding:
// hiding this capability would silently degrade aborts to cooperative-only.
func (a policyBoundReviewer) OwnedTree() *process.Tree {
	if provider, ok := a.AgentReviewer.(interface {
		OwnedTree() *process.Tree
	}); ok {
		return provider.OwnedTree()
	}
	return nil
}

// ReviewerFactory builds the agent for a bundle and a dimension, and also
// returns the name of the applied profile. Injectable in tests.
type ReviewerFactory func(bundle ReviewBundle, dimension string) (AgentReviewer, string, error)

// RefuterFactory constructs the cheap, independent reviewer used to challenge
// one semantic CRITICAL finding. It is invoked once for each finding.
type RefuterFactory func() (AgentReviewer, string, error)

// ErrRestrictedRequired is returned when a dimension's agent lacks the
// tool-restricted reviewer capability. Engine and durable transport share one
// exported wording so evidence can never drift between the two paths.
var ErrRestrictedRequired = errors.New("semantic review unavailable: restricted reviewer capability is required")

// ReviewTransport routes one dimension reviewer call through an alternative
// execution path such as the durable run controller. bundleName plus dimension
// identify the logical job; prompt is fully built by the engine so parsing
// stays shared after either path. The first result is the raw reviewer output;
// the second is the producing invocation identity reported by the transport
// (ticket 07 slice 2b): empty when the transport cannot attribute the call.
type ReviewTransport func(bundleName, dimension, prompt string, agent AgentReviewer) (string, string, error)

// ReviewEvidence carries the durable identities of one physical reviewer
// invocation. The legacy ReviewTransport callback still returns only the
// invocation ID; this richer seam lets semantic finalization reach the real
// durable run without deriving a run ID.
type ReviewEvidence struct {
	RunID        string
	InvocationID string
}

// ReviewTransportWithEvidence is the durable transport seam. It preserves
// evidence identities even when the provider returns a terminal error after
// producing partial output.
type ReviewTransportWithEvidence func(bundleName, dimension, prompt string, agent AgentReviewer) (string, ReviewEvidence, error)

// MetricsFinalizer is called once for every physically admitted durable review
// invocation, after its semantic disposition is known. Empty failureClass
// means the invocation completed without a semantic failure.
type MetricsFinalizer func(runID, invocationID, failureClass, detail string) error

// AuditOptions defines one audit job over a commit.
type AuditOptions struct {
	SHA                            string
	Message                        string
	Diff                           string
	Bundles                        []ReviewBundle
	Budget                         ReviewBudget
	Answers                        string           // --answer: user clarifications (one extra round)
	ProfileOverride                string           // --profile: forces a profile over the map
	OnDimension                    func(dim string) // optional: notified when each dimension starts
	ContextProvider                ContextProvider
	ContextPaths                   []string
	RefuterFactory                 RefuterFactory
	ReadSnapshotContent            SnapshotReader
	DescriptionSimilarityThreshold float64
	// DeterministicFindings are already-projected review.Finding{Source:
	// SourceValidation} findings (e.g. from a failed lint/build/test command)
	// that supersede an equivalent semantic finding in the same location
	// (T6.2). Empty by default: the caller decides when both sources should
	// coexist in the same report.
	DeterministicFindings []Finding
	// ReviewTransport, when set, routes each dimension's reviewer call through
	// an alternative execution path such as the durable run controller.
	// Production wiring always supplies the admitted durable transport;
	// nil remains only as the engine-level injection seam for direct fixtures.
	ReviewTransport ReviewTransport
	// ReviewTransportWithEvidence is the preferred durable seam. ReviewTransport
	// remains supported for direct fixtures and historical callers.
	ReviewTransportWithEvidence ReviewTransportWithEvidence
	// FinalizeMetrics is the owner-side callback for one physical run's final
	// semantic disposition. It is deliberately provider-neutral to avoid a
	// review-to-store import cycle.
	FinalizeMetrics MetricsFinalizer
	// Dispositions carries the append-only human answers recorded against
	// this SHA (FU-6). They apply after the automated refutation, so a
	// standing human answer wins over a fresh agent verdict for the same
	// fingerprint. Empty by default: callers without human answers behave
	// exactly as before.
	Dispositions []FindingDisposition
	// ModelVerifier answers whether a profile's model was verified by its
	// own agent. The engine consults it at stamp time; it never imports a
	// concrete prober — *modelprobe.Verifier satisfies this from the
	// outside. Nil keeps the honest default: nothing verified.
	ModelVerifier ModelVerifier
	// NetUnit* relabel the prompt as a NET-unit audit (T8.3).
	NetUnitLabel   string
	NetUnitHistory string
}

// DimensionOutcome is one dimension's verdict after the audit.
type DimensionOutcome struct {
	Bundle  string
	Dim     string
	Profile string
	Result  *DimensionResult
	Error   error
}

// ProviderExecutionFailure distinguishes provider invocation failures from
// deterministic semantic-output errors while preserving errors.Is behavior.
type ProviderExecutionFailure struct {
	Err error
}

// Error prefixes the cause the provider reported inside its own flow. A
// tool failure shows up as a timeout, and without this the operator reads
// the consequence instead of the reason.
func (e *ProviderExecutionFailure) Error() string { return reasonWithCause(e.Err.Error()) }
func (e *ProviderExecutionFailure) Unwrap() error { return e.Err }

// DimensionReviewRequest is the deep seam for one resolved dimension review.
type DimensionReviewRequest struct {
	Agent    AgentReviewer
	Bundle   ReviewBundle
	Contract reviewcontract.DimensionContract
	Options  AuditOptions
	Context  string
	// Profile is the resolved profile name the auditor factory reported
	// for this dimension. The engine queries Options.ModelVerifier with
	// it at stamp time; empty means unknown and stamps unverified.
	Profile string
}

// ModelVerifier answers whether the named profile was probed and matched
// in this session. Only a true answer stamps ModelVerified.
type ModelVerifier interface {
	Verified(profile string) bool
}

// DimensionReviewer owns prompt construction and semantic answer validation.
type DimensionReviewer struct{}

// AuditResult aggregates the commit's global verdict.
type AuditResult struct {
	SHA       string
	Dims      []DimensionOutcome
	Verdict   string // ok | warn | block | question | unavailable
	Questions []AgentQuestion
	Skipped   []SkippedBundle
	Findings  []Finding
	// CauseGroups groups distinct Findings that share a common root cause
	// (T6.3). It is a non-destructive view: it never merges or drops the
	// individual findings in Findings, it only groups references to them.
	CauseGroups []CauseGroup
	// ContextSkipReason records why optional review context was not available.
	// It is observational only and never changes the semantic verdict.
	ContextSkipReason string `json:"context_skip_reason,omitempty"`
}

const (
	BundleCorrectness     = "correctness"
	BundleQuality         = "quality"
	BundleSecurity        = "security"
	BundleContracts       = "contracts_compatibility"
	BundleConcurrencyData = "concurrency_data"
	EffortLow             = "low"
	PriorityRequired      = 1
	PriorityOptional      = 2
)

// ReviewBundle is one planned semantic-review agent and its dimensions.
type ReviewBundle struct {
	Name       string
	Dimensions []string
	Effort     string
	Priority   int
	Cost       int
}

// ReviewBudget limits the optional review bundles scheduled for one audit.
type ReviewBudget struct {
	MaxCost     int
	MaxDuration time.Duration
	Clock       func() time.Time
}

// SkippedBundle records a planned bundle that could not run within the budget.
type SkippedBundle struct {
	Name   string
	Reason string
}

// ReviewPlan is the risk-derived set of review bundles for a complete profile.
type ReviewPlan struct {
	Risk            risk.Result
	Characteristics []change.Feature
	Bundles         []ReviewBundle
}

// BundlesForRisk selects the review agents from the shared risk profile.
// Style is intentionally absent: deterministic lint owns it.
func BundlesForRisk(result risk.Result, features []change.Feature) []ReviewBundle {
	bundles := []ReviewBundle{
		{Name: BundleCorrectness, Dimensions: []string{DimLogic, DimSpec, DimTests}, Priority: PriorityRequired, Cost: 1},
	}
	switch result.Level {
	case risk.LevelNone:
		return nil
	case risk.LevelLow:
		bundles[0].Effort = EffortLow
		return bundles
	case risk.LevelStandard:
		return append(bundles, ReviewBundle{Name: BundleQuality, Dimensions: []string{DimDesign}, Priority: PriorityRequired, Cost: 1})
	case risk.LevelElevated:
		return append(bundles,
			ReviewBundle{Name: BundleQuality, Dimensions: []string{DimDesign}, Priority: PriorityRequired, Cost: 1},
			ReviewBundle{Name: BundleSecurity, Dimensions: []string{DimSecurity}, Priority: PriorityRequired, Cost: 1})
	case risk.LevelHigh:
		bundles = append(bundles,
			ReviewBundle{Name: BundleQuality, Dimensions: []string{DimDesign}, Priority: PriorityRequired, Cost: 1},
			ReviewBundle{Name: BundleSecurity, Dimensions: []string{DimSecurity}, Priority: PriorityRequired, Cost: 1})
		if featurePresent(features, "public_api") || featurePresent(features, "cross_module") {
			bundles = append(bundles, ReviewBundle{Name: BundleContracts, Dimensions: []string{DimSpec}, Priority: PriorityOptional, Cost: 1})
		}
		if featurePresent(features, "concurrency") || featurePresent(features, "database") {
			bundles = append(bundles, ReviewBundle{Name: BundleConcurrencyData, Dimensions: []string{DimLogic}, Priority: PriorityOptional, Cost: 1})
		}
		return bundles
	default:
		return BundlesForRisk(risk.Result{Level: risk.LevelHigh}, features)
	}
}

// PlanForProfile derives deterministic risk bundles from a complete change
// profile and the same evidence `sentinel explain` uses. The diff and the
// repository attributes are required rather than optional: supplying only
// symbols and paths starves the three detectors that read added lines, which is
// the divergence FU-10 records.
func PlanForProfile(profile change.ChangeProfile, paths []string, unifiedDiff, gitattributes string) ReviewPlan {
	characteristics := change.DetectFeatures(change.NewCharacteristicsInput(profile, paths, unifiedDiff, gitattributes))
	riskProfile := risk.Evaluate(profile, characteristics)
	return ReviewPlan{Risk: riskProfile, Characteristics: characteristics, Bundles: BundlesForRisk(riskProfile, characteristics)}
}

func featurePresent(features []change.Feature, name string) bool {
	for _, feature := range features {
		if feature.Name == name && feature.State == change.FeaturePresent {
			return true
		}
	}
	return false
}

// AuditCommit audits a commit against its dimensions under a semaphore of
// parallel concurrent jobs. It never touches the ledger: the caller persists
// the revision. Degradation is typed: an execution or parse error becomes an
// unavailable verdict with a reason, never an engine failure.
func AuditCommit(factory ReviewerFactory, parallel int, opts AuditOptions) AuditResult {
	result := AuditResult{SHA: opts.SHA}
	safePaths := sanitizeReviewPaths(opts.ContextPaths)
	reviewContext, contextErr := reviewerContext(opts.ContextProvider, opts.SHA, safePaths)
	if contextErr != nil {
		result.ContextSkipReason = contextErr.Error()
	}
	if parallel < 1 {
		parallel = 1
	}

	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	mutex := &sync.Mutex{}

	clock := opts.Budget.Clock
	if clock == nil {
		clock = time.Now
	}
	started := clock()
	cost := 0
	scheduledBundles := make(map[string]bool)
	run := func(bundle ReviewBundle) bool {
		if scheduledBundles[bundle.Name] || len(bundle.Dimensions) == 0 {
			return false
		}
		scheduledBundles[bundle.Name] = true
		cost += bundle.Cost
		for _, dim := range bundle.Dimensions {
			wg.Add(1)
			go func(bundle ReviewBundle, dimension string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if opts.OnDimension != nil {
					opts.OnDimension(dimension)
				}
				rd := DimensionOutcome{Bundle: bundle.Name, Dim: dimension}
				contract, contractErr := reviewcontract.Lookup(dimension)
				var agent AgentReviewer
				var profile string
				if contractErr != nil {
					rd.Error = contractErr
					rd.Result = &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: contractErr.Error()}
				} else {
					var err error
					agent, profile, err = factory(bundle, dimension)
					rd.Profile = profile
					if err != nil {
						rd.Error = err
						rd.Result = &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: err.Error()}
					} else {
						options := opts
						options.ContextPaths = safePaths
						rd.Result, rd.Error = (DimensionReviewer{}).Review(context.Background(), DimensionReviewRequest{Agent: agent, Bundle: bundle, Contract: contract, Options: options, Context: reviewContext, Profile: profile})
					}
				}
				rd.Result.Bundle = bundle.Name
				// Stamp in place: the aggregate is collected from result.Dims
				// AFTER refutation and standing dispositions run, so flips land
				// on the same elements the aggregate reads (collecting copies
				// here would freeze pre-disposition state).
				if rd.Error == nil && rd.Result != nil {
					for i := range rd.Result.V2Findings {
						rd.Result.V2Findings[i].InvocationID = rd.Result.InvocationID
					}
					stampSourceReview(rd.Result.V2Findings)
					stampEffectiveProducer(rd.Result.V2Findings, agent, opts.ModelVerifier, profile)
				}
				mutex.Lock()
				result.Dims = append(result.Dims, rd)
				mutex.Unlock()
			}(bundle, dim)
		}
		return true
	}
	for _, bundle := range opts.Bundles {
		if bundle.Priority == PriorityOptional {
			continue
		}
		if bundle.Cost <= 0 {
			bundle.Cost = 1
		}
		run(bundle)
	}
	wg.Wait()
	for _, bundle := range opts.Bundles {
		if bundle.Priority != PriorityOptional {
			continue
		}
		if bundle.Cost <= 0 {
			bundle.Cost = 1
		}
		if scheduledBundles[bundle.Name] || len(bundle.Dimensions) == 0 {
			result.Skipped = append(result.Skipped, SkippedBundle{Name: bundle.Name, Reason: "duplicate_bundle"})
			continue
		}
		if (opts.Budget.MaxCost > 0 && cost+bundle.Cost > opts.Budget.MaxCost) || (opts.Budget.MaxDuration > 0 && !clock().Before(started.Add(opts.Budget.MaxDuration))) {
			result.Skipped = append(result.Skipped, SkippedBundle{Name: bundle.Name, Reason: "budget_exhausted"})
			continue
		}
		run(bundle)
	}
	wg.Wait()
	if opts.ReviewTransportWithEvidence != nil {
		refuteCriticalFindingsWithEvidence(result.Dims, opts.RefuterFactory, opts.SHA, opts.ReadSnapshotContent, opts.ReviewTransportWithEvidence, opts.FinalizeMetrics)
	} else {
		refuteCriticalFindings(result.Dims, opts.RefuterFactory, opts.SHA, opts.ReadSnapshotContent, legacyReviewTransport(opts))
	}
	// Standing human answers apply after the automated refutation, so the
	// same fingerprint a person already answered never blocks again on a
	applyHumanDispositions(result.Dims, opts.SHA, opts.Dispositions)
	// Collect AFTER refutation and dispositions: the flips mutate the same
	// V2Findings elements stamped above, so refuted findings are skipped by
	// the aggregate instead of freezing pre-disposition copies.
	var semanticFindings []Finding
	for _, dim := range result.Dims {
		if dim.Result == nil {
			continue
		}
		semanticFindings = append(semanticFindings, dim.Result.V2Findings...)
	}
	findings := SupersedeDeterministicFindings(semanticFindings, opts.DeterministicFindings)
	result.Findings = append(aggregateFindings(findings, opts.DescriptionSimilarityThreshold), opts.DeterministicFindings...)
	result.CauseGroups = correlateFindingsByCause(result.Findings, opts.DescriptionSimilarityThreshold)
	result.Verdict, result.Questions = globalVerdict(result.Dims)
	return result
}

type refuterResponse struct {
	Refuted   bool   `json:"refuted"`
	Reason    string `json:"reason"`
	SHA       string `json:"sha"`
	File      string `json:"file"`
	LineStart int    `json:"line_start"`
	LineEnd   int    `json:"line_end"`
	Evidence  string `json:"evidence"`
}

// refuteCriticalFindings uses an independent SHA-bound restricted refuter once
// per semantic CRITICAL finding. Any unavailable or invalid answer preserves
// the original blocker.
//
// Ticket 12 slice 1 (envelope totality): a refutation CAN flip a verdict —
// it downgrades a confirmed semantic CRITICAL blocker to refuted — so it must
// flow through the same admitted invocation envelopes as dimension reviews:
// every refuter call is admitted through the transport, which production
// wiring always supplies (ticket 13, R11: the nil-transport rollback branch
// was removed with the review.durable_runs switch). A transport rejection
// surfaces as an error, which preserves the original blocker exactly like any
// other unavailable refuter answer.
func refuteCriticalFindings(dimensions []DimensionOutcome, factory RefuterFactory, sha string, readSnapshot SnapshotReader, transport ReviewTransport) {
	if transport == nil {
		return
	}
	refuteCriticalFindingsWithEvidence(dimensions, factory, sha, readSnapshot, func(bundle, dimension, prompt string, agent AgentReviewer) (string, ReviewEvidence, error) {
		output, invocation, err := transport(bundle, dimension, prompt, agent)
		return output, ReviewEvidence{InvocationID: invocation}, err
	}, nil)
}

func refuteCriticalFindingsWithEvidence(dimensions []DimensionOutcome, factory RefuterFactory, sha string, readSnapshot SnapshotReader, transport ReviewTransportWithEvidence, finalizer MetricsFinalizer) {
	if factory == nil || transport == nil {
		return
	}
	if readSnapshot == nil {
		readSnapshot = readSnapshotContent
	}
	for _, dimension := range dimensions {
		if dimension.Result == nil {
			continue
		}
		for i := range dimension.Result.Findings {
			finding := &dimension.Result.Findings[i]
			if finding.Severity != SevCritical || (finding.Source != "" && finding.Source != SourceReview) {
				continue
			}
			finding.Source = SourceReview
			finding.Status = StatusConfirmed
			refuter, _, err := factory()
			if err != nil {
				continue
			}
			contract, err := reviewcontract.Lookup(dimension.Dim)
			if err != nil {
				continue
			}
			if _, ok := refuter.(policyAwareReviewer); !ok {
				continue
			}
			prompt := buildRefutationPrompt(sha, dimension.Dim, *finding)
			// Admitted envelope flow: the refuter answer influences verdicts,
			// so it may never bypass admission.
			output, evidence, err := transport("refutation", dimension.Dim, prompt, bindPolicy(refuter, contract.ToolPolicy))
			if err != nil {
				var identity metricsEvidenceError
				if errors.As(err, &identity) {
					evidence.RunID, evidence.InvocationID, _, _ = identity.MetricsEvidence()
				}
				if finalizer != nil && evidence.RunID != "" && evidence.InvocationID != "" {
					_ = finalizer(evidence.RunID, evidence.InvocationID, "provider_error", err.Error())
				}
				continue
			}
			finalize := func(class, detail string) error {
				if finalizer == nil {
					// Metrics are not wired on this path (direct fixtures and
					// the legacy transport), so there is nothing to record and
					// nothing to protect.
					return nil
				}
				if evidence.RunID == "" || evidence.InvocationID == "" {
					return fmt.Errorf("refutation of %s:%d produced no durable identity to record", finding.File, finding.Line)
				}
				return finalizer(evidence.RunID, evidence.InvocationID, class, detail)
			}
			var response refuterResponse
			if err := json.Unmarshal([]byte(output), &response); err != nil || !response.Refuted || strings.TrimSpace(response.Reason) == "" {
				// The finding already stands confirmed on this path, so a
				// failed snapshot costs observability, not correctness.
				_ = finalize("invalid_output", "refuter response did not satisfy the contract")
				continue
			}
			evidenceHash, ok := validateRefutationEvidence(readSnapshot, sha, *finding, response)
			if !ok {
				_ = finalize("invalid_output", "refuter evidence did not match the immutable snapshot")
				continue
			}
			// This invocation is about to flip a confirmed CRITICAL, so its
			// durable evidence must exist before the downgrade is applied.
			// When the snapshot cannot be written the blocker stands: failing
			// closed can delay a correct downgrade, never hide a defect.
			if err := finalize("", ""); err != nil {
				continue
			}
			finding.Status = StatusRefuted
			finding.RefutationReason = response.Reason
			finding.RefutationEvidence = response.Evidence
			finding.RefutationLineStart = response.LineStart
			finding.RefutationLineEnd = response.LineEnd
			finding.RefutationRangeHash = evidenceHash
			// Automated provenance: audits and metrics separate this from a
			// human-issued refutation through RefutationActor (FU-6).
			finding.RefutationActor = RefutationActorRefuter
			dimension.Result.RefutedCritical = true
			// Ticket 13 hardening pool (R10 L1): the admitted refutation is a
			// distinct durable invocation whose identity is recorded through
			// the metrics finalizer above. The finding itself carries the
			// refutation evidence, range hash and actor, so audits and prune
			// provenance can tell an automated refutation from a human
			// answer (FU-6).
		}
		if canDowngradeBlock(*dimension.Result) {
			dimension.Result.Verdict = VerdictWarn
		}
	}
}
func validateRefutationEvidence(readSnapshot SnapshotReader, sha string, finding ReviewFinding, response refuterResponse) (string, bool) {
	safePaths := sanitizeReviewPaths([]string{finding.File})
	if len(safePaths) != 1 || response.SHA != sha || response.File != safePaths[0] || response.LineStart <= 0 || response.LineEnd < response.LineStart || response.LineEnd-response.LineStart >= 20 || int(finding.Line) < response.LineStart || int(finding.Line) > response.LineEnd {
		return "", false
	}
	content, err := readSnapshot(sha, safePaths[0])
	if err != nil {
		return "", false
	}
	lines := strings.Split(content, "\n")
	if response.LineEnd > len(lines) {
		return "", false
	}
	extract := strings.Join(lines[response.LineStart-1:response.LineEnd], "\n")
	evidence := strings.TrimSpace(response.Evidence)
	if len(evidence) < 12 || evidence != strings.TrimSpace(extract) {
		return "", false
	}
	sum := sha256.Sum256([]byte(extract))
	return fmt.Sprintf("%x", sum), true
}

// canDowngradeBlock reports whether a block verdict can soften to warn after
// its CRITICAL was refuted: the refutation flag is set, no reason overrides
// it, and no finding still blocks under the shared rule.
func canDowngradeBlock(result DimensionResult) bool {
	return result.Verdict == VerdictBlock &&
		result.RefutedCritical &&
		strings.TrimSpace(result.Reason) == "" &&
		!hasBlockingFinding(&result)
}

// applyHumanDispositions overlays the append-only human answers recorded
// against this SHA onto the fresh dimension results. A disposition that
// newly clears the last blocking CRITICAL of a block verdict downgrades it
// to warn, mirroring canDowngradeBlock but without setting RefutedCritical:
// that flag reports an automated refutation needing human review, while a
// human-issued answer already is the human review, so the gate must pass it
// instead of asking for attention again.
func applyHumanDispositions(dimensions []DimensionOutcome, sha string, dispositions []FindingDisposition) {
	if len(dispositions) == 0 {
		return
	}
	for i := range dimensions {
		dimension := &dimensions[i]
		if dimension.Result == nil {
			continue
		}
		cleared := false
		for _, disp := range dispositions {
			if disp.SHA != sha {
				continue
			}
			if ApplyDispositionToResult(dimension.Result, disp) {
				cleared = true
			}
		}
		if cleared && dimension.Result.Verdict == VerdictBlock &&
			strings.TrimSpace(dimension.Result.Reason) == "" &&
			!hasBlockingFinding(dimension.Result) {
			dimension.Result.Verdict = VerdictWarn
		}
	}
}

// hasBlockingFinding reports whether a dimension result still holds a
// finding that blocks release under the shared rule.
func hasBlockingFinding(result *DimensionResult) bool {
	for i := range result.Findings {
		if IsBlocking(result.Findings[i].Severity, result.Findings[i].Status) {
			return true
		}
	}
	return false
}

// SafeReviewPaths exposes the engine's reviewer-path sanitizer so
// out-of-engine transports bind the exact same safe list the legacy path
// uses. Ticket 05 Judgment Day JD-A1: binding raw caller lists would let
// dash-prefixed, control-character, absolute, and parent-relative names
// reach reviewers unfiltered on the durable path.
func SafeReviewPaths(paths []string) []string { return sanitizeReviewPaths(paths) }

func sanitizeReviewPaths(paths []string) []string {
	safe := make([]string, 0, len(paths))
	for _, rawPath := range paths {
		normalized := strings.ReplaceAll(rawPath, "\\", "/")
		clean := path.Clean(normalized)
		drive := len(clean) >= 2 && clean[1] == ':'
		if rawPath == "" || path.IsAbs(clean) || drive || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(rawPath, "-") || strings.ContainsAny(rawPath, "\x00\r\n*?[]{}!") {
			continue
		}
		safe = append(safe, clean)
	}
	return safe
}

// auditWithAgent executes the prompt, including the extra --answer round when
// the agent requests clarification, then parses its JSONL. A transport-reported
// invocation identity is added to the dimension result and each finding
// (ticket 07 slice 2b) as provenance metadata, never as fingerprint input.
func auditWithAgent(agent AgentReviewer, bundle ReviewBundle, dimension string, opts AuditOptions, reviewContext string) (*DimensionResult, error) {
	contract, err := reviewcontract.Lookup(dimension)
	if err != nil {
		return &DimensionResult{Dim: dimension, Verdict: VerdictUnavailable, Reason: err.Error()}, err
	}
	return (DimensionReviewer{}).Review(context.Background(), DimensionReviewRequest{Agent: agent, Bundle: bundle, Contract: contract, Options: opts, Context: reviewContext})
}

// Review executes one resolved contract and preserves raw provider output or a
// typed provider execution failure on the returned result for internal
// diagnosis. A malformed semantic payload gets one corrective retry; a
// settled retryable provider failure gets one bounded retry in a new physical
// run, while every terminal error remains typed and lossless.
type metricsEvidenceError interface {
	MetricsEvidence() (runID, invocationID, class, detail string)
}

func finalizeInvocation(opts AuditOptions, runID, invocationID, failureClass, detail string) error {
	if opts.FinalizeMetrics == nil || runID == "" || invocationID == "" {
		return nil
	}
	return opts.FinalizeMetrics(runID, invocationID, failureClass, detail)
}

// semanticFailure reports the deterministic output failure exactly as it was
// classified. Collapsing every class into invalid_output would tell the
// metrics that a denied tool was malformed output, which is the same
// conflation the retry policy already refuses to make.
func semanticFailure(err error) (string, string) {
	var semantic *SemanticOutputError
	if !errors.As(err, &semantic) {
		return "", ""
	}
	return string(semantic.Class), semantic.Error()
}
func (DimensionReviewer) Review(ctx context.Context, request DimensionReviewRequest) (*DimensionResult, error) {
	if err := ctx.Err(); err != nil {
		failure := &ProviderExecutionFailure{Err: err}
		return &DimensionResult{Dim: request.Contract.Name, Verdict: VerdictUnavailable, Reason: failure.Error(), ExecutionFailure: failure}, failure
	}
	agent, bundle, opts, contract := request.Agent, request.Bundle, request.Options, request.Contract
	if _, ok := agent.(policyAwareReviewer); !ok {
		return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: ErrRestrictedRequired.Error()}, ErrRestrictedRequired
	}
	run := func(prompt string) (string, error) {
		policyReviewer := agent.(policyAwareReviewer)
		return policyReviewer.ReviewWithPolicy(prompt, opts.SHA, opts.ContextPaths, contract.ToolPolicy)
	}
	finalizeProvider := func(runID, invocationID string, err error) error {
		class, detail := "", ""
		var evidenceErr metricsEvidenceError
		if errors.As(err, &evidenceErr) {
			reportedRun, reportedInvocation, reportedClass, reportedDetail := evidenceErr.MetricsEvidence()
			if reportedRun != "" {
				runID = reportedRun
			}
			if reportedInvocation != "" {
				invocationID = reportedInvocation
			}
			class, detail = reportedClass, reportedDetail
		}
		if detail == "" && err != nil {
			detail = err.Error()
		}
		return finalizeInvocation(opts, runID, invocationID, class, detail)
	}
	finalizeSemantic := func(runID, invocationID string, err error) error {
		class, detail := semanticFailure(err)
		return finalizeInvocation(opts, runID, invocationID, class, detail)
	}
	prompt := buildPromptWithContext(bundle, contract, opts.Message, opts.Diff, "", request.Context, opts.ContextPaths, opts.NetUnitLabel, opts.NetUnitHistory)
	output, invocation, runID, err := invokeReview(opts, bundle, contract.Name, bindPolicy(agent, contract.ToolPolicy), run, prompt)
	if err != nil && isTransientProviderFailure(err) {
		// The first provider failure is already a settled physical run; fold
		// it before admitting the bounded retry so no attempt disappears from
		// the durable history.
		if finalizeErr := finalizeProvider(runID, invocation, err); finalizeErr != nil {
			err = fmt.Errorf("review metrics finalization failed: %w", finalizeErr)
			failure := &ProviderExecutionFailure{Err: err}
			return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: failure.Error(), ExecutionFailure: failure}, failure
		}
		output, invocation, runID, err = invokeReview(opts, bundle, contract.Name, bindPolicy(agent, contract.ToolPolicy), run, prompt)
	}
	if err != nil {
		finalizeErr := finalizeProvider(runID, invocation, err)
		if finalizeErr != nil {
			err = fmt.Errorf("review metrics finalization failed: %w", finalizeErr)
		}
		failure := &ProviderExecutionFailure{Err: err}
		return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: failure.Error(), ExecutionFailure: failure}, failure
	}

	parsed, err := ParseDimensionResultForContract(output, contract)
	if shouldRetryFormat(err) {
		if finalizeErr := finalizeSemantic(runID, invocation, err); finalizeErr != nil {
			return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: finalizeErr.Error(), RawProviderOutput: output}, finalizeErr
		}
		output, invocation, runID, err = invokeReview(opts, bundle, contract.Name, bindPolicy(agent, contract.ToolPolicy), run, prompt+formatRetryInstruction)
		if err != nil {
			if finalizeErr := finalizeProvider(runID, invocation, err); finalizeErr != nil {
				err = fmt.Errorf("review metrics finalization failed: %w", finalizeErr)
			}
		} else {
			parsed, err = ParseDimensionResultForContract(output, contract)
			if err != nil {
				if finalizeErr := finalizeSemantic(runID, invocation, err); finalizeErr != nil {
					err = fmt.Errorf("review metrics finalization failed: %w", finalizeErr)
				}
			}
		}
	} else if err != nil {
		if finalizeErr := finalizeSemantic(runID, invocation, err); finalizeErr != nil {
			err = fmt.Errorf("review metrics finalization failed: %w", finalizeErr)
		}
	}
	if err != nil {
		return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: err.Error(), RawProviderOutput: output}, err
	}
	parsed.InvocationID = invocation
	parsed.RawProviderOutput = output

	// Second round only when the agent asked clarifying questions and the user answered.
	if parsed.Verdict == VerdictQuestion && opts.Answers != "" {
		if finalizeErr := finalizeInvocation(opts, runID, invocation, "", ""); finalizeErr != nil {
			return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: finalizeErr.Error(), RawProviderOutput: output}, finalizeErr
		}
		output, invocation, runID, err = invokeReview(opts, bundle, contract.Name, bindPolicy(agent, contract.ToolPolicy), run, buildPromptWithContext(bundle, contract, opts.Message, opts.Diff, opts.Answers, request.Context, opts.ContextPaths, opts.NetUnitLabel, opts.NetUnitHistory))
		if err != nil {
			if finalizeErr := finalizeProvider(runID, invocation, err); finalizeErr != nil {
				err = fmt.Errorf("review metrics finalization failed: %w", finalizeErr)
			}
			failure := &ProviderExecutionFailure{Err: err}
			return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: failure.Error(), ExecutionFailure: failure}, failure
		}
		parsed, err = ParseDimensionResultForContract(output, contract)
		if err != nil {
			if finalizeErr := finalizeSemantic(runID, invocation, err); finalizeErr != nil {
				err = fmt.Errorf("review metrics finalization failed: %w", finalizeErr)
			}
			return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: err.Error(), RawProviderOutput: output}, err
		}
		parsed.InvocationID = invocation
		parsed.RawProviderOutput = output
	}
	if finalizeErr := finalizeInvocation(opts, runID, invocation, "", ""); finalizeErr != nil {
		return &DimensionResult{Dim: contract.Name, Verdict: VerdictUnavailable, Reason: finalizeErr.Error(), RawProviderOutput: output}, finalizeErr
	}
	// Authority stamp: the prompt never asks the model for provenance, so a
	// model-claimed "source" is overridden here, exactly as the aggregate
	// projection stamps it again for the durable shape.
	for i := range parsed.Findings {
		parsed.Findings[i].Source = SourceReview
	}
	return parsed, nil
}

// permanentProviderFailures are the texts proving that repeating the call
// cannot change anything. They are matched by content because they come from
// the provider's stderr, which offers no stable codes.
var permanentProviderFailures = []string{
	// Model missing or misspelled in the configuration. Observed as
	// `"claude-opus" is not a model this version of Claude Code recognizes`.
	"is not a model",
	// The required tool policy does not exist for that provider: reviewCommand
	// rejects it before launching anything.
	"not configured for this provider",
}

// settledProviderRetryable is implemented by the error a transport returns
// when the run SETTLED durably with an unsuccessful result.
//
// That distinction is what makes the retry safe, and the review that blocked
// the first version of this code flagged it: a transport also returns
// ADMISSION failures and post-submission errors, and repeating those can
// duplicate invocations. A settled run, by contrast, already finished and is
// recorded, so retrying opens a new run and duplicates nothing.
//
// It is declared as an interface here instead of importing the concrete type
// because internal/reviewexec already imports this package: the dependency
// direction only allows the transport itself to declare its retryable error.
type settledProviderRetryable interface {
	ProviderSettledRetryable() bool
}

// isTransientProviderFailure decides whether a failure deserves the single
// retry. It is an ALLOWLIST: only a settled run with a retryable result
// qualifies. Everything else — admission, observation, evidence, or any error
// from the direct path — is rejected, because we cannot prove the provider
// never executed.
//
// That also keeps this retry from composing with invokeReview's:
// runWithRetry only acts when there is no transport, and that path never
// produces a settled error, so the budget stays at a single repetition.
//
// A format failure does NOT reach here: shouldRetryFormat decides that, after
// the provider has answered.
func isTransientProviderFailure(err error) bool {
	if err == nil {
		return false
	}
	var settled settledProviderRetryable
	if !errors.As(err, &settled) || !settled.ProviderSettledRetryable() {
		return false
	}
	text := err.Error()
	for _, permanent := range permanentProviderFailures {
		if strings.Contains(text, permanent) {
			return false
		}
	}
	return true
}

const formatRetryInstruction = "\n\nFORMAT RETRY: Your previous response did not satisfy the required review schema. Return only one complete BEGIN_REVIEW/END_REVIEW payload with verdict set to ok, warn, block, question, or unavailable. EVERY finding must carry a non-empty literal `evidence` quoted from the reviewed code and a `confidence` value; a single finding missing either one discards the whole response."

// shouldRetryFormat decides the ONE corrective retry a malformed semantic
// payload gets. Every SemanticOutputClass that means "the provider answered but
// the payload does not satisfy the contract" qualifies, because that is exactly
// what a corrective instruction can fix.
//
// It used to re-parse the output as a single JSON object and retry only when
// that object carried a non-empty INVALID verdict. That missed the failure that
// actually happens: a JSONL payload with a VALID verdict whose findings breach
// the evidence policy. All six canonical dimensions require literal evidence and
// confidence, and one offending finding discards the whole block, so those
// dimensions went straight to unavailable with no second chance.
//
// tool_denied is deliberately excluded: a denied tool is not a format problem,
// and repeating the prompt cannot grant permissions — it would only spend
// another provider call. Provider execution failures never reach here, because
// they are not SemanticOutputError at all.
func shouldRetryFormat(err error) bool {
	var semantic *SemanticOutputError
	if !errors.As(err, &semantic) {
		return false
	}
	switch semantic.Class {
	case SemanticOutputMissingPayload, SemanticOutputMalformedJSON, SemanticOutputSchemaInvalid:
		return true
	default:
		return false
	}
}

// stampSourceReview marks every finding produced by the semantic review with
// Source: SourceReview, with the same origin authority stampEffectiveProducer
// applies to the Producer: the prompt does not ask the model to declare its
// own provenance (T6.2 needs it to tell which finding a deterministic one may
// supersede), so depending on the JSON including it would leave the field
// empty in practice.
func stampSourceReview(findings []Finding) {
	for i := range findings {
		findings[i].Source = SourceReview
	}
}

// stampEffectiveProducer replaces the model-reported Producer with the
// effective identity reported by the agent adapter, so the persisted finding
// attributes its evidence to the binary and model that really answered.
func stampEffectiveProducer(findings []Finding, agent AgentReviewer, verifier ModelVerifier, profile string) {
	// No model-claimed true survives this function: the flag is cleared on
	// every finding first, then set only from the outside verifier below.
	// Otherwise a model injecting producer.model_verified:true into its
	// JSON would land a false true in the ledger on any path where the
	// effective identity is unavailable and the stamp returns early.
	for i := range findings {
		findings[i].Producer.ModelVerified = false
		if findings[i].EvidenceSet == nil {
			continue
		}
		for j := range findings[i].EvidenceSet.Values {
			findings[i].EvidenceSet.Values[j].Producer.ModelVerified = false
		}
	}
	reporter, ok := agent.(agentadapter.ReportsEffectiveAgent)
	if !ok {
		return
	}
	effective, ok := reporter.EffectiveAgent()
	if !ok || effective.Empty() {
		return
	}
	// ModelVerified is true only when an outside verifier confirms this
	// profile was probed and matched. A nil verifier, an unknown profile,
	// and any non-match keep the honest default of false — including over
	// a model-claimed true, which the authority stamp overwrites.
	verified := verifier != nil && verifier.Verified(profile)
	producer := Producer{
		Agent:         effective.Binary,
		Binary:        effective.Binary,
		Model:         effective.Model,
		Effort:        effective.Effort,
		ModelVerified: verified,
	}
	for i := range findings {
		findings[i].Producer = producer
		if findings[i].EvidenceSet == nil {
			continue
		}
		evidences := make([]FindingEvidence, len(findings[i].EvidenceSet.Values))
		for j, evidence := range findings[i].EvidenceSet.Values {
			evidence.Producer = producer
			evidences[j] = evidence
		}
		findings[i].EvidenceSet = &FindingEvidenceSet{Values: evidences}
	}
}

func legacyReviewTransport(opts AuditOptions) ReviewTransport {
	if opts.ReviewTransport != nil {
		return opts.ReviewTransport
	}
	if opts.ReviewTransportWithEvidence == nil {
		return nil
	}
	return func(bundleName, dimension, prompt string, agent AgentReviewer) (string, string, error) {
		output, evidence, err := opts.ReviewTransportWithEvidence(bundleName, dimension, prompt, agent)
		return output, evidence.InvocationID, err
	}
}

// invokeReview routes one reviewer call through the configured durable
// transport when present; nil keeps the direct restricted call with its
// transport retry (engine-level injection seam only: production wiring always
// supplies the admitted transport since ticket 13, R11). Both paths receive
// the identical prompt so parsing stays shared. The middle result is the
// producing invocation identity: whatever the transport reports, or empty on
// the direct path.
func invokeReview(opts AuditOptions, bundle ReviewBundle, dimension string, agent AgentReviewer, run func(string) (string, error), prompt string) (string, string, string, error) {
	if opts.ReviewTransportWithEvidence != nil {
		output, evidence, err := opts.ReviewTransportWithEvidence(bundle.Name, dimension, prompt, agent)
		if err != nil {
			var identity metricsEvidenceError
			if errors.As(err, &identity) {
				runID, invocationID, _, _ := identity.MetricsEvidence()
				if runID != "" {
					evidence.RunID = runID
				}
				if invocationID != "" {
					evidence.InvocationID = invocationID
				}
			}
		}
		return output, evidence.InvocationID, evidence.RunID, err
	}
	if opts.ReviewTransport != nil {
		output, invocation, err := opts.ReviewTransport(bundle.Name, dimension, prompt, agent)
		return output, invocation, "", err
	}
	output, err := runWithRetry(run, prompt)
	return output, "", "", err
}

func runWithRetry(run func(string) (string, error), prompt string) (string, error) {
	output, err := run(prompt)
	if err != nil && isTransportError(err) {
		return run(prompt)
	}
	return output, err
}

func isTransportError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary())
}

type Relation string
type Reason string

const (
	RelationAffectedTest Relation = "affected_test"
	RelationCaller       Relation = "caller"
	RelationCallee       Relation = "callee"
	RelationImpact       Relation = "impact"
	ReasonCodeGraph      Reason   = "codegraph_dependency"
)

type Reference struct {
	Path     string   `json:"path"`
	Relation Relation `json:"relation"`
	Reason   Reason   `json:"reason"`
}

type ContextProvider interface {
	Name() string
	Context(sha string, paths []string) ([]Reference, error)
}

func reviewerContext(provider ContextProvider, sha string, paths []string) (string, error) {
	if provider == nil {
		return "", nil
	}
	references, err := provider.Context(sha, paths)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(references)
	if err != nil {
		return "", err
	}
	if len(references) == 0 {
		return "", nil
	}
	return string(data), nil

}

// globalVerdict decides the commit's verdict: block wins, then question
// (unresolved), then unavailable, then warn, and finally ok.
func globalVerdict(dims []DimensionOutcome) (string, []AgentQuestion) {
	worst := VerdictOK
	var questions []AgentQuestion
	for _, dim := range dims {
		if dim.Result == nil {
			continue
		}
		switch dim.Result.Verdict {
		case VerdictBlock:
			worst = VerdictBlock
		case VerdictQuestion:
			questions = append(questions, dim.Result.Questions...)
			if worst != VerdictBlock {
				worst = VerdictQuestion
			}
		case VerdictUnavailable:
			if worst == VerdictOK || worst == VerdictWarn {
				worst = VerdictUnavailable
			}
		case VerdictWarn:
			if worst == VerdictOK {
				worst = VerdictWarn
			}
		}
	}
	return worst, questions
}

// Unavailable dimensions always show their reason and their class (admission
// vs infrastructure) even when the global verdict is block: without that a
// block hides that other dimensions never even got audited, and the operator
// cannot see the real reason.
func (r AuditResult) String() string {
	lines := make([]string, 0, len(r.Dims)+len(r.Skipped)+6)
	shortSHA := r.SHA
	if len(shortSHA) > 8 {
		shortSHA = shortSHA[:8]
	}
	lines = append(lines, fmt.Sprintf("🔎 Review of %s: %s", shortSHA, r.Verdict))
	if r.ContextSkipReason != "" {
		lines = append(lines, fmt.Sprintf("  review context skipped: %q", r.ContextSkipReason))
	}
	for _, rd := range r.Dims {
		verdict := "?"
		reason := ""
		if rd.Result != nil {
			verdict = rd.Result.Verdict
			reason = rd.Result.Reason
		}
		if reason == "" && rd.Error != nil {
			reason = rd.Error.Error()
		}
		if rd.Result == nil && rd.Error != nil && verdict == "?" {
			verdict = VerdictUnavailable
		}
		lines = append(lines, fmt.Sprintf("  %-9s %-11s %s", rd.Dim, verdict, rd.Profile))
		if verdict == VerdictUnavailable && strings.TrimSpace(reason) != "" {
			class := "infrastructure"
			if strings.HasPrefix(reason, "admission: ") {
				class = "admission"
			}
			lines = append(lines, fmt.Sprintf("    ↳ reason=%q class=%s", reason, class))
		}
	}
	for _, skipped := range r.Skipped {
		lines = append(lines, fmt.Sprintf("  %-9s %-11s %s", skipped.Name, "skipped", skipped.Reason))
	}
	if r.Verdict == VerdictBlock {
		var unav []string
		for _, rd := range r.Dims {
			isUnav := false
			if rd.Result != nil && rd.Result.Verdict == VerdictUnavailable {
				isUnav = true
			} else if rd.Result == nil && rd.Error != nil {
				isUnav = true
			}
			if isUnav {
				name := rd.Dim
				if name == "" && rd.Result != nil {
					name = rd.Result.Dim
				}
				if name == "" {
					name = "?"
				}
				unav = append(unav, name)
			}
		}
		if len(unav) > 0 {
			lines = append(lines, fmt.Sprintf("  ⚠️  %d dimensions unavailable (%s) — see reason above (does not hide the block, but explains the incomplete coverage)", len(unav), strings.Join(unav, ", ")))
		}
	}
	return "\n" + strings.Join(lines, "\n")
}
