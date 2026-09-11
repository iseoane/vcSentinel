package review

import (
	"strings"
	"testing"
)

// mkDisposition builds a human-answered disposition fixture for the net
// carry-over tests.
func mkDisposition(sha, fp, status string) FindingDisposition {
	return FindingDisposition{
		SHA: sha, Fingerprint: fp, Status: status, Reason: "r",
		Path: "a.go", Evidence: "// evidence line: guardian check",
		Actor: RefutationActorHuman, Source: DispositionSourceHuman,
	}
}

// carriedNetDispositions scopes by range and excludes the head itself: the
// engine already applied head-bound answers SHA-bound, so carrying them again
// would double-count the downgrade path.
func TestCarriedNetDispositionsScopesByRangeAndHead(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	_ = gitDir
	shaA := commitInBranch(t, "a.go", "package a\n\n// evidence line: guardian check\n")
	shaB := commitInBranch(t, "b.go", "package b\n")
	head := strings.TrimSpace(gitOutput(t, "rev-parse", "HEAD"))
	if head != shaB {
		t.Fatalf("head = %q, want the second commit %q", head, shaB)
	}
	revisions := []Record{{SHA: shaA}, {SHA: shaB}}
	got := carriedNetDispositions([]FindingDisposition{
		mkDisposition(shaA, "fp-in-range", StatusRefuted),
		mkDisposition(shaB, "fp-at-head", StatusRefuted),
		mkDisposition("outside-range", "fp-outside", StatusRefuted),
		mkDisposition(shaA, "", StatusRefuted),
	}, revisions, shaB)
	if len(got) != 1 || got[0].Fingerprint != "fp-in-range" {
		t.Fatalf("carried = %+v, want only the in-range non-head fingerprint", got)
	}
}

// Evidence revalidation: the same answer carries while its evidence still
// holds at the head and stops carrying once the head removes it. A moved
// line still carries because the search spans the whole file.
func TestCarriedNetDispositionsRequiresEvidenceAtHead(t *testing.T) {
	prepareBranchRepo(t)
	shaA := commitInBranch(t, "a.go", "package a\n\n// evidence line: guardian check\n")
	commitInBranch(t, "b.go", "package b\n")
	head := strings.TrimSpace(gitOutput(t, "rev-parse", "HEAD"))
	revisions := []Record{{SHA: shaA}, {SHA: head}}
	disp := mkDisposition(shaA, "fp-evidence", StatusRefuted)
	if got := carriedNetDispositions([]FindingDisposition{disp}, revisions, head); len(got) != 1 {
		t.Fatalf("carried with evidence present = %+v, want the disposition", got)
	}
	commitInBranch(t, "a.go", "package a\n\n// rewritten\n")
	newHead := strings.TrimSpace(gitOutput(t, "rev-parse", "HEAD"))
	revisions = append(revisions, Record{SHA: newHead})
	if got := carriedNetDispositions([]FindingDisposition{disp}, revisions, newHead); len(got) != 0 {
		t.Fatalf("carried after evidence removal = %+v, want nothing: the net must re-report", got)
	}
}

// End to end through runNetReview: without standing answers the fresh net
// CRITICAL blocks; with a refutation recorded against the intermediate
// commit whose evidence still holds at the head, the same net audit clears
// to warn. Pre-fix runNetReview ignored dispositions, so both runs blocked.
func TestRunNetReviewCarriesStandingRefutation(t *testing.T) {
	prepareBranchRepo(t)
	shaA := commitInBranch(t, "a.go", "package a\n\n// evidence line: guardian check\n")
	from := strings.TrimSpace(gitOutput(t, "merge-base", "main", "HEAD"))
	to := strings.TrimSpace(gitOutput(t, "rev-parse", "HEAD"))
	revisions := []Record{{SHA: shaA}}
	const netBlock = "BEGIN_REVIEW\n{\"dim\":\"logic\",\"verdict\":\"block\",\"findings\":[{\"dimension\":\"logic\",\"file\":\"a.go\",\"line\":3,\"severity\":\"CRITICAL\",\"description\":\"guardian defect\",\"evidence\":\"// evidence line: guardian check\",\"confidence\":\"high\"}]}\nEND_REVIEW\n"
	stub := &answeringStub{marker: "Pull request intention:", output: netBlock}
	plain, err := runNetReview(&NetReviewOptions{Intention: "carry e2e"}, BranchOptions{Factory: stubFactory(stub), Parallel: 1}, from, to, revisions)
	if err != nil {
		t.Fatalf("net review without dispositions: %v", err)
	}
	if plain.Audit.Verdict != VerdictBlock || len(plain.Audit.Findings) == 0 {
		t.Fatalf("net without dispositions = %q/%d, want block with findings", plain.Audit.Verdict, len(plain.Audit.Findings))
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
		dispositions = append(dispositions, mkDisposition(shaA, fp, StatusRefuted))
	}
	stubCarried := &answeringStub{marker: "Pull request intention:", output: netBlock}
	carried, err := runNetReview(&NetReviewOptions{Intention: "carry e2e"}, BranchOptions{Factory: stubFactory(stubCarried), Parallel: 1, Dispositions: dispositions}, from, to, revisions)
	if err != nil {
		t.Fatalf("net review with dispositions: %v", err)
	}
	for _, h := range carried.Audit.Findings {
		if IsBlocking(h.Severity, h.Status) {
			t.Fatalf("carried net finding still blocks: %+v", h)
		}
	}
	if carried.Audit.Verdict != VerdictWarn && carried.Audit.Verdict != VerdictOK {
		t.Fatalf("carried net verdict = %q, want warn or ok after the human-cleared block", carried.Audit.Verdict)
	}
}

// Cross-SHA carry: the answer is recorded against an intermediate commit
// while the net head is a later unrelated commit. The engine alone cannot
// clear it (SHA-bound); only the carried overlay with evidence revalidation
// at the head clears the re-reported fingerprint.
func TestRunNetReviewCarriesIntermediateRefutation(t *testing.T) {
	prepareBranchRepo(t)
	shaA := commitInBranch(t, "a.go", "package a\n\n// evidence line: guardian check\n")
	head := commitInBranch(t, "b.go", "package b\n")
	from := strings.TrimSpace(gitOutput(t, "merge-base", "main", "HEAD"))
	to := head
	revisions := []Record{{SHA: shaA}, {SHA: head}}
	const netBlock = "BEGIN_REVIEW\n{\"dim\":\"logic\",\"verdict\":\"block\",\"findings\":[{\"dimension\":\"logic\",\"file\":\"a.go\",\"line\":3,\"severity\":\"CRITICAL\",\"description\":\"guardian defect\",\"evidence\":\"// evidence line: guardian check\",\"confidence\":\"high\"}]}\nEND_REVIEW\n"
	stub := &answeringStub{marker: "Pull request intention:", output: netBlock}
	plain, err := runNetReview(&NetReviewOptions{Intention: "carry intermediate"}, BranchOptions{Factory: stubFactory(stub), Parallel: 1}, from, to, revisions)
	if err != nil {
		t.Fatalf("net review without dispositions: %v", err)
	}
	if plain.Audit.Verdict != VerdictBlock || len(plain.Audit.Findings) == 0 {
		t.Fatalf("net without dispositions = %q/%d, want block with findings", plain.Audit.Verdict, len(plain.Audit.Findings))
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
		dispositions = append(dispositions, mkDisposition(shaA, fp, StatusRefuted))
	}
	stubCarried := &answeringStub{marker: "Pull request intention:", output: netBlock}
	carried, err := runNetReview(&NetReviewOptions{Intention: "carry intermediate"}, BranchOptions{Factory: stubFactory(stubCarried), Parallel: 1, Dispositions: dispositions}, from, to, revisions)
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
	prepareBranchRepo(t)
	shaA := commitInBranch(t, "a.go", "package a\n\n// evidence line: guardian check\n")
	head := commitInBranch(t, "b.go", "package b\n")
	from := strings.TrimSpace(gitOutput(t, "merge-base", "main", "HEAD"))
	to := head
	revisions := []Record{{SHA: shaA}, {SHA: head}}
	const netBlock = "BEGIN_REVIEW\n{\"dim\":\"logic\",\"verdict\":\"block\",\"findings\":[{\"dimension\":\"logic\",\"file\":\"a.go\",\"line\":3,\"severity\":\"CRITICAL\",\"description\":\"guardian defect\",\"evidence\":\"// evidence line: guardian check\",\"confidence\":\"high\"}]}\nEND_REVIEW\n"
	stub := &answeringStub{marker: "Pull request intention:", output: netBlock}
	plain, err := runNetReview(&NetReviewOptions{Intention: "precedence"}, BranchOptions{Factory: stubFactory(stub), Parallel: 1}, from, to, revisions)
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
			mkDisposition(shaA, fp, StatusRefuted),
			mkDisposition(head, fp, StatusReopened))
	}
	stubCarried := &answeringStub{marker: "Pull request intention:", output: netBlock}
	carried, err := runNetReview(&NetReviewOptions{Intention: "precedence"}, BranchOptions{Factory: stubFactory(stubCarried), Parallel: 1, Dispositions: dispositions}, from, to, revisions)
	if err != nil {
		t.Fatalf("net review with dispositions: %v", err)
	}
	for _, h := range carried.Audit.Findings {
		if !IsBlocking(h.Severity, h.Status) {
			t.Fatalf("stale intermediate answer overrode the head reopen: %+v", h)
		}
	}
}

// The merge keeps every head answer, clones carried answers to the head SHA,
// and drops carried answers whose fingerprint the head already answered.
func TestMergeNetDispositionsForEnginePrefersHead(t *testing.T) {
	head := []FindingDisposition{mkDisposition("head", "fp-shared", StatusReopened)}
	carried := []FindingDisposition{
		mkDisposition("mid", "fp-shared", StatusRefuted),
		mkDisposition("mid", "fp-carried", StatusRefuted),
	}
	got := mergeNetDispositionsForEngine(head, carried, "head")
	if len(got) != 2 {
		t.Fatalf("merged = %+v, want head plus the non-shadowed carry", got)
	}
	if got[0].Fingerprint != "fp-shared" || got[0].Status != StatusReopened || got[0].SHA != "head" {
		t.Fatalf("merged[0] = %+v, want the head answer untouched", got[0])
	}
	if got[1].Fingerprint != "fp-carried" || got[1].SHA != "head" {
		t.Fatalf("merged[1] = %+v, want the carried answer cloned to the head", got[1])
	}
	if got := mergeNetDispositionsForEngine(nil, nil, "head"); len(got) != 0 {
		t.Fatalf("merged empty = %+v, want nothing", got)
	}
}

// Empty carried fingerprints never reach the engine input, even if an
// upstream caller forgets the pre-filter: an empty fingerprint matches
// nothing by identity, so cloning it can only smuggle ambiguity.
func TestMergeNetDispositionsForEngineDropsEmptyCarried(t *testing.T) {
	got := mergeNetDispositionsForEngine(
		[]FindingDisposition{mkDisposition("head", "fp-keep", StatusReopened)},
		[]FindingDisposition{mkDisposition("mid", "", StatusRefuted), mkDisposition("mid", "  ", StatusRefuted)},
		"head")
	if len(got) != 1 || got[0].Fingerprint != "fp-keep" {
		t.Fatalf("merged = %+v, want only the head answer", got)
	}
}
