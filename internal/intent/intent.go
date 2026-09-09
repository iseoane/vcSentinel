// Package intent owns the human intent trailers carried by slice plans and
// commits. Keeping the keys here prevents consumers from inventing subtly
// different parsing or rendering rules.
package intent

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	IntentKey          = "Sentinel-Intent"
	SourceKey          = "Sentinel-Intent-Source"
	MaxLength          = 300
	SourceDeclared     = "declared"
	SourceConversation = "conversation"
)

// Source is an alias rather than a distinct string type so callers can pass
// configuration and JSON values through Normalize without conversions.
type Source = string

// Intent is zero when no complete, recognized intent trailer pair exists.
type Intent struct {
	Text   string
	Source Source
}

var ErrInvalid = errors.New("invalid sentinel intent")

// Normalize folds whitespace and validates the source. A declared intent is
// rejected when it exceeds MaxLength; a conversation summary is truncated at
// a UTF-8 rune boundary. Writers never promote a source: the source passed by
// the caller remains authoritative. A human may amend a commit and re-declare
// its intent explicitly, but generated text cannot change conversation into
// declared intent.
func Normalize(text, source string) (Intent, error) {
	if strings.TrimSpace(text) == "" && strings.TrimSpace(source) == "" {
		return Intent{}, nil
	}
	if source != SourceDeclared && source != SourceConversation {
		return Intent{}, fmt.Errorf("%w: unknown source %q", ErrInvalid, source)
	}

	text = stripEchoedKey(text)
	text = strings.Join(strings.Fields(text), " ")
	if text == "" {
		return Intent{}, fmt.Errorf("%w: intent text is empty", ErrInvalid)
	}
	if !utf8.ValidString(text) {
		return Intent{}, fmt.Errorf("%w: intent text is not valid UTF-8", ErrInvalid)
	}

	runes := []rune(text)
	if len(runes) > MaxLength {
		if source == SourceDeclared {
			return Intent{}, fmt.Errorf("%w: declared intent exceeds %d characters", ErrInvalid, MaxLength)
		}
		text = strings.TrimSpace(string(runes[:MaxLength]))
	}
	if text == "" {
		return Intent{}, fmt.Errorf("%w: intent text is empty", ErrInvalid)
	}
	return Intent{Text: text, Source: source}, nil
}

func stripEchoedKey(text string) string {
	for {
		trimmed := strings.TrimSpace(text)
		if !strings.HasPrefix(trimmed, IntentKey+":") {
			return text
		}
		text = strings.TrimSpace(strings.TrimPrefix(trimmed, IntentKey+":"))
	}
}

// Render returns the canonical two-line trailer block. It deliberately does
// not infer or promote a source; invalid values render as an empty block so an
// unvalidated writer cannot emit a misleading trailer.
func Render(value Intent) string {
	if value.Text == "" && value.Source == "" {
		return ""
	}
	normalized, err := Normalize(value.Text, value.Source)
	if err != nil {
		return ""
	}
	return IntentKey + ": " + normalized.Text + "\n" + SourceKey + ": " + normalized.Source
}

// Append adds the canonical trailers without changing a zero intent. Existing
// trailers are preserved and receive one additional line; a subject or body
// receives the usual blank line before the trailer block.
func Append(message string, value Intent) string {
	trailer := Render(value)
	if trailer == "" {
		return message
	}

	base := strings.TrimRight(message, " \t\r\n")
	if base == "" {
		return trailer
	}
	if hasTrailingTrailerBlock(base) {
		return base + "\n" + trailer
	}
	return base + "\n\n" + trailer
}

// Parse reads only the final contiguous trailer block. Text that merely looks
// like a trailer in the body is ignored, and an incomplete or unknown source
// pair returns the zero Intent. Repeated keys use their last occurrence so a
// human's amendment of the final block is honored.
func Parse(message string) Intent {
	message = strings.TrimRight(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	if message == "" {
		return Intent{}
	}
	lines := strings.Split(message, "\n")
	start := len(lines)
	for start > 0 {
		line := strings.TrimSuffix(lines[start-1], "\r")
		if !isTrailerLine(line) {
			break
		}
		start--
	}
	if start == len(lines) {
		return Intent{}
	}

	text, source := "", ""
	seenText, seenSource := false, false
	for _, line := range lines[start:] {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case IntentKey:
			text, seenText = value, true
		case SourceKey:
			source, seenSource = value, true
		}
	}
	if !seenText || !seenSource {
		return Intent{}
	}
	normalized, err := Normalize(text, source)
	if err != nil {
		return Intent{}
	}
	return normalized
}

func hasTrailingTrailerBlock(message string) bool {
	lines := strings.Split(message, "\n")
	start := len(lines)
	for start > 0 && isTrailerLine(lines[start-1]) {
		start--
	}
	return start < len(lines)
}

func isTrailerLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	key, _, ok := strings.Cut(line, ":")
	return ok && strings.TrimSpace(key) != ""
}
