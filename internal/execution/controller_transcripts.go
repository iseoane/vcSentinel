package execution

import (
	"fmt"
	"os"

	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// TranscriptIdentity is the observed effective identity of the responder for
// one physical invocation. Every field is optional provenance reported by the
// adapter only after a successful request; nothing is ever fabricated, so
// identity the provider did not expose stays empty.
type TranscriptIdentity struct {
	Agent      string
	Model      string
	Effort     string
	StopReason string
}

// TranscriptReporter is optionally implemented by adapters whose provider
// conversation must be preserved as a tamper-evident transcript sidecar of
// the durable execution record (the semantic-review transport). Deterministic
// gate/validation adapters deliberately do not implement it: their jobs have
// no provider transcripts to preserve.
//
// The controller calls TranscriptMetadata only after Execute returned
// successfully on the terminal path, mirroring how effective-agent recording
// fires right after the answer that makes attribution honest.
type TranscriptReporter interface {
	TranscriptMetadata() TranscriptIdentity
}

// captureTranscript persists result.Output as an atomic sidecar under the
// run's execution directory and records its digest, size, and observed
// identity additively onto the outcome about to be persisted. Capture is
// strictly best-effort: any sidecar failure skips the annotation entirely and
// leaves the durable behavior byte-compatible with the pre-transcript flow,
// because a transcript must never turn a healthy review into an error.
func (c *Controller) captureTranscript(invocation *store.AttemptOutcome, result AdapterResult, reporter TranscriptReporter) {
	if result.Output == "" {
		return
	}
	digest, size, err := c.store.WriteTranscript(invocation.RunID, invocation.InvocationID, invocation.At, result.Output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "execution: transcript sidecar for invocation %s skipped: %v\n", invocation.InvocationID, err)
		return
	}
	identity := reporter.TranscriptMetadata()
	invocation.TranscriptSHA256 = digest
	invocation.TranscriptSize = size
	invocation.Agent = identity.Agent
	invocation.Model = identity.Model
	invocation.Effort = identity.Effort
	invocation.StopReason = identity.StopReason
}
