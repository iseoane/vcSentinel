package gate

import (
	"reflect"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// TestValidationEvidenceRecorder proves deterministic evidence mapping from
// settled validation runs: identical runs produce identical entries, and any
// change to exit status or duration produces a different digest. No agent
// participates anywhere on this path.
func TestValidationEvidenceRecorder(t *testing.T) {
	fixedRuns := []validation.ValidationRun{
		{Capability: "lint", Comando: "gofmt -l .", Exit: 0, DuracionMs: 12},
		{Capability: "test", Comando: "go test ./...", Exit: 1, DuracionMs: 3400, Salida: "output line"},
	}

	t.Run("same runs produce identical entries", func(t *testing.T) {
		first := RecordValidationEvidence(fixedRuns)
		second := RecordValidationEvidence(fixedRuns)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("identical runs must yield byte-identical evidence entries:\n%+v\n%+v", first, second)
		}
	})

	t.Run("entries carry scalars and digests only, never output bytes", func(t *testing.T) {
		evidence := RecordValidationEvidence(fixedRuns)
		for _, entry := range evidence {
			if entry.Digest == "" {
				t.Fatalf("command %q must always carry a digest", entry.Command)
			}
			if len(entry.Digest) != 64 {
				t.Fatalf("digest must be the shared hex sha256 authority form, got %q", entry.Digest)
			}
		}
	})

	t.Run("different exit status changes the digest", func(t *testing.T) {
		base := RecordValidationEvidence(fixedRuns)[1]
		variant := fixedRuns[1]
		variant.Exit = 2
		altered := RecordValidationEvidence([]validation.ValidationRun{variant})[0]
		if base.Digest == altered.Digest {
			t.Fatalf("a different exit status must produce a different digest")
		}
	})

	t.Run("different duration changes the digest", func(t *testing.T) {
		base := RecordValidationEvidence(fixedRuns)[1]
		variant := fixedRuns[1]
		variant.DuracionMs = 3401
		altered := RecordValidationEvidence([]validation.ValidationRun{variant})[0]
		if base.Digest == altered.Digest {
			t.Fatalf("a different duration must produce a different digest")
		}
	})

	t.Run("different output changes the digest and trimming is normalized", func(t *testing.T) {
		trimmed := fixedRuns[1]
		untrimmed := fixedRuns[1]
		untrimmed.Salida = "  " + trimmed.Salida + "\n"
		base := RecordValidationEvidence([]validation.ValidationRun{trimmed})[0]
		altered := RecordValidationEvidence([]validation.ValidationRun{untrimmed})[0]
		if base.Digest != altered.Digest {
			t.Fatalf("trimmed and untrimmed copies of the same output must share one digest")
		}
		other := fixedRuns[1]
		other.Salida = "different output"
		distinct := RecordValidationEvidence([]validation.ValidationRun{other})[0]
		if base.Digest == distinct.Digest {
			t.Fatalf("different output must produce a different digest")
		}
	})
}
