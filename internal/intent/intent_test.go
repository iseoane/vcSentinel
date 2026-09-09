package intent

import (
	"strings"
	"testing"
)

func TestNormalizeIntent(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		source     string
		want       Intent
		wantErr    bool
		wantLength int
	}{
		{name: "zero", want: Intent{}},
		{name: "folds whitespace", text: "  add\n  a   guard ", source: SourceDeclared, want: Intent{Text: "add a guard", Source: SourceDeclared}},
		{name: "strips echoed key", text: "Sentinel-Intent: add a guard", source: SourceDeclared, want: Intent{Text: "add a guard", Source: SourceDeclared}},
		{name: "unknown source", text: "add a guard", source: "other", wantErr: true},
		{name: "empty text", source: SourceDeclared, wantErr: true},
		{name: "declared over limit", text: strings.Repeat("x", MaxLength+1), source: SourceDeclared, wantErr: true},
		{name: "conversation truncates at rune boundary", text: strings.Repeat("x", MaxLength-1) + "éz", source: SourceConversation, want: Intent{Text: strings.Repeat("x", MaxLength-1) + "é", Source: SourceConversation}, wantLength: MaxLength},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Normalize(tt.text, tt.source)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Normalize() error = %v, want error: %t", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.Source != tt.want.Source || got.Text != tt.want.Text {
				t.Fatalf("Normalize() = %+v, want %+v", got, tt.want)
			}
			if tt.wantLength > 0 && len([]rune(got.Text)) != tt.wantLength {
				t.Fatalf("normalized text has %d runes, want %d", len([]rune(got.Text)), tt.wantLength)
			}
		})
	}
}

func TestRenderAndParseIntentTrailers(t *testing.T) {
	value := Intent{Text: "add a guard", Source: SourceDeclared}
	message := "subject\n\nbody\n\nExisting: keep\n" + Render(value) + "\n"
	got := Parse(message)
	if got != value {
		t.Fatalf("Parse() = %+v, want %+v", got, value)
	}

	crlf := strings.ReplaceAll(message, "\n", "\r\n")
	if got := Parse(crlf); got != value {
		t.Fatalf("Parse(CRLF) = %+v, want %+v", got, value)
	}
}

func TestParseRequiresCompleteKnownTrailerPair(t *testing.T) {
	cases := []string{
		"subject\n\nSentinel-Intent: add a guard",
		"subject\n\nSentinel-Intent-Source: declared",
		"subject\n\nSentinel-Intent: add a guard\nSentinel-Intent-Source: unknown",
		"subject\nSentinel-Intent: body text\n\nbody",
	}
	for _, message := range cases {
		if got := Parse(message); got != (Intent{}) {
			t.Errorf("Parse(%q) = %+v, want zero", message, got)
		}
	}

	message := "subject\n\nSentinel-Intent: first\nSentinel-Intent-Source: declared\nSentinel-Intent: last\nSentinel-Intent-Source: conversation"
	want := Intent{Text: "last", Source: SourceConversation}
	if got := Parse(message); got != want {
		t.Fatalf("Parse(last occurrence) = %+v, want %+v", got, want)
	}
}

func TestAppendIntentPreservesSpacingAndZero(t *testing.T) {
	value := Intent{Text: "add a guard", Source: SourceConversation}
	cases := []struct {
		name    string
		message string
		want    string
	}{
		{name: "subject", message: "subject", want: "subject\n\n" + Render(value)},
		{name: "body", message: "subject\n\nbody", want: "subject\n\nbody\n\n" + Render(value)},
		{name: "existing trailer", message: "subject\n\nExisting: keep", want: "subject\n\nExisting: keep\n" + Render(value)},
		{name: "trailing whitespace", message: "subject\n\nbody \n\n", want: "subject\n\nbody\n\n" + Render(value)},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := Append(tt.message, value); got != tt.want {
				t.Fatalf("Append() = %q, want %q", got, tt.want)
			}
		})
	}
	if got := Append("subject\n", Intent{}); got != "subject\n" {
		t.Fatalf("Append(zero) = %q, want original message", got)
	}
}
