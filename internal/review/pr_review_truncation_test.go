package review

import (
	"strings"
	"testing"
)

func TestTruncatePRReviewBodyPreservesRequiredSectionsAndCIStep(t *testing.T) {
	body := strings.Join([]string{
		"## Intent\nintent", "## What Changed\nchanged", "## Risk Assessment\nrisk", "## Testing\ntesting", "<!-- vas-sentinel-attestation:v1 {} -->", "## Pipeline",
		pipelineDetails("✅", "review", "reviewed", strings.Repeat("review evidence ", 300)),
		pipelineDetails("✅", "test", "tested", strings.Repeat("test evidence ", 300)),
		pipelineDetails("⚪", "ci", "not observed by Sentinel", "CI placeholder"),
	}, "\n")

	got, err := TruncatePRReviewBody(body, 2500)
	if err != nil {
		t.Fatalf("TruncatePRReviewBody() error = %v", err)
	}
	if len(got) > 2500 {
		t.Fatalf("truncated body = %d bytes, want <= 1400", len(got))
	}
	for _, required := range []string{"## Intent", "## What Changed", "## Risk Assessment", "<b>ci</b>", "</details>"} {
		if !strings.Contains(got, required) {
			t.Fatalf("truncated body is missing %q:\n%s", required, got)
		}
	}
	if strings.Count(got, "evidence blocks omitted for size") != 1 {
		t.Fatalf("truncated body must disclose omitted evidence exactly once:\n%s", got)
	}
}
