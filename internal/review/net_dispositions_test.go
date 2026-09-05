package review

import (
	"strings"
	"testing"
)

// carriedNetDispositions scopes by range and excludes the head itself: the
// engine already applied head-bound answers SHA-bound, so carrying them again
// would double-count the downgrade path.
func TestCarriedNetDispositionsScopesByRangeAndHead(t *testing.T) {
	gitDir := prepararRepoRama(t)
	_ = gitDir
	shaA := commitEnRama(t, "a.go", "package a\n\n// evidence line: guardian check\n")
	shaB := commitEnRama(t, "b.go", "package b\n")
	head := strings.TrimSpace(gitSalida(t, "rev-parse", "HEAD"))
	if head != shaB {
		t.Fatalf("head = %q, want the second commit %q", head, shaB)
	}
	mk := func(sha, fp string) FindingDisposition {
		return FindingDisposition{
			SHA: sha, Fingerprint: fp, Status: StatusRefuted,
			Reason: "verified safe", Path: "a.go",
			Evidence: "// evidence line: guardian check",
			Actor:    RefutationActorHuman, Source: DispositionSourceHuman,
		}
	}
	revisions := []Ficha{{SHA: shaA}, {SHA: shaB}}
	got := carriedNetDispositions([]FindingDisposition{
		mk(shaA, "fp-in-range"),
		mk(shaB, "fp-at-head"),
		mk("outside-range", "fp-outside"),
		mk(shaA, ""),
	}, revisions, shaB)
	if len(got) != 1 || got[0].Fingerprint != "fp-in-range" {
		t.Fatalf("carried = %+v, want only the in-range non-head fingerprint", got)
	}
}

// Evidence revalidation: the same answer carries while its evidence still
// holds at the head and stops carrying once the head removes it. A moved
// line still carries because the search spans the whole file.
func TestCarriedNetDispositionsRequiresEvidenceAtHead(t *testing.T) {
	prepararRepoRama(t)
	shaA := commitEnRama(t, "a.go", "package a\n\n// evidence line: guardian check\n")
	commitEnRama(t, "b.go", "package b\n")
	head := strings.TrimSpace(gitSalida(t, "rev-parse", "HEAD"))
	revisions := []Ficha{{SHA: shaA}, {SHA: head}}
	disp := FindingDisposition{
		SHA: shaA, Fingerprint: "fp-evidence", Status: StatusRefuted,
		Reason: "verified safe", Path: "a.go",
		Evidence: "// evidence line: guardian check",
		Actor:    RefutationActorHuman, Source: DispositionSourceHuman,
	}
	if got := carriedNetDispositions([]FindingDisposition{disp}, revisions, head); len(got) != 1 {
		t.Fatalf("carried with evidence present = %+v, want the disposition", got)
	}
	commitEnRama(t, "a.go", "package a\n\n// rewritten\n")
	newHead := strings.TrimSpace(gitSalida(t, "rev-parse", "HEAD"))
	revisions = append(revisions, Ficha{SHA: newHead})
	if got := carriedNetDispositions([]FindingDisposition{disp}, revisions, newHead); len(got) != 0 {
		t.Fatalf("carried after evidence removal = %+v, want nothing: the net must re-report", got)
	}
}

// End to end through runNetReview: without standing answers the fresh net
// CRITICAL blocks; with a refutation recorded against the intermediate
// commit whose evidence still holds at the head, the same net audit clears
// to warn. Pre-fix runNetReview ignored dispositions, so both runs blocked.
func TestRunNetReviewCarriesStandingRefutation(t *testing.T) {
	prepararRepoRama(t)
	shaA := commitEnRama(t, "a.go", "package a\n\n// evidence line: guardian check\n")
	from := strings.TrimSpace(gitSalida(t, "merge-base", "main", "HEAD"))
	to := strings.TrimSpace(gitSalida(t, "rev-parse", "HEAD"))
	revisions := []Ficha{{SHA: shaA}}
	const netBlock = "BEGIN_REVIEW\n{\"dim\":\"logic\",\"verdict\":\"block\",\"findings\":[{\"dimension\":\"logic\",\"file\":\"a.go\",\"line\":3,\"severity\":\"CRITICAL\",\"description\":\"guardian defect\",\"evidence\":\"// evidence line: guardian check\",\"confidence\":\"high\"}]}\nEND_REVIEW\n"
	stub := &answeringStub{marker: "Pull request intention:", output: netBlock}
	plain, err := runNetReview(&NetReviewOptions{Intention: "carry e2e"}, OpcionesRama{Fabrica: fabricaStub(stub), Parallel: 1}, from, to, revisions)
	if err != nil {
		t.Fatalf("net review without dispositions: %v", err)
	}
	if plain.Audit.Veredicto != VerdictBlock || len(plain.Audit.Findings) == 0 {
		t.Fatalf("net without dispositions = %q/%d, want block with findings", plain.Audit.Veredicto, len(plain.Audit.Findings))
	}
	fps := map[string]struct{}{}
	for _, h := range plain.Audit.Findings {
		if fp := strings.TrimSpace(EffectiveFingerprint(h)); fp != "" {
			fps[fp] = struct{}{}
		}
	}
	if len(fps) == 0 {
		t.Fatalf("net findings carry no fingerprint: %+v", plain.Audit.Findings)
	}
	var dispositions []FindingDisposition
	for fp := range fps {
		dispositions = append(dispositions, FindingDisposition{
			SHA: shaA, Fingerprint: fp, Status: StatusRefuted,
			Reason: "verified safe by hand", Path: "a.go",
			Evidence: "// evidence line: guardian check",
			Actor:    RefutationActorHuman, Source: DispositionSourceHuman,
		})
	}
	stubCarried := &answeringStub{marker: "Pull request intention:", output: netBlock}
	carried, err := runNetReview(&NetReviewOptions{Intention: "carry e2e", Dispositions: dispositions}, OpcionesRama{Fabrica: fabricaStub(stubCarried), Parallel: 1}, from, to, revisions)
	if err != nil {
		t.Fatalf("net review with dispositions: %v", err)
	}
	for _, h := range carried.Audit.Findings {
		if IsBlocking(h.Severity, h.Status) {
			t.Fatalf("carried net finding still blocks: %+v", h)
		}
	}
	if carried.Audit.Veredicto != VerdictWarn && carried.Audit.Veredicto != VerdictOK {
		t.Fatalf("carried net verdict = %q, want warn or ok after the human-cleared block", carried.Audit.Veredicto)
	}
}

// Cross-SHA carry: the answer is recorded against an intermediate commit
// while the net head is a later unrelated commit. The engine alone cannot
// clear it (SHA-bound); only the carried overlay with evidence revalidation
// at the head clears the re-reported fingerprint.
func TestRunNetReviewCarriesIntermediateRefutation(t *testing.T) {
	prepararRepoRama(t)
	shaA := commitEnRama(t, "a.go", "package a\n\n// evidence line: guardian check\n")
	head := commitEnRama(t, "b.go", "package b\n")
	from := strings.TrimSpace(gitSalida(t, "merge-base", "main", "HEAD"))
	to := head
	revisions := []Ficha{{SHA: shaA}, {SHA: head}}
	const netBlock = "BEGIN_REVIEW\n{\"dim\":\"logic\",\"verdict\":\"block\",\"findings\":[{\"dimension\":\"logic\",\"file\":\"a.go\",\"line\":3,\"severity\":\"CRITICAL\",\"description\":\"guardian defect\",\"evidence\":\"// evidence line: guardian check\",\"confidence\":\"high\"}]}\nEND_REVIEW\n"
	stub := &answeringStub{marker: "Pull request intention:", output: netBlock}
	plain, err := runNetReview(&NetReviewOptions{Intention: "carry intermediate"}, OpcionesRama{Fabrica: fabricaStub(stub), Parallel: 1}, from, to, revisions)
	if err != nil {
		t.Fatalf("net review without dispositions: %v", err)
	}
	if plain.Audit.Veredicto != VerdictBlock || len(plain.Audit.Findings) == 0 {
		t.Fatalf("net without dispositions = %q/%d, want block with findings", plain.Audit.Veredicto, len(plain.Audit.Findings))
	}
	fps := map[string]struct{}{}
	for _, h := range plain.Audit.Findings {
		if fp := strings.TrimSpace(EffectiveFingerprint(h)); fp != "" {
			fps[fp] = struct{}{}
		}
	}
	if len(fps) == 0 {
		t.Fatalf("net findings carry no fingerprint: %+v", plain.Audit.Findings)
	}
	var dispositions []FindingDisposition
	for fp := range fps {
		dispositions = append(dispositions, FindingDisposition{
			SHA: shaA, Fingerprint: fp, Status: StatusRefuted,
			Reason: "verified safe by hand", Path: "a.go",
			Evidence: "// evidence line: guardian check",
			Actor:    RefutationActorHuman, Source: DispositionSourceHuman,
		})
	}
	stubCarried := &answeringStub{marker: "Pull request intention:", output: netBlock}
	carried, err := runNetReview(&NetReviewOptions{Intention: "carry intermediate", Dispositions: dispositions}, OpcionesRama{Fabrica: fabricaStub(stubCarried), Parallel: 1}, from, to, revisions)
	if err != nil {
		t.Fatalf("net review with dispositions: %v", err)
	}
	for _, h := range carried.Audit.Findings {
		if IsBlocking(h.Severity, h.Status) {
			t.Fatalf("carried intermediate finding still blocks: %+v", h)
		}
	}
}

// Head precedence: when the same fingerprint is answered at both the head
// and an intermediate commit, the head answer wins. Without it the engine
// would receive the head answer plus the carried clone (both SHA-bound to
// the head) and its last-wins overlay would let the stale intermediate
// answer override the fresher head answer.
func TestRunNetReviewHeadAnswerWinsOverCarried(t *testing.T) {
	prepararRepoRama(t)
	shaA := commitEnRama(t, "a.go", "package a\n\n// evidence line: guardian check\n")
	head := commitEnRama(t, "b.go", "package b\n")
	from := strings.TrimSpace(gitSalida(t, "merge-base", "main", "HEAD"))
	to := head
	revisions := []Ficha{{SHA: shaA}, {SHA: head}}
	const netBlock = "BEGIN_REVIEW\n{\"dim\":\"logic\",\"verdict\":\"block\",\"findings\":[{\"dimension\":\"logic\",\"file\":\"a.go\",\"line\":3,\"severity\":\"CRITICAL\",\"description\":\"guardian defect\",\"evidence\":\"// evidence line: guardian check\",\"confidence\":\"high\"}]}\nEND_REVIEW\n"
	stub := &answeringStub{marker: "Pull request intention:", output: netBlock}
	plain, err := runNetReview(&NetReviewOptions{Intention: "precedence"}, OpcionesRama{Fabrica: fabricaStub(stub), Parallel: 1}, from, to, revisions)
	if err != nil {
		t.Fatalf("net review without dispositions: %v", err)
	}
	fps := map[string]struct{}{}
	for _, h := range plain.Audit.Findings {
		if fp := strings.TrimSpace(EffectiveFingerprint(h)); fp != "" {
			fps[fp] = struct{}{}
		}
	}
	if len(fps) == 0 {
		t.Fatalf("net findings carry no fingerprint: %+v", plain.Audit.Findings)
	}
	var dispositions []FindingDisposition
	for fp := range fps {
		dispositions = append(dispositions,
			FindingDisposition{
				SHA: shaA, Fingerprint: fp, Status: StatusRefuted,
				Reason: "verified safe by hand", Path: "a.go",
				Evidence: "// evidence line: guardian check",
				Actor:    RefutationActorHuman, Source: DispositionSourceHuman,
			},
			FindingDisposition{
				SHA: head, Fingerprint: fp, Status: StatusReopened,
				Reason: "regressed on this path", Path: "a.go",
				Evidence: "// evidence line: guardian check",
				Actor:    RefutationActorHuman, Source: DispositionSourceHuman,
			})
	}
	stubCarried := &answeringStub{marker: "Pull request intention:", output: netBlock}
	carried, err := runNetReview(&NetReviewOptions{Intention: "precedence", Dispositions: dispositions}, OpcionesRama{Fabrica: fabricaStub(stubCarried), Parallel: 1}, from, to, revisions)
	if err != nil {
		t.Fatalf("net review with dispositions: %v", err)
	}
	for _, h := range carried.Audit.Findings {
		if !IsBlocking(h.Severity, h.Status) {
			t.Fatalf("stale intermediate answer overrode the head reopen: %+v", h)
		}
	}
}
