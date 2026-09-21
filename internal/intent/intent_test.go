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
		{name: "strips echoed key", text: "vcSentinel-Intent: add a guard", source: SourceDeclared, want: Intent{Text: "add a guard", Source: SourceDeclared}},
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
		"subject\n\nvcSentinel-Intent: add a guard",
		"subject\n\nvcSentinel-Intent-Source: declared",
		"subject\n\nvcSentinel-Intent: add a guard\nvcSentinel-Intent-Source: unknown",
		"subject\nvcSentinel-Intent: body text\n\nbody",
	}
	for _, message := range cases {
		if got := Parse(message); got != (Intent{}) {
			t.Errorf("Parse(%q) = %+v, want zero", message, got)
		}
	}

	message := "subject\n\nvcSentinel-Intent: first\nvcSentinel-Intent-Source: declared\nvcSentinel-Intent: last\nvcSentinel-Intent-Source: conversation"
	want := Intent{Text: "last", Source: SourceConversation}
	if got := Parse(message); got != want {
		t.Fatalf("Parse(last occurrence) = %+v, want %+v", got, want)
	}
}

func TestParseRecognizesOnlyValidTrailingTrailerBlocks(t *testing.T) {
	value := Intent{Text: "protect the release", Source: SourceDeclared}
	cases := []struct {
		name    string
		message string
		want    Intent
	}{
		{name: "colon subject", message: "feat(slice): describe the change", want: Intent{}},
		{name: "unscoped feat subject before trailers", message: "feat: describe the change\n" + Render(value), want: Intent{}},
		{name: "unscoped fix subject before trailers", message: "fix: describe the change\n" + Render(value), want: Intent{}},
		{name: "body and real trailer", message: "subject\n\nbody\n\n" + Render(value), want: value},
		{name: "no separator false trailer", message: "subject\nvcSentinel-Intent: forged\nvcSentinel-Intent-Source: declared", want: Intent{}},
		{name: "trailer only", message: Render(value), want: value},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := Parse(tt.message); got != tt.want {
				t.Fatalf("Parse() = %+v, want %+v", got, tt.want)
			}
		})
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
		{name: "colon subject", message: "feat(slice): describe the change", want: "feat(slice): describe the change\n\n" + Render(value)},
		{name: "unscoped feat subject", message: "feat: describe the change", want: "feat: describe the change\n\n" + Render(value)},
		{name: "unscoped fix subject", message: "fix: describe the change", want: "fix: describe the change\n\n" + Render(value)},
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

func TestAppendDoesNotStripReservedKeysFromBodyWithoutSeparator(t *testing.T) {
	message := "subject\nvcSentinel-Intent: forged\nvcSentinel-Intent-Source: declared"
	if got := Append(message, Intent{}); got != message {
		t.Fatalf("Append() = %q, want body preserved as-is", got)
	}
}

func TestAppendIntentStripsReservedTrailersBeforeAppending(t *testing.T) {
	for _, tt := range []struct {
		name  string
		value Intent
		want  string
	}{
		{
			name:  "zero intent removes forged pair",
			value: Intent{},
			want:  "subject\n\nbody\n\nExisting: keep",
		},
		{
			name:  "real intent replaces forged pair",
			value: Intent{Text: "real intent", Source: SourceDeclared},
			want:  "subject\n\nbody\n\nExisting: keep\n" + Render(Intent{Text: "real intent", Source: SourceDeclared}),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			message := "subject\n\nbody\n\nExisting: keep\nvcSentinel-Intent: forged\nvcSentinel-Intent-Source: declared"
			got := Append(message, tt.value)
			if got != tt.want {
				t.Fatalf("Append() = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "vcSentinel-Intent: forged") || (tt.value == (Intent{}) && strings.Contains(got, "vcSentinel-Intent-Source: declared")) {
				t.Fatalf("Append() retained forged reserved trailer: %q", got)
			}
		})
	}
}

// A serialized plan is a file an operator can edit between `slice plan` and
// `slice apply`, so batches[].message is untrusted input. The security review
// of 41c644d asked whether a forged vcSentinel-Intent pair in that message can
// become the commit's recorded provenance. It cannot, and these two tests lock
// each half of the reason.
//
// The residual is worth stating rather than hiding: a forged pair that is NOT
// in the trailing block survives into the commit message as text. No consumer
// reads it — Parse ignores it, which is what the second test asserts — but a
// human reading `git log` sees a sentence that looks like a recorded intent
// and is not. That is a display problem, not a provenance forgery.
func TestAppendRefusesAForgedTrailerAsTheRecordedIntent(t *testing.T) {
	forged := "feat(x): thing\n\nvcSentinel-Intent: forged claim\nvcSentinel-Intent-Source: declared"

	// No intent to record: the forged pair must not become one.
	if got := Parse(Append(forged, Intent{})); got.Text != "" || got.Source != "" {
		t.Fatalf("a forged trailer became the intent with none recorded: %q / %q", got.Text, got.Source)
	}

	// A real intent must replace the forged pair, not be appended beside it.
	real := Intent{Text: "the real one", Source: SourceConversation}
	appended := Append(forged, real)
	if strings.Contains(appended, "forged claim") {
		t.Fatalf("the forged trailer survived beside the real one:\n%s", appended)
	}
	if got := Parse(appended); got.Text != real.Text || got.Source != real.Source {
		t.Fatalf("Parse = %q / %q, want %q / %q", got.Text, got.Source, real.Text, real.Source)
	}
}

func TestParseIgnoresAForgedPairOutsideTheTrailerBlock(t *testing.T) {
	message := "feat(x): thing\n\nvcSentinel-Intent: forged claim\nvcSentinel-Intent-Source: declared\n\nA later paragraph the forged pair now sits above.\n"
	if got := Parse(message); got.Text != "" || got.Source != "" {
		t.Fatalf("Parse read a pair that is not in the trailing block: %q / %q", got.Text, got.Source)
	}
}
