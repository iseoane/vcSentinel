// Validation evidence recording for the durable-runs roadmap (R9 slice 1):
// it turns a completed validation.Run result list into deterministic
// evidence frames without invoking any agent anywhere on this path.
//
// The store's durable model deliberately never persists raw output bytes
// (AttemptOutcome carries only OutputHash, and event frames carry no payload
// size caps because full output is never stored). This recorder respects
// that model: it emits bounded scalar fields plus a digest, never output
// bytes. The digest binds command, capability name, exit status, duration,
// and the trimmed combined output through execution.HashAdapterOutput, the
// single hashing authority shared with durable outcome admission, so the
// same settled runs always produce identical entries and any change to exit
// status or duration produces a different digest.
package gate

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/validation"
)

// ValidationEvidence is the deterministic evidence frame content of one
// settled validation command. It is intentionally small enough for any
// future durable frame: scalars plus one digest, no output bytes.
type ValidationEvidence struct {
	Command    string
	Capability string
	Exit       int
	DurationMs int64
	// Digest binds the whole deterministic tuple (command, capability, exit
	// status, duration, trimmed output) so identical runs yield identical
	// digests and differing exit status or duration yields a different one.
	Digest string
}

// RecordValidationEvidence maps completed validation runs to deterministic
// evidence entries in the same order the executor produced them. Output is
// trimmed before hashing; raw output bytes never leave this function.
func RecordValidationEvidence(runs []validation.ValidationRun) []ValidationEvidence {
	evidence := make([]ValidationEvidence, 0, len(runs))
	for _, run := range runs {
		evidence = append(evidence, ValidationEvidence{
			Command:    run.Command,
			Capability: run.Capability,
			Exit:       run.Exit,
			DurationMs: run.DurationMs,
			Digest: execution.HashAdapterOutput(strings.Join([]string{
				run.Command,
				run.Capability,
				strconv.Itoa(run.Exit),
				strconv.FormatInt(run.DurationMs, 10),
				strings.TrimSpace(run.Output),
			}, "\x00")),
		})
	}
	return evidence
}

// String serializes the deterministic tuple for durable binding: a settled
// validation job's adapter output carries exactly this text, so the
// execution controller persists its hash as the AttemptOutcome.OutputHash.
// Raw output bytes never appear here — only scalars plus the digest.
func (e ValidationEvidence) String() string {
	return fmt.Sprintf("command=%s capability=%s exit=%d duration_ms=%d digest=%s",
		e.Command, e.Capability, e.Exit, e.DurationMs, e.Digest)
}
