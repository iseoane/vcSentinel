package review

import (
	"errors"
	"strings"
	"testing"
)

// FU-6: one shared blocking predicate across engine, gate, and
// BranchBlockers. A human refutation clears its finding; accepted_by_user
// is never a free unblock; fixed is nonblocking; reopened still blocks.
func TestIsBlockingSharesOneRuleAcrossConsumers(t *testing.T) {
	cases := []struct {
		name     string
		severity string
		status   string
		want     bool
	}{
		{"pending critical blocks", SevCritical, StatusPending, true},
		{"confirmed critical blocks", SevCritical, StatusConfirmed, true},
		{"empty status critical blocks", SevCritical, "", true},
		{"accepted_by_user critical still blocks", SevCritical, StatusAcceptedByUser, true},
		{"reopened critical blocks", SevCritical, StatusReopened, true},
		{"refuted critical clears", SevCritical, StatusRefuted, false},
		{"fixed critical clears", SevCritical, StatusFixed, false},
		{"non-critical never blocks", SevWarning, StatusConfirmed, false},
		{"non-critical refuted never blocks", SevWarning, StatusRefuted, false},
		{"padded refuted clears", SevCritical, "  REFUTED  ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsBlocking(tc.severity, tc.status); got != tc.want {
				t.Fatalf("IsBlocking(%q, %q) = %v, want %v", tc.severity, tc.status, got, tc.want)
			}
		})
	}
}

// FU-6: one domain projection overlays append-only human dispositions onto
// effective findings without mutating them. Only the matching fingerprint is
// cleared; unrelated findings keep blocking.
func TestApplyDispositionsClearsOnlyTheMatchingFinding(t *testing.T) {
	target := Finding{
		Dimension: DimSecurity, Severity: SevCritical, Status: StatusConfirmed,
		Description: "injected query", Fingerprint: "fp-target",
		Location: Location{File: "a.go", LineStart: 10},
		Producer: Producer{Agent: "agent-a", Model: "model-a"},
	}
	other := Finding{
		Dimension: DimSecurity, Severity: SevCritical, Status: StatusConfirmed,
		Description: "unchecked input", Fingerprint: "fp-other",
		Location: Location{File: "b.go", LineStart: 30},
		Producer: Producer{Agent: "agent-a", Model: "model-a"},
	}
	dispositions := []FindingDisposition{{
		SHA: "abc123", Fingerprint: "fp-target", Status: StatusRefuted,
		Reason: "verified safe", Path: "a.go", LineStart: 9, LineEnd: 11,
		Evidence: "safe call", RangeHash: "deadbeef",
		Actor: RefutationActorHuman, Source: DispositionSourceHuman,
	}}

	got := ApplyDispositions([]Finding{target, other}, dispositions)

	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Status != StatusRefuted || got[0].RefutationActor != RefutationActorHuman {
		t.Fatalf("target = %+v, want refuted with human provenance", got[0])
	}
	if got[0].InvocationID != "" {
		t.Fatalf("target invocation = %q, human decisions cite no invocation", got[0].InvocationID)
	}
	if got[0].Producer.Agent != "agent-a" || got[0].Producer.Model != "model-a" {
		t.Fatalf("target producer = %+v, applying a disposition must not rewrite who produced the finding", got[0].Producer)
	}
	if !IsBlocking(got[1].Severity, got[1].Status) {
		t.Fatalf("unrelated finding = %+v, must keep blocking", got[1])
	}
	if target.Status != StatusConfirmed {
		t.Fatal("the input finding was mutated in place")
	}
}

// FU-6: an ambiguous fingerprint must fail closed at application time: when
// two live findings share one fingerprint, neither may be cleared by it.
func TestApplyDispositionsRefusesAmbiguousFingerprints(t *testing.T) {
	mk := func(desc string) Finding {
		return Finding{
			Dimension: DimLogic, Severity: SevCritical, Status: StatusConfirmed,
			Description: desc, Fingerprint: "fp-shared",
			Location: Location{File: "a.go", LineStart: 10},
		}
	}
	dispositions := []FindingDisposition{{
		SHA: "abc123", Fingerprint: "fp-shared", Status: StatusRefuted,
		Reason: "verified safe", Actor: RefutationActorHuman, Source: DispositionSourceHuman,
	}}

	got := ApplyDispositions([]Finding{mk("first"), mk("second")}, dispositions)
	for i, h := range got {
		if !IsBlocking(h.Severity, h.Status) {
			t.Fatalf("finding %d = %+v, ambiguous fingerprints must stay blocking", i, h)
		}
	}
}

// FU-6 defect 1: a standing disposition records the severity the human
// answered as audit metadata, and the application layer treats it as a
// severity ceiling. The same fingerprint re-audited at a MORE severe
// severity must still block: the recorded answer answered the finding as it
// was recorded, not the escalated one. Equal severity still applies, and
// legacy records without a recorded severity keep applying
// (apply-as-unknown) so no historical refutation is retroactively
// un-answered.
func TestApplyDispositionsRefusesEscalatedSeverity(t *testing.T) {
	target := Finding{
		Dimension: DimSecurity, Severity: SevCritical, Status: StatusConfirmed,
		Description: "injected query", Fingerprint: "fp-escalated",
		Location: Location{File: "a.go", LineStart: 10},
	}
	standing := FindingDisposition{
		SHA: "abc123", Fingerprint: "fp-escalated", Status: StatusRefuted,
		Reason: "verified safe", Path: "a.go", LineStart: 9, LineEnd: 11,
		Evidence: "safe call", RangeHash: "deadbeef",
		Actor: RefutationActorHuman, Source: DispositionSourceHuman,
		TargetSeverity: SevWarning,
	}

	t.Run("same fingerprint with escalated severity still blocks", func(t *testing.T) {
		got := ApplyDispositions([]Finding{target}, []FindingDisposition{standing})
		if got[0].Status != StatusConfirmed {
			t.Fatalf("status = %q, want the escalated finding left untouched", got[0].Status)
		}
		if !IsBlocking(got[0].Severity, got[0].Status) {
			t.Fatalf("finding = %+v, an escalated finding must keep blocking", got[0])
		}
		if got[0].RefutationActor != "" || got[0].RefutationReason != "" {
			t.Fatalf("refutation = %q/%q, a refused disposition must not stamp anything", got[0].RefutationActor, got[0].RefutationReason)
		}
	})
	t.Run("equal severity still applies", func(t *testing.T) {
		equal := target
		equal.Severity = SevWarning
		got := ApplyDispositions([]Finding{equal}, []FindingDisposition{standing})
		if got[0].Status != StatusRefuted {
			t.Fatalf("status = %q, want the standing refutation applied at equal severity", got[0].Status)
		}
	})
	t.Run("de-escalated severity still applies", func(t *testing.T) {
		lower := target
		lower.Severity = SevWarning
		answer := standing
		answer.TargetSeverity = SevCritical
		got := ApplyDispositions([]Finding{lower}, []FindingDisposition{answer})
		if got[0].Status != StatusRefuted {
			t.Fatalf("status = %q, want the standing refutation applied when the re-audit de-escalates", got[0].Status)
		}
	})
	t.Run("legacy record without recorded severity still applies", func(t *testing.T) {
		legacy := standing
		legacy.TargetSeverity = ""
		got := ApplyDispositions([]Finding{target}, []FindingDisposition{legacy})
		if got[0].Status != StatusRefuted {
			t.Fatalf("status = %q, want apply-as-unknown for legacy records", got[0].Status)
		}
	})
}

// FU-6 defect 1 through the engine overlay entry point: a standing
// refutation against a re-audited finding that escalated past its recorded
// severity reports nothing cleared, and the finding keeps blocking.
func TestApplyDispositionToResultRefusesEscalatedSeverity(t *testing.T) {
	result := &DimensionResult{
		Dim:     DimLogic,
		Verdict: VerdictBlock,
		Findings: []ReviewFinding{
			{Dimension: DimLogic, File: "a.go", Line: 2, Severity: SevCritical, Description: "bug", Status: StatusConfirmed},
		},
	}
	disp := FindingDisposition{
		Fingerprint: EffectiveFingerprint(findingWithDisposition(DimLogic, result.Findings[0])),
		Status:      StatusRefuted, Reason: "verified safe",
		Actor: RefutationActorHuman, Source: DispositionSourceHuman,
		TargetSeverity: SevWarning,
	}

	if ApplyDispositionToResult(result, disp) {
		t.Fatal("an escalated finding must not be reported as cleared")
	}
	if !IsBlocking(result.Findings[0].Severity, result.Findings[0].Status) {
		t.Fatalf("finding = %+v, an escalated finding must keep blocking", result.Findings[0])
	}
}

// FU-6: addressing is by reviewed revision plus stable fingerprint. A missing
// fingerprint and an ambiguous one both fail closed without persisting.
func TestResolveDispositionTargetRejectsMissingAndAmbiguous(t *testing.T) {
	mk := func(desc, fp string) Finding {
		return Finding{
			Dimension: DimLogic, Severity: SevCritical, Status: StatusConfirmed,
			Description: desc, Fingerprint: fp,
			Location: Location{File: "a.go", LineStart: 10},
		}
	}
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			{Dimension: DimLogic, File: "a.go", Line: 10, Severity: SevCritical, Description: "first"},
			{Dimension: DimLogic, File: "a.go", Line: 10, Severity: SevCritical, Description: "second"},
		}}},
		AggregatedFindings: []Finding{mk("first", "fp-first"), mk("second", "fp-second")},
	}

	if _, err := ResolveDispositionTarget(revision, ""); err == nil {
		t.Fatal("empty fingerprint was accepted")
	}
	if _, err := ResolveDispositionTarget(revision, "   "); err == nil {
		t.Fatal("whitespace fingerprint was accepted")
	}
	if _, err := ResolveDispositionTarget(revision, "fp-absent"); err == nil {
		t.Fatal("missing fingerprint was accepted")
	}
	target, err := ResolveDispositionTarget(revision, "fp-first")
	if err != nil {
		t.Fatalf("exact fingerprint rejected: %v", err)
	}
	if target.Description != "first" {
		t.Fatalf("target = %+v, want the first finding", target)
	}

	twins := Revision{
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			{Dimension: DimLogic, File: "a.go", Line: 10, Severity: SevCritical, Description: "twin"},
			{Dimension: DimLogic, File: "a.go", Line: 10, Severity: SevCritical, Description: "twin"},
		}}},
		AggregatedFindings: []Finding{mk("twin", "fp-twin"), mk("twin", "fp-twin")},
	}
	if _, err := ResolveDispositionTarget(twins, "fp-twin"); err == nil {
		t.Fatal("ambiguous fingerprint was accepted")
	}
}

// FU-6: the human path reuses the existing refutation gate byte for byte:
// SHA echo, safe path, range bounds, finding containment, exact snapshot
// evidence, and range hashing. The evidence comes from the audited object.
func TestValidateHumanRefutationRangeEnforcesTheRefutationGate(t *testing.T) {
	const sha = "abc12345"
	const content = "const unrelated = true\ncriticalCall()\n"
	reader := func(gotSHA, file string) (string, error) {
		if gotSHA != sha {
			return "", errors.New("unknown commit")
		}
		if file != "a.go" {
			return "", errors.New("unknown file")
		}
		return content, nil
	}

	safePath, evidence, hash, err := ValidateHumanRefutationRange(reader, sha, "a.go", 2, "the committed implementation is safe", "a.go", 2, 2)
	if err != nil {
		t.Fatalf("valid range rejected: %v", err)
	}
	if safePath != "a.go" {
		t.Fatalf("path = %q, want the sanitized evidence path", safePath)
	}
	if evidence != "criticalCall()" {
		t.Fatalf("evidence = %q, want the exact snapshot extract", evidence)
	}
	if want := "1f4902c8f5b87a9dc694f279ef8bd2e6d491934eb00169644a05462d7f11b34f"; hash != want {
		t.Fatalf("hash = %q, want %q", hash, want)
	}

	cases := []struct {
		name        string
		reason      string
		file        string
		lineStart   int
		lineEnd     int
		findingFile string
		findingLine int
		sha         string
	}{
		{"empty reason", "", "a.go", 2, 2, "a.go", 2, sha},
		{"sha mismatch", "safe", "a.go", 2, 2, "a.go", 2, "other"},
		{"other file", "safe", "b.go", 2, 2, "a.go", 2, sha},
		{"absolute path", "safe", "/etc/passwd", 2, 2, "a.go", 2, sha},
		{"traversal", "safe", "../a.go", 2, 2, "a.go", 2, sha},
		{"dash-prefixed", "safe", "-a.go", 2, 2, "a.go", 2, sha},
		{"windows drive", "safe", `C:\a.go`, 2, 2, "a.go", 2, sha},
		{"finding outside range", "safe", "a.go", 1, 1, "a.go", 2, sha},
		{"reversed range", "safe", "a.go", 3, 2, "a.go", 2, sha},
		{"oversized range", "safe", "a.go", 1, 21, "a.go", 2, sha},
		{"range outside file", "safe", "a.go", 2, 4, "a.go", 2, sha},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, err := ValidateHumanRefutationRange(reader, tc.sha, tc.findingFile, tc.findingLine, tc.reason, tc.file, tc.lineStart, tc.lineEnd); err == nil {
				t.Fatal("invalid range was accepted")
			}
		})
	}
}

// FU-6: an unreadable snapshot fails closed.
func TestValidateHumanRefutationRangeRejectsSnapshotErrors(t *testing.T) {
	broken := func(string, string) (string, error) {
		return "", errors.New("snapshot unavailable")
	}
	if _, _, _, err := ValidateHumanRefutationRange(broken, "abc12345", "a.go", 2, "safe", "a.go", 2, 2); err == nil {
		t.Fatal("snapshot error was accepted")
	}
}

// FU-6: short evidence cannot carry a refutation, exactly as in the
// automated gate (12 significant characters minimum).
func TestValidateHumanRefutationRangeRejectsShortEvidence(t *testing.T) {
	reader := func(string, string) (string, error) {
		return "x := 1\ny := 2\n", nil
	}
	if _, _, _, err := ValidateHumanRefutationRange(reader, "abc12345", "a.go", 1, "safe", "a.go", 1, 1); err == nil {
		t.Fatal("short evidence was accepted")
	}
}

// FU-6: untrusted CLI ranges must be rejected before slicing the immutable
// snapshot. Inverted or out-of-bounds starts used to panic here instead of
// failing closed.
func TestValidateHumanRefutationRangeRejectsInvalidBoundsWithoutPanic(t *testing.T) {
	reader := func(string, string) (string, error) {
		return "const safe = true\ncriticalCall()\n", nil
	}
	cases := []struct {
		name      string
		lineStart int
		lineEnd   int
	}{
		{"inverted", 2, 1},
		{"start beyond file", 4, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("ValidateHumanRefutationRange panicked: %v", recovered)
				}
			}()
			if _, _, _, err := ValidateHumanRefutationRange(reader, "abc12345", "a.go", 2, "safe premise", "a.go", tc.lineStart, tc.lineEnd); err == nil {
				t.Fatal("invalid bounds were accepted")
			}
		})
	}
}

// FU-6 defect 2: a finding without a line (Location.LineStart <= 0, the
// convention for deterministic findings citing a bare file path, e.g.
// `gofmt -l`) could never be refuted: the shared gate requires the finding
// line to fall inside the evidence range. The human path scopes the evidence
// to the audited file instead: the extract must still match the committed
// object exactly, so the gate stays fail-closed.
func TestValidateHumanRefutationRangeAcceptsFileScopedEvidenceForLinelessFindings(t *testing.T) {
	const sha = "abc12345"
	reader := func(gotSHA, file string) (string, error) {
		if gotSHA != sha {
			return "", errors.New("unknown commit")
		}
		if file != "a.go" {
			return "", errors.New("unknown file")
		}
		return "const unrelated = true\ncriticalCall()\n", nil
	}
	for _, findingLine := range []int{0, -3} {
		safePath, evidence, hash, err := ValidateHumanRefutationRange(reader, sha, "a.go", findingLine, "the committed implementation is safe", "a.go", 2, 2)
		if err != nil {
			t.Fatalf("line-less finding (line %d) rejected a valid file-scoped evidence range: %v", findingLine, err)
		}
		if safePath != "a.go" {
			t.Fatalf("path = %q, want the sanitized evidence path", safePath)
		}
		if evidence != "criticalCall()" {
			t.Fatalf("evidence = %q, want the exact snapshot extract", evidence)
		}
		if want := "1f4902c8f5b87a9dc694f279ef8bd2e6d491934eb00169644a05462d7f11b34f"; hash != want {
			t.Fatalf("hash = %q, want %q", hash, want)
		}
	}
}

// FU-6 defect 2: the file-scoped path for line-less findings keeps every
// other gate check: the evidence stays pinned to the finding's cited file,
// the 20-line window, the file bounds, and the 12-character minimum.
func TestValidateHumanRefutationRangeLinelessStillFailsClosed(t *testing.T) {
	const sha = "abc12345"
	reader := func(gotSHA, file string) (string, error) {
		if gotSHA != sha {
			return "", errors.New("unknown commit")
		}
		switch file {
		case "a.go":
			return "const unrelated = true\ncriticalCall()\n", nil
		case "b.go":
			return "package other\n", nil
		case "long.go":
			return strings.Repeat("x := 1\n", 25), nil
		}
		return "", errors.New("unknown file")
	}
	cases := []struct {
		name      string
		file      string
		lineStart int
		lineEnd   int
	}{
		{"other file", "b.go", 1, 1},
		{"traversal path", "../a.go", 1, 1},
		{"oversized range", "long.go", 1, 21},
		{"range outside file", "a.go", 2, 4},
		{"empty extract", "a.go", 3, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, _, err := ValidateHumanRefutationRange(reader, sha, "a.go", 0, "the committed implementation is safe", tc.file, tc.lineStart, tc.lineEnd); err == nil {
				t.Fatal("invalid evidence was accepted for a line-less finding")
			}
		})
	}
}

// FU-6: the engine overlay applies the human answer to the matching finding
// by its stable fingerprint, exactly the identity the persisted record
// carries, so the engine verdict and the persisted revision agree.
func TestApplyDispositionToResultAppliesHumanAnswer(t *testing.T) {
	result := &DimensionResult{
		Dim:     DimLogic,
		Verdict: VerdictBlock,
		Findings: []ReviewFinding{
			{Dimension: DimLogic, File: "a.go", Line: 2, Severity: SevCritical, Description: "bug", Status: StatusConfirmed},
		},
	}
	disp := FindingDisposition{
		SHA:         "abc12345",
		Fingerprint: EffectiveFingerprint(findingWithDisposition(DimLogic, result.Findings[0])),
		Status:      StatusRefuted,
		Reason:      "verified safe", Path: "a.go", LineStart: 2, LineEnd: 2,
		Evidence: "criticalCall()", RangeHash: "1f49",
		Actor: RefutationActorHuman, Source: DispositionSourceHuman,
		TargetDimension: DimLogic, TargetLine: 2, TargetDescription: "bug",
	}

	if !ApplyDispositionToResult(result, disp) {
		t.Fatal("expected the disposition to clear a blocking finding")
	}
	applied := result.Findings[0]
	if applied.Status != StatusRefuted || applied.RefutationActor != RefutationActorHuman || applied.RefutationRangeHash != "1f49" {
		t.Fatalf("finding = %+v, want the human answer applied", applied)
	}
}

// FU-6: a disposition may only clear one identity. Twin findings at the same
// location and description share one computed fingerprint, so applying to
// every match would turn an ambiguous human answer into multiple unblocks.
func TestApplyDispositionToResultRejectsAmbiguousFingerprint(t *testing.T) {
	result := &DimensionResult{
		Dim:     DimLogic,
		Verdict: VerdictBlock,
		Findings: []ReviewFinding{
			{Dimension: DimLogic, File: "a.go", Line: 2, Severity: SevCritical, Description: "twin", Status: StatusConfirmed},
			{Dimension: DimLogic, File: "a.go", Line: 2, Severity: SevCritical, Description: "twin", Status: StatusConfirmed},
		},
	}
	disp := FindingDisposition{
		Fingerprint: EffectiveFingerprint(findingWithDisposition(DimLogic, result.Findings[0])),
		Status:      StatusRefuted,
	}

	if ApplyDispositionToResult(result, disp) {
		t.Fatal("ambiguous fingerprint reported a cleared finding")
	}
	for i, f := range result.Findings {
		if !IsBlocking(f.Severity, f.Status) {
			t.Fatalf("finding %d = %+v, must stay blocking", i, f)
		}
	}
}

// FU-6 fix: without an exact fingerprint match nothing is disposed, even
// when the recorded location identity would fit. Location is audit
// metadata, never a match key.
func TestApplyDispositionToResultIgnoresLocationWithoutFingerprint(t *testing.T) {
	result := &DimensionResult{
		Dim:     DimLogic,
		Verdict: VerdictBlock,
		Findings: []ReviewFinding{
			{Dimension: DimLogic, File: "a.go", Line: 2, Severity: SevCritical, Description: "bug", Status: StatusConfirmed},
		},
	}
	disp := FindingDisposition{
		SHA: "abc12345", Fingerprint: "fp-stale", Status: StatusRefuted,
		Reason: "verified safe", Path: "a.go", LineStart: 1, LineEnd: 3,
		Evidence: "criticalCall()", RangeHash: "1f49",
		Actor: RefutationActorHuman, Source: DispositionSourceHuman,
		TargetDimension: DimLogic, TargetLine: 2, TargetDescription: "bug",
	}

	if ApplyDispositionToResult(result, disp) {
		t.Fatal("a stale fingerprint cleared a live finding at the same location")
	}
	if !IsBlocking(result.Findings[0].Severity, result.Findings[0].Status) {
		t.Fatalf("finding = %+v, must keep blocking", result.Findings[0])
	}
}

// FU-6 fix: the engine applies standing human answers by exact fingerprint
// through a probe audit first: the recorded fingerprint is the effective
// fingerprint of the live finding's durable projection, never a location
// guess.
func auditFixtureTargetFingerprint(t *testing.T, sha, agentJSON string, refuterResponses []string, description string) string {
	t.Helper()
	factory, _ := fixedFactory([]string{agentJSON})
	refuterFactory, _ := fixedRefuterFactory(refuterResponses)
	probe := AuditCommit(factory, 1, AuditOptions{
		SHA: sha, Bundles: testBundles(DimLogic), RefuterFactory: refuterFactory,
		ReviewTransport:     directTransport(sha),
		ReadSnapshotContent: func(string, string) (string, error) { return "x\n", nil },
	})
	for _, h := range probe.Dims[0].Result.Findings {
		if h.Description == description {
			return EffectiveFingerprint(findingWithDisposition(probe.Dims[0].Dim, h))
		}
	}
	t.Fatalf("fixture yields no finding described %q", description)
	return ""
}

// FU-6: the engine applies standing human answers after the automated
// refutation and downgrades the verdict they clear. Only the matching
// finding is cleared.
func TestAuditCommitAppliesStandingHumanDispositions(t *testing.T) {
	const sha = "abc12345"
	const agentJSON = `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":2,"severity":"CRITICAL","description":"bug"},{"dimension":"logic","file":"b.go","line":30,"severity":"CRITICAL","description":"other"}]}`
	// The automated refuter declines, so without the human answer both
	// findings would stand confirmed and the verdict would stay block.
	declines := []string{
		`{"refuted":false,"reason":"cannot confirm safety","sha":"abc12345","file":"a.go","evidence":"","line_start":0,"line_end":0}`,
		`{"refuted":false,"reason":"cannot confirm safety","sha":"abc12345","file":"b.go","evidence":"","line_start":0,"line_end":0}`,
	}
	fingerprint := auditFixtureTargetFingerprint(t, sha, agentJSON, declines, "bug")
	dispositions := []FindingDisposition{{
		SHA: sha, Fingerprint: fingerprint, Status: StatusRefuted,
		Reason: "verified safe by hand", Path: "a.go", LineStart: 1, LineEnd: 3,
		Evidence: "criticalCall()", RangeHash: "1f49",
		Actor: RefutationActorHuman, Source: DispositionSourceHuman,
	}}

	factory, _ := fixedFactory([]string{agentJSON})
	refuterFactory, _ := fixedRefuterFactory(declines)
	result := AuditCommit(factory, 1, AuditOptions{
		SHA: sha, Bundles: testBundles(DimLogic), RefuterFactory: refuterFactory,
		ReviewTransport:     directTransport(sha),
		ReadSnapshotContent: func(string, string) (string, error) { return "x\n", nil },
		Dispositions:        dispositions,
	})
	if result.Verdict != VerdictBlock {
		t.Fatalf("verdict = %q, want block while the unrelated finding still stands", result.Verdict)
	}
	findings := result.Dims[0].Result.Findings
	if findings[0].Status != StatusRefuted || findings[0].RefutationActor != RefutationActorHuman {
		t.Fatalf("target = %+v, want the human answer applied", findings[0])
	}
	if !IsBlocking(findings[1].Severity, findings[1].Status) {
		t.Fatalf("unrelated = %+v, must keep blocking", findings[1])
	}
}

// FU-6: when the standing answers clear every blocker, the re-audit
// downgrades to warn without reporting an automated refutation: the human
// review already happened, so the gate must pass instead of asking for
// attention again.
func TestAuditCommitDowngradesWhenHumanAnswersClearEveryBlocker(t *testing.T) {
	const sha = "abc12345"
	const agentJSON = `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":2,"severity":"CRITICAL","description":"bug"}]}`
	declines := []string{
		`{"refuted":false,"reason":"cannot confirm safety","sha":"abc12345","file":"a.go","evidence":"","line_start":0,"line_end":0}`,
	}
	fingerprint := auditFixtureTargetFingerprint(t, sha, agentJSON, declines, "bug")
	dispositions := []FindingDisposition{{
		SHA: sha, Fingerprint: fingerprint, Status: StatusRefuted,
		Reason: "verified safe by hand", Path: "a.go", LineStart: 1, LineEnd: 3,
		Evidence: "criticalCall()", RangeHash: "1f49",
		Actor: RefutationActorHuman, Source: DispositionSourceHuman,
	}}

	factory, _ := fixedFactory([]string{agentJSON})
	refuterFactory, _ := fixedRefuterFactory(declines)
	result := AuditCommit(factory, 1, AuditOptions{
		SHA: sha, Bundles: testBundles(DimLogic), RefuterFactory: refuterFactory,
		ReviewTransport:     directTransport(sha),
		ReadSnapshotContent: func(string, string) (string, error) { return "x\n", nil },
		Dispositions:        dispositions,
	})
	if result.Verdict != VerdictWarn {
		t.Fatalf("verdict = %q, want the human-cleared block downgraded", result.Verdict)
	}
	if result.Dims[0].Result.RefutedCritical {
		t.Fatal("RefutedCritical is set: a human answer must not read as an automated refutation needing review")
	}
}

// FU-6: every blocking consumer sees the same effective disposition. A
// recorded refuted or fixed CRITICAL no longer blocks the branch; an
// accepted or reopened one still does; a standing human answer clears its
// finding through the overlay.
func TestBranchBlockersSeesTheSameEffectiveDisposition(t *testing.T) {
	raw := func(file string, line int, desc, status string) ReviewFinding {
		return ReviewFinding{File: file, Line: Line(line), Severity: SevCritical, Description: desc, Status: status}
	}
	revision := Revision{
		Result: "block",
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			raw("refuted.go", 1, "answered by the refuter", StatusRefuted),
			raw("fixed.go", 2, "repaired after the audit", StatusFixed),
			raw("accepted.go", 3, "acknowledged risk", StatusAcceptedByUser),
			raw("reopened.go", 4, "regressed", StatusReopened),
			raw("pending.go", 5, "unanswered", StatusConfirmed),
		}}},
	}
	ficha := Record{SHA: "abc123", Revisions: []Revision{revision}}

	bloqueantes := BranchBlockers([]Record{ficha}, nil)
	files := map[string]bool{}
	for _, h := range bloqueantes {
		files[h.File] = true
	}
	for _, cleared := range []string{"refuted.go", "fixed.go"} {
		if files[cleared] {
			t.Fatalf("%s blocks, want refuted and fixed findings cleared", cleared)
		}
	}
	for _, blocking := range []string{"accepted.go", "reopened.go", "pending.go"} {
		if !files[blocking] {
			t.Fatalf("%s does not block, want accepted, reopened, and pending findings blocking", blocking)
		}
	}
	overlayRevision := Revision{
		Result: "block",
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			raw("pending.go", 5, "unanswered", StatusConfirmed),
			raw("other.go", 6, "still open", StatusConfirmed),
		}}},
		AggregatedFindings: []Finding{
			{
				Dimension: DimLogic, Severity: SevCritical, Status: "",
				Description: "unanswered", Fingerprint: "fp-pending",
				Location: Location{File: "pending.go", LineStart: 5},
				Evidence: "risky call", Title: "unchecked input",
			},
			{
				Dimension: DimLogic, Severity: SevCritical, Status: "",
				Description: "still open", Fingerprint: "fp-other",
				Location: Location{File: "other.go", LineStart: 6},
				Evidence: "risky call", Title: "unchecked input",
			},
		},
	}
	overlay := []FindingDisposition{{
		SHA: "abc123", Fingerprint: "fp-pending", Status: StatusRefuted,
		Reason: "verified safe", Path: "pending.go", LineStart: 4, LineEnd: 6,
		Evidence: "safe call here", RangeHash: "aa",
		Actor: RefutationActorHuman, Source: DispositionSourceHuman,
		TargetDimension: DimLogic, TargetLine: 5, TargetDescription: "unanswered",
	}}
	overlayRecord := Record{SHA: "abc123", Revisions: []Revision{overlayRevision}}
	cleared := BranchBlockers([]Record{overlayRecord}, overlay)
	if len(cleared) != 1 || cleared[0].File != "other.go" {
		t.Fatalf("cleared = %+v, want exactly the unrelated finding blocking", cleared)
	}
}

// FU-6 fix: a disposition applies by unique SHA + fingerprint match only.
// A different fingerprint never clears a finding, even when dimension,
// path, line, and description all coincide: location is not identity.
func TestApplyDispositionToResultRequiresExactFingerprintMatch(t *testing.T) {
	result := &DimensionResult{
		Dim:     DimLogic,
		Verdict: VerdictBlock,
		Findings: []ReviewFinding{
			{File: "a.go", Line: 2, Severity: SevCritical, Description: "bug", Status: StatusConfirmed},
		},
	}
	disp := FindingDisposition{
		SHA: "abc12345", Fingerprint: "fp-unrelated", Status: StatusRefuted,
		Reason: "verified safe", Path: "a.go", LineStart: 1, LineEnd: 3,
		Evidence: "criticalCall()", RangeHash: "1f49",
		Actor: RefutationActorHuman, Source: DispositionSourceHuman,
		TargetDimension: DimLogic, TargetLine: 2, TargetDescription: "bug",
	}

	if ApplyDispositionToResult(result, disp) {
		t.Fatal("a non-matching fingerprint cleared the finding through the location fallback")
	}
	if !IsBlocking(result.Findings[0].Severity, result.Findings[0].Status) {
		t.Fatalf("finding = %+v, must keep blocking without an exact fingerprint match", result.Findings[0])
	}
}
