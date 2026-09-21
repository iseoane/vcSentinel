package store

import (
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/review"
)

func TestAlreadyReviewedBlobNeverSeen(t *testing.T) {
	s := NewStore(t.TempDir())

	reviewed, findings, err := s.AlreadyReviewed("blob-never-seen")
	if err != nil {
		t.Fatalf("AlreadyReviewed: %v", err)
	}
	if reviewed {
		t.Error("AlreadyReviewed = true for a blob that was never registered")
	}
	if findings != nil {
		t.Errorf("findings = %v, want nil", findings)
	}
}

// TestAlreadyReviewedBlobCleanWithoutFindings covers the key distinction: a
// blob that did appear in a CommitIndex, but with no associated fingerprints
// (a clean commit, without findings), must count as reviewed=true. Otherwise
// a problem-free file would be re-audited every time.
func TestAlreadyReviewedBlobCleanWithoutFindings(t *testing.T) {
	s := NewStore(t.TempDir())
	idx := &CommitIndex{SHA: "clean-sha", Blobs: map[string]string{"a.go": "clean-blob"}}
	if err := s.SaveCommitIndex(idx); err != nil {
		t.Fatalf("SaveCommitIndex: %v", err)
	}

	reviewed, findings, err := s.AlreadyReviewed("clean-blob")
	if err != nil {
		t.Fatalf("AlreadyReviewed: %v", err)
	}
	if !reviewed {
		t.Error("AlreadyReviewed = false for a blob that did appear in a CommitIndex (clean)")
	}
	if len(findings) != 0 {
		t.Errorf("findings = %+v, want empty (clean blob)", findings)
	}
}

// TestAlreadyReviewedBlobWithFindings covers the case with real v2 findings
// associated with the blob: they must be resolved by fingerprint and
// returned.
func TestAlreadyReviewedBlobWithFindings(t *testing.T) {
	s := NewStore(t.TempDir())
	h := &review.Finding{
		Fingerprint: "fp-blob-with-finding",
		Title:       "something",
		Severity:    "high",
		Location:    review.Location{File: "a.go", Blob: "blob-with-finding"},
	}
	if err := s.SaveFinding(h); err != nil {
		t.Fatalf("SaveFinding: %v", err)
	}
	idx := &CommitIndex{
		SHA:          "sha-with-finding",
		Fingerprints: []string{"fp-blob-with-finding"},
		Blobs:        map[string]string{"a.go": "blob-with-finding"},
	}
	if err := s.SaveCommitIndex(idx); err != nil {
		t.Fatalf("SaveCommitIndex: %v", err)
	}

	reviewed, findings, err := s.AlreadyReviewed("blob-with-finding")
	if err != nil {
		t.Fatalf("AlreadyReviewed: %v", err)
	}
	if !reviewed {
		t.Error("AlreadyReviewed = false for a blob with an associated finding")
	}
	if len(findings) != 1 || findings[0].Fingerprint != "fp-blob-with-finding" {
		t.Errorf("findings = %+v, want [fp-blob-with-finding]", findings)
	}
}

// TestBlobSHAsNeverRegistered: a blob that was never registered returns nil
// without error (distinct from a registered blob with an empty list, which
// in practice does not happen because registerBlobs only writes when there
// is a SHA to add, but the "never seen" contract must be unambiguous).
func TestBlobSHAsNeverRegistered(t *testing.T) {
	s := NewStore(t.TempDir())

	shas, err := s.BlobSHAs("never-registered-blob")
	if err != nil {
		t.Fatalf("BlobSHAs: %v", err)
	}
	if shas != nil {
		t.Errorf("shas = %v, want nil for a never-registered blob", shas)
	}
}

// TestBlobSHAsReturnsRegisteredOnes: covers the happy path used by
// commitCoveredByBlobs (internal/review) to compute intersections.
func TestBlobSHAsReturnsRegisteredOnes(t *testing.T) {
	s := NewStore(t.TempDir())
	idx := &CommitIndex{SHA: "sha-x", Blobs: map[string]string{"a.go": "blob-x"}}
	if err := s.SaveCommitIndex(idx); err != nil {
		t.Fatalf("SaveCommitIndex: %v", err)
	}

	shas, err := s.BlobSHAs("blob-x")
	if err != nil {
		t.Fatalf("BlobSHAs: %v", err)
	}
	if len(shas) != 1 || shas[0] != "sha-x" {
		t.Errorf("shas = %v, want [sha-x]", shas)
	}
}

// TestRegisterCommitBlobsPreservesFingerprints: RegisterCommitBlobs must
// not overwrite the Fingerprints/V1 that the CommitIndex of that sha
// already had.
func TestRegisterCommitBlobsPreservesFingerprints(t *testing.T) {
	s := NewStore(t.TempDir())
	idx := &CommitIndex{SHA: "sha1", Fingerprints: []string{"fp1"}, Message: "feat(x)"}
	if err := s.SaveCommitIndex(idx); err != nil {
		t.Fatalf("SaveCommitIndex: %v", err)
	}

	if err := s.RegisterCommitBlobs("sha1", map[string]string{"a.go": "blob1"}); err != nil {
		t.Fatalf("RegisterCommitBlobs: %v", err)
	}

	got, err := s.ReadCommitIndex("sha1")
	if err != nil {
		t.Fatalf("ReadCommitIndex: %v", err)
	}
	if got == nil || len(got.Fingerprints) != 1 || got.Fingerprints[0] != "fp1" {
		t.Errorf("Fingerprints not preserved: %+v", got)
	}
	if got.Blobs["a.go"] != "blob1" {
		t.Errorf("Blobs = %+v, want a.go->blob1", got.Blobs)
	}
}
