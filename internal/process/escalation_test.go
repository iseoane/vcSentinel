package process

import (
	"testing"
	"time"
)

// fakeEscalationClock replaces time.After with one manually pulsed channel,
// so unit tests drive grace and final-budget expiry deterministically.
type fakeEscalationClock struct {
	pulses chan time.Time
}

func newFakeEscalationClock() *fakeEscalationClock {
	return &fakeEscalationClock{pulses: make(chan time.Time, 4)}
}

func (f *fakeEscalationClock) After(time.Duration) <-chan time.Time { return f.pulses }

func (f *fakeEscalationClock) pulse() { f.pulses <- time.Now() }

type escalationRecorder struct {
	terminations int
	attempted    int
	reaped       int
	attemptedHit chan struct{}
	terminated   chan struct{}
	reapedHit    chan struct{}
}

func newEscalationRecorder() *escalationRecorder {
	return &escalationRecorder{
		attemptedHit: make(chan struct{}, 4),
		terminated:   make(chan struct{}, 4),
		reapedHit:    make(chan struct{}, 4),
	}
}

func escalatorFor(rec *escalationRecorder, clock *fakeEscalationClock, exit <-chan struct{}) Escalator {
	return Escalator{
		Grace:       50 * time.Millisecond,
		FinalBudget: 50 * time.Millisecond,
		Exit:        exit,
		After:       clock.After,
		Terminate: func() error {
			rec.terminations++
			rec.terminated <- struct{}{}
			return nil
		},
		Hooks: EscalationHooks{
			OnTerminateAttempted: func(time.Time) {
				rec.attempted++
				rec.attemptedHit <- struct{}{}
			},
			OnReaped: func(time.Time) {
				rec.reaped++
				rec.reapedHit <- struct{}{}
			},
		},
	}
}

// TestRunEscalationCooperativeExitNeedsNoEvidence proves a tree exiting inside
// the grace window resolves cooperatively: no terminate call and no evidence
// hooks fire, because existing successful paths write no new events.
func TestRunEscalationCooperativeExitNeedsNoEvidence(t *testing.T) {
	exit := make(chan struct{})
	close(exit)
	rec := newEscalationRecorder()
	clock := newFakeEscalationClock()

	outcome := RunEscalation(escalatorFor(rec, clock, exit))

	if outcome != OutcomeCooperative {
		t.Fatalf("outcome = %d, want cooperative exit", outcome)
	}
	if rec.terminations != 0 || rec.attempted != 0 || rec.reaped != 0 {
		t.Fatalf("evidence recorded %+v; cooperative exit must stay silent", rec)
	}
}

// TestRunEscalationGraceExpiryTerminatesExactlyOnce proves grace expiry issues
// exactly one termination-attempted transition and exactly one Terminate call.
func TestRunEscalationGraceExpiryTerminatesExactlyOnce(t *testing.T) {
	exit := make(chan struct{})
	rec := newEscalationRecorder()
	clock := newFakeEscalationClock()

	outcomeCh := make(chan EscalationOutcome, 1)
	go func() { outcomeCh <- RunEscalation(escalatorFor(rec, clock, exit)) }()

	clock.pulse()
	<-rec.attemptedHit
	<-rec.terminated
	if rec.terminations != 1 || rec.attempted != 1 {
		t.Fatalf("after grace expiry: terminations=%d attempted=%d, want exactly one of each", rec.terminations, rec.attempted)
	}

	close(exit)
	select {
	case outcome := <-outcomeCh:
		if outcome != OutcomeReaped {
			t.Fatalf("outcome = %d, want reaped after confirmed exit", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("escalation did not resolve after exit confirmation")
	}
	if rec.reaped != 1 {
		t.Fatalf("reaped hook fired %d times, want exactly once", rec.reaped)
	}
}

// TestRunEscalationUnconfirmableReapIsOrphanedExactlyOnce proves that when
// exit cannot be confirmed within the final budget the attempt resolves
// orphaned with no reaped evidence ever appended.
func TestRunEscalationUnconfirmableReapIsOrphanedExactlyOnce(t *testing.T) {
	exit := make(chan struct{})
	defer close(exit)
	rec := newEscalationRecorder()
	clock := newFakeEscalationClock()

	outcomeCh := make(chan EscalationOutcome, 1)
	go func() { outcomeCh <- RunEscalation(escalatorFor(rec, clock, exit)) }()

	clock.pulse()
	<-rec.attemptedHit
	<-rec.terminated
	clock.pulse()

	select {
	case outcome := <-outcomeCh:
		if outcome != OutcomeOrphaned {
			t.Fatalf("outcome = %d, want orphaned after unconfirmed final budget", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("escalation did not resolve orphaned after final budget")
	}
	if rec.reaped != 0 {
		t.Fatalf("reaped evidence appended %d times; an unconfirmable reap must never claim success", rec.reaped)
	}
}
