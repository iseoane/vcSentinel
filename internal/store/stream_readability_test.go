// Historical-readability contract for ticket 13 (R11): after the switch
// removal, streams written by pre-R9 binaries must remain fully inspectable.
// The fixture hand-seeds the exact on-disk shape an older binary left behind
// — immutable admission records (request.json/policy.json), a hash-chained
// events.jsonl containing ONLY its terminal frame (no transient states), and
// one admitted AttemptOutcome — and proves every current reader consumes it
// without conversion or repair.
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

const legacyRunID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// seedLegacyRunDirectory writes the minimal pre-R9 stream shape directly to
// disk: admission records plus exactly one terminal event frame and its
// attempt outcome. Nothing transient is present because the historical run
// had already settled when it was written.
func seedLegacyRunDirectory(t *testing.T, root string) {
	t.Helper()
	directory := filepath.Join(root, "executions", "v1", legacyRunID)
	if err := os.MkdirAll(filepath.Join(directory, "outcomes"), 0o700); err != nil {
		t.Fatalf("mkdir run directory: %v", err)
	}

	request := ExecutionRequest{
		RunID:       legacyRunID,
		JobID:       "job-legacy-1",
		RequestID:   "req-legacy-1",
		CandidateID: "cand-legacy-1",
		PromptID:    "prompt-legacy-1",
	}
	writeLegacyRecord(t, filepath.Join(directory, "request.json"), request)
	policy := RunPolicy{ID: "policy:review"}
	writeLegacyRecord(t, filepath.Join(directory, "policy.json"), policy)

	at := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	// A settled pre-R9 review run: the full lifecycle chain from creation to
	// its single terminal frame, with no transient tail (no awaiting-decision
	// or retry state left open). Frames are hash-chained exactly like the
	// append-only writer chains them.
	lifecycle := []struct {
		from     agentrun.LifecycleState
		to       agentrun.LifecycleState
		terminal agentrun.TerminalClass
	}{
		{agentrun.StateCreated, agentrun.StateQueued, agentrun.TerminalNone},
		{agentrun.StateQueued, agentrun.StateAdmitted, agentrun.TerminalNone},
		{agentrun.StateAdmitted, agentrun.StateRunning, agentrun.TerminalNone},
		{agentrun.StateRunning, agentrun.StateSucceeded, agentrun.TerminalSuccess},
	}
	var frames []EventFrame
	predecessor := ""
	for index, step := range lifecycle {
		frame := EventFrame{
			Sequence:        uint64(index + 1),
			Revision:        uint64(index + 1),
			At:              at,
			RunID:           legacyRunID,
			JobID:           request.JobID,
			InvocationID:    "inv-legacy-1",
			LineageID:       "lin-legacy-1",
			PredecessorHash: predecessor,
			From:            step.from,
			To:              step.to,
			Terminal:        step.terminal,
		}
		if step.terminal != agentrun.TerminalNone {
			frame.OutcomeClass = agentrun.OutcomeSuccess
			frame.OutputHash = "deadbeef"
		}
		frame.ContentHash = hashEventContent(frame.content())
		frames = append(frames, frame)
		predecessor = frame.ContentHash
	}
	writeLegacyJSONL(t, filepath.Join(directory, "events.jsonl"), frames)

	outcome := AttemptOutcome{
		RunID:        legacyRunID,
		JobID:        request.JobID,
		InvocationID: "inv-legacy-1",
		LineageID:    "lin-legacy-1",
		Class:        agentrun.OutcomeSuccess,
		OutputHash:   "deadbeef",
		At:           at,
	}
	writeLegacyRecord(t, filepath.Join(directory, "outcomes", "inv-legacy-1.json"), outcome)
}

func writeLegacyRecord(t *testing.T, path string, record any) {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeLegacyJSONL(t *testing.T, path string, frames []EventFrame) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, frame := range frames {
		if err := encoder.Encode(frame); err != nil {
			t.Fatalf("encode event frame: %v", err)
		}
	}
}

// TestPreR9StreamRemainsReadable pins reader compatibility over the seeded
// historical stream: listing, admission-record reads, validated event pages,
// attempt outcomes, and the execution projection all resolve from the store
// contents alone. The removal changed no persisted format, so this shape
// keeps reading forever.
func TestPreR9StreamRemainsReadable(t *testing.T) {
	commonDir := t.TempDir()
	root := filepath.Join(commonDir, "vas-sentinel")
	seedLegacyRunDirectory(t, root)
	st := NewStore(commonDir)

	ids, err := st.ListExecutionIDs()
	if err != nil || len(ids) != 1 || ids[0] != legacyRunID {
		t.Fatalf("ListExecutionIDs() = %v/%v, want exactly [%s]", ids, err, legacyRunID)
	}

	request, err := st.ReadExecutionRequest(legacyRunID)
	if err != nil {
		t.Fatalf("ReadExecutionRequest(%s) error = %v", legacyRunID, err)
	}
	if request.RunID != legacyRunID || request.ParentRunID != "" {
		t.Fatalf("request = %+v, want the parentless legacy admission identity", request)
	}

	page, err := st.ReadEvents(legacyRunID, 0, 128)
	if err != nil {
		t.Fatalf("ReadEvents error = %v", err)
	}
	if len(page.Events) != 4 {
		t.Fatalf("event count = %d, want the full settled lifecycle chain", len(page.Events))
	}
	terminal := page.Events[len(page.Events)-1]
	if terminal.From != agentrun.StateRunning || terminal.To != agentrun.StateSucceeded || terminal.Terminal != agentrun.TerminalSuccess {
		t.Fatalf("terminal frame = %+v, want running→succeeded with success class", terminal)
	}
	for _, frame := range page.Events {
		if frame.ContentHash == "" || frame.ContentHash != hashEventContent(frame.content()) {
			t.Fatal("historical stream failed its content-hash chain validation")
		}
	}

	outcomes, err := st.ReadAttemptOutcomes(legacyRunID)
	if err != nil || len(outcomes) != 1 {
		t.Fatalf("ReadAttemptOutcomes = %+v/%v, want the single admitted outcome", outcomes, err)
	}
	if outcomes[0].InvocationID != "inv-legacy-1" || outcomes[0].Class != agentrun.OutcomeSuccess || outcomes[0].OutputHash != "deadbeef" {
		t.Fatalf("outcome = %+v, want the persisted legacy invocation evidence", outcomes[0])
	}

	// The reconciled view is what operator tooling reads for runs written
	// before derived projection files existed: it rebuilds the terminal state
	// from the event stream alone and never writes anything back.
	projection, err := st.ReadReconciledProjection(legacyRunID)
	if err != nil {
		t.Fatalf("ReadReconciledProjection error = %v", err)
	}
	if projection.State != agentrun.StateSucceeded {
		t.Fatalf("reconciled state = %q, want succeeded", projection.State)
	}
}
