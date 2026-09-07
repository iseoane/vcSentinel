package review

import (
	"errors"
	"testing"
)

func TestSplitPendingQuestions(t *testing.T) {
	resolveOK := func(blobByFile map[string]string) ResolveBlob {
		return func(file string) (string, error) {
			blob, ok := blobByFile[file]
			if !ok {
				return "", errors.New("fixture file without a test blob: " + file)
			}
			return blob, nil
		}
	}
	knownFrom := func(answers map[string]string) KnownAnswer {
		return func(blob, questionID string) (string, bool, error) {
			answer, ok := answers[blob+"|"+questionID]
			return answer, ok, nil
		}
	}

	t.Run("question without File always stays pending", func(t *testing.T) {
		// resolveBlob must never be called with file == "": if the
		// q.File == "" guard were removed, this test must fail instead of
		// slipping silently through the resolveBlob error branch (which is
		// indistinguishable from this case for SplitPendingQuestions itself).
		resolverRejectsEmptyFile := func(file string) (string, error) {
			if file == "" {
				t.Fatalf("resolveBlob should not be called with an empty file: the q.File == \"\" guard must cut first")
			}
			return "", errors.New("fixture file without a test blob: " + file)
		}
		questions := []AgentQuestion{{ID: "q1", Text: "sin archivo"}}
		pending, answered, err := SplitPendingQuestions(questions,
			resolverRejectsEmptyFile, knownFrom(map[string]string{"blobX|q1": "respuesta"}))
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if len(pending) != 1 || pending[0].ID != "q1" {
			t.Fatalf("pending = %+v, want [q1]", pending)
		}
		if len(answered) != 0 {
			t.Fatalf("answered = %+v, want empty (never resolved by blob)", answered)
		}
	})

	t.Run("question with a known answer for its blob is excluded from pending", func(t *testing.T) {
		questions := []AgentQuestion{{ID: "q1", Text: "usa camelCase?", File: "a.go"}}
		pending, answered, err := SplitPendingQuestions(questions,
			resolveOK(map[string]string{"a.go": "blobA"}),
			knownFrom(map[string]string{"blobA|q1": "yes, camelCase"}))
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if len(pending) != 0 {
			t.Fatalf("pending = %+v, want empty", pending)
		}
		if len(answered) != 1 || answered[0].Question.ID != "q1" || answered[0].Answer != "yes, camelCase" {
			t.Fatalf("answered = %+v, want [{q1: %q}]", answered, "yes, camelCase")
		}
	})

	t.Run("question with no known answer for its blob stays pending", func(t *testing.T) {
		questions := []AgentQuestion{{ID: "q1", Text: "usa camelCase?", File: "a.go"}}
		pending, answered, err := SplitPendingQuestions(questions,
			resolveOK(map[string]string{"a.go": "blobA"}),
			knownFrom(nil))
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if len(pending) != 1 || pending[0].ID != "q1" {
			t.Fatalf("pending = %+v, want [q1]", pending)
		}
		if len(answered) != 0 {
			t.Fatalf("answered = %+v, want empty", answered)
		}
	})

	t.Run("resolveBlob error treats the question as pending, never blocks", func(t *testing.T) {
		questions := []AgentQuestion{{ID: "q1", Text: "usa camelCase?", File: "missing.go"}}
		pending, answered, err := SplitPendingQuestions(questions,
			resolveOK(nil), // missing.go has no entry -> resolveBlob returns an error
			knownFrom(nil))
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if len(pending) != 1 || pending[0].ID != "q1" {
			t.Fatalf("pending = %+v, want [q1]", pending)
		}
		if len(answered) != 0 {
			t.Fatalf("answered = %+v, want empty", answered)
		}
	})

	t.Run("knownAnswer error propagates as the function's error", func(t *testing.T) {
		wantErr := errors.New("store unavailable")
		failingKnown := func(blob, questionID string) (string, bool, error) {
			return "", false, wantErr
		}
		questions := []AgentQuestion{{ID: "q1", Text: "usa camelCase?", File: "a.go"}}
		pending, answered, err := SplitPendingQuestions(questions,
			resolveOK(map[string]string{"a.go": "blobA"}), failingKnown)
		if !errors.Is(err, wantErr) {
			t.Fatalf("err = %v, want %v", err, wantErr)
		}
		if pending != nil || answered != nil {
			t.Fatalf("pending/answered = %+v/%+v, want nil/nil on error", pending, answered)
		}
	})

	// TestSplitPendingQuestions/same_ID_across_multiple_files is the literal
	// regression test for the id-collision data-loss bug: before the fix,
	// the second return value was a bare map[string]string keyed by
	// AgentQuestion.ID alone, so two different questions sharing an ID (no
	// cross-dimension uniqueness guarantee) but about different files/blobs
	// collapsed into a single map entry — only the last one processed
	// survived, silently dropping the other's answer.
	t.Run("same ID across multiple files mixes pending and answered without data loss", func(t *testing.T) {
		questions := []AgentQuestion{
			{ID: "q1", Text: "¿en a.go?", File: "a.go"},
			{ID: "q1", Text: "¿en b.go?", File: "b.go"},
			{ID: "q1", Text: "¿en c.go?", File: "c.go"},
		}
		pending, answered, err := SplitPendingQuestions(questions,
			resolveOK(map[string]string{"a.go": "blobA", "b.go": "blobB", "c.go": "blobC"}),
			knownFrom(map[string]string{
				"blobA|q1": "answer for a.go",
				"blobB|q1": "answer for b.go",
				// c.go deliberately has no recorded answer: it must stay
				// pending even though it shares ID "q1" with the other two.
			}))
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if len(pending) != 1 || pending[0].File != "c.go" {
			t.Fatalf("pending = %+v, want only [q1 about c.go]", pending)
		}
		if len(answered) != 2 {
			t.Fatalf("answered = %+v, want 2: before the fix, a map[string]string keyed by ID collapsed both answers into one", answered)
		}
		byFile := map[string]string{}
		for _, a := range answered {
			byFile[a.Question.File] = a.Answer
		}
		if byFile["a.go"] != "answer for a.go" || byFile["b.go"] != "answer for b.go" {
			t.Fatalf("answered = %+v, want both answers preserved per file/blob, not collapsed by shared ID", answered)
		}
	})
}
