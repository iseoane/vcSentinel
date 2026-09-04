package review

import (
	"errors"
	"testing"
)

// FU-6: one shared blocking predicate across engine, gate, and
// BloqueantesDeRama. A human refutation clears its finding; accepted_by_user
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
	target := Hallazgo{
		Dimension: DimSecurity, Severity: SevCritical, Status: StatusConfirmed,
		Description: "injected query", Fingerprint: "fp-target",
		Location: Ubicacion{Archivo: "a.go", LineaInicio: 10},
		Producer: Productor{Agente: "agent-a", Modelo: "model-a"},
	}
	other := Hallazgo{
		Dimension: DimSecurity, Severity: SevCritical, Status: StatusConfirmed,
		Description: "unchecked input", Fingerprint: "fp-other",
		Location: Ubicacion{Archivo: "b.go", LineaInicio: 30},
		Producer: Productor{Agente: "agent-a", Modelo: "model-a"},
	}
	dispositions := []FindingDisposition{{
		SHA: "abc123", Fingerprint: "fp-target", Status: StatusRefuted,
		Reason: "verified safe", Path: "a.go", LineStart: 9, LineEnd: 11,
		Evidence: "safe call", RangeHash: "deadbeef",
		Actor: RefutationActorHuman, Source: DispositionSourceHuman,
	}}

	got := ApplyDispositions([]Hallazgo{target, other}, dispositions)

	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Status != StatusRefuted || got[0].RefutationActor != RefutationActorHuman {
		t.Fatalf("target = %+v, want refuted with human provenance", got[0])
	}
	if got[0].InvocationID != "" {
		t.Fatalf("target invocation = %q, human decisions cite no invocation", got[0].InvocationID)
	}
	if got[0].Producer.Agente != "agent-a" || got[0].Producer.Modelo != "model-a" {
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
	mk := func(desc string) Hallazgo {
		return Hallazgo{
			Dimension: DimLogic, Severity: SevCritical, Status: StatusConfirmed,
			Description: desc, Fingerprint: "fp-shared",
			Location: Ubicacion{Archivo: "a.go", LineaInicio: 10},
		}
	}
	dispositions := []FindingDisposition{{
		SHA: "abc123", Fingerprint: "fp-shared", Status: StatusRefuted,
		Reason: "verified safe", Actor: RefutationActorHuman, Source: DispositionSourceHuman,
	}}

	got := ApplyDispositions([]Hallazgo{mk("first"), mk("second")}, dispositions)
	for i, h := range got {
		if !IsBlocking(h.Severity, h.Status) {
			t.Fatalf("finding %d = %+v, ambiguous fingerprints must stay blocking", i, h)
		}
	}
}

// FU-6: addressing is by reviewed revision plus stable fingerprint. A missing
// fingerprint and an ambiguous one both fail closed without persisting.
func TestResolveDispositionTargetRejectsMissingAndAmbiguous(t *testing.T) {
	mk := func(desc, fp string) Hallazgo {
		return Hallazgo{
			Dimension: DimLogic, Severity: SevCritical, Status: StatusConfirmed,
			Description: desc, Fingerprint: fp,
			Location: Ubicacion{Archivo: "a.go", LineaInicio: 10},
		}
	}
	revision := Revision{
		Dims: []DimensionResult{{Dim: DimLogic, Findings: []ReviewFinding{
			{Dimension: DimLogic, File: "a.go", Line: 10, Severity: SevCritical, Description: "first"},
			{Dimension: DimLogic, File: "a.go", Line: 10, Severity: SevCritical, Description: "second"},
		}}},
		AggregatedFindings: []Hallazgo{mk("first", "fp-first"), mk("second", "fp-second")},
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
		AggregatedFindings: []Hallazgo{mk("twin", "fp-twin"), mk("twin", "fp-twin")},
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
		name       string
		lineStart  int
		lineEnd    int
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

// FU-6: the dimension-level application pairs both shapes. A v2 finding
// matches by fingerprint and drags its v1 counterpart along, so the engine
// verdict (read from both shapes) and the persisted revision agree.
func TestApplyDispositionToResultPairsBothShapes(t *testing.T) {
	result := &DimensionResult{
		Dim:     DimLogic,
		Verdict: VerdictBlock,
		Findings: []ReviewFinding{
			{File: "a.go", Line: 2, Severity: SevCritical, Description: "bug", Status: StatusConfirmed},
		},
		Hallazgos: []Hallazgo{
			{
				Dimension: DimLogic, Severity: SevCritical, Status: StatusConfirmed,
				Description: "bug", Fingerprint: "fp-strong",
				Location:     Ubicacion{Archivo: "a.go", LineaInicio: 2},
				Producer:     Productor{Agente: "agent-a", Modelo: "model-a"},
				InvocationID: "invocation-1",
			},
		},
	}
	disp := FindingDisposition{
		SHA: "abc12345", Fingerprint: "fp-strong", Status: StatusRefuted,
		Reason: "verified safe", Path: "a.go", LineStart: 2, LineEnd: 2,
		Evidence: "criticalCall()", RangeHash: "1f49",
		Actor: RefutationActorHuman, Source: DispositionSourceHuman,
		TargetDimension: DimLogic, TargetLine: 2, TargetDescription: "bug",
	}

	if !ApplyDispositionToResult(result, disp) {
		t.Fatal("expected the disposition to clear a blocking finding")
	}
	v1 := result.Findings[0]
	if v1.Status != StatusRefuted || v1.RefutationActor != RefutationActorHuman || v1.RefutationRangeHash != "1f49" {
		t.Fatalf("v1 = %+v, want the human answer applied", v1)
	}
	v2 := result.Hallazgos[0]
	if v2.Status != StatusRefuted || v2.RefutationActor != RefutationActorHuman {
		t.Fatalf("v2 = %+v, want the human answer applied", v2)
	}
	if v2.InvocationID != "" {
		t.Fatalf("v2 invocation = %q, human decisions cite no invocation", v2.InvocationID)
	}
	if v2.Producer.Modelo != "model-a" {
		t.Fatalf("v2 producer = %+v, applying must not rewrite who produced the finding", v2.Producer)
	}
}

// FU-6 fix: without an exact fingerprint match nothing is disposed, even
// when v2 findings are present. The recorded location identity is audit
// metadata, never a match key.
func TestApplyDispositionToResultIgnoresLocationWithoutFingerprint(t *testing.T) {
	result := &DimensionResult{
		Dim:     DimLogic,
		Verdict: VerdictBlock,
		Findings: []ReviewFinding{
			{File: "a.go", Line: 2, Severity: SevCritical, Description: "bug", Status: StatusConfirmed},
		},
		Hallazgos: []Hallazgo{
			{
				Dimension: DimLogic, Severity: SevCritical, Status: StatusConfirmed,
				Description: "bug", Fingerprint: "fp-live",
				Location: Ubicacion{Archivo: "a.go", LineaInicio: 2},
			},
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
		t.Fatalf("v1 = %+v, must keep blocking", result.Findings[0])
	}
	if !IsBlocking(result.Hallazgos[0].Severity, result.Hallazgos[0].Status) {
		t.Fatalf("v2 = %+v, must keep blocking", result.Hallazgos[0])
	}
}

// FU-6 fix: the engine applies standing human answers by exact fingerprint
// through a probe audit first: the recorded fingerprint is the effective
// fingerprint of the live v2 finding, never a location guess.
func auditFixtureTargetFingerprint(t *testing.T, sha, agentJSON string, refuterResponses []string, description string) string {
	t.Helper()
	fabrica, _ := fabricaFija([]string{agentJSON})
	fabricaRefutador, _ := fabricaRefutadorFija(refuterResponses)
	probe := AuditarCommit(fabrica, 1, OpcionesAuditoria{
		SHA: sha, Bundles: bundlesPrueba(DimLogic), FabricaRefutador: fabricaRefutador,
		ReviewTransport:       transporteDirecto(sha),
		LeerContenidoSnapshot: func(string, string) (string, error) { return "x\n", nil },
	})
	for _, h := range probe.Dims[0].Resultado.Hallazgos {
		if h.Description == description {
			return EffectiveFingerprint(h)
		}
	}
	t.Fatalf("fixture yields no v2 finding described %q", description)
	return ""
}

// FU-6: the engine applies standing human answers after the automated
// refutation and downgrades the verdict they clear. Only the matching
// finding is cleared.
func TestAuditarCommitAppliesStandingHumanDispositions(t *testing.T) {
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

	fabrica, _ := fabricaFija([]string{agentJSON})
	fabricaRefutador, _ := fabricaRefutadorFija(declines)
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{
		SHA: sha, Bundles: bundlesPrueba(DimLogic), FabricaRefutador: fabricaRefutador,
		ReviewTransport:       transporteDirecto(sha),
		LeerContenidoSnapshot: func(string, string) (string, error) { return "x\n", nil },
		Dispositions:          dispositions,
	})
	if resultado.Veredicto != VerdictBlock {
		t.Fatalf("verdict = %q, want block while the unrelated finding still stands", resultado.Veredicto)
	}
	findings := resultado.Dims[0].Resultado.Findings
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
func TestAuditarCommitDowngradesWhenHumanAnswersClearEveryBlocker(t *testing.T) {
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

	fabrica, _ := fabricaFija([]string{agentJSON})
	fabricaRefutador, _ := fabricaRefutadorFija(declines)
	resultado := AuditarCommit(fabrica, 1, OpcionesAuditoria{
		SHA: sha, Bundles: bundlesPrueba(DimLogic), FabricaRefutador: fabricaRefutador,
		ReviewTransport:       transporteDirecto(sha),
		LeerContenidoSnapshot: func(string, string) (string, error) { return "x\n", nil },
		Dispositions:          dispositions,
	})
	if resultado.Veredicto != VerdictWarn {
		t.Fatalf("verdict = %q, want the human-cleared block downgraded", resultado.Veredicto)
	}
	if resultado.Dims[0].Resultado.RefutedCritical {
		t.Fatal("RefutedCritical is set: a human answer must not read as an automated refutation needing review")
	}
}

// FU-6: every blocking consumer sees the same effective disposition. A
// recorded refuted or fixed CRITICAL no longer blocks the branch; an
// accepted or reopened one still does; a standing human answer clears its
// finding through the overlay.
func TestBloqueantesDeRamaSeesTheSameEffectiveDisposition(t *testing.T) {
	raw := func(file string, line int, desc, status string) ReviewFinding {
		return ReviewFinding{File: file, Line: Linea(line), Severity: SevCritical, Description: desc, Status: status}
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
	ficha := Ficha{SHA: "abc123", Revisions: []Revision{revision}}

	bloqueantes := BloqueantesDeRama([]Ficha{ficha})
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
		AggregatedFindings: []Hallazgo{
			{
				Dimension: DimLogic, Severity: SevCritical, Status: "",
				Description: "unanswered", Fingerprint: "fp-pending",
				Location: Ubicacion{Archivo: "pending.go", LineaInicio: 5},
				Evidence: "risky call", Title: "unchecked input",
			},
			{
				Dimension: DimLogic, Severity: SevCritical, Status: "",
				Description: "still open", Fingerprint: "fp-other",
				Location: Ubicacion{Archivo: "other.go", LineaInicio: 6},
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
	overlayFicha := Ficha{SHA: "abc123", Revisions: []Revision{overlayRevision}}
	cleared := BloqueantesDeRamaWithDispositions([]Ficha{overlayFicha}, overlay)
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
