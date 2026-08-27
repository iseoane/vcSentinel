package art

import (
	"strings"
	"testing"
)

func TestPainterHonorsFlag(t *testing.T) {
	colored := painter{colors: true}.paint(Red, "x")
	if !strings.Contains(colored, "\x1b[38;5;167m") {
		t.Fatalf("painter with colors lost the escape: %q", colored)
	}
	if got := (painter{colors: false}).paint(Red, "x"); got != "x" {
		t.Fatalf("painter without colors altered content: %q", got)
	}
}

func TestSpanLinePadsToWidth(t *testing.T) {
	p := painter{colors: false}
	line := p.spanLine(10, span{"ab", White}, span{"cd", Red})
	if runeLen(line) != 10 {
		t.Fatalf("line width %d, want 10: %q", runeLen(line), line)
	}
	if !strings.HasPrefix(line, "abcd") {
		t.Fatalf("line lost content order: %q", line)
	}
}

func TestSpanLineTruncatesOverflow(t *testing.T) {
	p := painter{colors: false}
	line := p.spanLine(4, span{"abcdef", White})
	if runeLen(line) != 4 || line != "abcd" {
		t.Fatalf("overflow line = %q, want %q", line, "abcd")
	}
}

func TestSpanLineMeasuresThroughColors(t *testing.T) {
	p := painter{colors: true}
	line := p.spanLine(10, span{"ab", White}, span{"cd", Red})
	if runeLen(stripEscapes(line)) != 10 {
		t.Fatalf("visible width %d, want 10", runeLen(stripEscapes(line)))
	}
	if !strings.Contains(line, "\x1b[") {
		t.Fatal("expected colored spans")
	}
}

func TestTruncateVisibleKeepsEscapes(t *testing.T) {
	in := Paint(Red, "abcdef")
	out := truncateVisible(in, 3)
	if got := stripEscapes(out); got != "abc" {
		t.Fatalf("truncateVisible visible = %q, want abc", got)
	}
	if !strings.HasSuffix(out, "\x1b[0m") {
		t.Fatal("truncateVisible left the color open")
	}
}

func TestFitRunesTruncatesAndPads(t *testing.T) {
	if got := fitRunes("abcdef", 4); got != "abcd" {
		t.Fatalf("fitRunes overflow = %q, want %q", got, "abcd")
	}
	if got := fitRunes("ab", 4); got != "ab  " {
		t.Fatalf("fitRunes pad = %q, want %q", got, "ab  ")
	}
}
