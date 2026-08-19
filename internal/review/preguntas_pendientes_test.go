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
		// resolveBlob nunca debe llamarse con file == "": si el guard
		// q.File == "" se eliminara, esta prueba debe fallar en vez de
		// colar silenciosamente por la rama de error de resolveBlob (que es
		// indistinguible de este caso para el propio SplitPendingQuestions).
		resolverSinLlamadaVacia := func(file string) (string, error) {
			if file == "" {
				t.Fatalf("resolveBlob no debería llamarse con file vacío: el guard q.File == \"\" debe cortar antes")
			}
			return "", errors.New("archivo sin blob de prueba: " + file)
		}
		questions := []AgentQuestion{{ID: "q1", Text: "sin archivo"}}
		pending, answered, err := SplitPendingQuestions(questions,
			resolverSinLlamadaVacia, knownFrom(map[string]string{"blobX|q1": "respuesta"}))
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
			knownFrom(map[string]string{"blobA|q1": "sí, camelCase"}))
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if len(pending) != 0 {
			t.Fatalf("pending = %+v, want empty", pending)
		}
		if len(answered) != 1 || answered[0].Question.ID != "q1" || answered[0].Answer != "sí, camelCase" {
			t.Fatalf("answered = %+v, want [{q1: %q}]", answered, "sí, camelCase")
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
				"blobA|q1": "respuesta para a.go",
				"blobB|q1": "respuesta para b.go",
				// c.go deliberadamente sin respuesta registrada: debe seguir
				// pendiente aunque comparta ID "q1" con las otras dos.
			}))
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if len(pending) != 1 || pending[0].File != "c.go" {
			t.Fatalf("pending = %+v, want solo [q1 sobre c.go]", pending)
		}
		if len(answered) != 2 {
			t.Fatalf("answered = %+v, want 2: antes del fix, un map[string]string por ID colapsaba ambas respuestas en una sola", answered)
		}
		porArchivo := map[string]string{}
		for _, a := range answered {
			porArchivo[a.Question.File] = a.Answer
		}
		if porArchivo["a.go"] != "respuesta para a.go" || porArchivo["b.go"] != "respuesta para b.go" {
			t.Fatalf("answered = %+v, want ambas respuestas preservadas por archivo/blob, no colapsadas por ID compartido", answered)
		}
	})
}
