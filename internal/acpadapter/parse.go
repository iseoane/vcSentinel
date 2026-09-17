package acpadapter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
)

// DefaultMaxLineBytes bounds one NDJSON line of acpx stdout. Real protocol
// lines stay far below this; anything larger is a framing violation.
const DefaultMaxLineBytes = 1 << 20

// Usage contains only token fields with the ACP terminal payload's recognized
// numeric shape. Pointer fields preserve an observed zero from an absent value.
// CacheWriteInputTokens stays nil for acpx-sourced usage: the captured wire
// (fixtures end-turn.jsonl and cancelled.jsonl, plus the helper-process
// transcripts) reports no cache-write member, so no key is mapped here.
type Usage struct {
	InputTokens           *int64
	OutputTokens          *int64
	TotalTokens           *int64
	CachedInputTokens     *int64
	CacheWriteInputTokens *int64
	ReasoningTokens       *int64
}

// StreamSummary carries everything the strict scan extracted from one acpx
// stdout stream: the assembled assistant text, how many framing violations
// were skipped, the model observed on an initialize result (when the adapter
// exposes configOptions at all), the terminal stopReason, and recognized usage.
// Raw stdout bytes are retained by the caller (Run), not here.
type StreamSummary struct {
	Output         string
	Violations     int
	ObservedModel  string
	ObservedEffort string
	StopReason     string
	UsageJSON      string
	Usage          *Usage
}

// wireEnvelope is the minimal probe every NDJSON line is decoded against
// before its parts are inspected individually.
type wireEnvelope struct {
	Method string          `json:"method"`
	Result json.RawMessage `json:"result"`
	Params json.RawMessage `json:"params"`
}
type usageProbe struct {
	InputTokens       *int64 `json:"inputTokens"`
	OutputTokens      *int64 `json:"outputTokens"`
	TotalTokens       *int64 `json:"totalTokens"`
	CachedInputTokens *int64 `json:"cachedInputTokens"`
	ReasoningTokens   *int64 `json:"reasoningTokens"`
}

// terminalProbe matches any result object carrying a stopReason; usage is
// kept raw so evidence hashing sees exactly what crossed the wire.
type terminalProbe struct {
	StopReason string          `json:"stopReason"`
	Usage      json.RawMessage `json:"usage"`
}

// configProbe matches initialize results that advertise session config
// options. opencode exposes id=model; claude adapters omit configOptions
// entirely — absence is normal, never an error.
type configProbe struct {
	ConfigOptions []struct {
		ID           string `json:"id"`
		CurrentValue string `json:"currentValue"`
		Value        string `json:"value"`
	} `json:"configOptions"`
}

// chunkProbe matches session/update notifications carrying incremental
// assistant text. It decodes the envelope's params object directly.
type chunkProbe struct {
	Update struct {
		SessionUpdate string `json:"sessionUpdate"`
		Content       struct {
			Text string `json:"text"`
		} `json:"content"`
	} `json:"update"`
}

// ParseStream scans one acpx stdout stream line by line. Non-JSON lines and
// lines over maxLineBytes are counted as framing violations and skipped —
// upstream adapters have been observed polluting stdout, and a polluted line
// must never fail an otherwise healthy turn. The output is the concatenation
// of agent_message_chunk text; classification inputs are the LAST terminal
// stopReason seen plus the observed model from any result advertising it.
func ParseStream(r io.Reader, maxLineBytes int) StreamSummary {
	if maxLineBytes <= 0 {
		maxLineBytes = DefaultMaxLineBytes
	}
	summary := StreamSummary{}
	var out bytes.Buffer
	reader := bufio.NewReaderSize(r, 64*1024)
	for {
		line, tooLong, readErr := readBoundedLine(reader, maxLineBytes)
		switch {
		case tooLong:
			summary.Violations++
		case len(line) > 0:
			absorbLine(line, &summary, &out)
		}
		if readErr != nil {
			break
		}
	}
	summary.Output = out.String()
	return summary
}

// absorbLine decodes one well-sized NDJSON line into the running summary.
func absorbLine(line []byte, summary *StreamSummary, out *bytes.Buffer) {
	var envelope wireEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		// Not JSON at all: the known opencode [skill-registry] pollution
		// class. Skipped and counted by the caller's tooLong/invalid path.
		summary.Violations++
		return
	}
	if len(envelope.Result) > 0 && string(envelope.Result) != "null" {
		var terminal terminalProbe
		if err := json.Unmarshal(envelope.Result, &terminal); err == nil && terminal.StopReason != "" {
			summary.StopReason = terminal.StopReason
			if len(terminal.Usage) > 0 && string(terminal.Usage) != "null" {
				summary.UsageJSON = string(terminal.Usage)
				var usage usageProbe
				if err := json.Unmarshal(terminal.Usage, &usage); err == nil {
					summary.Usage = &Usage{
						InputTokens:       cloneInt64(usage.InputTokens),
						OutputTokens:      cloneInt64(usage.OutputTokens),
						TotalTokens:       cloneInt64(usage.TotalTokens),
						CachedInputTokens: cloneInt64(usage.CachedInputTokens),
						ReasoningTokens:   cloneInt64(usage.ReasoningTokens),
					}
				}
			}
		}
		var cfg configProbe
		if err := json.Unmarshal(envelope.Result, &cfg); err == nil {
			for _, option := range cfg.ConfigOptions {
				value := option.CurrentValue
				if value == "" {
					value = option.Value
				}
				switch option.ID {
				case "model":
					summary.ObservedModel = value
				case "effort", "reasoning_effort":
					summary.ObservedEffort = value
				}
			}
		}
	}
	if len(envelope.Params) > 0 {
		var chunk chunkProbe
		if err := json.Unmarshal(envelope.Params, &chunk); err == nil &&
			chunk.Update.SessionUpdate == "agent_message_chunk" {
			out.WriteString(chunk.Update.Content.Text)
		}
	}
}

// readBoundedLine returns one logical newline-terminated line, capped at
// maxLineBytes. A longer logical line is reported as tooLong with an empty
// slice: the remainder is drained but never buffered, so memory stays
// bounded no matter how long the offending line grows. A final line without
// a trailing newline is still delivered; io.EOF follows once the stream is
// exhausted.
func readBoundedLine(reader *bufio.Reader, maxLineBytes int) (line []byte, tooLong bool, err error) {
	var buf bytes.Buffer
	for {
		chunk, isPrefix, readErr := reader.ReadLine()
		if len(chunk) > 0 && !tooLong {
			buf.Write(chunk)
			if buf.Len() > maxLineBytes {
				tooLong = true
				buf.Reset()
			}
		}
		if isPrefix {
			// ReadLine hit its internal buffer limit; keep draining this
			// logical line. Once tooLong is set, drained bytes are dropped.
			continue
		}
		if readErr != nil {
			if tooLong {
				return nil, true, nil
			}
			if buf.Len() == 0 {
				return nil, false, readErr
			}
			return buf.Bytes(), false, nil
		}
		return buf.Bytes(), tooLong, nil
	}
}
func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
