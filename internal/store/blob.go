package store

import "github.com/ISeoane-Quental/vcSentinel/internal/review"

// subdirBlobs is the blob→SHA inverted index (T2.7): without it, answering
// "was this blob seen before?" would require scanning every commits/*.json
// one by one; with it, it is a direct read per blob.
const subdirBlobs = "blobs"

// blobIndex is the content of blobs/<blob>.json: which commit SHAs already
// registered that content blob (via CommitIndex.Blobs).
type blobIndex struct {
	Blob string   `json:"blob"`
	SHAs []string `json:"shas"`
}

// registerBlobs adds idx.SHA to blobs/<blob>.json for each blob of
// idx.Blobs, without duplicating if that SHA was already registered for
// that blob. It reuses writeJSON (the same atomic temp+rename pattern as
// the rest of the store): no separate write mechanism is needed.
func (s *Store) registerBlobs(idx *CommitIndex) error {
	for _, blob := range idx.Blobs {
		var ib blobIndex
		ok, err := s.readJSON(subdirBlobs, blob, &ib)
		if err != nil {
			return err
		}
		if !ok {
			ib = blobIndex{Blob: blob}
		}
		alreadyRegistered := false
		for _, sha := range ib.SHAs {
			if sha == idx.SHA {
				alreadyRegistered = true
				break
			}
		}
		if !alreadyRegistered {
			ib.SHAs = append(ib.SHAs, idx.SHA)
		}
		if err := s.writeJSON(subdirBlobs, blob, &ib); err != nil {
			return err
		}
	}
	return nil
}

// readBlobIndex reads blobs/<blob>.json. ok=false if the blob was never
// registered (no error). Shared by AlreadyReviewed and BlobSHAs so the
// inverted index is not deserialized twice.
func (s *Store) readBlobIndex(blob string) (ib blobIndex, ok bool, err error) {
	ok, err = s.readJSON(subdirBlobs, blob, &ib)
	return ib, ok, err
}

// BlobSHAs returns the commit SHAs that registered blob (the inverted index
// blobs/<blob>.json), or nil if the blob was never registered (no error).
// Unlike AlreadyReviewed, which resolves v2 findings by fingerprint, it
// exposes the raw SHAs: commitCoveredByBlobs (internal/review) needs it to
// compute the exact INTERSECTION of SHAs across all the blobs of a commit,
// not just whether "some" SHA covers each blob separately (that allowed a
// mix of distinct commits, e.g. a squash, to slip through as if it were a
// single safe rebase).
func (s *Store) BlobSHAs(blob string) ([]string, error) {
	ib, ok, err := s.readBlobIndex(blob)
	if err != nil || !ok {
		return nil, err
	}
	return ib.SHAs, nil
}

// AlreadyReviewed reports whether blob already appeared in some previously
// saved CommitIndex (reviewed=true), and returns the review.Finding (v2)
// whose Location.Blob matches it.
//
// Deliberate distinction, the point where a naive design goes wrong:
// "reviewed without findings" (the blob appeared in an already-audited
// commit, but a clean one: no fingerprint of that commit points to it) is
// DISTINCT from "never reviewed" (the blob does not appear in any saved
// CommitIndex). If deciding "needs auditing" only looked at whether
// findings is empty, a clean file would be re-audited every time because
// "no findings" would be confused with "unknown". That is why reviewed is
// an explicit boolean, independent of whether findings has elements.
func (s *Store) AlreadyReviewed(blob string) (reviewed bool, findings []review.Finding, err error) {
	ib, ok, err := s.readBlobIndex(blob)
	if err != nil {
		return false, nil, err
	}
	if !ok || len(ib.SHAs) == 0 {
		return false, nil, nil
	}

	seen := make(map[string]bool)
	for _, sha := range ib.SHAs {
		idx, err := s.ReadCommitIndex(sha)
		if err != nil {
			return false, nil, err
		}
		if idx == nil {
			continue
		}
		for _, fp := range idx.Fingerprints {
			if seen[fp] {
				continue
			}
			h, err := s.ReadFinding(fp)
			if err != nil {
				return false, nil, err
			}
			if h != nil && h.Location.Blob == blob {
				findings = append(findings, *h)
				seen[fp] = true
			}
		}
	}
	return true, findings, nil
}
