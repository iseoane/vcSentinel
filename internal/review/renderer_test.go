package review

import (
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// recordHelper builds a realistic record for the renderer tests.
func recordHelper(sha, message, model string, revs ...Revision) Record {
	return Record{SHA: sha, Message: message, Model: model, Revisions: revs}
}

// revisionHelper builds a revision with the given verdict and findings.
func revisionHelper(result string, dims ...DimensionResult) Revision {
	return Revision{At: time.Now().UTC(), Result: result, Dims: dims}
}

func TestBranchBlockersKeepRelocatedEvidenceBlocking(t *testing.T) {
	prepareBranchRepo(t)
	sha := commitInBranch(t, "x.go", "package x\n// defect evidence\n")
	if err := os.WriteFile("x.go", []byte("package x\n// moved away\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("y.go", []byte("package y\n// defect evidence\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "add", "x.go", "y.go")
	runGit(t, "commit", "-qm", "fix: move defect")

	record := Record{SHA: sha, Revisions: []Revision{{
		Result: VerdictBlock,
		AggregatedFindings: []Finding{{
			Dimension: DimLogic, Severity: SevCritical, Status: StatusConfirmed,
			Description: "defect", Evidence: "// defect evidence",
			Location: Location{File: "x.go", LineStart: 2, LineEnd: 2},
		}},
	}}}
	if got := BranchBlockers([]Record{record}, nil); len(got) != 1 {
		t.Fatalf("BranchBlockers = %d, want relocated evidence to keep blocking", len(got))
	}
}

func TestRenderSummaryDoesNotClaimUnbackedFixCredit(t *testing.T) {
	prepareBranchRepo(t)
	sha := commitInBranch(t, "x.go", "package x\n// defect evidence\n")
	if err := os.WriteFile("x.go", []byte("package x\n// fixed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "add", "x.go")
	runGit(t, "commit", "-qm", "fix: remove defect")

	record := Record{SHA: sha, Revisions: []Revision{{
		Result: VerdictBlock,
		AggregatedFindings: []Finding{{
			Dimension: DimLogic, Severity: SevCritical, Status: StatusConfirmed,
			Description: "defect", Evidence: "// defect evidence",
			Location: Location{File: "x.go", LineStart: 2, LineEnd: 2},
		}},
	}}}
	out := RenderSummary([]Record{record}, nil)
	if strings.Contains(out, "fix credited") || !strings.Contains(out, "| — |") {
		t.Fatalf("RenderSummary = %s, want resolved record without fix credit", out)
	}
}

func TestBranchRisksRetainSurvivingWarningsAfterCriticalRetirement(t *testing.T) {
	prepareBranchRepo(t)
	sha := commitInBranch(t, "x.go", "package x\n// critical evidence\n// warning evidence\n")
	if err := os.WriteFile("x.go", []byte("package x\n// fixed\n// warning evidence\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "add", "x.go")
	runGit(t, "commit", "-qm", "fix: remove critical")

	record := Record{SHA: sha, Revisions: []Revision{{
		Result: VerdictBlock,
		AggregatedFindings: []Finding{
			{Dimension: DimLogic, Severity: SevCritical, Status: StatusConfirmed, Description: "critical", Evidence: "// critical evidence", Location: Location{File: "x.go", LineStart: 2}},
			{Dimension: DimLogic, Severity: SevWarning, Status: StatusConfirmed, Description: "warning survives", Evidence: "// warning evidence", Location: Location{File: "x.go", LineStart: 3}},
		},
	}}}
	out := RenderSummary([]Record{record}, nil)
	if !strings.Contains(out, "warning survives") {
		t.Fatalf("RenderSummary omitted surviving warning:\n%s", out)
	}
	if blockers := BranchBlockers([]Record{record}, nil); len(blockers) != 0 {
		t.Fatalf("BranchBlockers = %d, want no blocker after every critical retired", len(blockers))
	}
}

func TestBranchBlockersRetireIntermediateFindingWhenEvidenceIsGoneAtHead(t *testing.T) {
	prepareBranchRepo(t)
	sha := commitInBranch(t, "x.go", "package x\n// defect evidence\n")
	if err := os.WriteFile("x.go", []byte("package x\n// fixed\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "add", "x.go")
	runGit(t, "commit", "-qm", "fix: remove defect")

	record := Record{SHA: sha, Revisions: []Revision{{
		Result: VerdictBlock,
		AggregatedFindings: []Finding{{
			Dimension: DimLogic, Severity: SevCritical, Status: StatusConfirmed,
			Description: "defect", Evidence: "// defect evidence",
			Location: Location{File: "x.go", LineStart: 2, LineEnd: 2},
		}},
	}}}
	if got := BranchBlockers([]Record{record}, nil); len(got) != 0 {
		t.Fatalf("BranchBlockers = %d, want no blocker after the evidence was removed at HEAD", len(got))
	}
	if got := VerdictDeBranch([]Record{record}, nil); got != VerdictOK {
		t.Fatalf("VerdictDeBranch = %q, want ok after the evidence was removed at HEAD", got)
	}
}

func TestBranchBlockersKeepEvidencePresentAndUnverifiableFindings(t *testing.T) {
	newRecord := func(sha, evidence string) Record {
		return Record{SHA: sha, FixedIn: "fix123", Revisions: []Revision{{
			Result: VerdictBlock,
			AggregatedFindings: []Finding{{
				Dimension: DimLogic, Severity: SevCritical, Status: StatusConfirmed,
				Description: "defect", Evidence: evidence,
				Location: Location{File: "x.go", LineStart: 2, LineEnd: 2},
			}},
		}}}
	}

	t.Run("evidence still present protects item 14", func(t *testing.T) {
		prepareBranchRepo(t)
		sha := commitInBranch(t, "x.go", "package x\n// defect evidence\n")
		if got := BranchBlockers([]Record{newRecord(sha, "// defect evidence")}, nil); len(got) != 1 {
			t.Fatalf("BranchBlockers = %d, want the still-present critical", len(got))
		}
		if got := VerdictDeBranch([]Record{newRecord(sha, "// defect evidence")}, nil); got != VerdictBlock {
			t.Fatalf("VerdictDeBranch = %q, want block while evidence remains", got)
		}
	})

	t.Run("empty evidence fails closed", func(t *testing.T) {
		prepareBranchRepo(t)
		sha := commitInBranch(t, "x.go", "package x\n// fixed\n")
		if got := BranchBlockers([]Record{newRecord(sha, "")}, nil); len(got) != 1 {
			t.Fatalf("BranchBlockers = %d, want the unverifiable critical", len(got))
		}
	})

	t.Run("missing file fails closed", func(t *testing.T) {
		prepareBranchRepo(t)
		sha := commitInBranch(t, "x.go", "package x\n// defect evidence\n")
		runGit(t, "rm", "-q", "x.go")
		runGit(t, "commit", "-qm", "fix: remove file")
		if got := BranchBlockers([]Record{newRecord(sha, "// defect evidence")}, nil); len(got) != 1 {
			t.Fatalf("BranchBlockers = %d, want the unverifiable critical", len(got))
		}
	})
}

func TestStatusRecordPendingDoesNotUseBranchHeadEvidence(t *testing.T) {
	prepareBranchRepo(t)
	sha := commitInBranch(t, "x.go", "package x\n// fixed\n")
	record := Record{SHA: sha, FixedIn: "fix123", Revisions: []Revision{{
		Result: VerdictBlock,
		AggregatedFindings: []Finding{{
			Dimension: DimLogic, Severity: SevCritical, Status: StatusConfirmed,
			Evidence: "// gone", Location: Location{File: "x.go", LineStart: 2},
		}},
	}}}
	if !RecordPending(record, nil) {
		t.Fatal("RecordPending = false, want plain record listing to remain unchanged")
	}
}

func TestRenderMatrixBasic(t *testing.T) {
	records := []Record{
		recordHelper("6b127cd", "docs(review): phase 2 concept", "opencode.cheap",
			revisionHelper("ok",
				DimensionResult{Dim: DimSpec, Verdict: VerdictOK},
				DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock},
				DimensionResult{Dim: DimLogic, Verdict: VerdictOK},
			)),
		recordHelper("945b5b5", "feat(config): verification commands", "deepseek-v4-flash-free",
			revisionHelper("warn",
				DimensionResult{Dim: DimSpec, Verdict: VerdictOK},
				DimensionResult{Dim: DimTests, Verdict: VerdictWarn},
			)),
	}

	out := RenderMatrix(records)

	// Exact assertion: full canonical header, rows in order and cells with
	// the verdict of the last revision (— for absent dimensions).
	expected := "| Commit | logic | style | design | tests | security | spec |\n" +
		"|---|---|---|---|---|---|---|\n" +
		"| `6b127cd` docs(review): phase 2 concept | ✅ | — | — | — | 🚨 | ✅ |\n" +
		"| `945b5b5` feat(config): verification commands | — | — | — | ⚠️ | — | ✅ |\n"
	if out != expected {
		t.Errorf("matrix mismatch:\ngot:\n%s\nwant:\n%s", out, expected)
	}
}

// TestRenderUnauditedNoticeEmpty: a fully audited branch renders nothing —
// the notice must not clutter a report that has no gap to report.
func TestRenderUnauditedNoticeEmpty(t *testing.T) {
	if out := RenderUnauditedNotice(nil); out != "" {
		t.Errorf("RenderUnauditedNotice(nil) = %q, want empty", out)
	}
}

// TestRenderUnauditedNoticeListsShaSubjectAndCommand: the notice must inform,
// never block (docs/issues/actionable.md item 2) — it names every commit
// with its short SHA and subject, and gives the exact command to audit them.
func TestRenderUnauditedNoticeListsShaSubjectAndCommand(t *testing.T) {
	pending := []UnauditedCommit{
		{SHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Subject: "feat(x): add x"},
		{SHA: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Subject: "fix(y): fix y"},
	}
	out := RenderUnauditedNotice(pending)
	for _, want := range []string{
		"2 commit", "aaaaaaa", "feat(x): add x", "bbbbbbb", "fix(y): fix y",
		"sentinel review aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderUnauditedNotice missing %q:\n%s", want, out)
		}
	}
}

func TestRenderMatrixEmpty(t *testing.T) {
	if out := RenderMatrix(nil); !strings.Contains(out, "No audited commits") {
		t.Errorf("empty matrix = %q, want the no-commits notice", out)
	}
}

func TestRenderMatrixClearedRevision(t *testing.T) {
	record := recordHelper("945b5b5", "feat(config): commands", "opencode.cheap",
		revisionHelper("block",
			DimensionResult{Dim: DimSpec, Verdict: VerdictBlock,
				Findings: []ReviewFinding{{Dimension: DimSpec, File: "a.go", Line: 1, Severity: SevCritical}}},
		),
		revisionHelper("ok",
			DimensionResult{Dim: DimSpec, Verdict: VerdictOK},
		),
	)

	out := RenderMatrix([]Record{record})

	// The last revision wins and a past one after a block gets marked.
	if !strings.Contains(out, "✅ (rev 2 — CRITICAL cleared)") {
		t.Errorf("missing the cleared-revision mark:\n%s", out)
	}
	if strings.Contains(out, "🚨") {
		t.Errorf("the matrix must not show the block of the first revision:\n%s", out)
	}
}

func TestRenderSummaryRisks(t *testing.T) {
	records := []Record{
		recordHelper("6b127cd", "docs(review): phase 2 concept", "opencode.cheap",
			revisionHelper("block",
				DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock,
					Findings: []ReviewFinding{
						{Dimension: DimSecurity, File: "a.go", Line: 42, Severity: SevCritical, Description: "exposed data"},
						{Dimension: DimSecurity, File: "a.go", Line: 10, Severity: SevAdvisory, Description: "minor suggestion"},
					}},
			)),
		recordHelper("945b5b5", "feat(config): commands", "deepseek-v4-flash-free",
			revisionHelper("warn",
				DimensionResult{Dim: DimTests, Verdict: VerdictWarn,
					Findings: []ReviewFinding{
						{Dimension: DimTests, File: "z.go", Line: 10, Severity: SevWarning, Description: "fragile test"},
					}},
			)),
	}

	out := RenderSummary(records, nil)

	// Global count.
	if !strings.Contains(out, "🟢 ok: 0 · 🟡 warn: 1 · 🚨 block: 1") {
		t.Errorf("wrong global count:\n%s", out)
	}
	// Risks: CRITICAL and WARNING yes; ADVISORY no. Since T6.5 the pending risks
	// reads EffectiveFindings() and always renders through renderMergedFinding;
	// a v1 finding converted from Dims never had a real Source, so the
	// "(source, confidence)" segment is omitted entirely instead of fabricating
	// a "(unknown, confidence 0.00)" that is not real data (T6.5bis review
	// finding: logic WARNING).
	if !strings.Contains(out, "🚨 `6b127cd` [security] CRITICAL — exposed data (a.go:42)") {
		t.Errorf("missing the CRITICAL risk:\n%s", out)
	}
	if !strings.Contains(out, "⚠️ `945b5b5` [tests] WARNING — fragile test (z.go:10)") {
		t.Errorf("missing the WARNING risk:\n%s", out)
	}
	if strings.Contains(out, "minor suggestion") {
		t.Errorf("ADVISORY entries are not risks and must not appear in the summary:\n%s", out)
	}
}

func TestRenderSummaryFixed(t *testing.T) {
	record := recordHelper("6b127cd", "docs(review): concept", "opencode.cheap",
		revisionHelper("block", DimensionResult{Dim: DimSpec, Verdict: VerdictBlock}),
	)
	record.FixedIn = "a1b2c3d"

	out := RenderSummary([]Record{record}, nil)
	if !strings.Contains(out, "🔧 fix credited to `a1b2c3d`") {
		t.Errorf("missing the fix mark:\n%s", out)
	}
}

func TestTruncateBody(t *testing.T) {
	short := "short text"
	if got := TruncateBody(short, 1024); got != short {
		t.Errorf("TruncateBody(short) = %q, want unchanged", got)
	}

	long := strings.Repeat("x", 100)
	got := TruncateBody(long, 50)
	if len(got) > 50 {
		t.Errorf("TruncateBody = %d bytes, want ≤50", len(got))
	}
	if !strings.Contains(got, "Body truncated") || !strings.Contains(got, "bytes omitted") {
		t.Errorf("the truncation must be marked explicitly, got: %q", got)
	}
}

// TestTruncateBodyZeroLimit: without a limit (0 or negative) the text does
// not change.
func TestTruncateBodyZeroLimit(t *testing.T) {
	text := "abc"
	if got := TruncateBody(text, 0); got != text {
		t.Errorf("TruncateBody(0) = %q, want unchanged", got)
	}
	if got := TruncateBody(text, -5); got != text {
		t.Errorf("TruncateBody(-5) = %q, want unchanged", got)
	}
}

// TestTruncateBodyMarkerLargerThanLimit: when even the marker does not fit,
// the result is only the marker clipped to the limit, with no text content.
func TestTruncateBodyMarkerLargerThanLimit(t *testing.T) {
	got := TruncateBody(strings.Repeat("x", 500), 10)
	if len(got) > 10 {
		t.Errorf("TruncateBody = %d bytes, want ≤10", len(got))
	}
	if strings.Contains(got, "x") {
		t.Errorf("the result must not contain text content, got: %q", got)
	}
}

// TestTruncateBodyNeverSplitsRunes: the cut never splits a UTF-8 rune and
// the result is always valid text.
func TestTruncateBodyNeverSplitsRunes(t *testing.T) {
	text := "áéíóúüñ " + strings.Repeat("ñ", 200)
	got := TruncateBody(text, 57)
	if len(got) > 57 {
		t.Errorf("TruncateBody = %d bytes, want ≤57", len(got))
	}
	if !utf8.ValidString(got) {
		t.Errorf("TruncateBody split a UTF-8 rune: %q", got)
	}
}

// TestRenderSummaryEmpty: with no records, the summary warns instead of
// inventing.
func TestRenderSummaryEmpty(t *testing.T) {
	if out := RenderSummary(nil, nil); !strings.Contains(out, "No audited commits") {
		t.Errorf("empty summary = %q, want the no-commits notice", out)
	}
}

// TestCountQuestionUnavailable: the global count includes question and
// unavailable only when they exist.
func TestCountQuestionUnavailable(t *testing.T) {
	records := []Record{
		recordHelper("aaaaaaa", "feat(a): a", "m",
			revisionHelper(VerdictQuestion, DimensionResult{Dim: DimSpec, Verdict: VerdictQuestion})),
		recordHelper("bbbbbbb", "feat(b): b", "m",
			revisionHelper(VerdictUnavailable, DimensionResult{Dim: DimSpec, Verdict: VerdictUnavailable})),
	}

	out := RenderSummary(records, nil)
	if !strings.Contains(out, "❓ question: 1") || !strings.Contains(out, "⛔ unavailable: 1") {
		t.Errorf("missing the question/unavailable counts:\n%s", out)
	}
	if strings.Contains(out, "🔧") {
		t.Errorf("there must be no fixes in this fixture:\n%s", out)
	}
}

func TestRiskLine(t *testing.T) {
	records := []Record{
		recordHelper("6b127cd", "feat(a)", "m",
			revisionHelper("warn", DimensionResult{Dim: DimLogic, Verdict: VerdictWarn})),
		recordHelper("945b5b5", "feat(b)", "m",
			revisionHelper("ok", DimensionResult{Dim: DimLogic, Verdict: VerdictOK})),
	}
	line := riskLine(records, nil)
	if !strings.Contains(line, "warn") {
		t.Errorf("risk line does not show the branch verdict: %s", line)
	}
	if !strings.Contains(line, "⚠️") {
		t.Errorf("the risk line must carry the emoji of the worst verdict: %s", line)
	}
	if !strings.Contains(line, "ok: 1") || !strings.Contains(line, "warn: 1") {
		t.Errorf("the risk line must count the results: %s", line)
	}
}

func TestBranchVerdictWeighted(t *testing.T) {
	cases := []struct {
		name     string
		records  []Record
		expected string
	}{
		{
			"block beats warn",
			[]Record{
				recordHelper("u1", "a", "m", revisionHelper("warn")),
				recordHelper("u2", "b", "m", revisionHelper("block")),
			},
			VerdictBlock,
		},
		{
			"warn beats ok",
			[]Record{
				recordHelper("u1", "a", "m", revisionHelper("ok")),
				recordHelper("u2", "b", "m", revisionHelper("warn")),
			},
			VerdictWarn,
		},
		{
			"no records = ok",
			nil,
			VerdictOK,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := VerdictDeBranch(c.records, nil); got != c.expected {
				t.Errorf("VerdictDeBranch = %q, want %q", got, c.expected)
			}
		})
	}
}

// TestBranchVerdictFixedDoesNotBlock: a block fixed in a later commit
// (FixedIn) no longer contributes to the branch verdict.
func TestBranchVerdictFixedDoesNotBlock(t *testing.T) {
	blocked := recordHelper("u1", "feat(a)", "m", revisionHelper("block"))
	blocked.FixedIn = "a1b2c3d"
	records := []Record{blocked, recordHelper("u2", "feat(b)", "m", revisionHelper("ok"))}
	if got := VerdictDeBranch(records, nil); got != VerdictOK {
		t.Errorf("VerdictDeBranch = %q, want ok (fixed block)", got)
	}
}

// TestBranchPredicatesAgreeWithActiveBlock covers the Item 14 defect: a
// partial fix credited on a record whose current findings still carry live
// CRITICALs must not retire it from the branch predicates, and the summary
// table must not label it as fixed while the same report blocks over it.
func TestBranchPredicatesAgreeWithActiveBlock(t *testing.T) {
	partiallyFixed := func() Record {
		record := recordHelper("370cb04", "feat(a)", "m",
			revisionHelper(VerdictBlock, DimensionResult{Dim: DimLogic, Verdict: VerdictBlock,
				Findings: []ReviewFinding{{Dimension: DimLogic, Severity: SevCritical, Description: "still unfixed"}}}))
		record.FixedIn = "531f23e"
		return record
	}

	t.Run("a live critical still blocks the branch", func(t *testing.T) {
		records := []Record{partiallyFixed()}
		if got := VerdictDeBranch(records, nil); got != VerdictBlock {
			t.Errorf("VerdictDeBranch = %q, want block while a critical is live", got)
		}
		if got := BranchBlockers(records, nil); len(got) != 1 {
			t.Errorf("BranchBlockers = %d, want the live critical", len(got))
		}
		if got := RenderSummary(records, nil); strings.Contains(got, "credited") {
			t.Errorf("RenderSummary credits a fix on a blocking record:\n%s", got)
		}
	})

	t.Run("a resolved record retires and renders the credited fix", func(t *testing.T) {
		record := partiallyFixed()
		record.Revisions[0].Dims[0].Findings[0].Status = StatusRefuted
		records := []Record{record}
		if got := VerdictDeBranch(records, nil); got != VerdictOK {
			t.Errorf("VerdictDeBranch = %q, want ok once no finding blocks", got)
		}
		if got := BranchBlockers(records, nil); len(got) != 0 {
			t.Errorf("BranchBlockers = %d, want none", len(got))
		}
		if got := RenderSummary(records, nil); !strings.Contains(got, "fix credited to `531f23e`") {
			t.Errorf("RenderSummary omits the fix provenance:\n%s", got)
		}
	})

	// A human answer recorded in the append-only dispositions log retires the
	// record on every reporting surface, not only in the blockers: refuting
	// the last live CRITICAL of a credited record used to leave the branch
	// verdict at block and the summary without its credit.
	t.Run("a standing refutation retires the record on every surface", func(t *testing.T) {
		record := partiallyFixed()
		records := []Record{record}
		live := CurrentFindings(record)
		if len(live) != 1 {
			t.Fatalf("CurrentFindings = %d, want the single live critical", len(live))
		}
		dispositions := []FindingDisposition{{
			SHA: record.SHA, Fingerprint: EffectiveFingerprint(live[0]), Status: StatusRefuted,
			Reason: "verified safe", Actor: RefutationActorHuman, Source: DispositionSourceHuman,
		}}
		if RecordPending(record, dispositions) {
			t.Error("RecordPending = true with the only live critical refuted")
		}
		if got := VerdictDeBranch(records, dispositions); got != VerdictOK {
			t.Errorf("VerdictDeBranch = %q, want ok with the critical refuted", got)
		}
		if got := BranchBlockers(records, dispositions); len(got) != 0 {
			t.Errorf("BranchBlockers = %d, want none", len(got))
		}
		out := RenderSummary(records, dispositions)
		if !strings.Contains(out, "fix credited to `531f23e`") {
			t.Errorf("RenderSummary omits the credit of a retired record:\n%s", out)
		}
		if strings.Contains(out, "still unfixed") {
			t.Errorf("the Risks section of the same output lists the refuted finding:\n%s", out)
		}
	})
}

// A record still counts while one of its criticals is live, and only the
// refuted one drops out: the inherited section of a stacked PR and the
// branch blockers read this same projection, so neither can show a finding
// the human already answered.
func TestEffectiveRecordFindingsOverlaysStandingAnswers(t *testing.T) {
	record := recordHelper("370cb04", "feat(a)", "m",
		revisionHelper(VerdictBlock, DimensionResult{Dim: DimLogic, Verdict: VerdictBlock,
			Findings: []ReviewFinding{
				{Dimension: DimLogic, File: "a.go", Severity: SevCritical, Description: "answered"},
				{Dimension: DimLogic, File: "b.go", Severity: SevCritical, Description: "still live"},
			}}))
	record.FixedIn = "531f23e"

	current := CurrentFindings(record)
	if len(current) != 2 {
		t.Fatalf("CurrentFindings = %d, want both criticals", len(current))
	}
	answer := func(h Finding) FindingDisposition {
		return FindingDisposition{
			SHA: record.SHA, Fingerprint: EffectiveFingerprint(h), Status: StatusRefuted,
			Reason: "verified safe", Actor: RefutationActorHuman, Source: DispositionSourceHuman,
		}
	}

	one := effectiveRecordFindings(record, []FindingDisposition{answer(current[0])})
	if len(one) != 2 {
		t.Fatalf("effectiveRecordFindings = %d findings, want the record still projected", len(one))
	}
	for _, h := range one {
		if IsBlocking(h.Severity, h.Status) && h.Description == "answered" {
			t.Error("the refuted finding still blocks after the standing answer")
		}
	}

	both := effectiveRecordFindings(record, []FindingDisposition{answer(current[0]), answer(current[1])})
	if len(both) != 0 {
		t.Errorf("effectiveRecordFindings = %+v, want nothing once every critical is answered", both)
	}
}

func TestVerificationSection(t *testing.T) {
	t.Run("deterministic with exit codes", func(t *testing.T) {
		out := verificationSection(TemplateVerification{
			Mode: "determinista",
			Comandos: []VerifiedCommand{
				{Comando: "go vet ./...", Exit: 0},
				{Comando: "go test ./...", Exit: 1},
			},
		})
		if !strings.Contains(out, "go vet ./...") || !strings.Contains(out, "exit 0") {
			t.Errorf("missing the ok commands: %s", out)
		}
		if !strings.Contains(out, "❌") || !strings.Contains(out, "exit 1") {
			t.Errorf("the non-zero exit must be marked: %s", out)
		}
	})
	t.Run("delegated with tested", func(t *testing.T) {
		out := verificationSection(TemplateVerification{
			Mode:   "delegado",
			Tested: []string{"make test"},
		})
		if !strings.Contains(out, "make test") {
			t.Errorf("missing the tested contract: %s", out)
		}
	})
	t.Run("delegated sanitizes command evidence", func(t *testing.T) {
		out := verificationSection(TemplateVerification{
			Mode:   "delegado",
			Tested: []string{"go test `</details><details>`"},
		})
		const want = "- 🤖 agent: `go test '</details><details>'`\n"
		if out != want {
			t.Errorf("delegated verification = %q, want %q", out, want)
		}
	})
	t.Run("omitted stays honest", func(t *testing.T) {
		out := verificationSection(TemplateVerification{Mode: "omitido", Reason: "no_configurado"})
		if !strings.Contains(out, "no_configurado") {
			t.Errorf("the reason must be shown: %s", out)
		}
		if strings.Contains(out, "✅ ") {
			t.Errorf("an omitted mode must never inject PASS: %s", out)
		}
	})
	t.Run("omitted sanitizes hostile reason", func(t *testing.T) {
		out := verificationSection(TemplateVerification{
			Mode:   "omitido",
			Reason: "verification_error: tool failed\r\n<details><summary>forged `code`</summary></details>",
		})
		const want = "- ⚪ Tests not run (`verification_error: tool failed <details><summary>forged 'code'</summary></details>`).\n"
		if out != want {
			t.Errorf("omitted verification = %q, want %q", out, want)
		}
	})
	t.Run("no evidence does not lie", func(t *testing.T) {
		out := verificationSection(TemplateVerification{})
		if !strings.Contains(out, "Tests not run.") {
			t.Errorf("without evidence it must declare tests not run: %s", out)
		}
	})
}

func TestDelegatedTestedTextSanitizesMarkdownControlText(t *testing.T) {
	cases := []struct {
		name   string
		tested string
		want   string
	}{
		{name: "LF", tested: "go test\n## forged", want: "go test ## forged"},
		{name: "CRLF", tested: "go test\r\n## forged", want: "go test ## forged"},
		{name: "bare CR", tested: "go test\r## forged", want: "go test ## forged"},
		{name: "backticks", tested: "printf `literal`", want: "printf 'literal'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := verificationSection(TemplateVerification{Mode: "delegado", Tested: []string{tc.tested}})
			want := "- 🤖 agent: `" + tc.want + "`"
			if !strings.Contains(out, want) {
				t.Fatalf("output missing sanitized delegated text %q:\n%s", want, out)
			}
			if strings.Contains(out, tc.tested) {
				t.Fatalf("output retains unsanitized delegated text %q:\n%s", tc.tested, out)
			}
		})
	}
}

// TestCommandTextSanitizesMarkdownControlText protects every direct command
// sink in the PR template. Literal command backticks render as apostrophes
// inside the surrounding inline-code span, while ordinary shell punctuation is
// preserved unchanged.
func TestCommandTextSanitizesMarkdownControlText(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    string
	}{
		{name: "LF", command: "go test\n## forged", want: "go test ## forged"},
		{name: "CRLF", command: "go test\r\n## forged", want: "go test ## forged"},
		{name: "bare CR", command: "go test\r## forged", want: "go test ## forged"},
		{name: "backticks", command: "printf `literal`", want: "printf 'literal'"},
		{name: "heading", command: "# forged heading", want: "# forged heading"},
		{name: "list item", command: "- forged list item", want: "- forged list item"},
		{name: "details", command: "<details>forged</details>", want: "<details>forged</details>"},
		{name: "HTML comment", command: "<!-- forged -->", want: "<!-- forged -->"},
		{name: "shell punctuation", command: `printf 'hello'; echo "$HOME" && exit 0`, want: `printf 'hello'; echo "$HOME" && exit 0`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command := VerifiedCommand{Comando: tc.command, Exit: 0}
			outputs := []struct {
				name string
				body string
			}{
				{name: "verification", body: verificationSection(TemplateVerification{
					Mode:     "determinista",
					Comandos: []VerifiedCommand{command},
				})},
				{name: "validation", body: validationSection([]VerifiedCommand{command})},
			}
			for _, output := range outputs {
				t.Run(output.name, func(t *testing.T) {
					want := "- ✅ `" + tc.want + "` (exit 0)"
					if !strings.Contains(output.body, want) {
						t.Fatalf("output missing sanitized command %q:\n%s", want, output.body)
					}
					if tc.command != tc.want && strings.Contains(output.body, tc.command) {
						t.Fatalf("output retains unsanitized command %q:\n%s", tc.command, output.body)
					}
				})
			}
		})
	}
}

// TestValidationSection: real exit codes from internal/validation (T1.8),
// distinct from verificationSection — with no commands it never invents a
// PASS.
func TestValidationSection(t *testing.T) {
	t.Run("with real exit codes", func(t *testing.T) {
		out := validationSection([]VerifiedCommand{
			{Comando: "go vet ./...", Exit: 0},
			{Comando: "go test ./...", Exit: 1},
		})
		if !strings.Contains(out, "go vet ./...") || !strings.Contains(out, "exit 0") {
			t.Errorf("missing the green commands: %s", out)
		}
		if !strings.Contains(out, "❌") || !strings.Contains(out, "exit 1") {
			t.Errorf("the non-zero exit must be marked: %s", out)
		}
	})
	t.Run("no commands invents nothing", func(t *testing.T) {
		out := validationSection(nil)
		if strings.Contains(out, "✅") {
			t.Errorf("without commands it must not invent a PASS: %s", out)
		}
	})
}

// TestRenderPRTemplateSeparatesValidationAndVerification: the template
// separates the pre-validation (ValidationRun) from the post-hoc
// verification (ops.Verify) into two dedicated sections, without mixing
// them (T1.8).
func TestRenderPRTemplateSeparatesValidationAndVerification(t *testing.T) {
	records := []Record{recordHelper("u1", "feat(a)", "m", revisionHelper("ok"))}
	verification := TemplateVerification{
		Mode:       "determinista",
		Comandos:   []VerifiedCommand{{Comando: "go test ./... (post-hoc)", Exit: 0}},
		Validation: []VerifiedCommand{{Comando: "go build ./... (validacion previa)", Exit: 1}},
	}
	out := RenderPRTemplate(records, nil, verification, "0.2.0", nil)
	if !strings.Contains(out, "## Validation") {
		t.Fatalf("missing the pre-validation section: %s", out)
	}
	if !strings.Contains(out, "go build ./... (validacion previa)") {
		t.Errorf("pre-validation must list its own command: %s", out)
	}
	if !strings.Contains(out, "go test ./... (post-hoc)") {
		t.Errorf("post-hoc verification must still appear: %s", out)
	}
}

func TestRenderPRTemplate(t *testing.T) {
	records := []Record{
		recordHelper("u1a", "feat(a)", "m",
			revisionHelper("ok",
				DimensionResult{Dim: DimLogic, Verdict: VerdictOK},
				DimensionResult{Dim: DimTests, Verdict: VerdictOK})),
	}
	overview := &OverviewResult{
		Coherent:  true,
		Rationale: "Coherent change\nthat completes the phase\nin three lines\nfor the rationale.",
	}
	verification := TemplateVerification{
		Mode: "determinista",
		Comandos: []VerifiedCommand{
			{Comando: "go vet ./...", Exit: 0},
		},
	}

	out := RenderPRTemplate(records, overview, verification, "0.2.0", nil)
	if !strings.Contains(out, "Audit verdict") {
		t.Errorf("missing the risk line: %s", out)
	}
	if !strings.Contains(out, "Coherent change") {
		t.Errorf("missing the overview rationale: %s", out)
	}
	if !strings.Contains(out, "OWN") {
		t.Errorf("missing the matrix: %s", out)
	}
	if !strings.Contains(out, "go vet ./...") {
		t.Errorf("missing the verification section: %s", out)
	}
	if !strings.Contains(out, "Generated by vcSentinel 0.2.0") {
		t.Errorf("missing the signature with version: %s", out)
	}
	if !strings.Contains(out, "not CI") {
		t.Errorf("the signature must clarify it is not CI: %s", out)
	}
	if len(out) > PRBodyLimit {
		t.Errorf("template exceeds the limit: %d", len(out))
	}
}

func TestRenderTemplateNoOverviewHonest(t *testing.T) {
	records := []Record{
		recordHelper("u1", "feat(a)", "m", revisionHelper("ok")),
	}
	out := RenderPRTemplate(records, nil, TemplateVerification{Mode: "omitido"}, "0.2.0", nil)
	if !strings.Contains(out, "No overview") {
		t.Errorf("with no overview it must be said, not silently omitted: %s", out)
	}
}

// TestBranchBlockersFiltersCriticals: only the CRITICAL findings of the
// last revision block; empty ones, no revision and lesser severities do not
// count.
func TestBranchBlockersFiltersCriticals(t *testing.T) {
	critical := ReviewFinding{Dimension: DimSecurity, File: "a.go", Line: 42,
		Severity: SevCritical, Description: "exposed data"}
	cases := []struct {
		name    string
		records []Record
		want    int
	}{
		{
			name:    "no records blocks nothing",
			records: nil,
			want:    0,
		},
		{
			name:    "record without revisions does not block",
			records: []Record{recordHelper("u1", "feat(a)", "m")},
			want:    0,
		},
		{
			name: "only lesser severities do not block",
			records: []Record{recordHelper("u1", "feat(a)", "m",
				revisionHelper("warn",
					DimensionResult{Dim: DimTests, Verdict: VerdictWarn,
						Findings: []ReviewFinding{{Dimension: DimTests, Severity: SevWarning, Description: "fragile"}}},
					DimensionResult{Dim: DimSpec, Verdict: VerdictOK,
						Findings: []ReviewFinding{{Dimension: DimSpec, Severity: SevAdvisory, Description: "scope"}}},
				))},
			want: 0,
		},
		{
			name: "mix with at least one CRITICAL lists only the criticals",
			records: []Record{
				recordHelper("u1", "feat(a)", "m",
					revisionHelper("block",
						DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock,
							Findings: []ReviewFinding{
								critical,
								{Dimension: DimSecurity, Severity: SevWarning, Description: "minor"},
							}},
					)),
				recordHelper("u2", "feat(b)", "m", revisionHelper("ok")),
			},
			want: 1,
		},
		{
			name: "only the last revision wins",
			records: []Record{recordHelper("u1", "feat(a)", "m",
				revisionHelper("block",
					DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock,
						Findings: []ReviewFinding{critical}}),
				revisionHelper("ok",
					DimensionResult{Dim: DimSecurity, Verdict: VerdictOK}),
			)},
			want: 0,
		},
		{
			name: "a resolved block does not block",
			records: func() []Record {
				resolved := critical
				resolved.Status = StatusRefuted
				fixed := recordHelper("f1", "feat(a)", "m",
					revisionHelper("block",
						DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock,
							Findings: []ReviewFinding{resolved}}))
				fixed.FixedIn = "a1b2c3d"
				return []Record{fixed}
			}(),
			want: 0,
		},
		{
			name: "a credited fix does not retire a live critical",
			records: func() []Record {
				fixed := recordHelper("f1", "feat(a)", "m",
					revisionHelper("block",
						DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock,
							Findings: []ReviewFinding{critical}}))
				fixed.FixedIn = "a1b2c3d"
				return []Record{fixed}
			}(),
			want: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BranchBlockers(c.records, nil)
			if len(got) != c.want {
				t.Errorf("BranchBlockers() = %d findings, expected %d: %+v",
					len(got), c.want, got)
			}
		})
	}
}

// TestBranchBlockersMapsAggregatedFindingsFields: an integration test that
// exercises BranchBlockers through a real AggregatedFindings CRITICAL
// Finding (T6.1 merge + T6.2 supersede result) and asserts the actual
// content of the returned ReviewFinding — every field
// reviewFindingFromFinding maps — not only len(got). Without this,
// reverting the T6.5 hunk that switched BranchBlockers from iterating
// Dims to ultima.EffectiveFindings() would not fail any test today, because
// TestBranchBlockersFiltersCriticals only ever builds Dims-based fixtures
// (T6.5 review finding: tests WARNING).
func TestBranchBlockersMapsAggregatedFindingsFields(t *testing.T) {
	record := recordHelper("6b127cd", "fix(auth): tighten token check", "m", Revision{
		At:     time.Now().UTC(),
		Result: "block",
		AggregatedFindings: []Finding{
			{
				Dimension:   DimSecurity,
				Severity:    SevCritical,
				Source:      SourceReview,
				Confidence:  0.9,
				Description: "token comparison is not constant-time",
				Location:    Location{File: "auth.go", LineStart: 42},
			},
		},
	})

	got := BranchBlockers([]Record{record}, nil)
	if len(got) != 1 {
		t.Fatalf("BranchBlockers() = %d findings, expected 1: %+v", len(got), got)
	}
	expected := ReviewFinding{
		Dimension:   DimSecurity,
		File:        "auth.go",
		Line:        42,
		Severity:    SevCritical,
		Description: "token comparison is not constant-time",
		Source:      SourceReview,
	}
	if got[0] != expected {
		t.Errorf("BranchBlockers()[0] = %+v, want %+v", got[0], expected)
	}
}

// TestRisksRenderMergedFindingWithSourceAndEvidence: a merged Finding
// (T6.1 aggregation + T6.2 supersede result, AuditResult.Findings
// persisted on Revision.AggregatedFindings by T6.5) renders its distinguished
// Source (review vs validation) and every accumulated evidence from
// EvidenceSet instead of only the single legacy Evidence string.
func TestRisksRenderMergedFindingWithSourceAndEvidence(t *testing.T) {
	record := recordHelper("6b127cd", "fix(auth): tighten token check", "m", Revision{
		At:     time.Now().UTC(),
		Result: "block",
		AggregatedFindings: []Finding{
			{
				Dimension:   DimSecurity,
				Severity:    SevCritical,
				Source:      SourceReview,
				Confidence:  0.9,
				Description: "token comparison is not constant-time",
				Location:    Location{File: "auth.go", LineStart: 42},
				EvidenceSet: &FindingEvidenceSet{Values: []FindingEvidence{
					{Dimension: DimSecurity, Evidence: "token == expected", Confidence: 0.8},
					{Dimension: DimLogic, Evidence: "no hmac.Equal usage found", Confidence: 0.95},
				}},
			},
		},
	})

	out := RenderPRTemplate([]Record{record}, nil, TemplateVerification{Mode: "omitido"}, "0.2.0", nil)

	if !strings.Contains(out, "review") {
		t.Errorf("merged finding must show its distinguished Source (review): %s", out)
	}
	if !strings.Contains(out, "token == expected") || !strings.Contains(out, "no hmac.Equal usage found") {
		t.Errorf("merged finding must show every accumulated evidence from EvidenceSet: %s", out)
	}
	if !strings.Contains(out, "0.90") {
		t.Errorf("merged finding must show the combined confidence: %s", out)
	}
}

// TestRisksMergedValidationSourceLabel: a merged Finding sourced from
// deterministic validation (SourceValidation) is labeled distinctly from one
// sourced from semantic review (SourceReview).
func TestRisksMergedValidationSourceLabel(t *testing.T) {
	record := recordHelper("945b5b5", "fix(build): repair lint failure", "m", Revision{
		At:     time.Now().UTC(),
		Result: "block",
		AggregatedFindings: []Finding{
			{
				Dimension:   DimStyle,
				Severity:    SevWarning,
				Source:      SourceValidation,
				Confidence:  1,
				Description: "gofmt reported an unformatted file",
				Location:    Location{File: "main.go", LineStart: 1},
				Evidence:    "gofmt -l main.go",
			},
		},
	})

	lines := pendingRisksWithDispositions([]Record{record}, nil)
	if len(lines) != 1 {
		t.Fatalf("pending risks = %d lines, expected 1: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "validation") {
		t.Errorf("merged finding sourced from validation must be labeled distinctly: %s", lines[0])
	}
	if strings.Contains(lines[0], "review") {
		t.Errorf("a validation-sourced finding must not be mislabeled review: %s", lines[0])
	}
	if !strings.Contains(lines[0], "gofmt -l main.go") {
		t.Errorf("a non-merged Finding (EvidenceSet nil) must fall back to its legacy Evidence string: %s", lines[0])
	}
}

// TestRisksFallBackToLegacyDimsWithoutAggregatedFindings: a Revision saved
// before T6.5 (or by a caller that never propagated AggregatedFindings)
// still surfaces its Dims-based findings — EffectiveFindings (T6.5 review
// finding: design) converts them to Finding so the pending risks keep working
// unchanged from the caller's point of view.
func TestRisksFallBackToLegacyDimsWithoutAggregatedFindings(t *testing.T) {
	record := recordHelper("aaaaaaa", "feat(a): legacy", "m",
		revisionHelper("block",
			DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock,
				Findings: []ReviewFinding{{Dimension: DimSecurity, File: "a.go", Line: 1, Severity: SevCritical, Description: "legacy finding"}}},
		))

	lines := pendingRisksWithDispositions([]Record{record}, nil)
	if len(lines) != 1 || !strings.Contains(lines[0], "legacy finding") {
		t.Errorf("legacy Dims-based rendering must still work when AggregatedFindings is empty: %v", lines)
	}
}

// TestRisksFilterAdvisoryFromAggregatedFindings: the AggregatedFindings
// path must exclude ADVISORY the same way the legacy path always did — this
// path had no coverage of its own severity filter before (T6.5 review
// finding: tests WARNING).
func TestRisksFilterAdvisoryFromAggregatedFindings(t *testing.T) {
	record := recordHelper("6b127cd", "fix(x): thing", "m", Revision{
		At:     time.Now().UTC(),
		Result: "block",
		AggregatedFindings: []Finding{
			{Dimension: DimSecurity, Severity: SevCritical, Description: "critical one"},
			{Dimension: DimSpec, Severity: SevAdvisory, Description: "advisory one"},
		},
	})

	lines := pendingRisksWithDispositions([]Record{record}, nil)
	if len(lines) != 1 {
		t.Fatalf("pending risks = %d lines, expected 1 (ADVISORY excluded): %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "critical one") {
		t.Errorf("missing the CRITICAL finding: %v", lines)
	}
	if strings.Contains(lines[0], "advisory one") {
		t.Errorf("ADVISORY must not appear in the pending risks: %v", lines)
	}
}

// FU-6: PR risk rendering uses the same effective disposition projection as
// branch blocking, so a human-refuted CRITICAL cannot remain in the template.
func TestRenderBranchPRTemplateHidesRefutedCritical(t *testing.T) {
	record := recordHelper("abc12345", "fix(auth): explain false positive", "m", Revision{
		At:     time.Now().UTC(),
		Result: VerdictBlock,
		AggregatedFindings: []Finding{{
			Dimension: DimSecurity, Severity: SevCritical, Status: StatusConfirmed,
			Fingerprint: "fp-critical", Description: "refuted critical",
			Location: Location{File: "auth.go", LineStart: 12},
		}},
	})
	res := &BranchResult{Records: []Record{record}}
	dispositions := []FindingDisposition{{
		SHA: "abc12345", Fingerprint: "fp-critical", Status: StatusRefuted,
	}}

	body := RenderBranchPRTemplate(res, TemplateVerification{Mode: "omitido"}, "0.2.0", dispositions)
	if strings.Contains(body, "refuted critical") {
		t.Fatalf("PR template still renders the refuted critical:\n%s", body)
	}
}

// TestRenderBranchPRTemplateReportsUnauditedCommits: docs/issues/actionable.md
// item 2 — a PR body must say plainly when a commit on the branch has no
// review record of its own, name it, and point at the audit command. It must
// not gate: the net verdict remains authoritative regardless.
func TestRenderBranchPRTemplateReportsUnauditedCommits(t *testing.T) {
	res := &BranchResult{
		Records:   nil,
		Unaudited: []UnauditedCommit{{SHA: "cafe1234cafe1234cafe1234cafe1234cafe1234", Subject: "feat(x): x"}},
		Net:       &NetReview{Audit: AuditResult{Verdict: VerdictOK}},
	}
	body := RenderBranchPRTemplate(res, TemplateVerification{Mode: "omitido"}, "0.2.0", nil)
	for _, want := range []string{"no review record", "cafe123", "feat(x): x", "sentinel review cafe1234cafe1234cafe1234cafe1234cafe1234"} {
		if !strings.Contains(body, want) {
			t.Errorf("PR body missing %q:\n%s", want, body)
		}
	}
}

// TestRenderMergedFindingOmitsEmptyLocation: a Finding without a resolved
// location must not render the placeholder "(:0)" — the location suffix is
// omitted entirely instead (T6.5 review finding: logic ADVISORY). This same
// Finding also never carries a real Source (empty), so the
// "(source, confidence)" segment is omitted too instead of fabricating
// "(unknown, confidence 0.00)" (T6.5bis review finding: logic WARNING).
func TestRenderMergedFindingOmitsEmptyLocation(t *testing.T) {
	h := Finding{Dimension: DimLogic, Severity: SevWarning, Description: "no location resolved"}
	got := renderMergedFinding("abc1234", h)
	expected := "- ⚠️ `abc1234` [logic] WARNING — no location resolved"
	if got != expected {
		t.Errorf("renderMergedFinding() = %q, want %q", got, expected)
	}
}

// TestRenderMergedFindingOmitsSourceSegmentOnlyWhenEmpty: a Finding with a
// real, non-empty Source (even one EffectiveFindings never produces itself,
// e.g. a future/unknown value) still renders the "(source, confidence)"
// segment through mergedFindingSourceLabel's own "unknown" fallback — only
// Source == "" (the legacy-conversion signal) omits the segment entirely.
func TestRenderMergedFindingOmitsSourceSegmentOnlyWhenEmpty(t *testing.T) {
	h := Finding{Dimension: DimLogic, Severity: SevWarning, Source: "future-source", Confidence: 0.42, Description: "d"}
	got := renderMergedFinding("abc1234", h)
	expected := "- ⚠️ `abc1234` [logic] WARNING (unknown, confidence 0.42) — d"
	if got != expected {
		t.Errorf("renderMergedFinding() = %q, want %q", got, expected)
	}
}

// TestRenderMergedFindingSanitizesDescriptionAndLocation: Finding.Description
// and Location.File share Evidence's untrusted origin — both are decoded
// straight from the LLM's rawFinding JSON (finding.go), same as Evidence —
// yet renderMergedFinding interpolated them raw into the same Markdown list
// item that already sanitizes Evidence. An embedded newline or backtick in
// either could otherwise break or forge a Markdown list line, exactly the
// injection sanitizeEvidence exists to prevent (T6.5bis review finding:
// security WARNING). Description is prose, not evidence/code, so it is
// sanitized without being wrapped in inline code (unlike Evidence).
func TestRenderMergedFindingSanitizesDescriptionAndLocation(t *testing.T) {
	h := Finding{
		Dimension:   DimSecurity,
		Severity:    SevWarning,
		Source:      SourceReview,
		Confidence:  0.5,
		Description: "line one\nline two with a ` backtick",
		Location:    Location{File: "a\nb`.go", LineStart: 1},
	}
	got := renderMergedFinding("abc1234", h)
	if strings.Count(got, "\n") != 0 {
		t.Fatalf("no evidence lines expected here, result must be a single line: %q", got)
	}
	if strings.Contains(got, "line one\nline two") {
		t.Errorf("Description newline must be collapsed, not embedded raw: %q", got)
	}
	if strings.Contains(got, "with a ` backtick") {
		t.Errorf("a literal backtick in Description must be escaped: %q", got)
	}
	if strings.Contains(got, "a\nb") || strings.Contains(got, "b`.go") {
		t.Errorf("Location.File newline/backtick must be sanitized: %q", got)
	}
}

// TestMergedFindingEvidenceLinesFallsBackWhenEvidenceSetEmpty: an
// EvidenceSet that is non-nil but carries no Values (e.g. deserialized from
// {"evidence_set":{"values":[]}}) must still fall back to the legacy
// Evidence string instead of silently dropping it (T6.5 review finding:
// logic WARNING).
func TestMergedFindingEvidenceLinesFallsBackWhenEvidenceSetEmpty(t *testing.T) {
	h := Finding{
		Dimension:   DimLogic,
		Evidence:    "legacy evidence text",
		Confidence:  0.5,
		EvidenceSet: &FindingEvidenceSet{},
	}
	lines := mergedFindingEvidenceLines(h)
	if len(lines) != 1 || !strings.Contains(lines[0], "legacy evidence text") {
		t.Errorf("mergedFindingEvidenceLines() = %v, expected fallback to Evidence", lines)
	}
}

// TestSanitizeEvidenceTruncatesLongEvidence: evidence text is bounded before
// it reaches the PR body — an oversized fragment (possibly an embedded
// secret in a security finding) must not be published in full on an
// external, indexable, cached surface (T6.5 review finding: security
// WARNING).
func TestSanitizeEvidenceTruncatesLongEvidence(t *testing.T) {
	long := strings.Repeat("x", evidenceEmbedMaxBytes+100)
	got := sanitizeEvidence(long)
	if len(got) > evidenceEmbedMaxBytes+2 { // +2: the wrapping backticks.
		t.Errorf("sanitizeEvidence did not bound the evidence length: %d bytes", len(got))
	}
}

// TestSanitizeEvidenceCollapsesNewlinesAndEscapesBackticks: evidence text is
// untrusted (an LLM inference or a command's literal output); an embedded
// newline or backtick must not be able to break or forge the surrounding
// Markdown list structure sent to GitHub (T6.5 review finding: security
// WARNING).
func TestSanitizeEvidenceCollapsesNewlinesAndEscapesBackticks(t *testing.T) {
	got := sanitizeEvidence("line one\nline two\r\nwith a ` backtick")
	if strings.Contains(got, "\n") || strings.Contains(got, "\r") {
		t.Errorf("sanitizeEvidence must collapse internal line breaks: %q", got)
	}
	if !strings.HasPrefix(got, "`") || !strings.HasSuffix(got, "`") {
		t.Fatalf("sanitized evidence must be wrapped in inline code: %q", got)
	}
	inner := got[1 : len(got)-1]
	if strings.Contains(inner, "`") {
		t.Errorf("a literal backtick inside the evidence must not terminate the code span early: %q", got)
	}
}

// TestRenderTemplateRisksSection: the "## Risks" section appears with
// the placeholder when there are no risks and with rendered lines when there
// are pending CRITICAL/WARNING findings (ADVISORY ones are not listed).
func TestRenderTemplateRisksSection(t *testing.T) {
	recordsWithoutRisks := []Record{recordHelper("u1", "feat(a)", "m", revisionHelper("ok"))}
	outEmpty := RenderPRTemplate(recordsWithoutRisks, nil, TemplateVerification{Mode: "omitido"}, "0.2.0", nil)
	if !strings.Contains(outEmpty, "## Risks") {
		t.Fatalf("missing the Risks section: %s", outEmpty)
	}
	if !strings.Contains(outEmpty, "No pending risks") {
		t.Errorf("with no risks the honest placeholder must be shown: %s", outEmpty)
	}

	recordsWithRisks := []Record{recordHelper("u1", "feat(a)", "m",
		revisionHelper("warn",
			DimensionResult{Dim: DimSecurity, Verdict: VerdictWarn,
				Findings: []ReviewFinding{
					{Dimension: DimSecurity, File: "a.go", Line: 7,
						Severity: SevCritical, Description: "exposed data"},
					{Dimension: DimSpec, File: "b.go", Line: 1,
						Severity: SevAdvisory, Description: "broad scope"},
				}},
		))}
	outWith := RenderPRTemplate(recordsWithRisks, nil, TemplateVerification{Mode: "omitido"}, "0.2.0", nil)
	if !strings.Contains(outWith, "exposed data") {
		t.Errorf("the CRITICAL must be listed under Risks: %s", outWith)
	}
	if !strings.Contains(outWith, "CRITICAL") {
		t.Errorf("the risk line must quote the severity: %s", outWith)
	}
	if strings.Contains(outWith, "broad scope") {
		t.Errorf("ADVISORY entries are not risks and must not be listed: %s", outWith)
	}
	if strings.Contains(outWith, "No pending risks") {
		t.Errorf("with risks the placeholder must not be shown: %s", outWith)
	}

	// A resolved block is not a pending risk.
	fixed := recordHelper("f1", "feat(a)", "m",
		revisionHelper("block",
			DimensionResult{Dim: DimSecurity, Verdict: VerdictBlock,
				Findings: []ReviewFinding{{Dimension: DimSecurity, Severity: SevCritical,
					Description: "exposed data", Status: StatusRefuted}}},
		))
	fixed.FixedIn = "a1b2c3d"
	outFixed := RenderPRTemplate([]Record{fixed}, nil, TemplateVerification{Mode: "omitido"}, "0.2.0", nil)
	if strings.Contains(outFixed, "exposed data") {
		t.Errorf("the findings of a corrected record are not pending risks: %s", outFixed)
	}
	if !strings.Contains(outFixed, "No pending risks") {
		t.Errorf("with everything fixed the placeholder must be shown: %s", outFixed)
	}
}
