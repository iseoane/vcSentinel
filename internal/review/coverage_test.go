package review

import (
	"strings"
	"testing"
	"time"
)

// These tests pin piece 2 of docs/design/review-flow-ownership.md: the
// coverage contract and the shared authoritative-revision selector. They are
// the failing tests written first (strict TDD).
//
// Vocabulary:
//   - AUTHORITATIVE revision: a run whose dimension plan was derived from the
//     change (legacy revisions without the coverage field also count).
//   - SUPPLEMENTARY revision: a run explicitly narrowed by the operator
//     (sentinel review --dims): recorded and visible, never authoritative,
//     never enough to call a commit reviewed, but its CRITICAL findings still
//     surface as alarms (owner decision: surface findings, never clear).

func TestCoverageClassification(t *testing.T) {
	cases := []struct {
		name     string
		coverage RevisionCoverage
		want     bool
	}{
		{"legacy (no field) classifies authoritative", "", true},
		{"explicit authoritative", CoverageAuthoritative, true},
		{"supplementary is not authoritative", CoverageSupplementary, false},
		// An unknown value is not silently authoritative: it is safer to stop
		// counting it as a verdict than to let an unrecognized writer clear one.
		{"unknown value fails closed as not authoritative", RevisionCoverage("mystery"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (Revision{Coverage: tc.coverage}).IsAuthoritative(); got != tc.want {
				t.Errorf("Revision{Coverage: %q}.IsAuthoritative() = %v, want %v", tc.coverage, got, tc.want)
			}
		})
	}
}

// TestRevisionCoveragePersistsAndRoundTrips proves the writer contract: the
// coverage a revision actually had must survive a save/read cycle, so later
// consumers can tell an authoritative run from a supplementary one.
func TestRevisionCoveragePersistsAndRoundTrips(t *testing.T) {
	for _, coverage := range []RevisionCoverage{CoverageAuthoritative, CoverageSupplementary} {
		ledger := NewLedger(t.TempDir())
		rev := Revision{At: time.Now().UTC(), Result: VerdictOK, Coverage: coverage}
		if err := ledger.SaveRevision("sha1", "msg", "bucket", "model", rev); err != nil {
			t.Fatalf("SaveRevision(%s): %v", coverage, err)
		}
		record, err := ledger.ReadRecord("sha1")
		if err != nil || record == nil || len(record.Revisions) != 1 {
			t.Fatalf("record = %+v, %v; want the saved revision", record, err)
		}
		if got := record.Revisions[0].Coverage; got != coverage {
			t.Errorf("round-tripped coverage = %q, want %q", got, coverage)
		}
	}
}

// recordWithAuthSupp is a shorthand for the piece-2 core scenario: an
// authoritative audit followed (or not) by an operator-narrowed one.
func authRevisionForTest(result string, dims ...DimensionResult) Revision {
	return Revision{At: time.Now().UTC(), Result: result, Dims: dims}
}

func supplementaryRevisionForTest(result string, findings ...Finding) Revision {
	return Revision{
		At:                 time.Now().UTC(),
		Result:             result,
		Coverage:           CoverageSupplementary,
		AggregatedFindings: findings,
	}
}

func alarmFinding(dim, sev, status, desc, fingerprint string) Finding {
	return Finding{
		Dimension:   dim,
		Severity:    sev,
		Status:      status,
		Description: desc,
		Fingerprint: fingerprint,
		Location:    Location{File: "a.go", LineStart: 1},
	}
}

func TestLastAuthoritativeRevisionSkipsSupplementary(t *testing.T) {
	authOK := authRevisionForTest(VerdictOK)
	authBlock := authRevisionForTest(VerdictBlock)
	supp := supplementaryRevisionForTest(VerdictOK)

	// Authoritative first, supplementary last: the supplementary run must not
	// become the current verdict (a narrow audit cannot clear or replace it).
	rev, idx, ok := LastAuthoritativeRevision(Record{Revisions: []Revision{authBlock, supp}})
	if !ok || rev.Result != VerdictBlock || idx != 0 {
		t.Fatalf("auth-then-supp: revision=%+v idx=%d ok=%v, want the authoritative block at 0", rev, idx, ok)
	}

	// Supplementary in the middle: the last authoritative still wins.
	rev, idx, ok = LastAuthoritativeRevision(Record{Revisions: []Revision{authBlock, supp, authOK}})
	if !ok || rev.Result != VerdictOK || idx != 2 {
		t.Fatalf("auth-supp-auth: revision=%+v idx=%d ok=%v, want the last authoritative ok at 2", rev, idx, ok)
	}
}

func TestLastAuthoritativeRevisionEmptyAndSupplementaryOnly(t *testing.T) {
	if _, _, ok := LastAuthoritativeRevision(Record{}); ok {
		t.Fatal("an empty record has no authoritative revision")
	}
	only := supplementaryRevisionForTest(VerdictBlock)
	if _, _, ok := LastAuthoritativeRevision(Record{Revisions: []Revision{only}}); ok {
		t.Fatal("a supplementary-only record has no authoritative revision: it was never enough to review the commit")
	}
}

func TestLastAuthoritativeRevisionLegacyAllCount(t *testing.T) {
	// A pre-contract record (revisions without the coverage field) keeps its
	// meaning: every revision counts as authoritative, the last one is current.
	first := authRevisionForTest(VerdictWarn)
	second := authRevisionForTest(VerdictOK)
	rev, idx, ok := LastAuthoritativeRevision(Record{Revisions: []Revision{first, second}})
	if !ok || rev.Result != VerdictOK || idx != 1 {
		t.Fatalf("legacy record: revision=%+v idx=%d ok=%v, want the last revision ok at 1", rev, idx, ok)
	}
}

func TestCurrentFindingsEmptyRecord(t *testing.T) {
	if got := CurrentFindings(Record{}); len(got) != 0 {
		t.Fatalf("CurrentFindings(empty) = %+v, want none", got)
	}
}

func TestCurrentFindingsTakeLastAuthoritativeOnly(t *testing.T) {
	older := authRevisionForTest(VerdictBlock, DimensionResult{
		Dim: DimLogic, Verdict: VerdictBlock,
		Findings: []ReviewFinding{{Dimension: DimLogic, Severity: SevCritical, Description: "stale"}},
	})
	newer := authRevisionForTest(VerdictOK)
	findings := CurrentFindings(Record{Revisions: []Revision{older, newer}})
	if len(findings) != 0 {
		t.Fatalf("CurrentFindings = %+v, want none: the newer authoritative audit superseded the older one", findings)
	}
}

// TestCurrentFindingsNarrowBlockSurfaces pins the owner decision: a narrow
// run earns the right to raise an alarm. Its CRITICAL finding surfaces even
// though the authoritative verdict stays OK.
func TestCurrentFindingsNarrowBlockSurfaces(t *testing.T) {
	authOK := authRevisionForTest(VerdictOK)
	alarm := supplementaryRevisionForTest(VerdictBlock,
		alarmFinding(DimSecurity, SevCritical, StatusConfirmed, "exposed token", "fp-alarm"))
	findings := CurrentFindings(Record{Revisions: []Revision{authOK, alarm}})
	if len(findings) != 1 || findings[0].Fingerprint != "fp-alarm" {
		t.Fatalf("CurrentFindings = %+v, want the narrow CRITICAL alarm surfaced", findings)
	}
}

// TestCurrentFindingsNarrowOkCannotClear pins the verdict half of the owner
// decision: a narrow OK adds nothing and can never clear the authoritative
// block's findings.
func TestCurrentFindingsNarrowOkCannotClear(t *testing.T) {
	authBlock := authRevisionForTest(VerdictBlock, DimensionResult{
		Dim: DimSecurity, Verdict: VerdictBlock,
		Findings: []ReviewFinding{{Dimension: DimSecurity, Severity: SevCritical, Description: "real bug", Status: StatusConfirmed}},
	})
	narrowOK := supplementaryRevisionForTest(VerdictOK)
	findings := CurrentFindings(Record{Revisions: []Revision{authBlock, narrowOK}})
	if len(findings) != 1 || findings[0].Description != "real bug" {
		t.Fatalf("CurrentFindings = %+v, want the authoritative block finding to survive the narrow OK", findings)
	}
}

func TestCurrentFindingsNarrowWarningNotSurfaced(t *testing.T) {
	authOK := authRevisionForTest(VerdictOK)
	narrowWarn := supplementaryRevisionForTest(VerdictWarn,
		alarmFinding(DimLogic, SevWarning, StatusConfirmed, "fragile", "fp-warn"))
	findings := CurrentFindings(Record{Revisions: []Revision{authOK, narrowWarn}})
	if len(findings) != 0 {
		t.Fatalf("CurrentFindings = %+v, want none: a narrow WARNING has no teeth (surface findings, never clear)", findings)
	}
}

// TestCurrentFindingsAlarmSupersededByLaterAuthoritativeSameDim: a full
// (authoritative) re-audit that covers the alarmed dimension is the only
// authority that can clear its alarm.
func TestCurrentFindingsAlarmSupersededByLaterAuthoritativeSameDim(t *testing.T) {
	authOK := authRevisionForTest(VerdictOK)
	alarm := supplementaryRevisionForTest(VerdictBlock,
		alarmFinding(DimSecurity, SevCritical, StatusConfirmed, "exposed token", "fp-alarm"))
	authReaudit := authRevisionForTest(VerdictOK,
		DimensionResult{Dim: DimSecurity, Verdict: VerdictOK},
		DimensionResult{Dim: DimLogic, Verdict: VerdictOK})
	findings := CurrentFindings(Record{Revisions: []Revision{authOK, alarm, authReaudit}})
	if len(findings) != 0 {
		t.Fatalf("CurrentFindings = %+v, want none: the authoritative re-audit covered security", findings)
	}
}

// TestCurrentFindingsAlarmSurvivesAuthoritativeThatSkipsDim: a full re-audit
// whose derived plan did not include the alarmed dimension cannot clear it.
func TestCurrentFindingsAlarmSurvivesAuthoritativeThatSkipsDim(t *testing.T) {
	authOK := authRevisionForTest(VerdictOK)
	alarm := supplementaryRevisionForTest(VerdictBlock,
		alarmFinding(DimSecurity, SevCritical, StatusConfirmed, "exposed token", "fp-alarm"))
	authReaudit := authRevisionForTest(VerdictOK,
		DimensionResult{Dim: DimLogic, Verdict: VerdictOK}) // plan skipped security
	findings := CurrentFindings(Record{Revisions: []Revision{authOK, alarm, authReaudit}})
	if len(findings) != 1 || findings[0].Fingerprint != "fp-alarm" {
		t.Fatalf("CurrentFindings = %+v, want the security alarm to survive a re-audit that skipped security", findings)
	}
}

func TestCurrentFindingsSupplementaryOnlyRecord(t *testing.T) {
	alarm := supplementaryRevisionForTest(VerdictBlock,
		alarmFinding(DimSecurity, SevCritical, StatusConfirmed, "exposed token", "fp-alarm"))
	findings := CurrentFindings(Record{Revisions: []Revision{alarm}})
	if len(findings) != 1 || findings[0].Fingerprint != "fp-alarm" {
		t.Fatalf("CurrentFindings = %+v, want the alarm of a supplementary-only record surfaced", findings)
	}
}

// TestCurrentFindingsKeepsDuplicateAlarms: two narrow runs re-raising the same
// defect are NOT collapsed — the domain contract treats duplicate fingerprints
// as ambiguous and the disposition commands fail closed rather than guessing
// which occurrence the human meant (ApplyDispositions still clears every
// matching fingerprint for blockers/risks).
func TestCurrentFindingsKeepsDuplicateAlarms(t *testing.T) {
	authOK := authRevisionForTest(VerdictOK)
	first := supplementaryRevisionForTest(VerdictBlock,
		alarmFinding(DimSecurity, SevCritical, StatusConfirmed, "exposed token", "fp-alarm"))
	second := supplementaryRevisionForTest(VerdictBlock,
		alarmFinding(DimSecurity, SevCritical, StatusConfirmed, "exposed token", "fp-alarm"))
	findings := CurrentFindings(Record{Revisions: []Revision{authOK, first, second}})
	if len(findings) != 2 {
		t.Fatalf("CurrentFindings = %+v, want the duplicate alarms kept (ambiguity is resolved by failing closed, not guessed)", findings)
	}
	record := Record{Revisions: []Revision{authOK, first, second}}
	if _, err := ResolveRecordDispositionTarget(record, "fp-alarm"); err == nil {
		t.Fatal("a fingerprint matching more than one finding must fail closed as ambiguous")
	}
}

func TestResolveRecordDispositionTargetFindsAlarm(t *testing.T) {
	authOK := authRevisionForTest(VerdictOK)
	alarm := supplementaryRevisionForTest(VerdictBlock,
		alarmFinding(DimSecurity, SevCritical, StatusConfirmed, "exposed token", "fp-alarm"))
	record := Record{Revisions: []Revision{authOK, alarm}}

	target, err := ResolveRecordDispositionTarget(record, "fp-alarm")
	if err != nil {
		t.Fatalf("a narrow CRITICAL must be addressable by a human disposition: %v", err)
	}
	if target.Fingerprint != "fp-alarm" || !IsBlocking(target.Severity, target.Status) {
		t.Fatalf("target = %+v, want the surfaced alarm finding", target)
	}

	if _, err := ResolveRecordDispositionTarget(record, "fp-absent"); err == nil {
		t.Fatal("an absent fingerprint must fail closed")
	}

	refuted := []FindingDisposition{{
		SHA: "abc", Fingerprint: "fp-alarm", Status: StatusRefuted,
		Actor: RefutationActorHuman, Source: DispositionSourceHuman,
	}}
	effective, err := ResolveRecordDispositionTargetWithDispositions(record, "fp-alarm", refuted)
	if err != nil {
		t.Fatalf("resolving with dispositions failed: %v", err)
	}
	if NormalizeStatus(effective.Status) != StatusRefuted {
		t.Fatalf("effective status = %q, want refuted (the human answer overlaid the alarm)", effective.Status)
	}
}

func TestRecordBlockStateIncludesAlarms(t *testing.T) {
	authOK := authRevisionForTest(VerdictOK)
	alarm := supplementaryRevisionForTest(VerdictBlock,
		alarmFinding(DimSecurity, SevCritical, StatusConfirmed, "exposed token", "fp-alarm"))
	if !RecordHasActiveBlock(Record{Revisions: []Revision{authOK, alarm}}) {
		t.Fatal("an active narrow alarm must count as a currently-blocking state (fix retirement)")
	}
	if RecordHasActiveBlock(Record{Revisions: []Revision{authOK}}) {
		t.Fatal("a clean authoritative record is not blocking")
	}
	if RecordHasActiveBlock(Record{}) {
		t.Fatal("an empty record is not blocking")
	}
}

func TestRendererMatrixSupplementaryCannotClear(t *testing.T) {
	record := Record{SHA: "945b5b5", Message: "feat(config): commands", Model: "m",
		Revisions: []Revision{
			authRevisionForTest(VerdictBlock, DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock}),
			supplementaryRevisionForTest(VerdictOK),
		},
	}
	out := RenderMatrix([]Record{record})
	if !strings.Contains(out, "🚨") {
		t.Errorf("a narrow OK run must not clear the authoritative block in the matrix:\n%s", out)
	}
	if strings.Contains(out, "✅ (rev") {
		t.Errorf("a narrow OK is not a recovery from block; no cleared marker expected:\n%s", out)
	}
}

func TestRendererMatrixSupplementaryOnlyRendersNoVerdict(t *testing.T) {
	record := Record{SHA: "945b5b5", Message: "feat(config): commands", Model: "m",
		Revisions: []Revision{
			supplementaryRevisionForTest(VerdictOK),
		},
	}
	out := RenderMatrix([]Record{record})
	row := "| `945b5b5` feat(config): commands | — | — | — | — | — | — |"
	if !strings.Contains(out, row) {
		t.Errorf("a supplementary-only record has no authoritative verdict; cells must be '—':\n%s\nwant row: %s", out, row)
	}
	if strings.Contains(out, "✅") {
		t.Errorf("a supplementary OK must not render as an audited dimension:\n%s", out)
	}
}

func TestRendererMatrixRecoveredMarkerCountsClearingRevision(t *testing.T) {
	// block (authoritative), ok (authoritative), ok (supplementary): the
	// cleared marker names the AUTHORITATIVE revision that cleared (rev 2),
	// not the raw last entry.
	record := Record{SHA: "945b5b5", Message: "feat(config): commands", Model: "m",
		Revisions: []Revision{
			authRevisionForTest(VerdictBlock, DimensionResult{Dim: DimSpec, Verdict: VerdictBlock}),
			authRevisionForTest(VerdictOK, DimensionResult{Dim: DimSpec, Verdict: VerdictOK}),
			supplementaryRevisionForTest(VerdictOK),
		},
	}
	out := RenderMatrix([]Record{record})
	if !strings.Contains(out, "✅ (rev 2 — CRITICAL cleared)") {
		t.Errorf("the cleared marker must point at the authoritative clearing revision:\n%s", out)
	}
}

func TestRendererSummaryCountsAuthoritativeVerdicts(t *testing.T) {
	records := []Record{
		{SHA: "aaaa", Message: "auth block", Revisions: []Revision{
			authRevisionForTest(VerdictBlock, DimensionResult{Dim: DimLogic, Verdict: VerdictBlock}),
			supplementaryRevisionForTest(VerdictOK),
		}},
		{SHA: "bbbb", Message: "supp only", Revisions: []Revision{
			supplementaryRevisionForTest(VerdictOK),
		}},
	}
	out := RenderSummary(records, nil)
	if !strings.Contains(out, "🟢 ok: 0 · 🟡 warn: 0 · 🚨 block: 1") {
		t.Errorf("verdict counts must follow the authoritative verdicts only:\n%s", out)
	}
	if strings.Contains(out, "`bbbb`") {
		t.Errorf("a supplementary-only record has no authoritative verdict and must not render a summary row:\n%s", out)
	}
}

func TestBranchBlockersSurfaceNarrowAlarmAndHonourRefutation(t *testing.T) {
	record := Record{SHA: "aaaa1111", Message: "feat(x): thing", Revisions: []Revision{
		authRevisionForTest(VerdictOK),
		supplementaryRevisionForTest(VerdictBlock,
			alarmFinding(DimSecurity, SevCritical, StatusConfirmed, "exposed token", "fp-alarm")),
	}}

	blockers := BranchBlockers([]Record{record})
	if len(blockers) != 1 || len(blockers[0].Description) == 0 || blockers[0].Description != "exposed token" {
		t.Fatalf("BranchBlockers = %+v, want the narrow CRITICAL blocking pr create", blockers)
	}

	dispositions := []FindingDisposition{{
		SHA: "aaaa1111", Fingerprint: "fp-alarm", Status: StatusRefuted,
		Actor: RefutationActorHuman, Source: DispositionSourceHuman,
	}}
	after := BranchBlockersWithDispositions([]Record{record}, dispositions)
	if len(after) != 0 {
		t.Fatalf("BranchBlockers after a human refutation = %+v, want none", after)
	}
}

func TestVerdictDeBranchIgnoresSupplementaryOnlyRecords(t *testing.T) {
	records := []Record{
		{SHA: "aaaa", Message: "supp only", Revisions: []Revision{
			supplementaryRevisionForTest(VerdictBlock,
				alarmFinding(DimSecurity, SevCritical, StatusConfirmed, "x", "fp-x")),
		}},
		{SHA: "bbbb", Message: "auth ok", Revisions: []Revision{
			authRevisionForTest(VerdictOK),
		}},
	}
	if got := VerdictDeBranch(records, nil); got != VerdictOK {
		t.Fatalf("VerdictDeBranch = %q, want ok: the branch verdict reads authoritative results only", got)
	}
}
