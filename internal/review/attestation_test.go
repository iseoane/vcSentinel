package review

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParseAttestation(t *testing.T) {
	valid := Attestation{
		HeadSHA: "abc123",
		Branch:  "feature/persisted-review",
		Verdict: VerdictOK,
		Steps: []AttestationStep{
			{Step: "review", Status: "passed"},
			{Step: "ci", Status: "not_observed"},
		},
	}
	comment, err := RenderAttestation(valid)
	if err != nil {
		t.Fatalf("RenderAttestation() error = %v", err)
	}

	tests := []struct {
		name    string
		body    string
		want    Attestation
		wantErr error
	}{
		{
			name: "valid attestation",
			body: "## Intent\n\n" + comment + "\n\n## Pipeline\n",
			want: valid,
		},
		{
			name:    "absent attestation",
			body:    "## Intent\n",
			wantErr: ErrAttestationAbsent,
		},
		{
			name:    "malformed JSON",
			body:    "<!-- vas-sentinel-attestation:v1 {not-json} -->",
			wantErr: ErrInvalidAttestation,
		},
		{
			name:    "unknown schema version",
			body:    "<!-- vas-sentinel-attestation:v2 {\"head_sha\":\"abc123\"} -->",
			wantErr: ErrUnsupportedAttestationSchema,
		},
		{
			name:    "multiple attestations",
			body:    comment + "\n" + comment,
			wantErr: ErrMultipleAttestations,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseAttestation(tt.body)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ParseAttestation() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseAttestation() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseAttestation() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRenderAttestationEscapesJSONWithinOneHTMLComment(t *testing.T) {
	comment, err := RenderAttestation(Attestation{
		HeadSHA: "abc123",
		Branch:  `feature/quote-"-safe`,
		Verdict: VerdictWarn,
		Steps:   []AttestationStep{{Step: "pr review", Status: "passed"}},
	})
	if err != nil {
		t.Fatalf("RenderAttestation() error = %v", err)
	}
	if !strings.HasPrefix(comment, "<!-- vas-sentinel-attestation:v1 {") || !strings.HasSuffix(comment, "} -->") {
		t.Fatalf("RenderAttestation() = %q, want one v1 HTML comment", comment)
	}
	if strings.Contains(comment, "\n") {
		t.Fatalf("RenderAttestation() = %q, want one line", comment)
	}
}
