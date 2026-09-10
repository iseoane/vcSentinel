package review

import (
	"errors"
	"fmt"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLedgerSaveAndReadRecord(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	rev := Revision{At: time.Now().UTC(), Result: VerdictOK, Dims: []DimensionResult{{Dim: DimSpec, Verdict: VerdictOK}}}
	if err := ledger.SaveRevision("abc123", "feat(x): thing", "backend", "test-model", rev); err != nil {
		t.Fatalf("SaveRevision returned error: %v", err)
	}

	record, err := ledger.ReadRecord("abc123")
	if err != nil {
		t.Fatalf("ReadRecord returned error: %v", err)
	}
	if record == nil {
		t.Fatal("ReadRecord returned nil for an existing record")
	}
	if record.SHA != "abc123" || record.Message != "feat(x): thing" || record.Bucket != "backend" || record.Model != "test-model" {
		t.Errorf("record = %+v, does not match what was saved", record)
	}
	if len(record.Revisions) != 1 || record.Revisions[0].Result != VerdictOK {
		t.Errorf("revisions = %+v, want 1 with result ok", record.Revisions)
	}
}

func TestLedgerRevisionsAppend(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	rev1 := Revision{At: time.Now().UTC(), Result: VerdictBlock}
	rev2 := Revision{At: time.Now().UTC(), Result: VerdictOK}
	if err := ledger.SaveRevision("def456", "msg", "", "", rev1); err != nil {
		t.Fatalf("first revision: %v", err)
	}
	if err := ledger.SaveRevision("def456", "msg", "", "", rev2); err != nil {
		t.Fatalf("second revision: %v", err)
	}

	record, err := ledger.ReadRecord("def456")
	if err != nil {
		t.Fatalf("ReadRecord returned error: %v", err)
	}
	if len(record.Revisions) != 2 {
		t.Fatalf("revisions = %d, want 2 (append, not overwritten)", len(record.Revisions))
	}
	if record.Revisions[0].Result != VerdictBlock || record.Revisions[1].Result != VerdictOK {
		t.Errorf("the append order was not preserved: %+v", record.Revisions)
	}
}

// TestLedgerPersistsAggregatedFindings verifies that Revision.AggregatedFindings
// (T6.5) round-trips through the JSON ledger file: it is the aggregated
// review.AuditCommit result (AuditResult.Findings), not the raw
// per-dimension DimensionResult.Findings already covered by Dims, and the
// renderer needs it to survive persistence to render it later. Location and
// EvidenceSet (with several FindingEvidence) round-trip too: they are the
// "fused evidence" the commit this test guards is actually named after, and
// renderMergedFinding consumes both directly (T6.5 review finding: tests).
func TestLedgerPersistsAggregatedFindings(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	location := Location{File: "auth.go", LineStart: 42, LineEnd: 44, Simbolo: "checkToken"}
	evidences := []FindingEvidence{
		{Dimension: DimSecurity, Evidence: "token == expected", Confidence: 0.8},
		{Dimension: DimLogic, Evidence: "no hmac.Equal usage found", Confidence: 0.95},
	}
	rev := Revision{
		At:     time.Now().UTC(),
		Result: VerdictBlock,
		AggregatedFindings: []Finding{
			{
				Dimension:   DimSecurity,
				Severity:    SevCritical,
				Source:      SourceReview,
				Confidence:  0.9,
				Description: "exposed secret",
				Location:    location,
				EvidenceSet: &FindingEvidenceSet{Values: evidences},
			},
		},
	}
	if err := ledger.SaveRevision("aggfind1", "feat(x): thing", "", "", rev); err != nil {
		t.Fatalf("SaveRevision returned error: %v", err)
	}

	record, err := ledger.ReadRecord("aggfind1")
	if err != nil {
		t.Fatalf("ReadRecord returned error: %v", err)
	}
	if len(record.Revisions) != 1 || len(record.Revisions[0].AggregatedFindings) != 1 {
		t.Fatalf("AggregatedFindings did not round-trip: %+v", record.Revisions)
	}
	got := record.Revisions[0].AggregatedFindings[0]
	if got.Source != SourceReview || got.Confidence != 0.9 || got.Description != "exposed secret" {
		t.Errorf("AggregatedFindings[0] = %+v, values changed across persistence", got)
	}
	if got.Location != location {
		t.Errorf("Location did not round-trip: got %+v, want %+v", got.Location, location)
	}
	if got.EvidenceSet == nil || len(got.EvidenceSet.Values) != len(evidences) {
		t.Fatalf("EvidenceSet did not round-trip: %+v", got.EvidenceSet)
	}
	for i, expected := range evidences {
		if got.EvidenceSet.Values[i] != expected {
			t.Errorf("EvidenceSet.Values[%d] = %+v, want %+v", i, got.EvidenceSet.Values[i], expected)
		}
	}
}

// TestRevisionEffectiveFindingsPrefersAggregated: when AggregatedFindings is
// non-empty, EffectiveFindings returns it as-is and ignores Dims entirely.
// It is the single selection point riesgos() and BranchBlockers must both
// consume (T6.5 review finding: design — before this method existed,
// BranchBlockers read only Dims and could still block on a semantic
// finding T6.2 had already superseded by a deterministic one).
func TestRevisionEffectiveFindingsPrefersAggregated(t *testing.T) {
	rev := Revision{
		AggregatedFindings: []Finding{{Dimension: DimSecurity, Severity: SevCritical, Description: "aggregated"}},
		Dims: []DimensionResult{{Dim: DimSpec, Findings: []ReviewFinding{
			{Dimension: DimSpec, Severity: SevWarning, Description: "must be ignored"},
		}}},
	}
	got := rev.EffectiveFindings()
	if len(got) != 1 || got[0].Description != "aggregated" {
		t.Errorf("EffectiveFindings() = %+v, expected only AggregatedFindings", got)
	}
}

// TestRevisionEffectiveFindingsConvertsDimsWithoutAggregated: with no
// AggregatedFindings, EffectiveFindings converts every raw v1 ReviewFinding
// from Dims to the v2 Finding shape, so callers get one uniform type
// regardless of a Revision's origin (a Revision saved before T6.5, or by a
// caller that never propagated AggregatedFindings).
func TestRevisionEffectiveFindingsConvertsDimsWithoutAggregated(t *testing.T) {
	rev := Revision{
		Dims: []DimensionResult{{
			Dim: DimSpec,
			Findings: []ReviewFinding{
				{Dimension: DimSpec, File: "a.go", Line: 7, Severity: SevWarning, Description: "converted"},
			},
		}},
	}
	got := rev.EffectiveFindings()
	if len(got) != 1 {
		t.Fatalf("EffectiveFindings() = %d findings, expected 1", len(got))
	}
	if got[0].Dimension != DimSpec || got[0].Severity != SevWarning || got[0].Description != "converted" ||
		got[0].Location.File != "a.go" || got[0].Location.LineStart != 7 {
		t.Errorf("EffectiveFindings() converted = %+v, values lost in conversion", got[0])
	}
}

// TestRevisionEffectiveFindingsNeverCarriesLegacySourceWithoutConfidence:
// a v1 ReviewFinding can have Source populated (e.g. SourceReview, stamped
// by the T5.7 critical-refutation path at engine.go:305) without ever
// having had a real Confidence — v1 has no such field. If
// findingFromReviewFinding copied that Source as-is, the converted
// Finding would pass renderMergedFinding's "h.Source != \"\"" gate and
// render a fabricated "(review, confidence 0.00)" — exactly the datum T6.5
// omits the whole segment to avoid (T6.5bis review finding: logic WARNING).
// So the conversion must never carry a Source without its matching real
// Confidence: EffectiveFindings leaves Source empty for every Dims-derived
// Finding, regardless of what the underlying ReviewFinding.Source held.
func TestRevisionEffectiveFindingsNeverCarriesLegacySourceWithoutConfidence(t *testing.T) {
	rev := Revision{
		Dims: []DimensionResult{{
			Dim: DimSecurity,
			Findings: []ReviewFinding{
				{Dimension: DimSecurity, File: "a.go", Line: 7, Severity: SevCritical, Description: "refuted", Source: SourceReview},
			},
		}},
	}
	got := rev.EffectiveFindings()
	if len(got) != 1 {
		t.Fatalf("EffectiveFindings() = %d findings, expected 1", len(got))
	}
	if got[0].Source != "" {
		t.Errorf("EffectiveFindings()[0].Source = %q, want \"\" (no real Confidence backs it)", got[0].Source)
	}
}

func TestLedgerMissingRecord(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	record, err := ledger.ReadRecord("noexiste")
	if err != nil {
		t.Fatalf("ReadRecord returned error: %v", err)
	}
	if record != nil {
		t.Error("ReadRecord should return nil for a SHA without a record")
	}
}

func TestLedgerCorruptFileIsError(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	path := ledger.RecordPath("abc123")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := ledger.ReadRecord("abc123"); err == nil {
		t.Error("a corrupt file should return an error, not nil")
	}
}

func TestLedgerDeleteMissingRecordIsNoOp(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)
	if err := ledger.DeleteRecord("ffffffffffffffffffffffffffffffffffffffff"); err != nil {
		t.Fatalf("DeleteRecord of a missing record returned error: %v", err)
	}
}

func TestLedgerPurgeOrphans(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	rev := Revision{At: time.Now().UTC(), Result: VerdictOK}
	for _, sha := range []string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	} {
		if err := ledger.SaveRevision(sha, "feat(x): thing", "backend", "test-model", rev); err != nil {
			t.Fatalf("SaveRevision(%s) returned error: %v", sha, err)
		}
	}

	// Without a Git repository behind it, cat-file fails and both records are
	// orphans: the purge must empty the ledger.
	deleted, err := ledger.PurgeOrphans(func(sha string) (bool, error) { return git.ContentInSomeRef(sha), nil })
	if err != nil {
		t.Fatalf("PurgeOrphans returned error: %v", err)
	}
	if len(deleted) != 2 {
		t.Errorf("PurgeOrphans deleted %d records, want 2", len(deleted))
	}
	remaining, err := ledger.ListRecords()
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("after purging %d records remain, want 0: %v", len(remaining), remaining)
	}
}

// TestLedgerPurgeOrphansDangling covers the real case: a commit rewritten
// with amend keeps existing in the object store as dangling, but is no
// longer reachable from any ref. ContentInSomeRef detects it and the record
// is purged; a live commit is kept.
func TestLedgerPurgeOrphansDangling(t *testing.T) {
	repo := t.TempDir()
	runGitIn := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v returned error: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}

	runGitIn("init", "-q")
	runGitIn("config", "user.email", "test@local")
	runGitIn("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitIn("add", "a.txt")
	runGitIn("commit", "-q", "-m", "first")
	liveSHA := runGitIn("rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("one\ntwo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitIn("add", "a.txt")
	runGitIn("commit", "-q", "-m", "second")
	danglingSHA := runGitIn("rev-parse", "HEAD")
	// Amend rewrites the commit: the previous SHA is left dangling but is
	// still a valid object in the store.
	runGitIn("commit", "--amend", "-q", "-m", "second fixed")
	if liveSHA == danglingSHA {
		t.Fatal("the SHAs must not match")
	}

	// The ledger points at the real repo so ContentInSomeRef resolves
	// against its refs.
	t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
	t.Setenv("GIT_WORK_TREE", repo)
	ledger := NewLedger(t.TempDir())
	rev := Revision{At: time.Now().UTC(), Result: VerdictOK}
	for _, sha := range []string{liveSHA, danglingSHA} {
		if err := ledger.SaveRevision(sha, "msg", "backend", "model", rev); err != nil {
			t.Fatalf("SaveRevision(%s) returned error: %v", sha, err)
		}
	}

	deleted, err := ledger.PurgeOrphans(func(sha string) (bool, error) { return git.ContentInSomeRef(sha), nil })
	if err != nil {
		t.Fatalf("PurgeOrphans returned error: %v", err)
	}
	if len(deleted) != 1 || deleted[0] != danglingSHA {
		t.Errorf("PurgeOrphans deleted %v, want only %s (the dangling one)", deleted, danglingSHA)
	}
	if record, _ := ledger.ReadRecord(liveSHA); record == nil {
		t.Errorf("the record of the live commit %s should not have been purged", liveSHA)
	}
}

func TestLedgerAtomicWrite(temp *testing.T) {
	// Several consecutive writes never leave a half-written file: at the end
	// there is always valid JSON with the last revision.
	temp.Run("sequential", func(t *testing.T) {
		dir := t.TempDir()
		ledger := NewLedger(dir)
		for i := 0; i < 20; i++ {
			rev := Revision{At: time.Now().UTC(), Result: VerdictOK}
			if err := ledger.SaveRevision("sha1", "m", "", "", rev); err != nil {
				t.Fatalf("write %d: %v", i, err)
			}
		}
		record, err := ledger.ReadRecord("sha1")
		if err != nil {
			t.Fatalf("after 20 writes the file ended up corrupt: %v", err)
		}
		if len(record.Revisions) != 20 {
			t.Errorf("revisions = %d, want 20", len(record.Revisions))
		}
	})
}

func TestLedgerMarkFixed(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	rev := Revision{At: time.Now().UTC(), Result: VerdictBlock}
	if err := ledger.SaveRevision("aaa111", "feat(x): with bug", "backend", "m", rev); err != nil {
		t.Fatalf("SaveRevision returned error: %v", err)
	}

	if err := ledger.MarkFixed("aaa111", "bbb222"); err != nil {
		t.Fatalf("MarkFixed returned error: %v", err)
	}
	record, err := ledger.ReadRecord("aaa111")
	if err != nil {
		t.Fatalf("ReadRecord returned error: %v", err)
	}
	if record.FixedIn != "bbb222" {
		t.Errorf("FixedIn = %q, want bbb222", record.FixedIn)
	}

	// The first fix wins: it is not overwritten by a later one.
	if err := ledger.MarkFixed("aaa111", "ccc333"); err != nil {
		t.Fatalf("second MarkFixed returned error: %v", err)
	}
	record, _ = ledger.ReadRecord("aaa111")
	if record.FixedIn != "bbb222" {
		t.Errorf("FixedIn = %q, want bbb222 to win", record.FixedIn)
	}
}

// TestLedgerAdoptRecord covers the T2.7 fix: adopting the record of a source
// SHA under a new destination SHA must preserve Message/Bucket/Model and
// ALL revisions (including the real findings), recoverable with
// ReadRecord(to).
func TestLedgerAdoptRecord(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	rev := Revision{
		At:     time.Now().UTC(),
		Result: VerdictWarn,
		Dims: []DimensionResult{{
			Dim:     DimLogic,
			Verdict: VerdictWarn,
			Findings: []ReviewFinding{{
				Dimension:   DimLogic,
				File:        "b.txt",
				Severity:    SevWarning,
				Description: "real test finding",
			}},
		}},
	}
	if err := ledger.SaveRevision("sha-old", "feat(b): thing", "pr", "test-model", rev); err != nil {
		t.Fatalf("SaveRevision: %v", err)
	}

	if err := ledger.AdoptRecord("sha-old", "sha-new"); err != nil {
		t.Fatalf("AdoptRecord: %v", err)
	}

	adopted, err := ledger.ReadRecord("sha-new")
	if err != nil {
		t.Fatalf("ReadRecord(sha-new): %v", err)
	}
	if adopted == nil {
		t.Fatal("ReadRecord(sha-new) returned nil after AdoptRecord")
	}
	if adopted.SHA != "sha-new" || adopted.Message != "feat(b): thing" || adopted.Bucket != "pr" || adopted.Model != "test-model" {
		t.Errorf("adopted record = %+v, does not match the origin one", adopted)
	}
	if len(adopted.Revisions) != 1 || len(adopted.Revisions[0].Dims) != 1 || len(adopted.Revisions[0].Dims[0].Findings) != 1 {
		t.Fatalf("adopted revisions = %+v, want the real finding intact", adopted.Revisions)
	}
	if adopted.Revisions[0].Dims[0].Findings[0].Description != "real test finding" {
		t.Errorf("adopted finding = %+v, want the original description preserved", adopted.Revisions[0].Dims[0].Findings[0])
	}

	// The origin record still exists: adopting is not moving.
	if origin, _ := ledger.ReadRecord("sha-old"); origin == nil {
		t.Error("the origin record should not disappear when adopted")
	}
}

func TestMergeReusedSpecRevisionDoesNotAliasSpecSlices(t *testing.T) {
	specDims := make([]DimensionResult, 1, 2)
	specDims[0] = DimensionResult{Dim: DimSpec, Verdict: VerdictOK}
	specFindings := make([]Finding, 1, 2)
	specFindings[0] = Finding{Dimension: DimSpec, Title: "fresh spec finding"}
	spec := Revision{Dims: specDims, AggregatedFindings: specFindings}
	base := Revision{
		Dims:               []DimensionResult{{Dim: DimLogic, Verdict: VerdictOK}},
		AggregatedFindings: []Finding{{Dimension: DimLogic, Title: "reused logic finding"}},
	}

	merged, err := mergeReusedSpecRevision(base, spec)
	if err != nil {
		t.Fatalf("mergeReusedSpecRevision: %v", err)
	}
	if got := spec.Dims[:cap(spec.Dims)][1].Dim; got != "" {
		t.Errorf("spec Dims spare capacity was modified to %q", got)
	}
	if got := spec.AggregatedFindings[:cap(spec.AggregatedFindings)][1].Dimension; got != "" {
		t.Errorf("spec AggregatedFindings spare capacity was modified to %q", got)
	}
	if len(merged.Dims) != 2 || len(merged.AggregatedFindings) != 2 {
		t.Errorf("merged revision = %+v, want fresh spec and reused logic entries", merged)
	}
}

// TestLedgerAdoptRecordWithMessageRecordsOriginAndDestinationMessage verifies
// that adoption preserves both the source provenance and the destination message.
func TestMergeReusedSpecRevisionKeepsOnlySpecFindingsAndMergedFixedState(t *testing.T) {
	base := Revision{
		Result:             VerdictBlock,
		Dims:               []DimensionResult{{Dim: DimLogic, Verdict: VerdictBlock}},
		AggregatedFindings: []Finding{{Dimension: DimLogic, Title: "preserved blocker"}},
	}
	spec := Revision{
		Result: VerdictOK,
		Dims:   []DimensionResult{{Dim: DimSpec, Verdict: VerdictOK}},
		AggregatedFindings: []Finding{
			{Dimension: DimSpec, Title: "fresh spec finding"},
			{Dimension: DimSecurity, Title: "must not enter spec reuse"},
		},
	}

	merged, err := mergeReusedSpecRevision(base, spec)
	if err != nil {
		t.Fatalf("mergeReusedSpecRevision: %v", err)
	}
	if merged.Result != VerdictBlock {
		t.Fatalf("merged Result = %q, want preserved blocking logic verdict", merged.Result)
	}
	if merged.Fixed {
		t.Fatal("a still-blocking merged revision must not claim it fixed the prior block")
	}
	for _, finding := range merged.AggregatedFindings {
		if finding.Title == "must not enter spec reuse" {
			t.Fatalf("merged findings include a non-spec finding from the spec-only revision: %+v", merged.AggregatedFindings)
		}
	}
}

func TestLedgerAdoptRecordWithMessageRecordsOriginAndDestinationMessage(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)
	if err := ledger.SaveRevision("sha-old", "feat(old): original", "pr", "model", Revision{At: time.Now(), Result: VerdictOK}); err != nil {
		t.Fatalf("SaveRevision: %v", err)
	}

	if err := ledger.AdoptRecordWithMessage("sha-old", "sha-new", "feat(new): rewritten message"); err != nil {
		t.Fatalf("AdoptRecordWithMessage: %v", err)
	}

	adopted, err := ledger.ReadRecord("sha-new")
	if err != nil {
		t.Fatalf("ReadRecord(sha-new): %v", err)
	}
	if adopted == nil {
		t.Fatal("ReadRecord(sha-new) returned nil after adoption")
	}
	if adopted.Message != "feat(new): rewritten message" {
		t.Errorf("Message = %q, want destination message", adopted.Message)
	}
	if adopted.OriginSHA != "sha-old" {
		t.Errorf("OriginSHA = %q, want sha-old", adopted.OriginSHA)
	}
	origin, err := ledger.ReadRecord("sha-old")
	if err != nil {
		t.Fatalf("ReadRecord(sha-old): %v", err)
	}
	if origin.Message != "feat(old): original" || origin.OriginSHA != "" {
		t.Errorf("origin changed during adoption: %+v", origin)
	}
}

func TestLedgerAdoptRecordWithMessagePreservesDestinationRevisions(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)
	sourceRevision := Revision{
		At: time.Now().UTC(), Result: VerdictOK,
		Dims: []DimensionResult{{Dim: DimLogic, Verdict: VerdictOK}},
	}
	if err := ledger.SaveRevision("sha-old", "feat(old): original", "pr", "model", sourceRevision); err != nil {
		t.Fatalf("SaveRevision(source): %v", err)
	}
	if err := ledger.AdoptRecordWithMessage("sha-old", "sha-new", "feat(new): rewritten message"); err != nil {
		t.Fatalf("first adoption: %v", err)
	}
	appended := Revision{
		At: time.Now().UTC(), Result: VerdictBlock,
		Dims: []DimensionResult{{Dim: DimSpec, Verdict: VerdictBlock}},
	}
	if err := ledger.SaveRevision("sha-new", "feat(new): rewritten message", "", "model", appended); err != nil {
		t.Fatalf("SaveRevision(destination): %v", err)
	}
	if err := ledger.AdoptRecordWithMessage("sha-old", "sha-new", "feat(new): rewritten message"); err != nil {
		t.Fatalf("repeated adoption: %v", err)
	}

	destination, err := ledger.ReadRecord("sha-new")
	if err != nil {
		t.Fatalf("ReadRecord(destination): %v", err)
	}
	if destination == nil || len(destination.Revisions) != 2 {
		t.Fatalf("destination revisions = %+v, want source and appended revisions preserved", destination)
	}
	if destination.Revisions[1].Result != VerdictBlock || destination.Message != "feat(new): rewritten message" || destination.OriginSHA != "sha-old" {
		t.Fatalf("destination metadata/revisions = %+v, want appended revision, destination message, and source provenance", destination)
	}
}

// TestLedgerAdoptRecordMissingSourceSHAIsError verifies that callers that bypass
// DecideBlobReuse still receive an error for an absent adoption source.
func TestLedgerAdoptRecordMissingSourceSHAIsError(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	if err := ledger.AdoptRecord("missing", "sha-new"); err == nil {
		t.Error("AdoptRecord of a source SHA without a record should return an error")
	}
	if record, _ := ledger.ReadRecord("sha-new"); record != nil {
		t.Error("a failing AdoptRecord should not create any destination record")
	}
}

// TestLedgerAdoptRecordIsIdempotent: calling it twice with the same
// arguments does not fail nor duplicate anything weird, it just overwrites
// with the same content.
func TestLedgerAdoptRecordIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	rev := Revision{At: time.Now().UTC(), Result: VerdictOK}
	if err := ledger.SaveRevision("sha-old", "feat(x)", "pr", "m", rev); err != nil {
		t.Fatalf("SaveRevision: %v", err)
	}

	if err := ledger.AdoptRecord("sha-old", "sha-new"); err != nil {
		t.Fatalf("first adoption: %v", err)
	}
	if err := ledger.AdoptRecord("sha-old", "sha-new"); err != nil {
		t.Fatalf("second adoption (idempotent): %v", err)
	}

	adopted, err := ledger.ReadRecord("sha-new")
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	if adopted == nil || len(adopted.Revisions) != 1 {
		t.Errorf("record after adopting twice = %+v, want 1 revision (no duplication)", adopted)
	}
}

func TestLedgerMarkFixedWithoutRecordIsNoOp(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)
	if err := ledger.MarkFixed("noexiste", "bbb222"); err != nil {
		t.Fatalf("MarkFixed on a SHA without a record returned error: %v", err)
	}
}

// TestPurgeOrphansAbortsWhenTheCriterionFails pins the ledger's own half of
// the contract, which the migrated tests do not reach: they wrap a helper that
// converts every git error into false and then force a nil error, so a
// regression that swallowed query failures would still pass there.
//
// The contract is that a question the purge cannot answer never authorises a
// deletion, and that what was already removed is reported rather than lost, so
// the caller knows the ledger is half-purged instead of assuming nothing
// happened.
func TestPurgeOrphansAbortsWhenTheCriterionFails(t *testing.T) {
	ledger := NewLedger(t.TempDir())
	// ListRecords sorts, so these names fix the traversal order: the orphan is
	// visited first, the unresolvable one second, and the third exists only to
	// prove the purge stopped. Without it an implementation could return the
	// failure, preserve the SHA it could not resolve, and delete everything
	// after it while satisfying every other assertion.
	orphaned := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	unreadable := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	later := "cccccccccccccccccccccccccccccccccccccccc"
	for _, sha := range []string{orphaned, unreadable, later} {
		if err := ledger.SaveRevision(sha, "fixture", "b", "m", Revision{At: time.Now(), Result: "ok"}); err != nil {
			t.Fatal(err)
		}
	}

	// The order the predicate is actually asked in is recorded, so a change in
	// traversal fails here instead of quietly weakening the test.
	var queried []string
	failure := errors.New("the object store cannot be read")
	deleted, err := ledger.PurgeOrphans(func(sha string) (bool, error) {
		queried = append(queried, sha)
		if sha == unreadable {
			return false, failure
		}
		return false, nil
	})
	if !slices.Equal(queried, []string{orphaned, unreadable}) {
		t.Fatalf("the predicate was asked for %v, want exactly %v; the purge did not stop at the failure",
			queried, []string{orphaned, unreadable})
	}

	if !errors.Is(err, failure) {
		t.Fatalf("PurgeOrphans() error = %v, want the predicate's failure; a query that cannot be answered must not authorise a deletion", err)
	}
	if record, lerr := ledger.ReadRecord(unreadable); lerr != nil || record == nil {
		t.Errorf("the record whose existence could not be resolved was deleted (record=%v, err=%v)", record, lerr)
	}
	// Whatever was already removed must come back with the error: the ledger is
	// half-purged, and a caller told only "it failed" would believe otherwise.
	// The whole list is asserted, not just membership: a purge that reported the
	// SHA it could not resolve, or one ordered after it, as deleted would satisfy
	// a containment check while lying about what it destroyed.
	if !slices.Equal(deleted, []string{orphaned}) {
		t.Errorf("PurgeOrphans() = %v, want exactly %v: only the record it had already deleted before aborting", deleted, []string{orphaned})
	}
	if record, lerr := ledger.ReadRecord(orphaned); lerr != nil || record != nil {
		t.Errorf("the genuinely orphaned record was not deleted before the abort (record=%v, err=%v)", record, lerr)
	}
	if record, lerr := ledger.ReadRecord(later); lerr != nil || record == nil {
		t.Errorf("a record ordered after the failure was deleted anyway (record=%v, err=%v); the purge continued past a question it could not answer", record, lerr)
	}
}

// TestListRecordsFailsWhenTheLedgerDirectoryCannotBeRead pins FU-16. The
// listing enumerated with filepath.Glob, which reports only ErrBadPattern and
// swallows every I/O error it meets while reading a directory, so an
// unreadable ledger came back as an empty list and a nil error. The callers
// that decide what a prune may destroy read that as "this ledger cites
// nothing": collectProvenanceReferences, through anotarReferenciasDeLedger,
// then treats the execution streams those records reference as unreferenced
// and deletes them, which is exactly what its own contract forbids.
//
// The fault is staged with a file shape and not with a permission bit. A mode
// change is a no-op under root, so a permission-based fixture would pass
// without exercising anything.
func TestListRecordsFailsWhenTheLedgerDirectoryCannotBeRead(t *testing.T) {
	gitDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(gitDir, "vas-sentinel"), []byte("not a directory"), 0644); err != nil {
		t.Fatal(err)
	}

	shas, err := NewLedger(gitDir).ListRecords()
	if err == nil {
		t.Fatalf("ListRecords() = %v, nil; want an error: a ledger that cannot be enumerated must never be reported as one holding no records", shas)
	}
	if len(shas) != 0 {
		t.Errorf("ListRecords() returned %v next to its error, want no SHAs", shas)
	}
}

// TestListRecordsFailsOnADanglingLedgerSymlink covers the shape that reports
// the same ErrNotExist as a ledger nobody ever wrote: os.ReadDir resolves the
// link and cannot separate a broken one from an absent path. Only absence is a
// real answer, so the distinction has to be made before the read.
//
// It matters on the destructive path in particular: the shared-ledger
// directory list guards every linked worktree with Lstat before Stat, but
// appends the common directory unconditionally, so a broken link there
// reaches this listing with no check in front of it.
// This test is the only one that pins the Lstat guard, and the skip below is
// therefore a real coverage limit rather than a formality: removing the guard
// and keeping ReadDir's own ErrNotExist check leaves the regular-file test green,
// because ReadDir answers ENOTDIR there. Measured, and recorded under FU-16 in
// docs/issues/decisions.md (FU-16 entry).
func TestListRecordsFailsOnADanglingLedgerSymlink(t *testing.T) {
	gitDir := t.TempDir()
	if err := os.Symlink(filepath.Join(gitDir, "ledger-that-was-removed"), filepath.Join(gitDir, "vas-sentinel")); err != nil {
		t.Skipf("this platform refuses to create a symlink without extra privileges: %v", err)
	}

	shas, err := NewLedger(gitDir).ListRecords()
	if err == nil {
		t.Fatalf("ListRecords() = %v, nil; want an error: a ledger path pointing nowhere is a broken ledger, not an empty one", shas)
	}
	if len(shas) != 0 {
		t.Errorf("ListRecords() returned %v next to its error, want no SHAs", shas)
	}
}

// TestListRecordsTreatsAMissingLedgerDirectoryAsEmpty holds the other side of
// FU-16 down. NewLedger does not create the directory — the first saved
// revision does — so its absence is a real answer and not a failure. A fix that
// propagated every ReadDir error would break `sentinel status`, the metrics
// reader and the prune's own provenance scan on any repository that never saved
// a review.
func TestListRecordsTreatsAMissingLedgerDirectoryAsEmpty(t *testing.T) {
	shas, err := NewLedger(t.TempDir()).ListRecords()
	if err != nil {
		t.Fatalf("ListRecords() error = %v, want nil: a ledger nobody has written to yet holds no records", err)
	}
	if len(shas) != 0 {
		t.Errorf("ListRecords() = %v, want no SHAs", shas)
	}
}

// TestSaveRevisionConcurrentDoesNotLoseRevisions is the in-process half of the
// contract. TestSaveRevisionAcrossProcesses is the half that matters, because
// a process-local mutex would satisfy this one and still lose revisions across
// checkouts; this stays because it fails in milliseconds and names the writer.
//
// It pins the contract the shared ledger made load bearing. Anchoring review, status and pr on the Git common
// directory means two checkouts now audit into the SAME record file, and
// SaveRevision is a read-append-rename cycle: without serialization each
// writer reads the same record, appends its own revision and replaces the other,
// so a clean result can erase a blocking one. `review --all` then reads that
// SHA as audited and the lost verdict never resurfaces.
//
// The revisions array is documented as append-only. This asserts that
// literally: every concurrent writer's revision must survive.
func TestSaveRevisionConcurrentDoesNotLoseRevisions(t *testing.T) {
	ledger := NewLedger(t.TempDir())
	const sha = "dddddddddddddddddddddddddddddddddddddddd"
	const writers = 8

	// A start gate rather than staggered launches: the lost update needs the
	// reads to overlap, and goroutines started in a loop tend not to.
	var ready, start, done sync.WaitGroup
	ready.Add(writers)
	done.Add(writers)
	start.Add(1)
	errCh := make(chan error, writers)
	for i := 0; i < writers; i++ {
		go func(i int) {
			defer done.Done()
			ready.Done()
			start.Wait()
			errCh <- ledger.SaveRevision(sha, "fixture", "b", "m",
				Revision{At: time.Now(), Result: VerdictOK, Agent: fmt.Sprintf("writer-%d", i)})
		}(i)
	}
	ready.Wait()
	start.Done()
	done.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("SaveRevision() error = %v", err)
		}
	}

	record, err := ledger.ReadRecord(sha)
	if err != nil {
		t.Fatalf("ReadRecord() error = %v", err)
	}
	if record == nil {
		t.Fatal("no record exists after eight concurrent writes")
	}
	if len(record.Revisions) != writers {
		t.Fatalf("the record holds %d revisions, want %d: one concurrent write replaced another writer's revision",
			len(record.Revisions), writers)
	}
	// Named agents, so the failure says WHICH writer was lost instead of only
	// that the count is short.
	seen := map[string]bool{}
	for _, rev := range record.Revisions {
		seen[rev.Agent] = true
	}
	for i := 0; i < writers; i++ {
		if !seen[fmt.Sprintf("writer-%d", i)] {
			t.Errorf("writer-%d's revision is not in the record: %v", i, seen)
		}
	}
}

// TestListRecordsIgnoresTheLockFile covers the seam between the two
// things this ledger learned recently: enumeration reports every *.json entry,
// and mutations now leave a <sha>.json.lock file for as long as they hold the
// record. A process killed inside the critical section leaves that file behind
// for good, and a listing that reported it would hand callers a SHA ending in
// ".json" — which ReadRecord then reads as a missing record, and PurgeOrphans
// as an orphan to delete.
func TestListRecordsIgnoresTheLockFile(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)
	const sha = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	if err := ledger.SaveRevision(sha, "fixture", "b", "m", Revision{At: time.Now(), Result: VerdictOK}); err != nil {
		t.Fatal(err)
	}
	// Staged as the leftover of a killed writer, which is the only way this
	// file outlives the call that made it.
	if err := os.WriteFile(ledger.RecordPath(sha)+".lock", nil, 0644); err != nil {
		t.Fatal(err)
	}

	shas, err := ledger.ListRecords()
	if err != nil {
		t.Fatalf("ListRecords() error = %v", err)
	}
	if !slices.Equal(shas, []string{sha}) {
		t.Errorf("ListRecords() = %v, want exactly %v: a stale lock file was reported as a record", shas, []string{sha})
	}
}

// writersAcrossProcesses and roundsPerWriter size the cross-process test:
// several rounds per writer widen the window in which two processes are
// inside the read-append-write of the same record, which a single round
// each does not reliably produce.
const (
	writersAcrossProcesses = 6
	roundsPerWriter        = 4
)

// TestSaveRevisionAcrossProcesses proves what the goroutine test cannot: the
// serialization holds between separate PROCESSES. That is the real shape of the
// contract, because the shared ledger exists so that two checkouts — two
// `sentinel review` invocations — write into one record. A mutex inside one
// process would keep the goroutine test green while every cross-checkout write
// still overwrote another.
//
// Each child re-executes this same test binary with the fixture directory in
// the environment, which is the standard way to get real processes out of `go
// test` without a second binary to build and keep in sync.
func TestSaveRevisionAcrossProcesses(t *testing.T) {
	if dir := os.Getenv("VAS_SENTINEL_TEST_LEDGER_DIR"); dir != "" {
		writeChildRevisions(t, dir, os.Getenv("VAS_SENTINEL_TEST_WRITER"))
		return
	}

	dir := t.TempDir()
	const sha = "ffffffffffffffffffffffffffffffffffffffff"
	// Seeded from the parent so the children only ever append: creating the
	// record concurrently would test a different thing.
	if err := NewLedger(dir).SaveRevision(sha, "fixture", "b", "m",
		Revision{At: time.Now(), Result: VerdictOK, Agent: "seed"}); err != nil {
		t.Fatal(err)
	}

	children := make([]*exec.Cmd, 0, writersAcrossProcesses)
	for i := 0; i < writersAcrossProcesses; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSaveRevisionAcrossProcesses$", "-test.v")
		cmd.Env = append(os.Environ(),
			"VAS_SENTINEL_TEST_LEDGER_DIR="+dir,
			fmt.Sprintf("VAS_SENTINEL_TEST_WRITER=writer-%d", i))
		if err := cmd.Start(); err != nil {
			t.Fatalf("starting writer %d: %v", i, err)
		}
		children = append(children, cmd)
	}
	for i, cmd := range children {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("writer %d failed: %v", i, err)
		}
	}

	record, err := NewLedger(dir).ReadRecord(sha)
	if err != nil {
		t.Fatalf("ReadRecord() error = %v", err)
	}
	if record == nil {
		t.Fatal("the record does not exist after the concurrent writers finished")
	}
	expected := 1 + writersAcrossProcesses*roundsPerWriter
	if len(record.Revisions) != expected {
		t.Fatalf("the record holds %d revisions, want %d: a write from one process replaced another process's revision",
			len(record.Revisions), expected)
	}
	perWriter := map[string]int{}
	for _, rev := range record.Revisions {
		perWriter[rev.Agent]++
	}
	for i := 0; i < writersAcrossProcesses; i++ {
		name := fmt.Sprintf("writer-%d", i)
		if perWriter[name] != roundsPerWriter {
			t.Errorf("%s left %d revisions, want %d: %v", name, perWriter[name], roundsPerWriter, perWriter)
		}
	}
}

// writeChildRevisions is the child half of TestSaveRevisionAcrossProcesses.
// It appends its rounds and reports failure through the process exit status,
// which is what the parent's cmd.Wait observes.
func writeChildRevisions(t *testing.T, dir, writer string) {
	t.Helper()
	ledger := NewLedger(dir)
	const sha = "ffffffffffffffffffffffffffffffffffffffff"
	for i := 0; i < roundsPerWriter; i++ {
		if err := ledger.SaveRevision(sha, "fixture", "b", "m",
			Revision{At: time.Now(), Result: VerdictOK, Agent: writer}); err != nil {
			t.Fatalf("%s round %d: %v", writer, i, err)
		}
	}
}

// raceRepetitions is how many times a two-goroutine race is replayed. One
// pass proves nothing: two goroutines rarely interleave inside a window this
// small, so an unlocked implementation passes a single run. Measured — the
// single-run versions of both tests below stayed green with the lock removed.
//
// Replaying is probabilistic and this comment does not pretend otherwise. What
// is not a guess is the calibration: with each lock removed in turn, the
// MarkFixed race failed on attempt 0 and the AdoptRecord race on attempt
// 19, both far inside three hundred. The cross-process contract, which no
// number of goroutines can establish, is pinned separately by
// TestSaveRevisionAcrossProcesses.
const raceRepetitions = 300

// inRace runs first and second concurrently behind one start gate and
// waits for both. It exists so the two tests below race the same way and the
// repetition lives in one place.
func inRace(first, second func()) {
	var start, done sync.WaitGroup
	start.Add(1)
	done.Add(2)
	for _, fn := range []func(){first, second} {
		go func(fn func()) {
			defer done.Done()
			start.Wait()
			fn()
		}(fn)
	}
	start.Done()
	done.Wait()
}

// TestMarkFixedConcurrentWithSaveRevision covers MarkFixed's
// lock against the property that is actually observable.
//
// Racing eight MarkFixed calls against each other proves nothing: they
// all read FixedIn empty, the last whole-record rename wins, and the result is
// one attribution either way, so such a test passes with the lock removed —
// measured. What the lock really protects is the read-modify-write against a
// CONCURRENT APPEND: MarkFixed rewrites the whole record, so an
// unserialized SaveRevision can be discarded by it, or discard its FixedIn.
//
// With the lock, either order ends in the same state, and it is asserted
// exactly: both revisions present AND the correction recorded.
func TestMarkFixedConcurrentWithSaveRevision(t *testing.T) {
	const sha = "1111111111111111111111111111111111111111"
	for attempt := 0; attempt < raceRepetitions; attempt++ {
		ledger := NewLedger(t.TempDir())
		if err := ledger.SaveRevision(sha, "fixture", "b", "m",
			Revision{At: time.Now(), Result: VerdictBlock, Agent: "initial"}); err != nil {
			t.Fatal(err)
		}
		inRace(func() {
			if err := ledger.MarkFixed(sha, "thefix"); err != nil {
				t.Errorf("MarkFixed() error = %v", err)
			}
		}, func() {
			if err := ledger.SaveRevision(sha, "fixture", "b", "m",
				Revision{At: time.Now(), Result: VerdictOK, Agent: "reaudit"}); err != nil {
				t.Errorf("SaveRevision() error = %v", err)
			}
		})

		record, err := ledger.ReadRecord(sha)
		if err != nil || record == nil {
			t.Fatalf("attempt %d: ReadRecord() = %v, %v", attempt, record, err)
		}
		if record.FixedIn != "thefix" {
			t.Fatalf("attempt %d: FixedIn = %q, want \"thefix\": the concurrent append replaced the record the correction had just written", attempt, record.FixedIn)
		}
		if len(record.Revisions) != 2 {
			t.Fatalf("attempt %d: the record holds %d revisions, want 2: the correction replaced the record a concurrent audit had just appended to", attempt, len(record.Revisions))
		}
	}
}

// TestAdoptRecordConcurrentWithSaveRevision covers the destination side of
// AdoptRecord's lock. Adoption preserves an existing destination record, so an
// adoption racing an append to that same SHA must retain both destination
// revisions — and the rebase path that calls it runs inside branch analysis,
// which is exactly where another commit's audit may be writing.
//
// Both orders are legal. What the lock guarantees is that both writers' work
// survives, never a record missing one of the two revisions.
func TestAdoptRecordConcurrentWithSaveRevision(t *testing.T) {
	const source = "2222222222222222222222222222222222222222"
	const destination = "3333333333333333333333333333333333333333"
	for attempt := 0; attempt < raceRepetitions; attempt++ {
		ledger := NewLedger(t.TempDir())
		if err := ledger.SaveRevision(source, "fixture", "b", "m",
			Revision{At: time.Now(), Result: VerdictOK, Agent: "source"}); err != nil {
			t.Fatal(err)
		}
		if err := ledger.SaveRevision(destination, "fixture", "b", "m",
			Revision{At: time.Now(), Result: VerdictOK, Agent: "destination"}); err != nil {
			t.Fatal(err)
		}
		inRace(func() {
			if err := ledger.AdoptRecord(source, destination); err != nil {
				t.Errorf("AdoptRecord() error = %v", err)
			}
		}, func() {
			if err := ledger.SaveRevision(destination, "fixture", "b", "m",
				Revision{At: time.Now(), Result: VerdictOK, Agent: "late"}); err != nil {
				t.Errorf("SaveRevision() error = %v", err)
			}
		})

		record, err := ledger.ReadRecord(destination)
		if err != nil || record == nil {
			t.Fatalf("attempt %d: ReadRecord() = %v, %v", attempt, record, err)
		}
		authors := make([]string, 0, len(record.Revisions))
		for _, rev := range record.Revisions {
			authors = append(authors, rev.Agent)
		}
		// Adoption merges the source revisions into the destination instead of
		// replacing or ignoring them, so all three must survive the race, in
		// their chronological order: the source was audited first, the
		// destination existed before the adoption, and the concurrent append
		// landed last. Losing any of them means adoption discarded an existing
		// revision or the append.
		if !slices.Equal(authors, []string{"source", "destination", "late"}) {
			t.Fatalf("attempt %d: the destination record holds %v, want [source destination late]: adoption discarded a revision or the concurrent append",
				attempt, authors)
		}
	}
}

// TestPurgeOrphansReportsDeletedRecordEvenWhenLockReleaseFails covers the seam
// between two things this ledger learned in the same change: deletion runs
// under the per-SHA lock, and a lock that cannot be released is reported
// instead of discarded. Together they had a hole. The deferred release turned a
// SUCCESSFUL deletion into an error, and the purge returned before recording
// the SHA, so the record was gone while the caller was told nothing was deleted
// — and events are cleaned from that very list, so they survived pointing at a
// record the command had just removed.
//
// The release failure is injected through releaseLockFile. There is no file
// shape that reaches it: the lock only exists inside the critical section, and
// any shape staged before it makes the acquisition fail instead, which is a
// different branch.
func TestPurgeOrphansReportsDeletedRecordEvenWhenLockReleaseFails(t *testing.T) {
	ledger := NewLedger(t.TempDir())
	const sha = "4444444444444444444444444444444444444444"
	if err := ledger.SaveRevision(sha, "fixture", "b", "m", Revision{At: time.Now(), Result: VerdictOK}); err != nil {
		t.Fatal(err)
	}

	// Installed AFTER seeding: the seed writes under the same lock, and failing
	// its release would abort the fixture instead of exercising the purge.
	original := ledger.releaseLockFile
	failure := errors.New("the lock file could not be removed")
	ledger.releaseLockFile = func(path string) error {
		_ = original(path) // still released, so the fixture leaks nothing
		return failure
	}

	deleted, err := ledger.PurgeOrphans(func(string) (bool, error) {
		return false, nil // orphan: the purge must delete it
	})

	if !errors.Is(err, ErrLockNotReleased) {
		t.Fatalf("PurgeOrphans() error = %v, want one wrapping ErrLockNotReleased", err)
	}
	if record, lerr := ledger.ReadRecord(sha); lerr != nil || record != nil {
		t.Fatalf("the record was not deleted (record=%v, err=%v); the fixture no longer exercises the case", record, lerr)
	}
	if !slices.Contains(deleted, sha) {
		t.Errorf("PurgeOrphans() = %v, want it to report %q: the record is deleted, and its events are cleaned from this very list",
			deleted, sha)
	}
}

// TestSaveRevisionDoesNotHideTheUnreleasedLock holds the other half of that
// distinction: a mutation whose lock leaked must still say so. Reporting the
// write as clean would leave every later writer of this SHA waiting the full
// timeout with nothing explaining why.
func TestSaveRevisionDoesNotHideTheUnreleasedLock(t *testing.T) {
	ledger := NewLedger(t.TempDir())
	const sha = "5555555555555555555555555555555555555555"
	original := ledger.releaseLockFile
	ledger.releaseLockFile = func(path string) error {
		_ = original(path)
		return errors.New("the lock file could not be removed")
	}

	err := ledger.SaveRevision(sha, "fixture", "b", "m", Revision{At: time.Now(), Result: VerdictOK})
	if !errors.Is(err, ErrLockNotReleased) {
		t.Fatalf("SaveRevision() error = %v, want one wrapping ErrLockNotReleased", err)
	}
	// The revision is on disk regardless: the failure is about the lock, not
	// the write, and a caller that retried would append it twice.
	if record, lerr := ledger.ReadRecord(sha); lerr != nil || record == nil || len(record.Revisions) != 1 {
		t.Errorf("ReadRecord() = %v, %v; want the revision persisted despite the lock failure", record, lerr)
	}
}

// TestMarkFixedFailsOnADanglingLedger is FU-16 one more time, in the
// guard added to keep MarkFixed a pure no-op for a ledger that has never
// stored anything. os.Stat resolves symlinks, so a ledger path pointing nowhere
// answers ErrNotExist exactly like an absent one, and the correction was
// dropped in silence — on a repository whose ledger is broken, which is when
// losing the record of a fix matters most.
//
// The listing three hundred lines above already separates the two with Lstat.
// This is the same separation in the same file.
func TestMarkFixedFailsOnADanglingLedger(t *testing.T) {
	gitDir := t.TempDir()
	if err := os.Symlink(filepath.Join(gitDir, "ledger-that-was-removed"), filepath.Join(gitDir, "vas-sentinel")); err != nil {
		t.Skipf("this platform refuses to create a symlink without extra privileges: %v", err)
	}

	err := NewLedger(gitDir).MarkFixed("6666666666666666666666666666666666666666", "thefix")
	if err == nil {
		t.Fatalf("MarkFixed() error = nil; want a failure: a ledger path pointing nowhere is a broken ledger, not one that never stored a record, and reporting success drops the correction")
	}
}

// TestVanishedLockIsNotReportedAsSuccess covers the case that used to be
// swallowed: nothing in this package removes a lock but its own holder, so a
// lock that is already gone when the release runs means mutual exclusion broke
// while the operation was running and another writer may have entered.
// Reporting that as a clean release hid a possible lost update behind the one
// signal that could have revealed it.
func TestVanishedLockIsNotReportedAsSuccess(t *testing.T) {
	ledger := NewLedger(t.TempDir())
	const sha = "7777777777777777777777777777777777777777"
	original := ledger.releaseLockFile
	ledger.releaseLockFile = func(path string) error {
		if err := original(path); err != nil {
			return err
		}
		// Second removal: reproduces "the lock was already gone" exactly as the
		// filesystem reports it, without racing anything.
		return original(path)
	}

	err := ledger.SaveRevision(sha, "fixture", "b", "m", Revision{At: time.Now(), Result: VerdictOK})
	if !errors.Is(err, ErrLockNotReleased) {
		t.Fatalf("SaveRevision() error = %v, want one wrapping ErrLockNotReleased", err)
	}
	// The cause travels wrapped, not formatted: a caller that needs to know the
	// lock vanished rather than resisted removal is the caller this error is for.
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("SaveRevision() error = %v, want the underlying filesystem cause inspectable with errors.Is", err)
	}
	// The wording is asserted, not just the identity. This error is returned for
	// deletions and for callbacks that changed nothing, so a message claiming a
	// write is wrong for most of its callers — and errors.Is alone would keep
	// passing with the old text.
	if strings.Contains(ErrLockNotReleased.Error(), "written") {
		t.Errorf("ErrLockNotReleased = %q; it is returned for deletions and no-ops, so it must not claim the record was written",
			ErrLockNotReleased.Error())
	}
}

// TestLedgerAdoptRecordImportsSourceRevisionsIntoSupplementaryDestination locks
// the case that made adoption unsafe: a destination whose only revision was
// written by a narrowed run. Preserving that destination without importing the
// authoritative source revisions left it without an authoritative verdict while
// AnalyzeBranch treated the non-nil record as reviewed and skipped its audit.
func TestLedgerAdoptRecordImportsSourceRevisionsIntoSupplementaryDestination(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	audited := time.Now().Add(-2 * time.Hour)
	if err := ledger.SaveRevision("sha-old", "feat(old): audited", "pr", "model", Revision{
		At:       audited,
		Result:   VerdictOK,
		Coverage: CoverageAuthoritative,
	}); err != nil {
		t.Fatalf("SaveRevision(sha-old): %v", err)
	}
	narrowed := time.Now().Add(-1 * time.Hour)
	if err := ledger.SaveRevision("sha-new", "feat(new): rewritten", "", "model", Revision{
		At:       narrowed,
		Result:   VerdictOK,
		Coverage: CoverageSupplementary,
	}); err != nil {
		t.Fatalf("SaveRevision(sha-new): %v", err)
	}

	if err := ledger.AdoptRecordWithMessage("sha-old", "sha-new", "feat(new): rewritten"); err != nil {
		t.Fatalf("AdoptRecordWithMessage: %v", err)
	}

	adopted, err := ledger.ReadRecord("sha-new")
	if err != nil {
		t.Fatalf("ReadRecord(sha-new): %v", err)
	}
	if adopted == nil {
		t.Fatal("ReadRecord(sha-new) returned nil after adoption")
	}
	if len(adopted.Revisions) != 2 {
		t.Fatalf("revisions = %d, want 2 (authoritative source plus supplementary destination)", len(adopted.Revisions))
	}
	if !adopted.Revisions[0].At.Equal(audited) {
		t.Errorf("revisions[0].At = %v, want the earlier source revision %v", adopted.Revisions[0].At, audited)
	}
	if !adopted.Revisions[1].At.Equal(narrowed) {
		t.Errorf("revisions[1].At = %v, want the later destination revision %v", adopted.Revisions[1].At, narrowed)
	}
	if _, _, ok := LastAuthoritativeRevision(*adopted); !ok {
		t.Error("adopted record has no authoritative revision: reuse would mark it covered without an authoritative verdict")
	}
}

// TestLedgerAdoptRecordIsIdempotentOverAppendedRevisions locks the idempotence
// AdoptRecord documents: a second adoption of the same origin must neither
// duplicate the source revisions nor discard what was appended after the first.
func TestLedgerAdoptRecordIsIdempotentOverAppendedRevisions(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	audited := time.Now().Add(-2 * time.Hour)
	if err := ledger.SaveRevision("sha-old", "feat(old): audited", "pr", "model", Revision{
		At:       audited,
		Result:   VerdictOK,
		Coverage: CoverageAuthoritative,
	}); err != nil {
		t.Fatalf("SaveRevision(sha-old): %v", err)
	}
	if err := ledger.AdoptRecordWithMessage("sha-old", "sha-new", "feat(new): rewritten"); err != nil {
		t.Fatalf("first AdoptRecordWithMessage: %v", err)
	}
	appended := time.Now()
	if err := ledger.SaveRevision("sha-new", "feat(new): rewritten", "", "model", Revision{
		At:       appended,
		Result:   VerdictWarn,
		Coverage: CoverageAuthoritative,
	}); err != nil {
		t.Fatalf("SaveRevision(sha-new): %v", err)
	}

	if err := ledger.AdoptRecordWithMessage("sha-old", "sha-new", "feat(new): rewritten"); err != nil {
		t.Fatalf("second AdoptRecordWithMessage: %v", err)
	}

	adopted, err := ledger.ReadRecord("sha-new")
	if err != nil {
		t.Fatalf("ReadRecord(sha-new): %v", err)
	}
	if len(adopted.Revisions) != 2 {
		t.Fatalf("revisions = %d, want 2: the source revision once plus the appended one", len(adopted.Revisions))
	}
	if !adopted.Revisions[1].At.Equal(appended) {
		t.Errorf("revisions[1].At = %v, want the appended revision %v preserved", adopted.Revisions[1].At, appended)
	}
	if adopted.Revisions[1].Result != VerdictWarn {
		t.Errorf("revisions[1].Result = %q, want the appended verdict preserved", adopted.Revisions[1].Result)
	}
}

// TestLedgerAdoptRecordKeepsDistinctRevisionsSharingATimestamp locks the reason
// adoption does not deduplicate. Revision.At is supplied by the caller —
// SaveRevision appends the Revision it is handed and nothing assigns or
// validates At — so two genuinely different revisions can carry the same
// timestamp. Treating At as identity dropped one of them, and because the
// source is merged first the loss fell on the destination's own verdict: a
// blocking finding could disappear from the history that coverage and blocker
// selection then read.
func TestLedgerAdoptRecordKeepsDistinctRevisionsSharingATimestamp(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	collision := time.Now().Add(-time.Hour)
	if err := ledger.SaveRevision("sha-old", "feat(old): audited", "pr", "model", Revision{
		At:       collision,
		Result:   VerdictOK,
		Agent:    "source",
		Coverage: CoverageAuthoritative,
	}); err != nil {
		t.Fatalf("SaveRevision(sha-old): %v", err)
	}
	// The destination's own revision blocks and shares the exact timestamp.
	if err := ledger.SaveRevision("sha-new", "feat(new): rewritten", "", "model", Revision{
		At:       collision,
		Result:   VerdictBlock,
		Agent:    "destination",
		Coverage: CoverageAuthoritative,
	}); err != nil {
		t.Fatalf("SaveRevision(sha-new): %v", err)
	}

	if err := ledger.AdoptRecordWithMessage("sha-old", "sha-new", "feat(new): rewritten"); err != nil {
		t.Fatalf("AdoptRecordWithMessage: %v", err)
	}

	adopted, err := ledger.ReadRecord("sha-new")
	if err != nil {
		t.Fatalf("ReadRecord(sha-new): %v", err)
	}
	if adopted == nil {
		t.Fatal("ReadRecord(sha-new) returned nil after adoption")
	}
	if len(adopted.Revisions) != 2 {
		t.Fatalf("revisions = %d, want 2: a shared timestamp is not a shared identity", len(adopted.Revisions))
	}
	agents := make(map[string]string, 2)
	for _, revision := range adopted.Revisions {
		agents[revision.Agent] = revision.Result
	}
	if agents["destination"] != VerdictBlock {
		t.Errorf("the destination's own blocking revision was lost: %+v", adopted.Revisions)
	}
	if agents["source"] != VerdictOK {
		t.Errorf("the source revision was lost: %+v", adopted.Revisions)
	}
}

// TestLedgerAdoptRecordImportsRevisionsAppendedToTheSourceAfterAdoption locks
// what made record provenance the wrong answer to "are the source revisions
// already here". OriginSHA says which origin was adopted, not which of its
// revisions arrived. Skipping the merge on a provenance match therefore kept a
// destination on an older passing verdict after the source had been re-audited
// into a block, while AnalyzeBranch read that stale authoritative revision and
// skipped the audit.
func TestLedgerAdoptRecordImportsRevisionsAppendedToTheSourceAfterAdoption(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	first := time.Now().Add(-3 * time.Hour)
	if err := ledger.SaveRevision("sha-old", "feat(old): audited", "pr", "model", Revision{
		At:       first,
		Result:   VerdictOK,
		Agent:    "first",
		Coverage: CoverageAuthoritative,
	}); err != nil {
		t.Fatalf("SaveRevision(sha-old): %v", err)
	}
	if err := ledger.AdoptRecordWithMessage("sha-old", "sha-new", "feat(new): rewritten"); err != nil {
		t.Fatalf("first AdoptRecordWithMessage: %v", err)
	}

	// The origin is re-audited after the adoption and now blocks.
	reaudit := time.Now().Add(-1 * time.Hour)
	if err := ledger.SaveRevision("sha-old", "feat(old): audited", "pr", "model", Revision{
		At:       reaudit,
		Result:   VerdictBlock,
		Agent:    "reaudit",
		Coverage: CoverageAuthoritative,
	}); err != nil {
		t.Fatalf("re-audit SaveRevision(sha-old): %v", err)
	}

	if err := ledger.AdoptRecordWithMessage("sha-old", "sha-new", "feat(new): rewritten"); err != nil {
		t.Fatalf("second AdoptRecordWithMessage: %v", err)
	}

	adopted, err := ledger.ReadRecord("sha-new")
	if err != nil {
		t.Fatalf("ReadRecord(sha-new): %v", err)
	}
	if len(adopted.Revisions) != 2 {
		t.Fatalf("revisions = %d, want 2: the first revision once plus the re-audit", len(adopted.Revisions))
	}
	current, _, ok := LastAuthoritativeRevision(*adopted)
	if !ok {
		t.Fatal("adopted record has no authoritative revision")
	}
	if current.Result != VerdictBlock || current.Agent != "reaudit" {
		t.Errorf("current authoritative revision = %q by %q, want the origin's re-audit block", current.Result, current.Agent)
	}
}

// TestLedgerAdoptRecordDoesNotDuplicateWhenDestinationHasOtherProvenance covers
// the second half of the same mistake: OriginSHA was only assigned when empty,
// so a destination carrying a different origin took the merge branch on every
// call and re-imported the source revisions each time.
func TestLedgerAdoptRecordDoesNotDuplicateWhenDestinationHasOtherProvenance(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	for _, origin := range []string{"sha-a", "sha-b"} {
		if err := ledger.SaveRevision(origin, "feat: "+origin, "pr", "model", Revision{
			At:       time.Now().Add(-3 * time.Hour),
			Result:   VerdictOK,
			Agent:    origin,
			Coverage: CoverageAuthoritative,
		}); err != nil {
			t.Fatalf("SaveRevision(%s): %v", origin, err)
		}
	}
	// The destination is first adopted from sha-a, so its provenance is sha-a.
	if err := ledger.AdoptRecordWithMessage("sha-a", "sha-new", "feat(new): rewritten"); err != nil {
		t.Fatalf("AdoptRecordWithMessage(sha-a): %v", err)
	}
	// Adopting from sha-b twice must not duplicate sha-b's revisions.
	for attempt := 0; attempt < 2; attempt++ {
		if err := ledger.AdoptRecordWithMessage("sha-b", "sha-new", "feat(new): rewritten"); err != nil {
			t.Fatalf("AdoptRecordWithMessage(sha-b) attempt %d: %v", attempt, err)
		}
	}

	adopted, err := ledger.ReadRecord("sha-new")
	if err != nil {
		t.Fatalf("ReadRecord(sha-new): %v", err)
	}
	agents := make(map[string]int, 2)
	for _, revision := range adopted.Revisions {
		agents[revision.Agent]++
	}
	if agents["sha-a"] != 1 || agents["sha-b"] != 1 {
		t.Errorf("revision counts = %v, want each origin's revision exactly once: %+v", agents, adopted.Revisions)
	}
}

// TestLedgerAdoptRecordPreservesRepeatedIdenticalRevisions locks why the merge
// takes the maximum multiplicity rather than collapsing to a set. revisions[]
// is append-only — re-auditing a SHA adds a revision and never overwrites one,
// and SaveRevision appends whatever it is handed — so a history can hold the
// same revision twice. A set-style union erased one of them, discarding an
// audit event the contract promises to keep.
func TestLedgerAdoptRecordPreservesRepeatedIdenticalRevisions(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	// Two structurally identical revisions in the source history: same
	// timestamp, same verdict, appended twice.
	repeated := Revision{
		At:       time.Now().Add(-2 * time.Hour),
		Result:   VerdictOK,
		Agent:    "repeated",
		Coverage: CoverageAuthoritative,
	}
	for i := 0; i < 2; i++ {
		if err := ledger.SaveRevision("sha-old", "feat(old): audited", "pr", "model", repeated); err != nil {
			t.Fatalf("SaveRevision(sha-old) %d: %v", i, err)
		}
	}

	if err := ledger.AdoptRecordWithMessage("sha-old", "sha-new", "feat(new): rewritten"); err != nil {
		t.Fatalf("first AdoptRecordWithMessage: %v", err)
	}
	// A second adoption must not shrink the history it already carries.
	if err := ledger.AdoptRecordWithMessage("sha-old", "sha-new", "feat(new): rewritten"); err != nil {
		t.Fatalf("second AdoptRecordWithMessage: %v", err)
	}

	adopted, err := ledger.ReadRecord("sha-new")
	if err != nil {
		t.Fatalf("ReadRecord(sha-new): %v", err)
	}
	if len(adopted.Revisions) != 2 {
		t.Fatalf("revisions = %d, want 2: an append-only history holding the same revision twice keeps both", len(adopted.Revisions))
	}
	origin, err := ledger.ReadRecord("sha-old")
	if err != nil {
		t.Fatalf("ReadRecord(sha-old): %v", err)
	}
	if len(origin.Revisions) != 2 {
		t.Errorf("origin revisions = %d, want 2: adoption must not alter the source", len(origin.Revisions))
	}
}

// TestLedgerAdoptRecordPreservesAppendOrderWhenRevisionsShareATimestamp locks
// why the merge emits the source verbatim instead of expanding grouped copies.
// Grouping placed every copy of a distinct revision at its first-seen position,
// so a source history of [A, B, A] whose revisions carry the same timestamp
// came back as [A, A, B]. The stable sort cannot separate them, so the newest
// authoritative revision — what the verdict is read from — became B rather than
// the later A.
func TestLedgerAdoptRecordPreservesAppendOrderWhenRevisionsShareATimestamp(t *testing.T) {
	dir := t.TempDir()
	ledger := NewLedger(dir)

	shared := time.Now().Add(-time.Hour)
	first := Revision{At: shared, Result: VerdictOK, Agent: "A", Coverage: CoverageAuthoritative}
	middle := Revision{At: shared, Result: VerdictBlock, Agent: "B", Coverage: CoverageAuthoritative}
	// Appended in this order: A, B, then A again. The last audit is A, so the
	// current authoritative verdict must be A's.
	for _, revision := range []Revision{first, middle, first} {
		if err := ledger.SaveRevision("sha-old", "feat(old): audited", "pr", "model", revision); err != nil {
			t.Fatalf("SaveRevision(sha-old): %v", err)
		}
	}
	if err := ledger.SaveRevision("sha-new", "feat(new): rewritten", "", "model", first); err != nil {
		t.Fatalf("SaveRevision(sha-new): %v", err)
	}

	if err := ledger.AdoptRecordWithMessage("sha-old", "sha-new", "feat(new): rewritten"); err != nil {
		t.Fatalf("AdoptRecordWithMessage: %v", err)
	}

	adopted, err := ledger.ReadRecord("sha-new")
	if err != nil {
		t.Fatalf("ReadRecord(sha-new): %v", err)
	}
	agents := make([]string, 0, len(adopted.Revisions))
	for _, revision := range adopted.Revisions {
		agents = append(agents, revision.Agent)
	}
	if len(agents) != 3 || agents[0] != "A" || agents[1] != "B" || agents[2] != "A" {
		t.Fatalf("revision order = %v, want [A B A]: the source's append order must survive", agents)
	}
	current, _, ok := LastAuthoritativeRevision(*adopted)
	if !ok {
		t.Fatal("adopted record has no authoritative revision")
	}
	if current.Agent != "A" || current.Result != VerdictOK {
		t.Errorf("current authoritative revision = %q by %q, want A's ok: the last append wins", current.Result, current.Agent)
	}
}
