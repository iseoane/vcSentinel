package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MaxTranscriptBytes bounds the raw provider output retained per invocation.
// The durable outcome still retains the hash of the full provider answer.
const MaxTranscriptBytes = 1 << 20

// TranscriptSidecar is the exact JSON shape persisted next to one durable
// execution record. It carries the raw provider stdout that the event stream
// deliberately never stores (event frames carry only hashes), so every
// admitted invocation stays inspectable without breaking the byte-compatible
// hash chain of events.jsonl.
type TranscriptSidecar struct {
	InvocationID string    `json:"invocation_id"`
	At           time.Time `json:"at"`
	Output       string    `json:"output"`
}

// WriteTranscript atomically persists the raw provider output of one physical
// invocation as <execution-dir>/transcripts/<invocationID>.json via temp file
// plus rename, mirroring the guardian's other record writes. It returns the
// sha256 hex digest over the exact bytes written plus their size, so callers
// can record tamper evidence on the invocation's outcome record.
func (s *Store) WriteTranscript(runID, invocationID string, at time.Time, output string) (string, int64, error) {
	if !validRunID(runID) || !validRunID(invocationID) {
		return "", 0, fmt.Errorf("store: invalid transcript identity run=%q invocation=%q", runID, invocationID)
	}
	if len(output) > MaxTranscriptBytes {
		return "", 0, fmt.Errorf("store: transcript exceeds %d-byte retention limit", MaxTranscriptBytes)
	}
	directory, err := s.executionDir(runID)
	if err != nil {
		return "", 0, err
	}
	if err := ensureExecutionExists(directory); err != nil {
		return "", 0, err
	}
	data, err := json.Marshal(TranscriptSidecar{InvocationID: invocationID, At: at, Output: output})
	if err != nil {
		return "", 0, err
	}
	path := filepath.Join(directory, "transcripts", invocationID+".json")
	if err := atomicWrite(path, data); err != nil {
		return "", 0, err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), int64(len(data)), nil
}

// RemoveTranscript discards an unreferenced transcript after terminal outcome
// persistence fails. Missing sidecars are already absent and therefore succeed.
func (s *Store) RemoveTranscript(runID, invocationID string) error {
	if !validRunID(runID) || !validRunID(invocationID) {
		return fmt.Errorf("store: invalid transcript identity run=%q invocation=%q", runID, invocationID)
	}
	directory, err := s.executionDir(runID)
	if err != nil {
		return err
	}
	if err := ensureExecutionExists(directory); err != nil {
		return err
	}
	path := filepath.Join(directory, "transcripts", invocationID+".json")
	return withExecutionLock(directory, func() error {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	})
}

// ExecutionDir exposes the on-disk execution directory of one run so callers
// can locate its outcomes and transcripts sidecars without duplicating the
// layout knowledge this package owns.
func (s *Store) ExecutionDir(runID string) (string, error) {
	return s.executionDir(runID)
}

// VerifyTranscript recomputes tamper evidence for one invocation's transcript
// sidecar against the digest recorded on its persisted outcome record.
// executionDir is the run's execution directory as returned by
// (*Store).ExecutionDir. It reports ok=false with a human-readable reason when
// the outcome record is missing or carries no digest, when the sidecar is
// missing or unreadable, or when the recomputed digest or size diverges from
// the recorded values. A true result proves the sidecar bytes are exactly the
// bytes captured at admission time.
func VerifyTranscript(executionDir, invocationID string) (bool, string) {
	if executionDir == "" || invocationID == "" {
		return false, "empty execution directory or invocation id"
	}
	outcomeData, err := os.ReadFile(filepath.Join(executionDir, "outcomes", invocationID+".json"))
	if err != nil {
		return false, fmt.Sprintf("outcome record unreadable: %v", err)
	}
	var outcome AttemptOutcome
	if err := json.Unmarshal(outcomeData, &outcome); err != nil {
		return false, fmt.Sprintf("outcome record corrupt: %v", err)
	}
	if outcome.TranscriptSHA256 == "" {
		return false, "no transcript digest recorded on the outcome"
	}
	sidecarBytes, err := os.ReadFile(filepath.Join(executionDir, "transcripts", invocationID+".json"))
	if err != nil {
		return false, fmt.Sprintf("transcript sidecar unreadable: %v", err)
	}
	digest := sha256.Sum256(sidecarBytes)
	if got := hex.EncodeToString(digest[:]); got != outcome.TranscriptSHA256 {
		return false, fmt.Sprintf("digest mismatch: recorded %s, computed %s", outcome.TranscriptSHA256, got)
	}
	if outcome.TranscriptSize != 0 && outcome.TranscriptSize != int64(len(sidecarBytes)) {
		return false, fmt.Sprintf("size mismatch: recorded %d, computed %d", outcome.TranscriptSize, len(sidecarBytes))
	}
	return true, ""
}
