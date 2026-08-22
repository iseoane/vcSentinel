package store

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

// appendLegacySuccessStream writes a complete pre-R7 event stream using only
// the kinds that existed before cancellation ownership: three start-driven
// admissions and one complete-driven success.
func appendLegacySuccessStream(t *testing.T, s *Store, job agentrun.LogicalJob) []EventFrame {
	t.Helper()
	invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	transitions := []struct {
		from, to agentrun.LifecycleState
		decision agentrun.Decision
	}{
		{agentrun.StateCreated, agentrun.StateQueued, agentrun.DecisionStart},
		{agentrun.StateQueued, agentrun.StateAdmitted, agentrun.DecisionStart},
		{agentrun.StateAdmitted, agentrun.StateRunning, agentrun.DecisionStart},
		{agentrun.StateRunning, agentrun.StateSucceeded, agentrun.DecisionComplete},
	}
	frames := make([]EventFrame, 0, len(transitions))
	for _, transition := range transitions {
		event, err := agentrun.NewNormalizedEvent(invocation, transition.from, transition.to, transition.decision, time.Unix(1700000000, 0).UTC())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendEvent(string(job.RunID()), event, uint64(len(frames))); err != nil {
			t.Fatalf("AppendEvent(%s->%s) error = %v", transition.from, transition.to, err)
		}
		frames = append(frames, frameForEvent(event, uint64(len(frames)+1), uint64(len(frames)+1), predecessorHashForTest(frames)))
	}
	return frames
}

func predecessorHashForTest(previous []EventFrame) string {
	if len(previous) == 0 {
		return ""
	}
	return previous[len(previous)-1].ContentHash
}

// TestLegacyStreamWithoutCancellationEventsProjectsIdentically proves that
// streams written before cancellation ownership still scan, validate, and
// project exactly as they did: no new kind is required, and recomputing the
// projection from the frames reproduces the persisted state.json bytes.
func TestLegacyStreamWithoutCancellationEventsProjectsIdentically(t *testing.T) {
	s := NuevoStore(t.TempDir())
	job := testJob()
	if err := s.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	frames := appendLegacySuccessStream(t, s, job)

	directory, err := s.executionDir(string(job.RunID()))
	if err != nil {
		t.Fatal(err)
	}

	page, err := s.ReadEvents(string(job.RunID()), 0, len(frames))
	if err != nil {
		t.Fatalf("ReadEvents() on legacy stream = %v; old streams must keep validating", err)
	}
	if len(page.Events) != len(frames) {
		t.Fatalf("frames = %d, want %d", len(page.Events), len(frames))
	}
	for index, frame := range page.Events {
		if frame != frames[index] {
			t.Fatalf("frame %d = %+v, want %+v", index+1, frame, frames[index])
		}
		switch frame.Decision {
		case agentrun.DecisionStart, agentrun.DecisionComplete:
		default:
			t.Fatalf("legacy frame %d carries decision %q; old streams must not gain new kinds", frame.Sequence, frame.Decision)
		}
	}

	recomputed := projectionFor(string(job.RunID()), page.Events)
	recomputedBytes, err := marshalRecord(recomputed)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(persisted), bytes.TrimSpace(recomputedBytes)) {
		t.Fatalf("persisted state.json differs from recomputed legacy projection:\n%s\n%s", persisted, recomputedBytes)
	}

	storedProjection, err := s.ReadProjection(string(job.RunID()))
	if err != nil {
		t.Fatal(err)
	}
	if storedProjection.State != agentrun.StateSucceeded || storedProjection.Terminal != agentrun.TerminalSuccess ||
		storedProjection.LastEventHash == "" || storedProjection.Revision != uint64(len(frames)) {
		t.Fatalf("projection = %+v, want legacy succeeded terminal with full revision", storedProjection)
	}
}
