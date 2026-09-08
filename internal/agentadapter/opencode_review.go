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
	// Steps counts the step_finish events observed in the stream. That is
	// the unit OpenCode's "Steps" agent-configuration budget actually
	// enforces: it is a MODEL TURN budget, not a tool-call budget. The probe
	// fixture (testdata/opencode/usage-probe.ndjson) proves the distinction:
	// its first turn runs TWO tool_use events before its step_finish, and its
	// second turn answers with text, so one step_finish pairs with several
	// tool events and counting tool_use would overcount the budget the
	// provider actually spends. That fixture consumes 2 turns, not 3.
	Steps int
	// TerminalEventObserved reports whether at least one step_finish event
	// appeared anywhere in the stream. It exists because StopReason alone
	// cannot distinguish two very different situations that both leave it
	// empty: a step_finish that happened to carry an unusual empty reason
	// (a terminal event DID occur), versus a stream that never produced a
	// single step_finish at all (no terminal event was ever observed — the
	// most severe truncation there is, since even the provider's own
	// end-of-turn signal never arrived).
	TerminalEventObserved bool
	// DeniedToolCalls lists every tool_use event whose part.state.status was
	// "error", in stream order. Measured evidence (11 real review
	// invocations) found perfect correlation between at least one denied
	// tool call and the turn ending truncated: OpenCode's restricted
	// permission set auto-rejects a read outside the audited/context paths,
	// and that denial — not the turn budget — is what kills the turn. The
	// denial arrives as stream event state, never as text in the answer,
	// which is why the pre-existing semanticOutputLooksToolDenied text
	// matcher in internal/review/finding.go could never catch it.
	DeniedToolCalls []DeniedToolCall
}

// DeniedToolCall records one tool_use event OpenCode reported as failed
// (part.state.status == "error"): the tool name and the error text, exactly
// as observed on the wire. Most observed causes are permission denials, but
// this simply records whatever error text the provider reported — it never
// infers a cause beyond what the wire said.
type DeniedToolCall struct {
	Tool  string
	Error string
}

// opencodeReviewEvent probes one NDJSON line of the review stream. The
// top-level type is OpenCode's event discriminator (step_start, tool_use,
// text, step_finish); part mirrors the message part that generated it.
type opencodeReviewEvent struct {
	Type string `json:"type"`
	Part struct {
		Type   string          `json:"type"`
		Tool   string          `json:"tool"`
		Text   string          `json:"text"`
		Reason string          `json:"reason"`
		Tokens json.RawMessage `json:"tokens"`
		State  struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"state"`
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
// keeps the same fail-closed JSONL discipline as extractOpenCodeCommitMessage
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
		steps      int
		denied     []DeniedToolCall
	)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			return opencodeReviewScan{}, fmt.Errorf("invalid opencode JSONL output: empty line")
		}
		events++
		var event opencodeReviewEvent
		if err := json.Unmarshal(line, &event); err != nil {
			return opencodeReviewScan{}, fmt.Errorf("invalid opencode JSONL output: %w", err)
		}
		if event.Type == "text" {
			texts.WriteString(event.Part.Text)
			continue
		}
		if event.Type == "tool_use" {
			if event.Part.State.Status == "error" {
				denied = append(denied, DeniedToolCall{Tool: event.Part.Tool, Error: event.Part.State.Error})
			}
			continue
		}
		if event.Type != "step_finish" {
			continue
		}
		lastReason = event.Part.Reason
		steps++
		if len(event.Part.Tokens) == 0 || string(event.Part.Tokens) == "null" {
			continue
		}
		usageJSON.Write(event.Part.Tokens)
		usageJSON.WriteByte('\n')
		var tokens opencodeUsageTokens
		if err := json.Unmarshal(event.Part.Tokens, &tokens); err != nil {
			return opencodeReviewScan{}, fmt.Errorf("invalid opencode JSONL output: step_finish usage member: %w", err)
		}
		sum.add(tokens)
	}
	if err := scanner.Err(); err != nil {
		return opencodeReviewScan{}, fmt.Errorf("invalid opencode JSONL output: %w", err)
	}
	if events == 0 {
		return opencodeReviewScan{}, fmt.Errorf("empty opencode JSONL output")
	}
	scan.Output = texts.String()
	scan.Usage = sum.usage()
	scan.UsageJSON = strings.TrimSuffix(usageJSON.String(), "\n")
	scan.StopReason = mapOpenCodeStopReason(lastReason)
	scan.Steps = steps
	scan.TerminalEventObserved = steps > 0
	scan.DeniedToolCalls = denied
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
