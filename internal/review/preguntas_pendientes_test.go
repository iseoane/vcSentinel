package review

import (
	"errors"
	"testing"
)

func TestSplitPendingQuestions(t *testing.T) {
	resolveOK := func(blobPorArchivo map[string]string) ResolveBlob {
		return func(file string) (string, error) {
			blob, ok := blobPorArchivo[file]
			if !ok {
				return "", errors.New("archivo sin blob de prueba: " + file)
			}
			return blob, nil
		}
	}
	knownFrom := func(respuestas map[string]string) KnownAnswer {
		return func(blob, questionID string) (string, bool, error) {
			answer, ok := respuestas[blob+"|"+questionID]
			return answer, ok, nil
		}
	}

	t.Run("question without File always stays pending", func(t *testing.T) {
		questions := []AgentQuestion{{ID: "q1", Text: "sin archivo"}}
		pending, known, err := SplitPendingQuestions(questions,
			resolveOK(nil), knownFrom(map[string]string{"blobX|q1": "respuesta"}))
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if len(pending) != 1 || pending[0].ID != "q1" {
			t.Fatalf("pending = %+v, want [q1]", pending)
		}
		if len(known) != 0 {
			t.Fatalf("known = %+v, want empty (never resolved by blob)", known)
		}
	})

	t.Run("question with a known answer for its blob is excluded from pending", func(t *testing.T) {
		questions := []AgentQuestion{{ID: "q1", Text: "usa camelCase?", File: "a.go"}}
		pending, known, err := SplitPendingQuestions(questions,
			resolveOK(map[string]string{"a.go": "blobA"}),
			knownFrom(map[string]string{"blobA|q1": "sí, camelCase"}))
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if len(pending) != 0 {
			t.Fatalf("pending = %+v, want empty", pending)
		}
		if known["q1"] != "sí, camelCase" {
			t.Fatalf("known[q1] = %q, want %q", known["q1"], "sí, camelCase")
		}
	})

	t.Run("question with no known answer for its blob stays pending", func(t *testing.T) {
		questions := []AgentQuestion{{ID: "q1", Text: "usa camelCase?", File: "a.go"}}
		pending, known, err := SplitPendingQuestions(questions,
			resolveOK(map[string]string{"a.go": "blobA"}),
			knownFrom(nil))
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if len(pending) != 1 || pending[0].ID != "q1" {
			t.Fatalf("pending = %+v, want [q1]", pending)
		}
		if len(known) != 0 {
			t.Fatalf("known = %+v, want empty", known)
		}
	})

	t.Run("resolveBlob error treats the question as pending, never blocks", func(t *testing.T) {
		questions := []AgentQuestion{{ID: "q1", Text: "usa camelCase?", File: "missing.go"}}
		pending, known, err := SplitPendingQuestions(questions,
			resolveOK(nil), // missing.go has no entry -> resolveBlob returns an error
			knownFrom(nil))
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if len(pending) != 1 || pending[0].ID != "q1" {
			t.Fatalf("pending = %+v, want [q1]", pending)
		}
		if len(known) != 0 {
			t.Fatalf("known = %+v, want empty", known)
		}
	})

	t.Run("knownAnswer error propagates as the function's error", func(t *testing.T) {
		wantErr := errors.New("store unavailable")
		failingKnown := func(blob, questionID string) (string, bool, error) {
			return "", false, wantErr
		}
		questions := []AgentQuestion{{ID: "q1", Text: "usa camelCase?", File: "a.go"}}
		pending, known, err := SplitPendingQuestions(questions,
			resolveOK(map[string]string{"a.go": "blobA"}), failingKnown)
		if !errors.Is(err, wantErr) {
			t.Fatalf("err = %v, want %v", err, wantErr)
		}
		if pending != nil || known != nil {
			t.Fatalf("pending/known = %+v/%+v, want nil/nil on error", pending, known)
		}
	})
}
