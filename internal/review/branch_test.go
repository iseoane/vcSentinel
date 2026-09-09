package review

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
)

// gitOutput runs git in the cwd and returns its standard output.
func gitOutput(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v failed: %v", args, err)
	}
	return string(output)
}

func runGit(t *testing.T, args ...string) {
	t.Helper()
	gitOutput(t, args...)
}

// auditorStub returns output by prompt: dimension audits receive auditOutput
// (valid JSONL); the overview (which contains "coherent") receives
// overviewOutput.
type auditorStub struct {
	auditOutput    string
	overviewOutput string
	mu             sync.Mutex
	auditCalls     int
	overviewCalls  int
}

func (a *auditorStub) RunPrompt(prompt string) (string, error) {
	if strings.Contains(prompt, "coherent") {
		a.mu.Lock()
		a.overviewCalls++
		a.mu.Unlock()
		return a.overviewOutput, nil
	}
	a.mu.Lock()
	a.auditCalls++
	a.mu.Unlock()
	return outputForDimension(a.auditOutput, prompt), nil
}

func outputForDimension(output, prompt string) string {
	return strings.ReplaceAll(output, `"logic"`, fmt.Sprintf("%q", dimensionFromPrompt(prompt)))
}

func (a *auditorStub) RunReview(prompt, _ string, _ []string) (string, error) {
	return a.RunPrompt(prompt)
}

func (a *auditorStub) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.RunReview(prompt, sha, paths)
}

// auditOutputOK is a canonical review result whose dimension is bound to the
// requested contract by outputForDimension.
const auditOutputOK = "BEGIN_REVIEW\n{\"dim\":\"logic\",\"verdict\":\"ok\"}\nEND_REVIEW\n"

func stubFactory(a AgentReviewer) ReviewerFactory {
	return func(_ ReviewBundle, dimension string) (AgentReviewer, string, error) {
		return a, "stub", nil
	}
}

type pathAuditorStub struct {
	auditorStub
	mu    sync.Mutex
	paths [][]string
}

func (a *pathAuditorStub) RunReview(prompt, _ string, paths []string) (string, error) {
	a.mu.Lock()
	a.paths = append(a.paths, append([]string(nil), paths...))
	a.mu.Unlock()
	return a.RunPrompt(prompt)
}

func (a *pathAuditorStub) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.RunReview(prompt, sha, paths)
}

// fakeStoreBlobs is a test StoreBlobs that does not depend on a real
// store.Store: it allows fixing the SHAs registered per blob by hand
// (to force commit mixes) and forcing the RegisterCommitBlobs error
// without touching disk.
type fakeStoreBlobs struct {
	shasPerBlob     map[string][]string // blob -> registered SHAs (BlobSHAs)
	recordError     error
	registeredBlobs map[string]map[string]string // sha -> blobs, for inspection
}

func (f *fakeStoreBlobs) AlreadyReviewed(blob string) (bool, []Finding, error) {
	return len(f.shasPerBlob[blob]) > 0, nil, nil
}

func (f *fakeStoreBlobs) BlobSHAs(blob string) ([]string, error) {
	return f.shasPerBlob[blob], nil
}

func (f *fakeStoreBlobs) RegisterCommitBlobs(sha string, blobs map[string]string) error {
	if f.recordError != nil {
		return f.recordError
	}
	if f.registeredBlobs == nil {
		f.registeredBlobs = make(map[string]map[string]string)
	}
	if f.shasPerBlob == nil {
		f.shasPerBlob = make(map[string][]string)
	}
	f.registeredBlobs[sha] = blobs
	for _, blob := range blobs {
		f.shasPerBlob[blob] = append(f.shasPerBlob[blob], sha)
	}
	return nil
}

func (f *fakeStoreBlobs) CommitBlobs(sha string) (map[string]string, error) {
	return f.registeredBlobs[sha], nil
}

// prepareBranchRepo creates a temp repo with the current branch "feature"
// created from the first commit of main, and returns the gitDir for the ledger.
func prepareBranchRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	t.Chdir(repo)

	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@vas.sentinel"},
		{"config", "user.name", "VAS Sentinel Test"},
		{"config", "core.hooksPath", ""},
	} {
		runGit(t, args...)
	}

	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte(strings.Repeat("b\n", 5)), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "add", "base.txt")
	runGit(t, "commit", "-m", "feat(base): branch base")
	runGit(t, "checkout", "-b", "feature")

	return filepath.Join(repo, ".git")
}

// commitInBranch adds a file and commits it on the current branch; returns the SHA.
func commitInBranch(t *testing.T, name, content string) string {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "add", name)
	runGit(t, "commit", "-m", "feat("+name+"): test content")
	return strings.TrimSpace(gitOutput(t, "rev-parse", "HEAD"))
}

// TestAnalyzeBranchAuditsPending: audits the branch commits without a
// record, leaves the records in the ledger and decides single at low volume.
func TestAnalyzeBranchReauditsOnlySpecWhenReusedMessageChanges(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	oldSHA := commitInBranch(t, "feat.txt", "content\n")
	ledger := NewLedger(gitDir)
	store := &fakeStoreBlobs{}
	stub := &auditorStub{auditOutput: auditOutputOK}

	if _, err := AnalyzeBranch(ledger, BranchOptions{Factory: stubFactory(stub), Parallel: 1, Store: store}); err != nil {
		t.Fatalf("first AnalyzeBranch: %v", err)
	}
	callsBeforeAmend := stub.auditCalls
	if callsBeforeAmend == 0 {
		t.Fatal("first audit did not call the reviewer")
	}

	runGit(t, "commit", "--amend", "-m", "feat(feat): rewritten message")
	newSHA := strings.TrimSpace(gitOutput(t, "rev-parse", "HEAD"))
	if newSHA == oldSHA {
		t.Fatal("amending the message did not rewrite the SHA")
	}

	factoryCalls := 0
	res, err := AnalyzeBranch(ledger, BranchOptions{
		Factory: stubFactory(stub), Parallel: 1, Store: store,
		DeterministicFindingsFactory: func(string, []string, string) []Finding {
			factoryCalls++
			return []Finding{{Dimension: DimSecurity, Title: "must not enter spec reuse"}}
		},
	})
	if err != nil {
		t.Fatalf("second AnalyzeBranch: %v", err)
	}
	if got := stub.auditCalls - callsBeforeAmend; got != 1 {
		t.Errorf("reviewer calls after a message-only amend = %d, want 1 spec audit", got)
	}
	if factoryCalls != 0 {
		t.Fatalf("DeterministicFindingsFactory calls = %d, want 0 for a spec-only reuse", factoryCalls)
	}
	if len(res.Records) != 1 {
		t.Fatalf("Records = %d, want one adopted record", len(res.Records))
	}
	record := res.Records[0]
	if record.SHA != newSHA || record.OriginSHA != oldSHA {
		t.Errorf("record provenance = SHA %q OriginSHA %q, want %q from %q", record.SHA, record.OriginSHA, newSHA, oldSHA)
	}
	if record.Message != "feat(feat): rewritten message" {
		t.Errorf("record Message = %q, want the amended message", record.Message)
	}
	latest, _, ok := LastAuthoritativeRevision(record)
	if !ok {
		t.Fatal("adopted record has no authoritative revision")
	}
	seen := map[string]bool{}
	for _, dim := range latest.Dims {
		seen[dim.Dim] = true
	}
	for _, dimension := range []string{DimLogic, DimSpec, DimTests} {
		if !seen[dimension] {
			t.Errorf("merged authoritative revision is missing reused or re-audited %q dimension: %+v", dimension, latest.Dims)
		}
	}
}

func TestCommitCoveredByBlobsRequiresMatchingPathsAndRejectsSelf(t *testing.T) {
	prepareBranchRepo(t)
	if err := os.MkdirAll(".github/workflows", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(".github/workflows/deploy.yml", []byte("shared bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "add", ".github/workflows/deploy.yml")
	runGit(t, "commit", "-m", "feat(ci): add workflow")
	sha := strings.TrimSpace(gitOutput(t, "rev-parse", "HEAD"))
	files, err := git.FilesOfCommit(sha)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := commitBlobs(sha, files)
	if err != nil {
		t.Fatal(err)
	}
	blob := blobs[".github/workflows/deploy.yml"]
	fake := &fakeStoreBlobs{
		shasPerBlob: map[string][]string{blob: {sha, "historical"}},
		registeredBlobs: map[string]map[string]string{
			sha:          {".github/workflows/deploy.yml": blob},
			"historical": {"docs/deploy.md": blob},
		},
	}
	covered, origin, err := commitCoveredByBlobs(fake, sha)
	if err != nil {
		t.Fatalf("commitCoveredByBlobs: %v", err)
	}
	if covered || origin != "" {
		t.Fatalf("commitCoveredByBlobs = (%t, %q), want no reuse for a path mismatch or destination self-match", covered, origin)
	}
}

func TestAnalyzeBranchDoesNotReuseSupplementaryOrigin(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	oldSHA := commitInBranch(t, "feat.txt", "content\n")
	ledger := NewLedger(gitDir)
	files, err := git.FilesOfCommit(oldSHA)
	if err != nil {
		t.Fatalf("FilesOfCommit: %v", err)
	}
	blobs, err := commitBlobs(oldSHA, files)
	if err != nil {
		t.Fatalf("commitBlobs: %v", err)
	}
	if err := ledger.SaveRevision(oldSHA, "feat(feat.txt): test content", "pr", "test", Revision{
		Result:   VerdictOK,
		Coverage: CoverageSupplementary,
		Dims:     []DimensionResult{{Dim: DimSpec, Verdict: VerdictOK}},
	}); err != nil {
		t.Fatalf("SaveRevision: %v", err)
	}
	store := &fakeStoreBlobs{
		shasPerBlob:     make(map[string][]string),
		registeredBlobs: map[string]map[string]string{oldSHA: blobs},
	}
	for _, blob := range blobs {
		store.shasPerBlob[blob] = []string{oldSHA}
	}

	runGit(t, "commit", "--amend", "-m", "feat(feat.txt): rewritten message")
	newSHA := strings.TrimSpace(gitOutput(t, "rev-parse", "HEAD"))
	stub := &auditorStub{auditOutput: auditOutputOK}
	res, err := AnalyzeBranch(ledger, BranchOptions{Factory: stubFactory(stub), Parallel: 1, Store: store})
	if err != nil {
		t.Fatalf("AnalyzeBranch: %v", err)
	}
	if stub.auditCalls == 0 {
		t.Fatal("supplementary-only origin was reused instead of auditing the destination")
	}
	if len(res.Records) != 1 || res.Records[0].SHA != newSHA {
		t.Fatalf("Records = %+v, want an audited destination record", res.Records)
	}
	if res.Records[0].OriginSHA != "" {
		t.Errorf("OriginSHA = %q, want no reuse provenance from a supplementary origin", res.Records[0].OriginSHA)
	}
}

func TestAnalyzeBranchAuditsWhenBlobOriginRecordIsMissing(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	sha := commitInBranch(t, "feat.txt", "content\n")
	files, err := git.FilesOfCommit(sha)
	if err != nil {
		t.Fatalf("FilesOfCommit: %v", err)
	}
	blobs, err := commitBlobs(sha, files)
	if err != nil {
		t.Fatalf("commitBlobs: %v", err)
	}
	store := &fakeStoreBlobs{shasPerBlob: make(map[string][]string)}
	for _, blob := range blobs {
		store.shasPerBlob[blob] = []string{"purged-origin"}
	}
	stub := &auditorStub{auditOutput: auditOutputOK}

	res, err := AnalyzeBranch(NewLedger(gitDir), BranchOptions{Factory: stubFactory(stub), Parallel: 1, Store: store})
	if err != nil {
		t.Fatalf("AnalyzeBranch with a stale blob origin: %v", err)
	}
	if stub.auditCalls == 0 {
		t.Fatal("AnalyzeBranch did not audit after the blob origin record was missing")
	}
	if len(res.Records) != 1 || res.Records[0].SHA != sha {
		t.Fatalf("Records = %+v, want a newly audited record for %s", res.Records, sha)
	}
	if res.Records[0].OriginSHA != "" {
		t.Errorf("OriginSHA = %q, want no reuse provenance after stale blob fallback", res.Records[0].OriginSHA)
	}
}

func TestAnalyzeBranchAuditsPending(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	sha := commitInBranch(t, "feat.txt", "1\n2\n3\n4\n5\n")
	ledger := NewLedger(gitDir)
	stub := &auditorStub{auditOutput: auditOutputOK}
	parallel := 2

	res, err := AnalyzeBranch(ledger, BranchOptions{Factory: stubFactory(stub), Parallel: parallel})
	if err != nil {
		t.Fatalf("AnalyzeBranch failed: %v", err)
	}
	if res.Net != nil {
		t.Fatal("default path executed a net review without opting in")
	}

	if len(res.SHAs) != 1 || res.SHAs[0] != sha {
		t.Errorf("SHAs = %v, want [%s]", res.SHAs, sha)
	}
	if len(res.Pending) != 1 {
		t.Errorf("Pending = %v, want [%s] (the new commit)", res.Pending, sha)
	}
	if len(res.Records) != 1 {
		t.Fatalf("Records = %d, want 1", len(res.Records))
	}
	if res.Records[0].SHA != sha || res.Records[0].Revisions[0].Result != VerdictOK {
		t.Errorf("wrong record: %+v", res.Records[0])
	}
	if res.Decision != "single" {
		t.Errorf("Decision = %q, want single (low volume)", res.Decision)
	}
	if res.Volume != 5 {
		t.Errorf("Volume = %d, want 5", res.Volume)
	}

	// The record was persisted in the ledger.
	persisted, err := ledger.ReadRecord(sha)
	if err != nil || persisted == nil || len(persisted.Revisions) != 1 {
		t.Errorf("the record was not saved in the ledger: %v", err)
	}
}

func TestDeterministicFindingsForCommitOnlyAppliesToExplicitSHA(t *testing.T) {
	deterministicFindings := []Finding{{Source: SourceValidation}}

	if got := deterministicFindingsForValidatedSHA("head-sha", "head-sha", deterministicFindings); len(got) != 1 {
		t.Errorf("commit == validatedSHA: findings = %#v, expected them to apply", got)
	}
	if got := deterministicFindingsForValidatedSHA("older-sha", "head-sha", deterministicFindings); got != nil {
		t.Errorf("commit != validatedSHA: findings = %#v, expected nil", got)
	}
	if got := deterministicFindingsForValidatedSHA("head-sha", "", deterministicFindings); got != nil {
		t.Errorf("empty validatedSHA: findings = %#v, expected nil (never infer by position)", got)
	}
}

func TestAuditCommitBranchPassesImmutableCommitPathsToRestrictedReviewer(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	sha := commitInBranch(t, "committed.go", "package committed\n")
	if err := os.WriteFile("uncommitted.go", []byte("package uncommitted\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ledger := NewLedger(gitDir)
	stub := &pathAuditorStub{auditorStub: auditorStub{auditOutput: auditOutputOK}}

	err := auditBranchCommit(ledger, sha, BranchOptions{
		Factory: func(_ ReviewBundle, _ string) (AgentReviewer, string, error) {
			return stub, "stub", nil
		},
		Parallel: 1,
	}, false)
	if err != nil {
		t.Fatalf("auditBranchCommit() error = %v", err)
	}
	if len(stub.paths) == 0 {
		t.Fatal("restricted reviewer received no planned paths")
	}
	for _, paths := range stub.paths {
		if expected := []string{"committed.go"}; !reflect.DeepEqual(paths, expected) {
			t.Errorf("reviewer paths = %v, expected immutable commit paths %v", paths, expected)
		}
	}
}

// TestAnalyzeBranchNotifiesOnCommitForEachPendingInOrder covers the
// traceability debt documented when closing F1: without OnCommit, OnDimension
// progress does not say which commit it belongs to, because AnalyzeBranch
// audits several commits in the same pass.
func TestAnalyzeBranchNotifiesOnCommitForEachPendingInOrder(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	sha1 := commitInBranch(t, "feat1.txt", "1\n2\n")
	sha2 := commitInBranch(t, "feat2.txt", "3\n4\n")
	ledger := NewLedger(gitDir)
	stub := &auditorStub{auditOutput: auditOutputOK}

	type notification struct {
		idx, total int
		sha        string
	}
	var notifications []notification

	_, err := AnalyzeBranch(ledger, BranchOptions{
		Factory:  stubFactory(stub),
		Parallel: 1,
		OnCommit: func(idx, total int, sha string) {
			notifications = append(notifications, notification{idx, total, sha})
		},
	})
	if err != nil {
		t.Fatalf("AnalyzeBranch failed: %v", err)
	}

	expected := []notification{{0, 2, sha1}, {1, 2, sha2}}
	if len(notifications) != len(expected) {
		t.Fatalf("OnCommit was called %d times, want %d: %+v", len(notifications), len(expected), notifications)
	}
	for i, e := range expected {
		if notifications[i] != e {
			t.Errorf("notification %d = %+v, want %+v", i, notifications[i], e)
		}
	}
}

// TestAnalyzeBranchOnlyPending: with the option enabled it audits nothing
// new and returns the already existing records.
func TestAnalyzeBranchOnlyPending(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	sha := commitInBranch(t, "feat.txt", "1\n2\n3\n")
	ledger := NewLedger(gitDir)
	stub := &auditorStub{auditOutput: auditOutputOK}

	if _, err := AnalyzeBranch(ledger, BranchOptions{Factory: stubFactory(stub), Parallel: 1}); err != nil {
		t.Fatalf("first pass failed: %v", err)
	}
	callsBefore := stub.auditCalls

	res, err := AnalyzeBranch(ledger, BranchOptions{Factory: stubFactory(stub), Parallel: 1, OnlyPending: true})
	if err != nil {
		t.Fatalf("AnalyzeBranch(OnlyPending) failed: %v", err)
	}

	if len(res.Pending) != 0 {
		t.Errorf("Pending = %v, want empty (everything already audited)", res.Pending)
	}
	if len(res.Records) != 1 || res.Records[0].SHA != sha {
		t.Errorf("Records = %+v, want only the existing record for %s", res.Records, sha)
	}
	if stub.auditCalls != callsBefore {
		t.Errorf("OnlyPending audits anyway: %d new calls", stub.auditCalls-callsBefore)
	}
}

// TestAnalyzeBranchOnlyPendingReportsSubject: OnlyPending must not just list
// SHAs — a human deciding whether to run `sentinel review <sha>` needs the
// commit's subject too, exactly like the audited matrix does (item 2 of
// docs/issues/actionable.md: the PR must report the gap, not hide it behind
// a bare SHA).
func TestAnalyzeBranchOnlyPendingReportsSubject(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	sha := commitInBranch(t, "feat.txt", "1\n2\n3\n")
	ledger := NewLedger(gitDir)
	stub := &auditorStub{auditOutput: auditOutputOK}

	res, err := AnalyzeBranch(ledger, BranchOptions{Factory: stubFactory(stub), Parallel: 1, OnlyPending: true})
	if err != nil {
		t.Fatalf("AnalyzeBranch(OnlyPending) failed: %v", err)
	}
	if stub.auditCalls != 0 {
		t.Fatalf("OnlyPending must not audit: %d calls", stub.auditCalls)
	}
	if len(res.Unaudited) != 1 || res.Unaudited[0].SHA != sha ||
		res.Unaudited[0].Subject != "feat(feat.txt): test content" {
		t.Errorf("Unaudited = %+v, want [{%s feat(feat.txt): test content}]", res.Unaudited, sha)
	}
}

// TestAnalyzeBranchUnauditedIsAccurateAfterAuditing: the whole point of
// BranchResult.Unaudited is telling a machine consumer "this commit carries
// no review record" apart from "this commit was audited and had no
// findings" (item 5). Unlike Pending (pre-audit discovery, kept for the
// "nuevas" event telemetry), Unaudited must be recomputed AFTER the audit
// loop: a commit audited within this very call must not still read as
// unaudited, or JSON consumers cannot tell the two states apart.
func TestAnalyzeBranchUnauditedIsAccurateAfterAuditing(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	sha := commitInBranch(t, "feat.txt", "1\n2\n3\n")
	ledger := NewLedger(gitDir)
	stub := &auditorStub{auditOutput: auditOutputOK}

	// OnlyPending is false: this commit gets audited within this very call.
	res, err := AnalyzeBranch(ledger, BranchOptions{Factory: stubFactory(stub), Parallel: 1, OnlyPending: false})
	if err != nil {
		t.Fatalf("AnalyzeBranch failed: %v", err)
	}
	if len(res.Unaudited) != 0 {
		t.Errorf("Unaudited = %+v, want empty: %s was just audited in this call", res.Unaudited, sha)
	}
	if len(res.Pending) != 1 || res.Pending[0] != sha {
		t.Errorf("Pending = %v, want [%s] (discovered new this pass, for the event telemetry)", res.Pending, sha)
	}
	if len(res.Records) != 1 || res.Records[0].SHA != sha {
		t.Errorf("Records = %+v, want the freshly audited record for %s", res.Records, sha)
	}
}

// TestDecisionChainByVolume: without an overview, a branch above the line
// threshold proposes a PR chain.
func TestDecisionChainByVolume(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	commitInBranch(t, "large.txt", strings.Repeat("x\n", 500))
	ledger := NewLedger(gitDir)
	stub := &auditorStub{auditOutput: auditOutputOK}

	res, err := AnalyzeBranch(ledger, BranchOptions{Factory: stubFactory(stub), Parallel: 1})
	if err != nil {
		t.Fatalf("AnalyzeBranch failed: %v", err)
	}
	if res.Decision != "chain" {
		t.Errorf("Decision = %q, want chain (500 lines, no overview)", res.Decision)
	}
}

// TestDecisionOverview: the overview (1 branch Spec call) refines the
// decision when the volume exceeds the threshold.
func TestDecisionOverview(t *testing.T) {
	cases := []struct {
		name     string
		coherent string
		want     string
	}{
		{"coherent → single", `{"coherent":true,"rationale":"A single change: the commits share the same goal and files."}`, "single"},
		{"incoherent → chain", `{"coherent":false,"rationale":"Independent units with seams between them."}`, "chain"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gitDir := prepareBranchRepo(t)
			commitInBranch(t, "large.txt", strings.Repeat("x\n", 500))
			ledger := NewLedger(gitDir)
			stub := &auditorStub{auditOutput: auditOutputOK, overviewOutput: tc.coherent}

			res, err := AnalyzeBranch(ledger, BranchOptions{
				Factory: stubFactory(stub), Parallel: 1, Overview: true,
			})
			if err != nil {
				t.Fatalf("AnalyzeBranch failed: %v", err)
			}
			if res.Decision != tc.want {
				t.Errorf("Decision = %q, want %q", res.Decision, tc.want)
			}
			if res.Overview == nil || res.Overview.Coherent != (tc.want == "single") {
				t.Errorf("Overview = %+v, want coherent=%v", res.Overview, tc.want == "single")
			}
			if stub.overviewCalls != 1 {
				t.Errorf("the overview must be exactly 1 call, got %d", stub.overviewCalls)
			}
		})
	}
}

// TestParseOverview: parses the agent's JSON and rejects invalid outputs.
func TestParseOverview(t *testing.T) {
	ok, err := ParseOverview(`text before
{"coherent":true,"rationale":"First line.\nSecond line.","changed":["Adds the summary."],"risk":"Risk is bounded."}
text after`)
	if err != nil {
		t.Fatalf("ParseOverview on valid JSON failed: %v", err)
	}
	if !ok.Coherent || !strings.Contains(ok.Rationale, "First line.") {
		t.Errorf("parsed overview = %+v", ok)
	}

	if _, err := ParseOverview("response without JSON"); err == nil {
		t.Error("ParseOverview accepted an output without JSON")
	}

	// JSON present but without the "coherent" field: rejected by the guard.
	if _, err := ParseOverview(`{"rationale":"text only"}`); err == nil {
		t.Error("ParseOverview accepted JSON without the coherent field")
	}

	// JSON with "coherent" but syntactically invalid: rejected by Unmarshal.
	if _, err := ParseOverview(`{"coherent": tru}`); err == nil {
		t.Error("ParseOverview accepted malformed JSON")
	}

	// JSON preamble before the coherence object: it must take the object
	// containing "coherent", not the first { with the last }.
	withPreamble, err := ParseOverview(`{"metadata":1}
{"coherent":false,"rationale":"Two units with seams.","changed":["Splits units."],"risk":"Seams require review."}`)
	if err != nil {
		t.Fatalf("ParseOverview did not tolerate a JSON preamble: %v", err)
	}
	if withPreamble.Coherent {
		t.Errorf("ParseOverview = %+v, want coherent=false", withPreamble)
	}
}

// TestClosingJSON: the balanced-braces matcher tolerates strings, escapes and
// nesting, and detects unclosed objects.
func TestClosingJSON(t *testing.T) {
	cases := []struct {
		name   string
		output string
		from   int
		want   int
		closed bool
	}{
		{"simple", `{"a":1}`, 0, 6, true},
		{"nested", `{"a":{"b":2}}`, 0, 12, true},
		{"braces in string", `{"a":"{b}"}`, 0, 10, true},
		{"escaped quote", `{"a":"\"}x"}`, 0, 11, true},
		{"incomplete object", `{"a":1`, 0, 0, false},
		{"non-zero start", `xx {"a":1} yy`, 3, 9, true},
		{"non-zero start incomplete", `xx {"a":1`, 3, 0, false},
	}
	for _, tc := range cases {
		end, ok := closingJSON(tc.output, tc.from)
		if ok != tc.closed || (ok && end != tc.want) {
			t.Errorf("%s: closingJSON(%q) = (%d, %v), want (%d, %v)",
				tc.name, tc.output, end, ok, tc.want, tc.closed)
		}
	}
}

// TestOverviewWithoutFactory: without a factory the overview fails with the
// ErrNoReviewerFactory sentinel, comparable with errors.Is from the caller.
func TestOverviewWithoutFactory(t *testing.T) {
	_, err := branchOverview(BranchOptions{}, "feature", nil)
	if !errors.Is(err, ErrNoReviewerFactory) {
		t.Errorf("branchOverview without a factory = %v, expected errors.Is ErrNoReviewerFactory", err)
	}
}

// TestCommitCoveredByBlobsMixedCommitsNotCovered: a commit whose files come
// from a MIX of distinct previous commits (simulating a squash) must not be
// considered covered, even though each individual blob does have SOME SHA
// that registered it. Before the fix, commitCoveredByBlobs consulted
// AlreadyReviewed per blob (only "some SHA covers this blob") and this mix
// slipped through as a safe rebase; it now requires the intersection of
// SHAs across all blobs to be non-empty.
func TestCommitCoveredByBlobsMixedCommitsNotCovered(t *testing.T) {
	prepareBranchRepo(t)
	// A single commit with two files simulates the result of a squash: its
	// two blobs exist, but they are registered (by hand, via the fake) under
	// DIFFERENT historical SHAs, as if each file came from a different
	// previous commit.
	if err := os.WriteFile("mix1.txt", []byte("content 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("mix2.txt", []byte("content 2\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "add", "mix1.txt", "mix2.txt")
	runGit(t, "commit", "-m", "feat(mix): simulates a squash of two commits")
	combinedSHA := strings.TrimSpace(gitOutput(t, "rev-parse", "HEAD"))

	files, err := git.FilesOfCommit(combinedSHA)
	if err != nil {
		t.Fatalf("FilesOfCommit: %v", err)
	}
	blobs, err := commitBlobs(combinedSHA, files)
	if err != nil {
		t.Fatalf("commitBlobs: %v", err)
	}
	if len(blobs) != 2 {
		t.Fatalf("blobs = %v, want 2 files (mix1.txt + mix2.txt)", blobs)
	}

	fake := &fakeStoreBlobs{shasPerBlob: map[string][]string{}}
	i := 0
	for _, blob := range blobs {
		fake.shasPerBlob[blob] = []string{fmt.Sprintf("historical-sha-%d", i)}
		i++
	}

	covered, originSHA, err := commitCoveredByBlobs(fake, combinedSHA)
	if err != nil {
		t.Fatalf("commitCoveredByBlobs: %v", err)
	}
	if covered {
		t.Errorf("commitCoveredByBlobs = (true, %q), want false: the files come from distinct historical SHAs, none covers the complete set", originSHA)
	}
}

// TestAuditCommitBranchContinuesWhenRecordBlobsFails: if RegisterCommitBlobs
// fails AFTER the audit and the ledger save already succeeded,
// auditBranchCommit must not propagate the error — it would abort
// AnalyzeBranch and lose a real result already persisted for the sake of a
// simple future-reuse optimization.
func TestAuditCommitBranchContinuesWhenRecordBlobsFails(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	sha := commitInBranch(t, "feat.txt", "1\n2\n3\n")
	ledger := NewLedger(gitDir)
	stub := &auditorStub{auditOutput: auditOutputOK}
	fake := &fakeStoreBlobs{recordError: errors.New("simulated blob registration failure")}

	err := auditBranchCommit(ledger, sha, BranchOptions{
		Factory: stubFactory(stub), Parallel: 1, Store: fake,
	}, false)
	if err != nil {
		t.Fatalf("auditBranchCommit should continue although RegisterCommitBlobs fails, returned: %v", err)
	}

	record, err := ledger.ReadRecord(sha)
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	if record == nil || len(record.Revisions) != 1 {
		t.Errorf("the record should have been saved in the ledger anyway: %+v", record)
	}
}

// TestBuildOverviewPrompt: the prompt lists the branch's commits.
func TestBuildOverviewPrompt(t *testing.T) {
	records := []Record{
		{SHA: "a1b2c3d4e5f6", Message: "feat(a): one"},
		{SHA: "1234567890ab", Message: "fix(b): two"},
	}
	prompt := BuildOverviewPrompt("feature", records)
	for _, part := range []string{"feature", "a1b2c3d", "feat(a): one", "fix(b): two", "coherent"} {
		if !strings.Contains(prompt, part) {
			t.Errorf("the prompt does not mention %q:\n%s", part, prompt)
		}
	}
}

func TestAuditCommitBranchRoutesThroughPerCommitTransport(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	sha := commitInBranch(t, "transport.go", "package transport\n")
	ledger := NewLedger(gitDir)
	stub := &auditorStub{auditOutput: auditOutputOK}

	var factorySHA string
	var factoryPaths []string
	transportCalled := false
	err := auditBranchCommit(ledger, sha, BranchOptions{
		Factory: func(_ ReviewBundle, _ string) (AgentReviewer, string, error) {
			return stub, "stub", nil
		},
		Parallel: 1,
		ReviewTransportFactory: func(commitSHA string, paths []string) ReviewTransport {
			factorySHA = commitSHA
			factoryPaths = paths
			return func(_, _, _ string, _ AgentReviewer) (string, string, error) {
				transportCalled = true
				return auditOutputOK, "", nil
			}
		},
	}, false)
	if err != nil {
		t.Fatalf("auditBranchCommit() error = %v", err)
	}
	if !transportCalled || factorySHA != sha {
		t.Fatalf("per-commit transport invoked=%v boundSHA=%q, want routed for %q", transportCalled, factorySHA, sha)
	}
	if len(factoryPaths) != 1 || factoryPaths[0] != "transport.go" {
		t.Fatalf("factory paths = %v, want the audited commit's immutable paths", factoryPaths)
	}
}

// credentialFindingFixture is a canned deterministic credential incident with
// the Unit A shape: validation source, empty dimension, WARNING, empty
// evidence. The review-package tests cannot import internal/secret (it
// imports this package), so the factory is stubbed and the real projection
// is covered in its own package plus the app wiring test.
func credentialFindingFixture() Finding {
	h := Finding{
		Source:      SourceValidation,
		Severity:    SevWarning,
		Confidence:  0.9,
		Status:      StatusPending,
		Title:       "exposed credential (github_token)",
		Description: "docs/runbook.md:2 matches github_token (value withheld)",
		Location:    Location{File: "docs/runbook.md", LineStart: 2},
	}
	h.Fingerprint = Fingerprint(h)
	return h
}

const credentialDiffMarker = "ghp_EXAMPLECREDENTIAL"

func credentialFactoryStub() func(string, []string, string) []Finding {
	return func(_ string, _ []string, diff string) []Finding {
		if strings.Contains(diff, credentialDiffMarker) {
			return []Finding{credentialFindingFixture()}
		}
		return nil
	}
}

func aggregatedByTitle(record Record, title string) []Finding {
	var out []Finding
	for _, revision := range record.Revisions {
		for _, h := range revision.AggregatedFindings {
			if h.Title == title {
				out = append(out, h)
			}
		}
	}
	return out
}

func buildTwoCommitBranch(t *testing.T) (gitDir, cleanSHA, credSHA string) {
	t.Helper()
	gitDir = prepareBranchRepo(t)
	if err := os.MkdirAll("docs", 0755); err != nil {
		t.Fatal(err)
	}
	cleanSHA = commitInBranch(t, "app.txt", "1\n2\n3\n4\n5\n")
	credSHA = commitInBranch(t, "docs/runbook.md", "deploy:\n  token = \""+credentialDiffMarker+"1234567890abcdef\"\n")
	return gitDir, cleanSHA, credSHA
}

func TestBranchFindingsFactoryIsPerCommit(t *testing.T) {
	gitDir, cleanSHA, credSHA := buildTwoCommitBranch(t)
	ledger := NewLedger(gitDir)
	stub := &auditorStub{auditOutput: auditOutputOK}

	res, err := AnalyzeBranch(ledger, BranchOptions{
		Factory:                      stubFactory(stub),
		Parallel:                     1,
		DeterministicFindingsFactory: credentialFactoryStub(),
	})
	if err != nil {
		t.Fatalf("AnalyzeBranch failed: %v", err)
	}
	if len(res.Records) != 2 {
		t.Fatalf("records = %d, want 2", len(res.Records))
	}
	if got := aggregatedByTitle(res.Records[0], "exposed credential (github_token)"); len(got) != 0 {
		t.Errorf("clean commit carries %d credential findings, want none", len(got))
	}
	credRecord, err := ledger.ReadRecord(credSHA)
	if err != nil || credRecord == nil {
		t.Fatalf("persisted credential record missing: %v", err)
	}
	got := aggregatedByTitle(*credRecord, "exposed credential (github_token)")
	if len(got) != 1 {
		t.Fatalf("credential commit carries %d findings, want 1", len(got))
	}
	if got[0].Evidence != "" {
		t.Errorf("Evidence = %q, want empty: the value must never persist", got[0].Evidence)
	}
	if got[0].Severity != SevWarning || got[0].Status != StatusPending || got[0].Dimension != "" {
		t.Errorf("finding = %+v, want WARNING/pending/dimensionless", got[0])
	}
	_ = cleanSHA
}

func TestBranchFindingsFactoryLeavesVerdictAndDecisionUntouched(t *testing.T) {
	run := func(t *testing.T, factory func(string, []string, string) []Finding) (results []string, decision string) {
		t.Helper()
		gitDir, _, _ := buildTwoCommitBranch(t)
		ledger := NewLedger(gitDir)
		res, err := AnalyzeBranch(ledger, BranchOptions{
			Factory:                      stubFactory(&auditorStub{auditOutput: auditOutputOK}),
			Parallel:                     1,
			DeterministicFindingsFactory: factory,
		})
		if err != nil {
			t.Fatalf("AnalyzeBranch failed: %v", err)
		}
		for _, record := range res.Records {
			results = append(results, record.Revisions[0].Result)
		}
		return results, res.Decision
	}

	plainResults, plainDecision := run(t, nil)
	wiredResults, wiredDecision := run(t, credentialFactoryStub())
	if strings.Join(plainResults, ",") != strings.Join(wiredResults, ",") {
		t.Errorf("verdicts without factory %v != with factory %v", plainResults, wiredResults)
	}
	if plainDecision != wiredDecision {
		t.Errorf("decision without factory %q != with factory %q", plainDecision, wiredDecision)
	}
}

func TestBranchFindingsFactoryAppendsGateFindings(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	if err := os.MkdirAll("docs", 0755); err != nil {
		t.Fatal(err)
	}
	sha := commitInBranch(t, "docs/runbook.md", "token = \""+credentialDiffMarker+"1234567890abcdef\"\n")
	ledger := NewLedger(gitDir)
	gateFinding := Finding{Source: SourceValidation, Dimension: DimStyle, Severity: SevWarning, Title: "gate validation", Location: Location{File: "docs/runbook.md", LineStart: 1}}
	gateFinding.Fingerprint = Fingerprint(gateFinding)

	res, err := AnalyzeBranch(ledger, BranchOptions{
		Factory:                      stubFactory(&auditorStub{auditOutput: auditOutputOK}),
		Parallel:                     1,
		DeterministicFindings:        []Finding{gateFinding},
		DeterministicFindingsSHA:     sha,
		DeterministicFindingsFactory: credentialFactoryStub(),
	})
	if err != nil {
		t.Fatalf("AnalyzeBranch failed: %v", err)
	}
	if len(res.Records) != 1 {
		t.Fatalf("records = %d, want 1", len(res.Records))
	}
	titles := map[string]bool{}
	for _, h := range res.Records[0].Revisions[0].AggregatedFindings {
		titles[h.Title] = true
	}
	if !titles["gate validation"] || !titles["exposed credential (github_token)"] {
		t.Errorf("titles = %v, want both the gate finding and the factory finding", titles)
	}
}

func TestBranchFindingsFactoryBinaryCommitIsSafe(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	payload := append([]byte("PNG\x00\x01\x02binary"), []byte(strings.Repeat("x", 200))...)
	if err := os.WriteFile("logo.png", payload, 0644); err != nil {
		t.Fatal(err)
	}
	runGit(t, "add", "logo.png")
	runGit(t, "commit", "-m", "feat(logo): binary asset")
	sha := strings.TrimSpace(gitOutput(t, "rev-parse", "HEAD"))
	ledger := NewLedger(gitDir)

	res, err := AnalyzeBranch(ledger, BranchOptions{
		Factory:                      stubFactory(&auditorStub{auditOutput: auditOutputOK}),
		Parallel:                     1,
		DeterministicFindingsFactory: credentialFactoryStub(),
	})
	if err != nil {
		t.Fatalf("AnalyzeBranch failed on a binary commit: %v", err)
	}
	if len(res.Records) != 1 || res.Records[0].SHA != sha {
		t.Fatalf("records = %v, want [%s]", res.Records, sha)
	}
	if got := aggregatedByTitle(res.Records[0], "exposed credential (github_token)"); len(got) != 0 {
		t.Errorf("binary commit carries %d credential findings, want none and no error", len(got))
	}
}

func TestBranchAuditStampsVerifiedModelFromVerifier(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	sha := commitInBranch(t, "feat.txt", "1\n2\n3\n4\n5\n")
	ledger := NewLedger(gitDir)
	factory := fixedEffectiveFactory(`{"dim":"logic","verdict":"warn","findings":[{"file":"feat.txt","line":1,"severity":"WARNING","description":"ignored error","confidence":0.6}]}`)

	res, err := AnalyzeBranch(ledger, BranchOptions{
		Factory:       factory,
		Parallel:      1,
		ModelVerifier: modelVerifierStub{verified: map[string]bool{"normal": true}},
	})
	if err != nil {
		t.Fatalf("AnalyzeBranch failed: %v", err)
	}
	if len(res.Records) != 1 || res.Records[0].SHA != sha {
		t.Fatalf("records = %v, want [%s]", res.Records, sha)
	}
	persisted, err := ledger.ReadRecord(sha)
	if err != nil || persisted == nil {
		t.Fatalf("persisted record missing: %v", err)
	}
	findings := persisted.Revisions[0].AggregatedFindings
	if len(findings) != 1 {
		t.Fatalf("findings = %#v, expected one", findings)
	}
	if !findings[0].Producer.ModelVerified {
		t.Error("ModelVerified = false with a matching verifier: the branch flow must consult it")
	}
}

func TestBranchAuditLeavesModelUnverifiedWithoutVerifier(t *testing.T) {
	gitDir := prepareBranchRepo(t)
	sha := commitInBranch(t, "feat.txt", "1\n2\n3\n4\n5\n")
	ledger := NewLedger(gitDir)
	factory := fixedEffectiveFactory(`{"dim":"logic","verdict":"warn","findings":[{"file":"feat.txt","line":1,"severity":"WARNING","description":"ignored error","confidence":0.6}]}`)

	res, err := AnalyzeBranch(ledger, BranchOptions{Factory: factory, Parallel: 1})
	if err != nil {
		t.Fatalf("AnalyzeBranch failed: %v", err)
	}
	if len(res.Records) != 1 || res.Records[0].SHA != sha {
		t.Fatalf("records = %v, want [%s]", res.Records, sha)
	}
	if res.Records[0].Revisions[0].AggregatedFindings[0].Producer.ModelVerified {
		t.Error("ModelVerified = true without a verifier: false is the honest default")
	}
}

func fixedEffectiveFactory(response string) ReviewerFactory {
	agent := fakeEffectiveAgent{
		response:  response,
		effective: agentadapter.EffectiveAgent{Binary: "opencode", Model: "gpt-5.6-terra", Effort: "high"},
		defined:   true,
	}
	return func(_ ReviewBundle, _ string) (AgentReviewer, string, error) {
		return agent, "normal", nil
	}
}
