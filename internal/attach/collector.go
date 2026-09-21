package attach

import "github.com/ISeoane-Quental/vcSentinel/internal/store"

// ReplayCollector accumulates a run's event stream from paged Subscribe
// deliveries under one exclusive cursor. ApplyPage appends only frames
// strictly after the cursor and advances it, so re-delivered pages are
// idempotent and a reconnect resumes exactly where the previous exchange
// stopped — nothing is duplicated or skipped on the collector side.
type ReplayCollector struct {
	cursor uint64
	frames []store.EventFrame
}

// NewReplayCollector starts an empty collector whose cursor sits at
// afterCursor: every frame with sequence <= afterCursor is considered
// already observed.
func NewReplayCollector(afterCursor uint64) *ReplayCollector {
	return &ReplayCollector{cursor: afterCursor}
}

// ApplyPage applies one delivered page and reports how many frames were new.
//
// Out-of-order delivery policy (deliberately the simplest deterministic one):
// frames apply in arrival order, the cursor never regresses, and any frame
// at or below the current cursor is dropped without buffering. A late page
// carrying sequences below the cursor is therefore skipped entirely and the
// next poll resumes from the cursor; no reorder window is kept.
func (c *ReplayCollector) ApplyPage(page store.EventPage) int {
	applied := 0
	for _, frame := range page.Events {
		if frame.Sequence <= c.cursor {
			continue // duplicate or late delivery below the resume point
		}
		c.frames = append(c.frames, frame)
		c.cursor = frame.Sequence
		applied++
	}
	return applied
}

// Frames returns a copy of every applied frame in application order, so
// callers cannot mutate the collector's accumulated stream.
func (c *ReplayCollector) Frames() []store.EventFrame {
	return append([]store.EventFrame(nil), c.frames...)
}

// Cursor returns the last applied event sequence; the next Subscribe request
// must use it as its exclusive AfterCursor.
func (c *ReplayCollector) Cursor() uint64 { return c.cursor }
