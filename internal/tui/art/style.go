package art

import "strings"

// Text styling for the dashboard contract: xterm-256 foreground colors with
// a global switch so the same layout renders plain (goldens, pipes) or
// colored (terminal, HTML preview). Padding logic always measures runes on
// plain text; Paint is applied only when a line is finalized.
type Color int

const (
	Reset  Color = -1
	Green  Color = 114
	Blue   Color = 75
	Yellow Color = 179
	Red    Color = 167
	Dim    Color = 243
	Purple Color = 140
	Cyan   Color = 110
	White  Color = 255
)

var colorsEnabled = true

// SetColors toggles ANSI emission for the whole package. DashboardPlain
// uses it internally; tests may too.
func SetColors(enabled bool) { colorsEnabled = enabled }

// Paint wraps s in an xterm-256 foreground escape. With colors disabled it
// returns s untouched, so rune content is identical in both modes.
func Paint(c Color, s string) string {
	if !colorsEnabled || c == Reset || s == "" {
		return s
	}
	return "\x1b[38;5;" + itoa(int(c)) + "m" + s + "\x1b[0m"
}

// span is one styled run of text inside a layout line.
type span struct {
	text string
	c    Color
}

// spanLine joins spans, padding with spaces so the visible rune width equals
// width. Spans hold plain text; Paint is applied here, and measurement always
// strips escapes so color never affects alignment.
func spanLine(width int, spans ...span) string {
	total := 0
	for _, s := range spans {
		total += runeLen(s.text)
	}
	var out strings.Builder
	remaining := width
	for i, s := range spans {
		text := s.text
		if i == len(spans)-1 && total < width {
			text += spaces(width - total)
		}
		if runeLen(text) > remaining {
			text = truncateRunes(text, remaining)
		}
		remaining -= runeLen(text)
		out.WriteString(Paint(s.c, text))
		if remaining <= 0 {
			break
		}
	}
	return out.String()
}

// builder accumulates strings with minimal overhead.
type builder struct{ parts []string }

func (b *builder) add(s string) { b.parts = append(b.parts, s) }
func (b *builder) String() string {
	out := ""
	for _, p := range b.parts {
		out += p
	}
	return out
}

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	r := make([]rune, n)
	for i := range r {
		r[i] = ' '
	}
	return string(r)
}

func truncateRunes(s string, w int) string {
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	return string(r[:w])
}

func runeLen(s string) int { return len([]rune(s)) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
