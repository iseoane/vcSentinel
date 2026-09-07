package review

import (
	"regexp"
	"strings"
)

// providerCausePrefix heads an enriched reason. It also serves as the
// idempotence guard: an already-enriched failure is not enriched again.
const providerCausePrefix = "provider reported: "

const (
	maxProviderCauses   = 3
	maxCauseLength      = 200
	providerCauseMarker = "Error: "
)

// ansiSequence recognizes the coloring that agent CLIs emit into their
// stream. Without stripping it, a cause would be split by control bytes and
// match nothing readable.
var ansiSequence = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// providerCauses extracts the error lines the provider printed inside its
// own stream.
//
// Why it exists: when a reviewer tool fails, the reviewer does not stop.
// It keeps working in the only expensive way it has left — reading whole
// files instead of searching — until the budget runs out. What reaches the
// operator is then a timeout, which is the consequence, with the real cause
// buried hundreds of lines below. This brings it to the front.
//
// It deliberately does not recognize specific tools. Keeping a table of
// "agent X needs binary Y" would be a list that ages with every provider;
// what is reported here is what the provider said failed.
func providerCauses(text string) []string {
	clean := ansiSequence.ReplaceAllString(text, "")
	seen := map[string]bool{}
	causes := make([]string, 0, maxProviderCauses)
	for _, line := range strings.Split(clean, "\n") {
		// The marker is required at the START of the already-clean line, not
		// at any position: "Error: " appears inside source code and inside
		// the evidence the reviewer quotes, and matching it loosely would
		// promote that text to the cause of the failure.
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, providerCauseMarker) {
			continue
		}
		cause := truncateCause(strings.TrimSpace(strings.TrimPrefix(trimmed, providerCauseMarker)))
		// Deduplication looks at the ALREADY truncated value, the one that
		// gets published. Comparing the original and storing the truncation
		// would let the same long cause through twice and exhaust the quota
		// with repetitions.
		if cause == "" || seen[cause] {
			continue
		}
		seen[cause] = true
		causes = append(causes, cause)
		if len(causes) == maxProviderCauses {
			break
		}
	}
	return causes
}

// truncateCause bounds by runes, not bytes: a cut in the middle of a
// multibyte character would produce invalid text right at the first thing
// the operator reads.
func truncateCause(cause string) string {
	runes := []rune(cause)
	if len(runes) <= maxCauseLength {
		return cause
	}
	return string(runes[:maxCauseLength]) + "…"
}

// reasonWithCause prepends the causes the provider reported, keeping the
// original text behind them: the full trace is still evidence and is not
// discarded; it just stops being the first thing read.
func reasonWithCause(message string) string {
	if strings.HasPrefix(message, providerCausePrefix) {
		return message
	}
	causes := providerCauses(message)
	if len(causes) == 0 {
		return message
	}
	return providerCausePrefix + strings.Join(causes, "; ") + " | " + message
}

// CompactProviderCause returns the safe form for an operational event: it
// retains only the causes printed by the provider, without carrying the full
// trace, which can contain hundreds of lines and the resulting timeout.
// Reasons with no recognizable provider cause are retained so admission or
// configuration failures are not hidden.
func CompactProviderCause(message string) string {
	if causes := providerCauses(message); len(causes) > 0 {
		return providerCausePrefix + strings.Join(causes, "; ")
	}
	if !strings.HasPrefix(message, providerCausePrefix) {
		return message
	}
	header := strings.TrimSpace(strings.TrimPrefix(message, providerCausePrefix))
	if short, _, ok := strings.Cut(header, " | "); ok {
		header = strings.TrimSpace(short)
	}
	return providerCausePrefix + truncateCause(header)
}
