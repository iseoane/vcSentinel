package review

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

// realGateFailure is the text a real gate produced: the timeout comes first
// and the cause — a reviewer tool that does not exist on the machine —
// appears colored several lines below.
const realGateFailure = "run restricted reviewer timed out after 10m0s: signal: terminated\n" +
	"context deadline exceeded: \x1b[0m\n" +
	"> reviewer · gpt-5.6-luna\n" +
	"\x1b[0m→ \x1b[0mRead internal/review/engine.go\x1b[90m [offset=1, limit=1040]\x1b[0m\n" +
	"\x1b[0m✗ \x1b[0mGrep \"FinalizeMetrics|ReviewEvidence\" failed\x1b[90m in internal/review/engine.go\x1b[0m\n" +
	"\x1b[91m\x1b[1mError: \x1b[0mripgrep execution failed\n" +
	"\x1b[0m✗ \x1b[0mGlob \"cmd/vcsentinel/autoria.go\" failed\x1b[90m in .\x1b[0m\n" +
	"\x1b[91m\x1b[1mError: \x1b[0mripgrep execution failed\n"

func TestProviderExecutionFailureLeadsWithTheProviderCause(t *testing.T) {
	failure := &ProviderExecutionFailure{Err: errors.New(realGateFailure)}
	got := failure.Error()

	if !strings.HasPrefix(got, providerCausePrefix+"ripgrep execution failed") {
		t.Fatalf("reason = %q, want it to lead with the provider's own cause", got)
	}
	header, trace, split := strings.Cut(got, " | ")
	if !split {
		t.Fatalf("reason = %q, want the cause separated from the retained trace", got)
	}
	// The stream printed the same failure twice; the lead states it once.
	if strings.Count(header, "ripgrep execution failed") != 1 {
		t.Errorf("lead = %q, want the repeated cause stated once", header)
	}
	if strings.Count(trace, "ripgrep execution failed") != 2 {
		t.Errorf("trace = %q, want both original occurrences retained", trace)
	}
	if !strings.Contains(got, "timed out after 10m0s") {
		t.Error("the original trace was dropped; it is evidence and must be retained behind the cause")
	}
}

func TestProviderCauses(t *testing.T) {
	t.Run("deduplicates and respects the cap", func(t *testing.T) {
		text := strings.Repeat("Error: same failure\n", 5) + "Error: another\nError: third\nError: fourth\n"
		causes := providerCauses(text)
		if len(causes) != maxProviderCauses {
			t.Fatalf("causes = %v, want %d", causes, maxProviderCauses)
		}
		if causes[0] != "same failure" || causes[1] != "another" {
			t.Errorf("causes = %v, want deduplicated in stream order", causes)
		}
	})

	t.Run("clamps an oversized cause", func(t *testing.T) {
		causes := providerCauses("Error: " + strings.Repeat("x", maxCauseLength*3))
		if len(causes) != 1 || len([]rune(causes[0])) != maxCauseLength+1 {
			t.Fatalf("cause of %d runes, want %d plus the truncation marker", len([]rune(causes[0])), maxCauseLength)
		}
	})

	t.Run("a multibyte cause is not split in half", func(t *testing.T) {
		// Truncating by bytes would break the character that crosses the
		// limit and the operator would read invalid text right in the header.
		causes := providerCauses("Error: " + strings.Repeat("ñ", maxCauseLength*2))
		if len(causes) != 1 {
			t.Fatalf("causes = %q, want one", causes)
		}
		if !utf8.ValidString(causes[0]) {
			t.Fatalf("cause = %q, want valid UTF-8", causes[0])
		}
		if runes := []rune(causes[0]); len(runes) != maxCauseLength+1 {
			t.Fatalf("cause of %d runes, want %d plus the truncation marker", len(runes), maxCauseLength)
		}
	})

	t.Run("two identical long causes are deduplicated", func(t *testing.T) {
		long := strings.Repeat("z", maxCauseLength*2)
		text := "Error: " + long + "\nError: " + long + "\nError: distinct\n"
		causes := providerCauses(text)
		if len(causes) != 2 || causes[1] != "distinct" {
			t.Fatalf("causes = %q, want the long cause once plus the distinct one", causes)
		}
	})

	t.Run("the marker inside a line is not a cause", func(t *testing.T) {
		// The reviewer quotes code and evidence; a line that MENTIONS the
		// marker is not a provider failure.
		text := "→ Read engine.go: return fmt.Errorf(\"Error: %v\", err)\nError: the real one\n"
		causes := providerCauses(text)
		if len(causes) != 1 || causes[0] != "the real one" {
			t.Fatalf("causes = %q, want only the line that starts with the marker", causes)
		}
	})

	t.Run("a CRLF stream yields the same cause", func(t *testing.T) {
		// A provider launched from Windows terminates its lines with CRLF.
		// The cause must come out identical to an LF stream: if the carriage
		// return survived the truncation, the same cause would count as two
		// distinct ones and the enriched reason would differ per system.
		crlf := "\x1b[91mError: \x1b[0mripgrep execution failed\r\nError: ripgrep execution failed\r\n"
		causes := providerCauses(crlf)
		if len(causes) != 1 || causes[0] != "ripgrep execution failed" {
			t.Fatalf("causes = %q, want the same single cause as an LF stream", causes)
		}
	})

	t.Run("a failure with no printed cause is left intact", func(t *testing.T) {
		message := "exit status 1"
		if causes := providerCauses(message); len(causes) != 0 {
			t.Fatalf("causes = %v, want none", causes)
		}
		if got := reasonWithCause(message); got != message {
			t.Fatalf("reason = %q, want it unchanged", got)
		}
	})

	t.Run("enriching is idempotent", func(t *testing.T) {
		once := reasonWithCause(realGateFailure)
		if twice := reasonWithCause(once); twice != once {
			t.Fatal("a second pass re-prefixed an already enriched reason")
		}
	})
}

func TestCompactProviderCauseExcludesEventTrace(t *testing.T) {
	enriched := reasonWithCause(realGateFailure)
	got := CompactProviderCause(enriched)
	want := providerCausePrefix + "ripgrep execution failed"
	if got != want {
		t.Fatalf("compact cause = %q, want %q", got, want)
	}
	if strings.Contains(got, "timed out") || strings.Contains(got, " | ") {
		t.Fatalf("compact cause = %q, want no timeout or retained raw trace", got)
	}

	const generic = "admission: missing reviewer binding"
	if got := CompactProviderCause(generic); got != generic {
		t.Fatalf("generic reason = %q, want unchanged", got)
	}
}
