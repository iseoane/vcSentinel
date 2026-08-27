package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

// ErrImmutableConflict reports an attempt to change an existing execution
// identity record.
var ErrImmutableConflict = errors.New("store: immutable execution record conflict")

// ErrRequestCorrupt means that a persisted execution request record failed
// to decode into its recorded shape, so its admission identities cannot be
// trusted.
var ErrRequestCorrupt = errors.New("store: corrupt execution request")

// ErrPolicyCorrupt means that a persisted run policy record failed to decode
// into its recorded shape, so its operator-facing operation label cannot be
// trusted.
var ErrPolicyCorrupt = errors.New("store: corrupt run policy")

// RunPolicy identifies the policy used to create a durable execution.
// Policy contents are resolved elsewhere; only this stable identity is stored.
// ParentRunID optionally records the orchestrating parent run so child runs
// admit with a persisted linkage back to their root; empty means the run has
// no durable parent (a root run).
//
// Operation optionally carries the operator-facing label ("review",
// "gate pre-push", "run") that activity surfaces render in place of the bare
// run id. Commit optionally carries the audited commit short SHA (7 runes)
// that the run audits, so the activity pane can show it without re-deriving
// it from the candidate. Worktree optionally carries the worktree path that
// launched the run, so the activity pane can filter by selected worktree.
// All are additive: old records never carry them, old readers ignore unknown
// keys, and omitempty keeps the persisted bytes of unlabeled runs identical
// to the legacy shape.
type RunPolicy struct {
	ID          string `json:"id"`
	ParentRunID string `json:"parent_run_id,omitempty"`
	Operation   string `json:"operation,omitempty"`
	Commit      string `json:"commit,omitempty"`
	Worktree    string `json:"worktree,omitempty"`
}

// ExecutionRequest is the immutable admission request persisted at CreateRun.
// Every field except ParentRunID is a derived agentrun identity; raw prompts,
// candidates, and capabilities never reach durable storage. ParentRunID is an
// additive, optional linkage to the orchestrating parent run: old records
// never carry it, old readers ignore unknown keys, and omitempty keeps the
// persisted bytes of parentless runs identical to the legacy shape.
type ExecutionRequest struct {
	RunID         string   `json:"run_id"`
	JobID         string   `json:"job_id"`
	RequestID     string   `json:"request_id"`
	CandidateID   string   `json:"candidate_id"`
	PromptID      string   `json:"prompt_id"`
	CapabilityIDs []string `json:"capability_ids,omitempty"`
	ParentRunID   string   `json:"parent_run_id,omitempty"`
}

// CreateRun creates the immutable execution records and an empty event log.
// Repeating the same request is safe; changing either identity record fails.
func (s *Store) CreateRun(job agentrun.LogicalJob, policy RunPolicy) error {
	runID := string(job.RunID())
	directory, err := s.executionDir(runID)
	if err != nil {
		return err
	}
	if policy.ID == "" {
		return errors.New("store: run policy identity is empty")
	}
	if policy.ParentRunID != "" && !validRunID(policy.ParentRunID) {
		return fmt.Errorf("store: invalid parent run id %q", policy.ParentRunID)
	}

	request := requestFor(job)
	request.ParentRunID = policy.ParentRunID
	requestData, err := marshalRecord(request)
	if err != nil {
		return err
	}
	policyData, err := marshalRecord(policy)
	if err != nil {
		return err
	}
	requestPath := filepath.Join(directory, "request.json")
	policyPath := filepath.Join(directory, "policy.json")

	// Ticket 14 prune admission window: admission takes the same
	// cross-process execution event lock PruneExecutions holds while it
	// re-verifies and removes a record, so an admission serializes against
	// every removal critical section. Without this, a run admitted while a
	// crash-interrupted-remnant cleanup held the lock could have its fresh
	// records wiped by that cleanup's unconditional child clearing. With
	// it, exactly two honest outcomes exist for an admission racing a
	// removal of the same directory: the removal completes first and the
	// admission recreates the record cleanly, or the admission lands first
	// and the locked cleanup refuses because real content appeared (see
	// removeExecutionRemnant). A record is never deleted out from under a
	// successful admission verdict.
	//
	// Deadlock audit against appendEventLocked ordering: nothing may call
	// CreateRun while already holding this directory's event lock. The only
	// production caller (Controller.Start) invokes CreateRun sequentially
	// before any AppendEvent/AppendTerminalEvent of the same run, and this
	// closure nests no further lock acquisition on the same directory.
	admit := func() error {
		return withExecutionLock(directory, func() error {
			if err := checkImmutableRecord(requestPath, requestData); err != nil {
				return err
			}
			if err := checkImmutableRecord(policyPath, policyData); err != nil {
				return err
			}
			if err := os.MkdirAll(directory, 0700); err != nil {
				return err
			}
			if err := writeImmutableRecord(requestPath, requestData); err != nil {
				return err
			}
			if err := writeImmutableRecord(policyPath, policyData); err != nil {
				return err
			}
			return ensureEventLog(directory)
		})
	}
	err = admit()
	if errors.Is(err, os.ErrNotExist) {
		// The racing removal deleted the whole directory in the instant
		// between this admission's MkdirAll and its lock-file open. That
		// removal is complete and final; recreate the directory and admit
		// once more instead of reporting a transient miss as a refusal.
		err = admit()
	}
	return err
}

func (s *Store) executionDir(runID string) (string, error) {
	if !validRunID(runID) {
		return "", fmt.Errorf("store: invalid run id %q", runID)
	}
	return filepath.Join(s.dir, "executions", "v1", runID), nil
}

// ListExecutionIDs returns every persisted run identifier under the
// executions directory, sorted lexicographically. A missing directory is the
// valid empty state before the first admitted run; stray non-directory or
// invalidly named entries are ignored so a partial write never breaks listing.
func (s *Store) ListExecutionIDs() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.dir, "executions", "v1"))
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && validRunID(entry.Name()) {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func validRunID(runID string) bool {
	if runID == "" || runID == "." || runID == ".." {
		return false
	}
	if strings.ContainsAny(runID, `/\`) || strings.IndexByte(runID, 0) >= 0 {
		return false
	}
	return filepath.Base(runID) == runID
}

// ReadExecutionRequest reads the immutable admission request persisted for
// one durable run. A missing record reports a not-found error wrapping
// ErrExecutionNotFound; bytes that fail to decode into the recorded shape —
// or decode with empty admission identities — report an error wrapping
// ErrRequestCorrupt, so callers can separate store damage from absent
// history and never mistake either for an admission verdict.
func (s *Store) ReadExecutionRequest(runID string) (ExecutionRequest, error) {
	directory, err := s.executionDir(runID)
	if err != nil {
		return ExecutionRequest{}, err
	}
	path := filepath.Join(directory, "request.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ExecutionRequest{}, fmt.Errorf("%w: %s", ErrExecutionNotFound, path)
	}
	if err != nil {
		return ExecutionRequest{}, err
	}
	var request ExecutionRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return ExecutionRequest{}, fmt.Errorf("%w: %s", ErrRequestCorrupt, path)
	}
	if request.RunID == "" || request.JobID == "" || request.RequestID == "" ||
		request.CandidateID == "" || request.PromptID == "" {
		return ExecutionRequest{}, fmt.Errorf("%w: %s", ErrRequestCorrupt, path)
	}
	return request, nil
}

// ReadRunPolicy reads one immutable policy record. Optional legacy fields may
// be empty; missing or structurally invalid records return their sentinels.
func (s *Store) ReadRunPolicy(runID string) (RunPolicy, error) {
	directory, err := s.executionDir(runID)
	if err != nil {
		return RunPolicy{}, err
	}
	path := filepath.Join(directory, "policy.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return RunPolicy{}, fmt.Errorf("%w: %s", ErrExecutionNotFound, path)
	}
	if err != nil {
		return RunPolicy{}, err
	}
	var policy RunPolicy
	if err := json.Unmarshal(data, &policy); err != nil || policy.ID == "" {
		return RunPolicy{}, fmt.Errorf("%w: %s", ErrPolicyCorrupt, path)
	}
	return policy, nil
}

// ReadRunOperation reads the operator-facing operation label persisted in one
// durable run's immutable policy record.
func (s *Store) ReadRunOperation(runID string) (string, error) {
	policy, err := s.ReadRunPolicy(runID)
	return policy.Operation, err
}

func (s *Store) UpdateRunOperation(runID, operation string) error {
	directory, err := s.executionDir(runID)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "policy.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrExecutionNotFound, path)
	}
	if err != nil {
		return err
	}
	var policy RunPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return fmt.Errorf("%w: %s", ErrPolicyCorrupt, path)
	}
	policy.Operation = operation
	updated, err := marshalRecord(policy)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, updated, 0600); err != nil {
		return err
	}
	return nil
}

func (s *Store) ReadRunCommit(runID string) (string, error) {
	policy, err := s.ReadRunPolicy(runID)
	if err != nil {
		return "", err
	}
	return policy.Commit, nil
}

func (s *Store) UpdateRunCommit(runID, commit string) error {
	directory, err := s.executionDir(runID)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "policy.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrExecutionNotFound, path)
	}
	if err != nil {
		return err
	}
	var policy RunPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return fmt.Errorf("%w: %s", ErrPolicyCorrupt, path)
	}
	policy.Commit = commit
	updated, err := marshalRecord(policy)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, updated, 0600); err != nil {
		return err
	}
	return nil
}

func (s *Store) ReadRunWorktree(runID string) (string, error) {
	policy, err := s.ReadRunPolicy(runID)
	if err != nil {
		return "", err
	}
	return policy.Worktree, nil
}

func (s *Store) UpdateRunWorktree(runID, worktree string) error {
	directory, err := s.executionDir(runID)
	if err != nil {
		return err
	}
	path := filepath.Join(directory, "policy.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrExecutionNotFound, path)
	}
	if err != nil {
		return err
	}
	var policy RunPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return fmt.Errorf("%w: %s", ErrPolicyCorrupt, path)
	}
	policy.Worktree = worktree
	updated, err := marshalRecord(policy)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, updated, 0600); err != nil {
		return err
	}
	return nil
}

func (s *Store) ReadRunReason(runID string) (string, error) {
	directory, err := s.executionDir(runID)
	if err != nil {
		return "", err
	}
	path := filepath.Join(directory, "events.jsonl")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: %s", ErrExecutionNotFound, path)
	}
	if err != nil {
		return "", err
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) == 0 || len(lines[0]) == 0 {
		return "", nil
	}
	var last map[string]any
	if err := json.Unmarshal(lines[len(lines)-1], &last); err != nil {
		return "", nil
	}
	if v, ok := last["outcome_error"].(string); ok {
		return v, nil
	}
	if v, ok := last["error"].(string); ok {
		return v, nil
	}
	return "", nil
}

func requestFor(job agentrun.LogicalJob) ExecutionRequest {
	request := job.Request()
	capabilityIDs := make([]string, 0, len(request.Capabilities()))
	for _, capability := range request.Capabilities() {
		capabilityIDs = append(capabilityIDs, string(capability.Identity()))
	}
	sort.Strings(capabilityIDs)
	return ExecutionRequest{
		RunID:         string(job.RunID()),
		JobID:         string(job.ID()),
		RequestID:     string(request.Identity()),
		CandidateID:   string(request.Candidate().Identity()),
		PromptID:      string(request.Prompt().Identity()),
		CapabilityIDs: capabilityIDs,
	}
}

func marshalRecord(value any) ([]byte, error) {
	return json.MarshalIndent(value, "", "  ")
}

func checkImmutableRecord(path string, expected []byte) error {
	actual, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if bytes.Equal(actual, expected) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrImmutableConflict, path)
}

func writeImmutableRecord(path string, expected []byte) error {
	actual, err := os.ReadFile(path)
	if err == nil {
		if bytes.Equal(actual, expected) {
			return nil
		}
		return fmt.Errorf("%w: %s", ErrImmutableConflict, path)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return atomicWrite(path, expected)
}

func ensureEventLog(directory string) error {
	path := filepath.Join(directory, "events.jsonl")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		return file.Close()
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("store: event log path is not a regular file: %s", path)
	}
	return nil
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err == nil {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(temporaryPath, path)
}
