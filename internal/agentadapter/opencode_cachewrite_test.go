package agentadapter

import (
	"strings"
	"testing"
)

// TestOpenCodeReviewSumsCacheWriteTokens pins the cache-write plumbing: the
// parser must sum cache.write across every step_finish event exactly as it
// sums cache.read, while a stream whose tokens carry no cache member leaves
// the field nil (absent stays absent, never zero).
func TestOpenCodeReviewSumsCacheWriteTokens(t *testing.T) {
	stream := "{\"type\":\"text\",\"part\":{\"type\":\"text\",\"text\":\"findings\"}}\n" +
		"{\"type\":\"step_finish\",\"part\":{\"type\":\"step-finish\",\"reason\":\"tool-calls\",\"tokens\":{\"total\":100,\"input\":80,\"output\":10,\"reasoning\":10,\"cache\":{\"write\":5,\"read\":0}}}}\n" +
		"{\"type\":\"step_finish\",\"part\":{\"type\":\"step-finish\",\"reason\":\"stop\",\"tokens\":{\"total\":200,\"input\":150,\"output\":20,\"reasoning\":30,\"cache\":{\"write\":7,\"read\":3}}}}\n"
	scan, err := scanOpenCodeReview(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("scanOpenCodeReview() error = %v", err)
	}
	if scan.Usage == nil {
		t.Fatal("Usage = nil, want summed usage")
	}
	if scan.Usage.CacheWriteInputTokens == nil {
		t.Fatal("CacheWriteInputTokens = nil, want the summed cache.write value")
	}
	if *scan.Usage.CacheWriteInputTokens != 12 {
		t.Errorf("CacheWriteInputTokens = %d, want 12 (5 + 7)", *scan.Usage.CacheWriteInputTokens)
	}
	if scan.Usage.CachedInputTokens == nil || *scan.Usage.CachedInputTokens != 3 {
		t.Errorf("CachedInputTokens = %+v, want 3 (0 + 3)", scan.Usage.CachedInputTokens)
	}
}

// TestOpenCodeReviewLeavesCacheWriteNilWithoutCacheMember pins the negative
// case: tokens without any cache member must map to a nil cache-write field,
// never a fabricated zero.
func TestOpenCodeReviewLeavesCacheWriteNilWithoutCacheMember(t *testing.T) {
	stream := "{\"type\":\"step_finish\",\"part\":{\"type\":\"step-finish\",\"reason\":\"stop\",\"tokens\":{\"input\":100}}}\n"
	scan, err := scanOpenCodeReview(strings.NewReader(stream))
	if err != nil {
		t.Fatalf("scanOpenCodeReview() error = %v", err)
	}
	if scan.Usage == nil {
		t.Fatal("Usage = nil, want the reported input tokens")
	}
	if scan.Usage.CacheWriteInputTokens != nil {
		t.Errorf("CacheWriteInputTokens = %d, want nil", *scan.Usage.CacheWriteInputTokens)
	}
}
