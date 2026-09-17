package agentadapter

import (
	"strings"
	"testing"
)

// TestClaudeReviewMapsCacheWriteTokens pins the cache-write plumbing: a usage
// member shaped like testdata/claude/usage-probe.json must surface
// cache_creation_input_tokens on acpadapter.Usage, while a usage member
// without that key leaves the field nil (absent stays absent, never zero).
func TestClaudeReviewMapsCacheWriteTokens(t *testing.T) {
	cases := []struct {
		name      string
		stream    string
		wantWrite *int64
	}{
		{
			name:      "probe-shaped usage surfaces cache creation tokens",
			stream:    `{"type":"result","subtype":"success","stop_reason":"end_turn","result":"OK","usage":{"input_tokens":2,"cache_creation_input_tokens":17,"cache_read_input_tokens":6465,"output_tokens":4,"output_tokens_details":{"thinking_tokens":0}}}`,
			wantWrite: new(int64(17)),
		},
		{
			name:      "observed zero cache creation survives as a non-nil pointer",
			stream:    `{"type":"result","subtype":"success","stop_reason":"end_turn","result":"OK","usage":{"input_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":6465,"output_tokens":4,"output_tokens_details":{"thinking_tokens":0}}}`,
			wantWrite: new(int64(0)),
		},
		{
			name:      "usage without the cache-write key leaves the field nil",
			stream:    `{"type":"result","subtype":"success","stop_reason":"end_turn","result":"OK","usage":{"input_tokens":2,"cache_read_input_tokens":6465,"output_tokens":4,"output_tokens_details":{"thinking_tokens":0}}}`,
			wantWrite: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scan, err := scanClaudeReview(strings.NewReader(tc.stream))
			if err != nil {
				t.Fatalf("scanClaudeReview() error = %v", err)
			}
			if scan.Usage == nil {
				t.Fatal("Usage = nil, want a mapped usage member")
			}
			switch {
			case tc.wantWrite == nil && scan.Usage.CacheWriteInputTokens != nil:
				t.Errorf("CacheWriteInputTokens = %d, want nil (absent stays absent)", *scan.Usage.CacheWriteInputTokens)
			case tc.wantWrite != nil && scan.Usage.CacheWriteInputTokens == nil:
				t.Errorf("CacheWriteInputTokens = nil, want %d", *tc.wantWrite)
			case tc.wantWrite != nil && *scan.Usage.CacheWriteInputTokens != *tc.wantWrite:
				t.Errorf("CacheWriteInputTokens = %d, want %d", *scan.Usage.CacheWriteInputTokens, *tc.wantWrite)
			}
		})
	}
}
