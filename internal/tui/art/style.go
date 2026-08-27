package art

import "strings"

// Text styling for the dashboard contract: xterm-256 foreground colors with
// the enabled flag carried explicitly by a painter value. Rendering never
// touches package-global state, so concurrent colored and plain renders
// cannot interfere. Padding logic always measures visible runes; color
// escapes are applied only when a line is finalized.

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
	Rose   Color = 218
)

// painter applies this render's color decision to every span it writes.
type painter struct{ colors bool }

// paint wraps s in an xterm-256 foreground escape when colors are enabled
// for this painter; otherwise it returns s untouched, so rune content is
// identical in both modes.
func (p painter) paint(c Color, s string) string {
	if !p.colors || c == Reset || s == "" {
		return s
	}
	return "\x1b[38;5;" + itoa(int(c)) + "m" + s + "\x1b[0m"
}

// spanLine joins spans, padding with spaces so the visible rune width equals
// width. Spans hold plain text; paint is applied here, and measurement always
// strips escapes so color never affects alignment.
func (p painter) spanLine(width int, spans ...span) string {
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
		out.WriteString(p.paint(s.c, text))
		if remaining <= 0 {
			break
		}
	}
	return out.String()
}

// Paint colors s unconditionally. It exists for tests and external consumers
// that need a colored fragment; the renderer uses painter instead, so no
// render path depends on mutable global state.
func Paint(c Color, s string) string {
	return painter{colors: true}.paint(c, s)
}

// span is one styled run of text inside a layout line.
type span struct {
	text string
	c    Color
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
