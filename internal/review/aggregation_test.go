package review

import (
	"math"
	"reflect"
	"testing"
)

// nthULPAfter steps x forward by exactly n ULPs, for constructing
// deterministic floating-point tie/boundary test cases.
func nthULPAfter(x float64, n int) float64 {
	for i := 0; i < n; i++ {
		x = math.Nextafter(x, math.Inf(1))
	}
	return x
}

func TestAggregateFindingsCollapsesExactFingerprints(t *testing.T) {
	first := Finding{
		Dimension:  DimLogic,
		Severity:   SevWarning,
		Confidence: 0.6,
		Title:      "unchecked error",
		Evidence:   "if err != nil { return }",
		Location:   Location{File: "config.go", LineStart: 12, Simbolo: "parseConfig"},
		Producer:   Producer{Agent: "logic-reviewer"},
	}
	first.Fingerprint = Fingerprint(first)
	second := first
	second.Severity = SevCritical
	second.Producer = Producer{Agent: "retry-reviewer"}

	aggregated := aggregateFindings([]Finding{first, second}, defaultDescriptionSimilarityThreshold)
	if len(aggregated) != 1 {
		t.Fatalf("aggregated findings = %d, expected 1", len(aggregated))
	}
	if got := aggregated[0]; got.Severity != SevCritical || got.EvidenceSet == nil || len(got.EvidenceSet.Values) != 2 {
		t.Errorf("aggregated finding = %#v, expected CRITICAL severity and 2 evidences", got)
	}
}

func TestAggregateFindingsRecomputesFingerprintFromCanonicalFields(t *testing.T) {
	first := Finding{
		Dimension: DimLogic, Title: "unchecked error", Evidence: "if err != nil { return }",
		Location:    Location{File: "config.go", LineStart: 12, Simbolo: "parseConfig"},
		Fingerprint: "untrusted-first",
	}
	second := first
	second.Fingerprint = "untrusted-second"

	aggregated := aggregateFindings([]Finding{first, second}, defaultDescriptionSimilarityThreshold)
	if len(aggregated) != 1 {
		t.Fatalf("aggregated findings = %d, expected 1", len(aggregated))
	}
	if got, want := aggregated[0].Fingerprint, Fingerprint(first); got != want {
		t.Errorf("fingerprint = %q, expected canonical %q", got, want)
	}
}

func TestAggregateFindingsUsesHighestSeverityAsCanonicalFinding(t *testing.T) {
	first := Finding{
		Dimension: DimLogic, Severity: SevWarning, Confidence: 0.6,
		Description: "ignored parse error permits invalid configuration", Evidence: "return nil",
		Location: Location{File: "config.go", LineStart: 12, LineEnd: 16, Simbolo: "parseConfig"},
		Producer: Producer{Agent: "logic-reviewer"},
	}
	highest := Finding{
		Dimension: DimDesign, Severity: SevCritical, Confidence: 0.8,
		Description: "invalid configuration is permitted after ignored parse error", Evidence: "return without handling the parse error",
		Location: Location{File: "config.go", LineStart: 14, LineEnd: 18, Simbolo: "parseConfig"},
		Producer: Producer{Agent: "design-reviewer"},
	}

	aggregated := aggregateFindings([]Finding{first, highest}, 0.3)
	if len(aggregated) != 1 {
		t.Fatalf("aggregated findings = %d, expected 1", len(aggregated))
	}
	got := aggregated[0]
	if got.Severity != highest.Severity || got.Description != highest.Description || got.Location != highest.Location || got.Evidence != highest.Evidence {
		t.Errorf("canonical finding = %#v, expected highest severity finding %#v", got, highest)
	}
	if got.EvidenceSet == nil || len(got.EvidenceSet.Values) != 2 {
		t.Errorf("evidence set = %#v, expected both reports", got.EvidenceSet)
	}
}

func TestAggregateFindingsExcludesRefutedFindings(t *testing.T) {
	refuted := Finding{Status: StatusRefuted, Fingerprint: "ignored"}
	if aggregated := aggregateFindings([]Finding{refuted}, defaultDescriptionSimilarityThreshold); len(aggregated) != 0 {
		t.Errorf("aggregated findings = %#v, expected refuted finding to be excluded", aggregated)
	}
}

func TestAreProximateFindingsRejectsDistinctLocationsAndBoundarySimilarity(t *testing.T) {
	base := Finding{
		Description: "ignored parse error", Location: Location{File: "config.go", LineStart: 12, LineEnd: 16, Simbolo: "parseConfig"},
	}
	for _, tc := range []struct {
		name      string
		other     Finding
		threshold float64
	}{
		{"different file", Finding{Description: base.Description, Location: Location{File: "other.go", LineStart: 12, LineEnd: 16, Simbolo: "parseConfig"}}, 0.5},
		{"different symbol", Finding{Description: base.Description, Location: Location{File: "config.go", LineStart: 12, LineEnd: 16, Simbolo: "other"}}, 0.5},
		{"non-overlapping range", Finding{Description: base.Description, Location: Location{File: "config.go", LineStart: 17, LineEnd: 20, Simbolo: "parseConfig"}}, 0.5},
		{"similarity at threshold", Finding{Description: "ignored validation error", Location: base.Location}, 0.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if areProximateFindings(base, tc.other, tc.threshold) {
				t.Errorf("findings must not be proximate")
			}
		})
	}
}

func TestCorrelateFindingsByCauseGroupsDistinctLocationsSharingRootCause(t *testing.T) {
	findings := []Finding{
		{Description: "session cache race condition breaks TestUserLogin", Confidence: 0.5, Location: Location{File: "session.go", Simbolo: "acquireSession"}},
		{Description: "TestUserLogin breaks because of session cache race condition", Confidence: 0.9, Location: Location{File: "cache.go", Simbolo: "cacheGet"}},
		{Description: "race condition in session cache breaks TestUserLogin intermittently", Confidence: 0.4, Location: Location{File: "login_test.go", Simbolo: "TestUserLogin"}},
		{Description: "TestUserLogin intermittently fails from session cache race condition", Confidence: 0.6, Location: Location{File: "runner.go", Simbolo: "runSuite"}},
		{Description: "the session cache race condition is why TestUserLogin breaks", Confidence: 0.3, Location: Location{File: "harness.go", Simbolo: "setupHarness"}},
	}

	groups := correlateFindingsByCause(findings, 0.4)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, expected 1: %#v", len(groups), groups)
	}
	group := groups[0]
	if len(group.Effects) != 5 {
		t.Fatalf("effects = %d, expected 5: %#v", len(group.Effects), group.Effects)
	}
	// Cause is the medoid (highest total description similarity to the rest
	// of the group), not simply the highest-Confidence member: findings[0]'s
	// wording overlaps most with the other four collectively, even though
	// findings[1] has the highest individual Confidence (0.9).
	if group.Cause != findings[0].Description {
		t.Errorf("cause = %q, expected medoid description %q", group.Cause, findings[0].Description)
	}
	// Prove no finding was dropped, duplicated, or corrupted: every input
	// finding must survive intact in Effects, in original scan order. Order
	// is deterministic because correlateFindingsByCause accumulates members
	// by scanning findings in ascending index order and records each group
	// at the index where its root is first encountered — not by which index
	// ends up as the union-find root itself (union has no rank/size
	// heuristic, so the root is not guaranteed to be the component's
	// minimum index).
	for i, want := range findings {
		if !reflect.DeepEqual(group.Effects[i], want) {
			t.Errorf("effects[%d] = %#v, expected %#v", i, group.Effects[i], want)
		}
	}
}

// Shared fixture for the two chain tests below (transitivity and
// medoid-vs-confidence-outlier): A is similar to B, B is similar to C, but A
// and C share no words. Both vary only Confidence (and, for the
// transitivity test, Location) on top of these descriptions. The dominant-
// cause tie-break test below uses its own independent fixture instead, since
// it is not a chain scenario.
const (
	chainDescriptionA        = "buffer overflow corrupts memory adjacent allocator"
	chainDescriptionB        = "adjacent allocator exhausts pool"
	chainDescriptionC        = "exhausts pool timeout expired session handle"
	chainSimilarityThreshold = 0.2
)

// TestCorrelateFindingsByCauseGroupsTransitivelyThroughSharedFinding
// reproduces the non-transitive, order-dependent bug fixed by the
// union-find rewrite: A is similar to B, B is similar to C, but A is not
// directly similar enough to C. The old anchor-only comparison split C into
// its own group (or dropped it) depending on iteration order, even though
// A, B, and C all share the same cause transitively through B.
func TestCorrelateFindingsByCauseGroupsTransitivelyThroughSharedFinding(t *testing.T) {
	a := Finding{Description: chainDescriptionA, Confidence: 0.5, Location: Location{File: "alloc.go", Simbolo: "allocate"}}
	b := Finding{Description: chainDescriptionB, Confidence: 0.9, Location: Location{File: "pool.go", Simbolo: "acquire"}}
	c := Finding{Description: chainDescriptionC, Confidence: 0.4, Location: Location{File: "session.go", Simbolo: "release"}}

	const threshold = chainSimilarityThreshold
	// Sanity-check the crafted descriptions actually exhibit the intended
	// non-transitive pairwise relationship before trusting the assertion
	// below: A~B and B~C exceed the threshold, but A~C does not.
	if got := descriptionSimilarity(a.Description, b.Description); got <= threshold {
		t.Fatalf("similarity(A,B) = %v, want > %v (test setup invalid)", got, threshold)
	}
	if got := descriptionSimilarity(b.Description, c.Description); got <= threshold {
		t.Fatalf("similarity(B,C) = %v, want > %v (test setup invalid)", got, threshold)
	}
	if got := descriptionSimilarity(a.Description, c.Description); got > threshold {
		t.Fatalf("similarity(A,C) = %v, want <= %v (test setup invalid)", got, threshold)
	}

	groups := correlateFindingsByCause([]Finding{a, b, c}, threshold)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, expected 1 transitive group joining A, B, C via B: %#v", len(groups), groups)
	}
	if len(groups[0].Effects) != 3 {
		t.Fatalf("effects = %d, expected 3 (A and C must join transitively through B, not split or drop C): %#v", len(groups[0].Effects), groups[0].Effects)
	}
	for i, want := range []Finding{a, b, c} {
		if !reflect.DeepEqual(groups[0].Effects[i], want) {
			t.Errorf("effects[%d] = %#v, expected %#v", i, groups[0].Effects[i], want)
		}
	}
}

// TestCorrelateFindingsByCauseLabelsChainWithMedoidNotConfidenceOutlier
// reproduces the mislabeling bug a semantic review found in the union-find
// rewrite: under transitive chaining, picking Cause by raw highest
// Confidence can select a chain endpoint that shares zero words with the
// opposite endpoint, even though a middle member (the bridge) is similar to
// both. Here c has the highest Confidence but zero similarity to a; b is the
// bridge with positive similarity to both a and c, so b must win regardless
// of confidence ordering.
func TestCorrelateFindingsByCauseLabelsChainWithMedoidNotConfidenceOutlier(t *testing.T) {
	a := Finding{Description: chainDescriptionA, Confidence: 0.5}
	b := Finding{Description: chainDescriptionB, Confidence: 0.3}
	c := Finding{Description: chainDescriptionC, Confidence: 0.9}

	const threshold = chainSimilarityThreshold
	// Same sanity checks as the sibling transitivity test above: this test
	// shares its fixture, so it must not silently rely on that test's setup
	// remaining valid.
	if got := descriptionSimilarity(a.Description, b.Description); got <= threshold {
		t.Fatalf("similarity(A,B) = %v, want > %v (test setup invalid)", got, threshold)
	}
	if got := descriptionSimilarity(b.Description, c.Description); got <= threshold {
		t.Fatalf("similarity(B,C) = %v, want > %v (test setup invalid)", got, threshold)
	}
	if got := descriptionSimilarity(a.Description, c.Description); got != 0 {
		t.Fatalf("similarity(A,C) = %v, want 0 (test setup invalid)", got)
	}

	groups := correlateFindingsByCause([]Finding{a, b, c}, threshold)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, expected 1: %#v", len(groups), groups)
	}
	if groups[0].Cause != b.Description {
		t.Errorf("cause = %q, expected the bridge %q (medoid), not the highest-confidence chain endpoint %q", groups[0].Cause, b.Description, c.Description)
	}
}

// TestDominantCauseBreaksTwoMemberTieByConfidence covers the 2-member tie
// both at the module's public boundary (correlateFindingsByCause ->
// CauseGroup.Cause) and via direct calls to dominantCause for both input
// orders, so a buggy "last one iterated wins" implementation cannot pass by
// coincidence. In a 2-member group, descriptionSimilarity is symmetric
// (sim(x,y) == sim(y,x)), so both members always score bit-identically and
// Confidence alone decides the winner.
func TestDominantCauseBreaksTwoMemberTieByConfidence(t *testing.T) {
	low := Finding{Description: "reused buffer without reinitializing state", Confidence: 0.2}
	high := Finding{Description: "state reinitializing without reused buffer", Confidence: 0.8}

	const threshold = 0.5
	if got := descriptionSimilarity(low.Description, high.Description); got <= threshold {
		t.Fatalf("similarity(low,high) = %v, want > %v (test setup invalid)", got, threshold)
	}

	groups := correlateFindingsByCause([]Finding{low, high}, threshold)
	if len(groups) != 1 {
		t.Fatalf("groups = %d, expected 1: %#v", len(groups), groups)
	}
	if groups[0].Cause != high.Description {
		t.Errorf("cause = %q, expected the higher-confidence member %q on a symmetric tie", groups[0].Cause, high.Description)
	}

	for _, order := range [][]Finding{{low, high}, {high, low}} {
		if got := dominantCause(order); got != high.Description {
			t.Errorf("dominantCause(%v) = %q, expected the higher-confidence member %q on a symmetric tie", order, got, high.Description)
		}
	}
}

// TestSelectDominantRaisesBestScoreOnTie reproduces, with exact synthetic
// ULP offsets, the bug fixed by raising bestScore at all in the tie branch
// (not just in the strict branch). A is the initial anchor. B ties with A
// (within tolerance) and has higher confidence, so B wins the tie-break; if
// bestScore stayed at A's stale score instead of following B's real score,
// it would still be A's low value here. C is far enough from A to look like
// a strict improvement over that stale anchor, but is actually within
// tolerance of B's real score and has lower confidence than B: with the
// bug, C wins outright via the strict branch against the stale anchor;
// fixed, C is correctly recognized as tied with B and loses on confidence.
func TestSelectDominantRaisesBestScoreOnTie(t *testing.T) {
	const terms = 3 // tolerance = terms * ulpsPerTerm(4) = 12 ULPs
	a := 1.0
	b := nthULPAfter(a, 6)  // 6 ULPs from A: within the 12-ULP tolerance of A
	c := nthULPAfter(a, 13) // 13 ULPs from A (looks like a strict win over the
	// stale anchor A), but only 7 ULPs from B: within tolerance of B's real
	// score, not a genuine improvement over it.
	scores := []float64{a, b, c}
	confidences := []float64{0.1, 0.9, 0.5} // B is highest, C is not

	if got := selectDominant(scores, confidences, terms); got != 1 {
		t.Fatalf("selectDominant = %d, want 1 (B: wins the tie-break with A on confidence, and C never becomes a genuine improvement over B's real score)", got)
	}
}

// TestSelectDominantNeverLowersBestScoreOnTie reproduces, with exact
// synthetic ULP offsets, the bug of raising bestScore unconditionally in the
// tie branch instead of only when the tied candidate's own score is higher.
// P0 is the initial anchor. P1 ties with P0, has the highest confidence of
// all, and a higher score, so it correctly wins the tie-break and raises
// bestScore to its own value. P2 ties with P1's real score but is itself
// lower than it and has middling confidence: with the bug, bestScore would
// be overwritten down to P2's lower value regardless of the comparison
// result. P3 also ties with P1's real score, so with the correct anchor it
// must lose to P1 on confidence — but it falls outside tolerance of the
// wrongly-lowered anchor P2, and has the lowest confidence of all: with the
// bug, P3 wins outright via the strict branch against that stale, too-low
// anchor instead of losing a tie against the true max.
func TestSelectDominantNeverLowersBestScoreOnTie(t *testing.T) {
	const terms = 3 // tolerance = terms * ulpsPerTerm(4) = 12 ULPs
	p0 := 1.0
	p1 := nthULPAfter(p0, 6) // 6 ULPs from P0: tied with P0 (<=12).
	p2 := nthULPAfter(p0, 2) // 2 ULPs from P0: tied with P1's real score
	// (|2-6|=4<=12) but lower than it.
	p3 := nthULPAfter(p0, 15) // 15 ULPs from P0: tied with P1's real score
	// (|15-6|=9<=12), so it must lose to P1 on confidence when the anchor is
	// correct. But 15 ULPs from the wrongly-lowered anchor P2 is 13 ULPs
	// (|15-2|=13>12): out of tolerance, so with the bug this looks like a
	// strict win over that stale, too-low anchor instead of a tie against
	// the true max.
	scores := []float64{p0, p1, p2, p3}
	confidences := []float64{0.1, 0.9, 0.5, 0.05} // P1 is highest, P3 is lowest

	if got := selectDominant(scores, confidences, terms); got != 1 {
		t.Fatalf("selectDominant = %d, want 1 (P1: the true max never drops to P2's lower tied score, so P3 stays tied with it and loses on confidence)", got)
	}
}

// TestSelectDominantLetsLowerScoredTiedCandidateWinOnConfidence fixes the
// core tie-break policy itself: X is processed first and becomes the
// anchor; Y ties with X (within tolerance) but has a strictly LOWER raw
// score and the higher confidence. Y must still win, because within a tie
// the score's exact value stops mattering — only confidence (then order)
// decides. A mutant that only lets the tie branch run when the candidate's
// score is also higher than the anchor (conflating "tied" with "improved")
// would incorrectly keep X here.
func TestSelectDominantLetsLowerScoredTiedCandidateWinOnConfidence(t *testing.T) {
	const terms = 3          // tolerance = terms * ulpsPerTerm(4) = 12 ULPs
	x := nthULPAfter(1.0, 6) // processed first: becomes the initial anchor.
	y := 1.0                 // 6 ULPs below X, tied with it (<=12), higher confidence.
	scores := []float64{x, y}
	confidences := []float64{0.1, 0.9} // Y is highest despite the lower score

	if got := selectDominant(scores, confidences, terms); got != 1 {
		t.Fatalf("selectDominant = %d, want 1 (Y: ties with X and wins on confidence despite a strictly lower raw score)", got)
	}
}

// TestScoresTie fixes scoresTie's exact scaling formula with deterministic,
// synthetic values. It intentionally does not attempt an end-to-end test
// through dominantCause with a real 4+ member group whose per-member
// summation order produces a genuine, non-contrived floating-point-noise
// tie: constructing real descriptionSimilarity values that differ by an
// exact, predictable number of ULPs (rather than by a realistically large,
// easily distinguishable margin) is not something that can be reliably
// authored by hand, only discovered by search — the formula itself is what
// is being fixed here, directly and deterministically.
func TestScoresTie(t *testing.T) {
	for _, tc := range []struct {
		name  string
		a, b  float64
		terms int
		want  bool
	}{
		{name: "bit identical", a: 1.5, b: 1.5, terms: 3, want: true},
		{name: "within a few ULPs for 3 terms", a: 1.0, b: nthULPAfter(1.0, 2), terms: 3, want: true},
		// terms=3 allows 3*ulpsPerTerm(4) = 12 ULPs of tolerance: exactly at
		// that boundary must still tie, one ULP past it must not.
		{name: "exactly at the scaled boundary", a: 1.0, b: nthULPAfter(1.0, 12), terms: 3, want: true},
		{name: "one ULP past the scaled boundary", a: 1.0, b: nthULPAfter(1.0, 13), terms: 3, want: false},
		// Same 10-ULP gap, but terms=2 (tolerance 8) rejects it while
		// terms=3 (tolerance 12) accepts it: this is what actually pins the
		// scaling behavior, unlike a gap both tolerances would reject alike.
		{name: "same gap ties for more terms but not fewer", a: 1.0, b: nthULPAfter(1.0, 10), terms: 2, want: false},
		{name: "same gap ties for more terms but not fewer (accepted at terms=3)", a: 1.0, b: nthULPAfter(1.0, 10), terms: 3, want: true},
		{name: "far apart at the same magnitude", a: 1.0, b: 1.0001, terms: 3, want: false},
		{name: "far apart near zero", a: 0.0, b: 1e-6, terms: 3, want: false},
		// terms=0 clamps to 1 (tolerance 4 ULPs), not 0 (which would demand
		// bit-identical values): this 4-ULP gap only ties because of that
		// clamp.
		{name: "terms clamped to 1 when non-positive", a: 1.0, b: nthULPAfter(1.0, 4), terms: 0, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := scoresTie(tc.a, tc.b, tc.terms); got != tc.want {
				t.Errorf("scoresTie(%v, %v, %d) = %v, want %v", tc.a, tc.b, tc.terms, got, tc.want)
			}
			if got := scoresTie(tc.b, tc.a, tc.terms); got != tc.want {
				t.Errorf("scoresTie(%v, %v, %d) = %v, want %v (should be symmetric)", tc.b, tc.a, tc.terms, got, tc.want)
			}
		})
	}
}

func TestCorrelateFindingsByCauseDropsUnrelatedFindings(t *testing.T) {
	for _, tc := range []struct {
		name      string
		findings  []Finding
		threshold float64
	}{
		{
			name: "unrelated descriptions",
			findings: []Finding{
				{Description: "unchecked error permits invalid configuration", Confidence: 0.5, Location: Location{File: "config.go", Simbolo: "parseConfig"}},
				{Description: "SQL injection via unsanitized user input in login handler", Confidence: 0.6, Location: Location{File: "auth.go", Simbolo: "handleLogin"}},
				{Description: "goroutine leak in background worker pool", Confidence: 0.7, Location: Location{File: "worker.go", Simbolo: "startPool"}},
			},
			threshold: 0.3,
		},
		{
			name: "similarity at threshold",
			findings: []Finding{
				{Description: "ignored parse error", Confidence: 0.5, Location: Location{File: "config.go", Simbolo: "parseConfig"}},
				{Description: "ignored validation error", Confidence: 0.6, Location: Location{File: "validate.go", Simbolo: "validate"}},
			},
			threshold: 0.5,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if groups := correlateFindingsByCause(tc.findings, tc.threshold); len(groups) != 0 {
				t.Errorf("groups = %#v, expected no correlated causes", groups)
			}
		})
	}
}

func TestCorroboratedConfidenceUsesIndependentProducersAndRepeatedMaximum(t *testing.T) {
	for _, tc := range []struct {
		name     string
		finding  Finding
		expected float64
	}{
		{
			name: "independent producers combine confidence",
			finding: Finding{EvidenceSet: &FindingEvidenceSet{Values: []FindingEvidence{
				{Producer: Producer{Agent: "one"}, Confidence: 0.6},
				{Producer: Producer{Agent: "two"}, Confidence: 0.7},
			}}},
			expected: 0.88,
		},
		{
			name: "repeated producer uses maximum confidence",
			finding: Finding{EvidenceSet: &FindingEvidenceSet{Values: []FindingEvidence{
				{Producer: Producer{Agent: "one"}, Confidence: 0.6},
				{Producer: Producer{Agent: "one"}, Confidence: 0.7},
			}}},
			expected: 0.7,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := corroboratedConfidence(tc.finding); math.Abs(got-tc.expected) > 1e-9 {
				t.Errorf("confidence = %v, expected %v", got, tc.expected)
			}
		})
	}
}
