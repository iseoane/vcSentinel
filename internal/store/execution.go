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

// RunPolicy identifies the policy used to create a durable execution.
// Policy contents are resolved elsewhere; only this stable identity is stored.
type RunPolicy struct {
	ID string `json:"id"`
}

type executionRequest struct {
	RunID         string   `json:"run_id"`
	JobID         string   `json:"job_id"`
	RequestID     string   `json:"request_id"`
	CandidateID   string   `json:"candidate_id"`
	PromptID      string   `json:"prompt_id"`
	CapabilityIDs []string `json:"capability_ids,omitempty"`
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

	requestData, err := marshalRecord(requestFor(job))
	if err != nil {
		return err
	}
	policyData, err := marshalRecord(policy)
	if err != nil {
		return err
	}
	requestPath := filepath.Join(directory, "request.json")
	policyPath := filepath.Join(directory, "policy.json")

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
}

func (s *Store) executionDir(runID string) (string, error) {
	if !validRunID(runID) {
		return "", fmt.Errorf("store: invalid run id %q", runID)
	}
	return filepath.Join(s.dir, "executions", "v1", runID), nil
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

func requestFor(job agentrun.LogicalJob) executionRequest {
	request := job.Request()
	capabilityIDs := make([]string, 0, len(request.Capabilities()))
	for _, capability := range request.Capabilities() {
		capabilityIDs = append(capabilityIDs, string(capability.Identity()))
	}
	sort.Strings(capabilityIDs)
	return executionRequest{
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
