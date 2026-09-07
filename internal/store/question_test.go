package store

import "testing"

// TestRecordedAnswer_SameBlobSameQuestion_NotAskedAgain covers the T7.5
// acceptance criterion: the same question about the same blob is not asked
// again in a second run (simulated here as a second call to RecordedAnswer
// on the same Store).
func TestRecordedAnswer_SameBlobSameQuestion_NotAskedAgain(t *testing.T) {
	gitCommonDir := t.TempDir()
	s := NewStore(gitCommonDir)

	if err := s.RecordAnswer("blob123", "q1", "yes, intentional", "iseoane"); err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}

	// First "run": the answer was already recorded.
	answer, ok, err := s.RecordedAnswer("blob123", "q1")
	if err != nil {
		t.Fatalf("RecordedAnswer: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true: the answer was already recorded")
	}
	if answer != "yes, intentional" {
		t.Errorf("answer = %q, want %q", answer, "yes, intentional")
	}

	// Second "run" (simulated with a new Store over the same git common
	// dir, as would happen between two separate process invocations): it
	// must keep finding the same answer without asking again.
	s2 := NewStore(gitCommonDir)
	answer2, ok2, err := s2.RecordedAnswer("blob123", "q1")
	if err != nil {
		t.Fatalf("RecordedAnswer (second run): %v", err)
	}
	if !ok2 || answer2 != answer {
		t.Errorf("second run: ok=%v answer=%q, want ok=true answer=%q", ok2, answer2, answer)
	}
}

// TestRecordedAnswer_DifferentBlob_NotFound confirms that the key includes
// the blob: the same questionID over a different blob must not be found.
func TestRecordedAnswer_DifferentBlob_NotFound(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.RecordAnswer("blob123", "q1", "answer", "iseoane"); err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}

	_, ok, err := s.RecordedAnswer("another-blob", "q1")
	if err != nil {
		t.Fatalf("RecordedAnswer: %v", err)
	}
	if ok {
		t.Error("ok = true, want false: the blob is different")
	}
}

// TestRecordedAnswer_DifferentQuestionID_NotFound confirms that the key
// includes questionID: the same question over the same blob but with a
// different questionID must not be found.
func TestRecordedAnswer_DifferentQuestionID_NotFound(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.RecordAnswer("blob123", "q1", "answer", "iseoane"); err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}

	_, ok, err := s.RecordedAnswer("blob123", "q2")
	if err != nil {
		t.Fatalf("RecordedAnswer: %v", err)
	}
	if ok {
		t.Error("ok = true, want false: the questionID is different")
	}
}

// TestAnswerKey_NoCollisionWithEmbeddedSeparator pins the real fix of this
// round: before it, answerKey concatenated "blob:%s#question:%s" without
// escaping, so a blob and a questionID that already contained the separator
// could collide across distinct pairs. If this test is reverted together
// with answerKey to that simple concatenation, it must fail: both pairs
// literally produced the same string "blob:X#question:q1#question:q2".
func TestAnswerKey_NoCollisionWithEmbeddedSeparator(t *testing.T) {
	a := answerKey("X#question:q1", "q2")
	b := answerKey("X", "q1#question:q2")
	if a == b {
		t.Fatalf("answerKey collides: (%q,%q) and (%q,%q) produce the same key %q",
			"X#question:q1", "q2", "X", "q1#question:q2", a)
	}
}

// TestRecordedAnswer_ComponentsWithEmbeddedSeparator_NoCollision is the
// same collision but through the public API: recording an answer for one
// (blob, questionID) pair must not become visible for a DIFFERENT pair
// whose naive concatenation would coincide with the first one's.
func TestRecordedAnswer_ComponentsWithEmbeddedSeparator_NoCollision(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.RecordAnswer("X#question:q1", "q2", "answer of the first pair", "iseoane"); err != nil {
		t.Fatalf("RecordAnswer: %v", err)
	}

	_, ok, err := s.RecordedAnswer("X", "q1#question:q2")
	if err != nil {
		t.Fatalf("RecordedAnswer: %v", err)
	}
	if ok {
		t.Error("ok = true, want false: it is a different (blob, questionID) pair, it must not find the other pair's answer")
	}
}

// TestRecordedAnswer_TwoAnswers_ReturnsMostRecent confirms the
// RecordedAnswer comment: if the same (blob, questionID) was answered more
// than once (decisions.jsonl is append-only and does not prevent it), the
// most recent answer is returned, not the first one.
func TestRecordedAnswer_TwoAnswers_ReturnsMostRecent(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.RecordAnswer("blob123", "q1", "first answer", "iseoane"); err != nil {
		t.Fatalf("RecordAnswer (first): %v", err)
	}
	if err := s.RecordAnswer("blob123", "q1", "second answer", "iseoane"); err != nil {
		t.Fatalf("RecordAnswer (second): %v", err)
	}

	answer, ok, err := s.RecordedAnswer("blob123", "q1")
	if err != nil {
		t.Fatalf("RecordedAnswer: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if answer != "second answer" {
		t.Errorf("answer = %q, want the most recent %q", answer, "second answer")
	}
}

// TestRecordedAnswer_NoPreviousRecord_OkFalseNoError confirms the
// cold-start case: no answer recorded yet is not an error.
func TestRecordedAnswer_NoPreviousRecord_OkFalseNoError(t *testing.T) {
	s := NewStore(t.TempDir())

	answer, ok, err := s.RecordedAnswer("new-blob", "new-question")
	if err != nil {
		t.Fatalf("RecordedAnswer: %v", err)
	}
	if ok {
		t.Error("ok = true, want false: there is no recorded answer yet")
	}
	if answer != "" {
		t.Errorf("answer = %q, want empty", answer)
	}
}
