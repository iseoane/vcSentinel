package agentadapter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/acpadapter"
)

// This file is the dedicated OpenCode review-stream parser. It decodes the
// NDJSON event stream OpenCode 1.18.29 emits on `--format json` during
// restricted reviews (probed live on 2026-09-06 with the production reviewer
// invocation; see internal/agentadapter/testdata/opencode/usage-probe.ndjson).
//
// It does NOT reuse acpadapter's wire decoding on purpose: the acpx protocol
// reports usage on the terminal result under camelCase keys (inputTokens,
// cachedInputTokens, ...), while OpenCode reports it per step_finish event
// under snake_case keys (tokens.input, tokens.cache.read, ...) and never
// sends a single terminal usage member. Sharing the decoder would conflate
// two different wire shapes.

// opencodeReviewScan is the normalized observation of one OpenCode
// --format json review stream.
type opencodeReviewScan struct {
	// Output is the review answer: every type=text part text concatenated in
	// stream order, untrimmed. The caller trims once at the boundary, exactly
	// like the plain-text provider paths.
	Output string
	// Usage sums the recognized token members over EVERY step_finish event.
	// It is nil when the stream never reported a tokens object — absent stays
	// absent, never zeros; observed zeros survive as non-nil pointers.
	Usage *acpadapter.Usage
	// UsageJSON retains the verbatim tokens member of every step_finish
	// event, one per line in stream order, so evidence consumers see exactly
	// what crossed the wire. OpenCode has no single terminal usage member
	// (usage is per model step and must be summed), so the raw members travel
	// together instead of the last one alone.
	UsageJSON string
	// StopReason is the LAST step_finish reason translated onto the
	// acpadapter stop-reason vocabulary: "stop" becomes "end_turn" (the
	// natural end of a turn, which Classify reports as success); every other
	// value — including "cancelled" and unfinished-terminal reasons such as
	// "tool-calls" — passes through verbatim, so Classify keeps mapping
	// anything unended to failure ("cancelled" alone keeps its cancellation
	// class). An empty reason means no terminal event was observed and is
	// never fabricated.
	StopReason string
}

// opencodeReviewEvent probes one NDJSON line of the review stream. The
// top-level type is OpenCode's event discriminator (step_start, tool_use,
// text, step_finish); part mirrors the message part that generated it.
type opencodeReviewEvent struct {
	Type string `json:"type"`
	Part struct {
		Type   string          `json:"type"`
		Text   string          `json:"text"`
		Reason string          `json:"reason"`
		Tokens json.RawMessage `json:"tokens"`
	} `json:"part"`
}

// opencodeUsageTokens mirrors OpenCode's per-step token report. Each step is
// self-contained (its total is the sum of its own members), so the session
// total is the sum across steps. Pointer fields preserve an observed zero
// from an absent value, matching acpadapter.Usage's convention.
type opencodeUsageTokens struct {
	Input     *int64 `json:"input"`
	Output    *int64 `json:"output"`
	Reasoning *int64 `json:"reasoning"`
	Total     *int64 `json:"total"`
	Cache     struct {
		Read  *int64 `json:"read"`
		Write *int64 `json:"write"`
	} `json:"cache"`
	// Cache.Write and the step_finish cost member are observed on the wire
	// but intentionally unmapped: acpadapter.Usage has no destination for
	// them, and inventing one is a store-schema change outside this slice.
}

// opencodeFieldSum accumulates one usage member while tracking whether the
// member was ever observed, so absent fields stay nil in the final Usage.
type opencodeFieldSum struct {
	seen bool
	sum  int64
}

func (f *opencodeFieldSum) observe(value *int64) {
	if value == nil {
		return
	}
	f.seen = true
	f.sum += *value
}

func (f opencodeFieldSum) pointer() *int64 {
	if !f.seen {
		return nil
	}
	return new(f.sum)
}

// opencodeUsageSum accumulates the usage members OpenCode reports per
// step_finish event.
type opencodeUsageSum struct {
	seen      bool
	input     opencodeFieldSum
	output    opencodeFieldSum
	reasoning opencodeFieldSum
	total     opencodeFieldSum
	cacheRead opencodeFieldSum
}

func (s *opencodeUsageSum) add(tokens opencodeUsageTokens) {
	s.seen = true
	s.input.observe(tokens.Input)
	s.output.observe(tokens.Output)
	s.reasoning.observe(tokens.Reasoning)
	s.total.observe(tokens.Total)
	s.cacheRead.observe(tokens.Cache.Read)
}

// usage renders the accumulated sums; nil when the stream never reported a
// tokens object at all.
func (s opencodeUsageSum) usage() *acpadapter.Usage {
	if !s.seen {
		return nil
	}
	return &acpadapter.Usage{
		InputTokens:       s.input.pointer(),
		OutputTokens:      s.output.pointer(),
		TotalTokens:       s.total.pointer(),
		CachedInputTokens: s.cacheRead.pointer(),
		ReasoningTokens:   s.reasoning.pointer(),
	}
}

// scanOpenCodeReview parses one OpenCode --format json review stream. It
// keeps the same fail-closed JSONL discipline as extraerMensajeCommitOpenCode
// (empty or malformed lines, and lines over the 1 MiB cap, abort the scan)
// because a broken stream means the review text cannot be trusted: leaking
// raw NDJSON — or a silently truncated answer — as the review text would be
// worse than failing the run.
func scanOpenCodeReview(stream io.Reader) (opencodeReviewScan, error) {
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	var (
		scan       opencodeReviewScan
		sum        opencodeUsageSum
		usageJSON  bytes.Buffer
		texts      strings.Builder
		lastReason string
		events     int
	)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			return opencodeReviewScan{}, fmt.Errorf("salida JSONL invalida de opencode: linea vacia")
		}
		events++
		var event opencodeReviewEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return opencodeReviewScan{}, fmt.Errorf("salida JSONL invalida de opencode: %w", err)
		}
		if event.Type == "text" {
			texts.WriteString(event.Part.Text)
			continue
		}
		if event.Type != "step_finish" {
			continue
		}
		lastReason = event.Part.Reason
		if len(event.Part.Tokens) == 0 || string(event.Part.Tokens) == "null" {
			continue
		}
		usageJSON.Write(event.Part.Tokens)
		usageJSON.WriteByte('\n')
		var tokens opencodeUsageTokens
		if err := json.Unmarshal(event.Part.Tokens, &tokens); err != nil {
			return opencodeReviewScan{}, fmt.Errorf("salida JSONL invalida de opencode: usage de step_finish: %w", err)
		}
		sum.add(tokens)
	}
	if err := scanner.Err(); err != nil {
		return opencodeReviewScan{}, fmt.Errorf("salida JSONL invalida de opencode: %w", err)
	}
	if events == 0 {
		return opencodeReviewScan{}, fmt.Errorf("salida JSONL vacia de opencode")
	}
	scan.Output = texts.String()
	scan.Usage = sum.usage()
	scan.UsageJSON = strings.TrimSuffix(usageJSON.String(), "\n")
	scan.StopReason = mapOpenCodeStopReason(lastReason)
	return scan, nil
}

// mapOpenCodeStopReason translates OpenCode's terminal step_finish reason
// onto the acpadapter stop-reason vocabulary. "stop" — the natural end of a
// turn — becomes "end_turn", the only reason acpadapter.Classify reports as
// success. "cancelled" passes through verbatim so it keeps its cancellation
// class. Every other value (unfinished-terminal reasons such as "tool-calls",
// or reasons this codebase has never observed) passes through verbatim too
// and therefore fails by default; the mapping never invents a reason.
func mapOpenCodeStopReason(raw string) string {
	if raw == "stop" {
		return "end_turn"
	}
	return raw
}
