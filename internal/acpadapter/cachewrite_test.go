package acpadapter

import (
	"strings"
	"testing"
)

// TestParseStreamLeavesCacheWriteNilWithoutWireMember pins the acpx side of
// the cache-write contract: the captured wire (fixtures end-turn.jsonl and
// cancelled.jsonl, plus the helper-process transcripts) reports no
// cache-write member, so parsing a terminal usage without one must leave the
// field nil — absent stays absent, never zero. No key is invented here.
func TestParseStreamLeavesCacheWriteNilWithoutWireMember(t *testing.T) {
	stream := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn","usage":{"inputTokens":42,"outputTokens":3,"totalTokens":45,"cachedInputTokens":7}}}`,
	}, "\n") + "\n"
	got := ParseStream(strings.NewReader(stream), DefaultLineCapBytes)
	if got.Usage == nil {
		t.Fatal("Usage = nil, want known terminal usage")
	}
	if got.Usage.CacheWriteInputTokens != nil {
		t.Errorf("CacheWriteInputTokens = %d, want nil (the acpx wire reports no cache-write member)", *got.Usage.CacheWriteInputTokens)
	}
	if got.Usage.CachedInputTokens == nil || *got.Usage.CachedInputTokens != 7 {
		t.Errorf("CachedInputTokens = %+v, want 7", got.Usage.CachedInputTokens)
	}
}
