package review

import (
	"errors"
	"strings"
	"testing"
)

// falloRealDelGate es el texto que produjo un gate real: el timeout va
// delante y la causa —una herramienta del revisor que no existe en la
// máquina— aparece coloreada varias líneas más abajo.
const falloRealDelGate = "run restricted reviewer timed out after 10m0s: signal: terminated\n" +
	"context deadline exceeded: \x1b[0m\n" +
	"> reviewer · gpt-5.6-luna\n" +
	"\x1b[0m→ \x1b[0mRead internal/review/engine.go\x1b[90m [offset=1, limit=1040]\x1b[0m\n" +
	"\x1b[0m✗ \x1b[0mGrep \"FinalizeMetrics|ReviewEvidence\" failed\x1b[90m in internal/review/engine.go\x1b[0m\n" +
	"\x1b[91m\x1b[1mError: \x1b[0mripgrep execution failed\n" +
	"\x1b[0m✗ \x1b[0mGlob \"cmd/sentinel/autoria.go\" failed\x1b[90m in .\x1b[0m\n" +
	"\x1b[91m\x1b[1mError: \x1b[0mripgrep execution failed\n"

func TestProviderExecutionFailureLeadsWithTheProviderCause(t *testing.T) {
	failure := &ProviderExecutionFailure{Err: errors.New(falloRealDelGate)}
	got := failure.Error()

	if !strings.HasPrefix(got, prefijoCausaProveedor+"ripgrep execution failed") {
		t.Fatalf("reason = %q, want it to lead with the provider's own cause", got)
	}
	cabecera, traza, partido := strings.Cut(got, " | ")
	if !partido {
		t.Fatalf("reason = %q, want the cause separated from the retained trace", got)
	}
	// The stream printed the same failure twice; the lead states it once.
	if strings.Count(cabecera, "ripgrep execution failed") != 1 {
		t.Errorf("lead = %q, want the repeated cause stated once", cabecera)
	}
	if strings.Count(traza, "ripgrep execution failed") != 2 {
		t.Errorf("trace = %q, want both original occurrences retained", traza)
	}
	if !strings.Contains(got, "timed out after 10m0s") {
		t.Error("the original trace was dropped; it is evidence and must be retained behind the cause")
	}
}

func TestCausasDelProveedor(t *testing.T) {
	t.Run("deduplica y respeta el tope", func(t *testing.T) {
		texto := strings.Repeat("Error: same failure\n", 5) + "Error: another\nError: third\nError: fourth\n"
		causas := causasDelProveedor(texto)
		if len(causas) != maxCausasProveedor {
			t.Fatalf("causas = %v, want %d", causas, maxCausasProveedor)
		}
		if causas[0] != "same failure" || causas[1] != "another" {
			t.Errorf("causas = %v, want deduplicated in stream order", causas)
		}
	})

	t.Run("acota una causa desmesurada", func(t *testing.T) {
		causas := causasDelProveedor("Error: " + strings.Repeat("x", maxLongitudCausa*3))
		if len(causas) != 1 || len([]rune(causas[0])) != maxLongitudCausa+1 {
			t.Fatalf("causa de %d runas, want %d más el marcador de recorte", len([]rune(causas[0])), maxLongitudCausa)
		}
	})

	t.Run("un fallo sin causa impresa se deja intacto", func(t *testing.T) {
		mensaje := "exit status 1"
		if causas := causasDelProveedor(mensaje); len(causas) != 0 {
			t.Fatalf("causas = %v, want none", causas)
		}
		if got := razonConCausa(mensaje); got != mensaje {
			t.Fatalf("reason = %q, want it unchanged", got)
		}
	})

	t.Run("enriquecer es idempotente", func(t *testing.T) {
		una := razonConCausa(falloRealDelGate)
		if dos := razonConCausa(una); dos != una {
			t.Fatal("a second pass re-prefixed an already enriched reason")
		}
	})
}
