package main

import "testing"

func TestEscapeANSI(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "escapes HTML text",
			input: `dashboard <preview> & details`,
			want:  `dashboard &lt;preview&gt; &amp; details`,
		},
		{
			name:  "converts xterm color cube and reset",
			input: "\x1b[38;5;196mred\x1b[0m",
			want:  `<span style="color:#ff0000">red</span>`,
		},
		{
			name:  "converts xterm grayscale and reset",
			input: "\x1b[38;5;255mgray\x1b[0m",
			want:  `<span style="color:#eeeeee">gray</span>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeANSI(tt.input); got != tt.want {
				t.Errorf("escapeANSI() = %q, want %q", got, tt.want)
			}
		})
	}
}
