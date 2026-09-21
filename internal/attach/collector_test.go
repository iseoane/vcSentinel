package attach

import (
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

// Ticket 17 slice 1: the replay collector must apply pages strictly after its
// cursor, stay idempotent against duplicate delivery, advance across paged
// reads, and never regress on out-of-order arrivals. The chosen out-of-order
// policy is drop-and-wait (no reorder buffer): late frames at or below the
// cursor are skipped and the next poll resumes from the cursor.

func collectorFrame(seq uint64, invocation string) store.EventFrame {
	return store.EventFrame{
		Sequence: seq, Revision: seq,
		At:    time.Unix(1700000000+int64(seq), 0).UTC(),
		RunID: "run-1", JobID: "job-1",
		InvocationID: invocation, LineageID: "lineage-1",
		From: agentrun.StateRunning, To: agentrun.StateRunning,
	}
}

func sequences(frames []store.EventFrame) []uint64 {
	got := make([]uint64, 0, len(frames))
	for _, frame := range frames {
		got = append(got, frame.Sequence)
	}
	return got
}

func TestApplyPageAppliesOnlyFramesStrictlyAfterCursor(t *testing.T) {
	collector := NewReplayCollector(2)
	page := store.EventPage{Events: []store.EventFrame{
		collectorFrame(2, "inv-root"),
		collectorFrame(3, "inv-root"),
		collectorFrame(4, "inv-root"),
	}}
	if applied := collector.ApplyPage(page); applied != 2 {
		t.Fatalf("applied = %d, want exactly the two frames after cursor 2", applied)
	}
	if got := sequences(collector.Frames()); len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("frames = %v, want [3 4]", got)
	}
	if collector.Cursor() != 4 {
		t.Fatalf("cursor = %d, want 4", collector.Cursor())
	}
}

func TestApplyPageIsIdempotentAgainstDuplicateDelivery(t *testing.T) {
	collector := NewReplayCollector(0)
	page := store.EventPage{Events: []store.EventFrame{
		collectorFrame(1, "inv-root"),
		collectorFrame(2, "inv-root"),
	}}
	if applied := collector.ApplyPage(page); applied != 2 {
		t.Fatalf("first application = %d, want 2", applied)
	}
	for i := 0; i < 3; i++ {
		if applied := collector.ApplyPage(page); applied != 0 {
			t.Fatalf("duplicate delivery %d applied %d frames, want 0", i+1, applied)
		}
	}
	if got := sequences(collector.Frames()); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("frames = %v, want exactly [1 2] with no duplicates", got)
	}
	if collector.Cursor() != 2 {
		t.Fatalf("cursor = %d, want 2", collector.Cursor())
	}
}

func TestPagedReadAdvancesCursorAcrossPages(t *testing.T) {
	collector := NewReplayCollector(0)
	first := store.EventPage{
		Events:  []store.EventFrame{collectorFrame(1, "inv-root"), collectorFrame(2, "inv-root")},
		HasMore: true, NextSequence: 2, NextRevision: 2,
	}
	if applied := collector.ApplyPage(first); applied != 2 || collector.Cursor() != 2 {
		t.Fatalf("first page: applied = %d, cursor = %d, want 2 and 2", applied, collector.Cursor())
	}
	second := store.EventPage{Events: []store.EventFrame{
		collectorFrame(3, "inv-root"), collectorFrame(4, "inv-root"),
	}}
	if applied := collector.ApplyPage(second); applied != 2 || collector.Cursor() != 4 {
		t.Fatalf("second page: applied = %d, cursor = %d, want 2 and 4", applied, collector.Cursor())
	}
	if got := sequences(collector.Frames()); len(got) != 4 {
		t.Fatalf("frames = %v, want the gap-free concatenation of both pages", got)
	}
	// Replaying the exhausted tail page changes nothing.
	if applied := collector.ApplyPage(second); applied != 0 {
		t.Fatalf("tail replay applied %d frames, want 0", applied)
	}
}

func TestOutOfOrderDeliveryDropsLateFramesAndNeverRegressesCursor(t *testing.T) {
	collector := NewReplayCollector(0)
	ahead := store.EventPage{Events: []store.EventFrame{
		collectorFrame(5, "inv-root"), collectorFrame(6, "inv-root"),
	}}
	if applied := collector.ApplyPage(ahead); applied != 2 || collector.Cursor() != 6 {
		t.Fatalf("ahead page: applied = %d, cursor = %d, want 2 and 6", applied, collector.Cursor())
	}
	late := store.EventPage{Events: []store.EventFrame{collectorFrame(4, "inv-root")}}
	if applied := collector.ApplyPage(late); applied != 0 {
		t.Fatalf("late frame applied = %d, want 0 under the drop-and-wait policy", applied)
	}
	if collector.Cursor() != 6 {
		t.Fatalf("cursor regressed to %d, want 6", collector.Cursor())
	}
	next := store.EventPage{Events: []store.EventFrame{collectorFrame(7, "inv-root")}}
	if applied := collector.ApplyPage(next); applied != 1 || collector.Cursor() != 7 {
		t.Fatalf("next page: applied = %d, cursor = %d, want 1 and 7", applied, collector.Cursor())
	}
}

func TestFramesReturnsACopyTheCallerCannotMutateThrough(t *testing.T) {
	collector := NewReplayCollector(0)
	if applied := collector.ApplyPage(store.EventPage{Events: []store.EventFrame{collectorFrame(1, "inv-root")}}); applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}
	frames := collector.Frames()
	frames[0].InvocationID = "tampered"
	if got := collector.Frames()[0].InvocationID; got != "inv-root" {
		t.Fatalf("caller mutation leaked into the collector (got %q)", got)
	}
}
