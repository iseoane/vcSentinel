package agentadapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/ISeoane-Quental/vas.sentinel/internal/acpadapter"
)

// This file is the dedicated Claude Code review-result parser. It decodes the
// single JSON object Claude Code 2.1.263 emits on `--output-format json` with
// --print during restricted reviews (probed live on 2026-09-06 with the
// production reviewer invocation; see
// internal/agentadapter/testdata/claude/usage-probe.json).
//
// It is deliberately NOT the OpenCode stream scanner: Claude prints ONE
// result object (not NDJSON), reports usage once with snake_case keys
// (input_tokens, cache_read_input_tokens, ...) and no total_tokens member,
// and carries the terminal outcome in its own stop_reason/subtype fields.

// claudeReviewScan is the normalized observation of one Claude Code
// --output-format json review result.
type claudeReviewScan struct {
	// Output is the review answer carried by the result member, untrimmed.
	// The caller trims once at the boundary; the probe verified that this is
	// byte-identical to the plain-text output for the same answer.
	Output string
	// Usage maps the recognized usage members. It is nil when the result
	// reports no usage member — absent stays absent, never zeros; observed
	// zeros survive as non-nil pointers. TotalTokens stays nil always: the
	// wire carries no total_tokens member and computing one would fabricate
	// evidence.
	Usage *acpadapter.Usage
	// UsageJSON retains the verbatim usage member so evidence consumers see
	// exactly what crossed the wire. Empty when the result reports none.
	UsageJSON string
	// StopReason is the result's terminal outcome. stop_reason passes through
	// verbatim — "end_turn" (probe-observed on success) keeps its success
	// class in acpadapter.Classify, everything else fails by default. When
	// the wire omits stop_reason, a success subtype still maps to "end_turn"
	// so a completed run is never misclassified; any other subtype passes
	// through verbatim. Empty only when neither member is present.
	StopReason string
}

// claudeResultProbe probes the fields of one result object that the review
// contract needs. cost, duration, session identity, modelUsage and the other
// members are observed on the wire but have no Usage destination (F9
// precedent), so they are not decoded.
type claudeResultProbe struct {
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype"`
	IsError    bool            `json:"is_error"`
	Result     string          `json:"result"`
	StopReason string          `json:"stop_reason"`
	Usage      json.RawMessage `json:"usage"`
}

// claudeUsageProbe mirrors the recognized members of Claude Code's usage
// report. Pointer fields preserve an observed zero from an absent value,
// matching acpadapter.Usage's convention. cache_creation_input_tokens is
// observed on the wire but intentionally unmapped: acpadapter.Usage has no
// destination for it (Cached counts cache reads only), exactly like the
// total_cost_usd member.
type claudeUsageProbe struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
	CacheRead    *int64 `json:"cache_read_input_tokens"`
	Details      struct {
		ThinkingTokens *int64 `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

// scanClaudeReview parses one Claude Code --output-format json review result:
// a single JSON object on stdout, not a line-delimited stream. It fails
// closed on an empty or malformed stream and on an is_error result, because
// a broken or failed run must surface as a review error instead of leaking
// raw JSON — or a failed run's text — as the review answer.
func scanClaudeReview(stream io.Reader) (claudeReviewScan, error) {
	raw, err := io.ReadAll(stream)
	if err != nil {
		return claudeReviewScan{}, fmt.Errorf("salida ilegible de claude: %w", err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return claudeReviewScan{}, fmt.Errorf("salida vacia de claude")
	}
	var result claudeResultProbe
	if err := json.Unmarshal(trimmed, &result); err != nil {
		return claudeReviewScan{}, fmt.Errorf("salida JSON invalida de claude: %w", err)
	}
	if result.IsError {
		return claudeReviewScan{}, fmt.Errorf("claude reporto un resultado de error (subtype %q)", result.Subtype)
	}
	scan := claudeReviewScan{
		Output:     result.Result,
		StopReason: mapClaudeStopReason(result.StopReason, result.Subtype),
	}
	if len(result.Usage) > 0 && string(result.Usage) != "null" {
		scan.UsageJSON = string(result.Usage)
		var usage claudeUsageProbe
		if err := json.Unmarshal(result.Usage, &usage); err != nil {
			return claudeReviewScan{}, fmt.Errorf("salida JSON invalida de claude: usage: %w", err)
		}
		scan.Usage = &acpadapter.Usage{
			InputTokens:       usage.InputTokens,
			OutputTokens:      usage.OutputTokens,
			CachedInputTokens: usage.CacheRead,
			ReasoningTokens:   usage.Details.ThinkingTokens,
		}
	}
	return scan, nil
}

// mapClaudeStopReason resolves the terminal outcome of one result object.
// stop_reason wins verbatim: Claude Code speaks the end_turn vocabulary
// directly (the probe captured "end_turn" on a successful review), so the
// success class arrives without translation and every other value
// ("tool_use", "max_tokens", ...) fails by default — a turn that ended
// without its final answer must not count as success. When the wire omits
// stop_reason, the subtype carries the outcome: a success subtype maps to
// "end_turn", anything else passes through verbatim.
func mapClaudeStopReason(stopReason, subtype string) string {
	if stopReason != "" {
		return stopReason
	}
	if subtype == "success" {
		return "end_turn"
	}
	return subtype
}
