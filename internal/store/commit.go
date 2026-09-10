package store

import "errors"

// CommitIndex is the traceability index commits/<sha>.json: deliberately
// lightweight, only which review.Finding fingerprints touch that SHA (the
// full content of each finding lives in findings/<fingerprint>.json).
// Message stays as optional context so the git commit does not have to be
// re-read when inspecting the index. Blobs (T2.7) is optional and maps
// file→blob of that commit: it is the basis of the inverted index
// blobs/<blob>.json (see blob.go) that makes it possible to recognize,
// after a rebase that changes the SHA without touching content, that a file
// was already reviewed. An index saved before T2.7 deserializes with Blobs
// nil (field absent in JSON → nil map, normal Go behavior), without
// breaking anything.
type CommitIndex struct {
	SHA          string            `json:"sha"`
	Message      string            `json:"message,omitempty"`
	Fingerprints []string          `json:"fingerprints"`
	Blobs        map[string]string `json:"blobs,omitempty"`
}

// SaveCommitIndex persists idx in commits/<sha>.json and, if idx.Blobs is
// not empty, also updates the inverted index blobs/<blob>.json of each blob
// it declares (see registerBlobs in blob.go): without this, AlreadyReviewed
// would never have data to answer with.
func (s *Store) SaveCommitIndex(idx *CommitIndex) error {
	if idx.SHA == "" {
		return errors.New("store: commit index without sha")
	}
	if err := s.writeJSON(subdirCommits, idx.SHA, idx); err != nil {
		return err
	}
	return s.registerBlobs(idx)
}

// ReadCommitIndex returns the index of that SHA, or nil if it does not
// exist yet.
func (s *Store) ReadCommitIndex(sha string) (*CommitIndex, error) {
	var idx CommitIndex
	ok, err := s.readJSON(subdirCommits, sha, &idx)
	if err != nil || !ok {
		return nil, err
	}
	return &idx, nil
}

// CommitBlobs returns a copy of a registered commit's file→blob mapping.
// A missing or legacy commit index has no reusable mapping and returns nil.
func (s *Store) CommitBlobs(sha string) (map[string]string, error) {
	idx, err := s.ReadCommitIndex(sha)
	if err != nil || idx == nil || len(idx.Blobs) == 0 {
		return nil, err
	}
	blobs := make(map[string]string, len(idx.Blobs))
	for path, blob := range idx.Blobs {
		blobs[path] = blob
	}
	return blobs, nil
}

// RegisterCommitBlobs saves (or extends) the CommitIndex of sha with the
// blobs of its files, preserving the Fingerprints it already had: without
// this merge, a second call on the same sha (for example, retrying a branch
// analysis) could overwrite already-registered findings. Intended for the
// caller that audits a commit and only knows its blobs, not v2
// fingerprints (T2.7: AnalyzeBranch keeps emitting v1 until F5).
//
// blobs is MERGED into idx.Blobs (file by file), not substituted: an
// earlier call on the same sha with a different blob set (today
// AnalyzeBranch only calls once with the complete set, but the contract
// must be safe also if that changes) would, with a substitution of the
// whole map, leave its entries orphaned in blobs/<blob>.json without
// idx.Blobs pointing to them again.
func (s *Store) RegisterCommitBlobs(sha string, blobs map[string]string) error {
	idx, err := s.ReadCommitIndex(sha)
	if err != nil {
		return err
	}
	if idx == nil {
		idx = &CommitIndex{SHA: sha}
	}
	if idx.Blobs == nil {
		idx.Blobs = make(map[string]string, len(blobs))
	}
	for file, blob := range blobs {
		idx.Blobs[file] = blob
	}
	return s.SaveCommitIndex(idx)
}
