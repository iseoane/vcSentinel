package agentadapter

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// claudeErrorStdout is a Claude Code --output-format json failure carried on
// STDOUT: a result object with is_error, a subtype and the agent's own
// message, while stderr stays empty.
const claudeErrorStdout = `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"the model is overloaded, try again later"}`

// TestRunReviewSurfacesClaudeStdoutOnEmptyStderr pins the defect: a review
// invocation that exits non-zero with an EMPTY stderr and a Claude error
// result on stdout must surface that result's message instead of collapsing
// to the bare exit status.
func TestRunReviewSurfacesClaudeStdoutOnEmptyStderr(t *testing.T) {
	t.Setenv("VCSENTINEL_TEST_STDOUT_FAIL", claudeErrorStdout)
	adapter := CLIAdapter{BinaryName: compileAgentBinary(t, "claude"), Timeout: 10 * time.Second}

	_, err := adapter.runBoundedReview(context.Background(), ReviewRequest{Prompt: "audit", SnapshotDir: t.TempDir()}, 10*time.Second)
	if err == nil {
		t.Fatal("runBoundedReview() = nil error, want the failing invocation to surface the agent's own output")
	}
	if !strings.Contains(err.Error(), "the model is overloaded, try again later") {
		t.Errorf("error = %q, want it to surface the agent's own stdout message instead of the bare exit status", err)
	}
	if !strings.Contains(err.Error(), "error_during_execution") {
		t.Errorf("error = %q, want it to name the result subtype", err)
	}
}

// TestRunReviewKeepsBareExitWhenStdoutUnusable pins the fallback boundary: an
// empty, non-JSON, or message-free stdout must keep today's behaviour
// exactly — the bare exit status, with no invented detail.
func TestRunReviewKeepsBareExitWhenStdoutUnusable(t *testing.T) {
	cases := []struct {
		name      string
		stdout    string
		silentOne bool
	}{
		{name: "empty stdout", silentOne: true},
		{name: "whitespace-only stdout", stdout: "  \n "},
		{name: "non-JSON stdout", stdout: "not json at all {{{"},
		{name: "JSON stdout without a message", stdout: `{"type":"result","subtype":"success","result":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.silentOne {
				t.Setenv("VCSENTINEL_TEST_EXIT_ONE", "1")
			} else {
				t.Setenv("VCSENTINEL_TEST_STDOUT_FAIL", tc.stdout)
			}
			adapter := CLIAdapter{BinaryName: compileAgentBinary(t, "claude"), Timeout: 10 * time.Second}

			_, err := adapter.runBoundedReview(context.Background(), ReviewRequest{Prompt: "audit", SnapshotDir: t.TempDir()}, 10*time.Second)
			if err == nil {
				t.Fatal("runBoundedReview() = nil error, want the bare exit status")
			}
			if err.Error() != "exit status 1" {
				t.Errorf("error = %q, want exactly the bare %q (no invented detail)", err.Error(), "exit status 1")
			}
		})
	}
}

// TestRunReviewPrefersStderrOverClaudeStdout pins the source order: a
// non-empty stderr keeps today's text unchanged, with stdout only as the
// fallback when stderr yields nothing.
func TestRunReviewPrefersStderrOverClaudeStdout(t *testing.T) {
	t.Setenv("VCSENTINEL_TEST_FAIL", "authentication expired")
	t.Setenv("VCSENTINEL_TEST_STDOUT_FAIL", claudeErrorStdout)
	adapter := CLIAdapter{BinaryName: compileAgentBinary(t, "claude"), Timeout: 10 * time.Second}

	_, err := adapter.runBoundedReview(context.Background(), ReviewRequest{Prompt: "audit", SnapshotDir: t.TempDir()}, 10*time.Second)
	if err == nil {
		t.Fatal("runBoundedReview() = nil error, want the stderr detail to surface")
	}
	if !strings.Contains(err.Error(), "authentication expired") {
		t.Errorf("error = %q, want the stderr detail with today's text unchanged", err)
	}
	if strings.Contains(err.Error(), "the model is overloaded") {
		t.Errorf("error = %q, stderr must win over the stdout fallback", err)
	}
}

// TestRunReviewTimeoutSurfacesClaudeStdoutKeepsDeadline pins the timeout
// branch: it gains the stdout detail while errors.Is(err,
// context.DeadlineExceeded) stays true end to end, because reviewexec's
// classifier depends on it to record a timeout rather than a failure.
func TestRunReviewTimeoutSurfacesClaudeStdoutKeepsDeadline(t *testing.T) {
	t.Setenv("VCSENTINEL_TEST_STDOUT_FAIL", claudeErrorStdout)
	t.Setenv("VCSENTINEL_TEST_SLEEP", "30")
	adapter := CLIAdapter{BinaryName: compileAgentBinary(t, "claude"), Timeout: 200 * time.Millisecond}

	_, err := adapter.runBoundedReview(context.Background(), ReviewRequest{Prompt: "audit", SnapshotDir: t.TempDir()}, 200*time.Millisecond)
	if err == nil {
		t.Fatal("runBoundedReview() = nil error, want the deadline to fire")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v; it must wrap context.DeadlineExceeded so the durable record classifies it as a timeout instead of a failure", err)
	}
	if !strings.Contains(err.Error(), "timed out after") {
		t.Errorf("error = %q, want the timeout wording unchanged", err)
	}
	if !strings.Contains(err.Error(), "the model is overloaded, try again later") {
		t.Errorf("error = %q, want the agent's own stdout message alongside the timeout", err)
	}
}

// TestRunReviewBoundsClaudeStdoutDetail pins the untrusted-output bound: raw
// provider output must not flood the record. The shape follows
// internal/review's truncateCause (bound by runes, ellipsis marks the cut)
// rather than inventing a second rule.
func TestRunReviewBoundsClaudeStdoutDetail(t *testing.T) {
	long := strings.Repeat("x", 500)
	t.Setenv("VCSENTINEL_TEST_STDOUT_FAIL", `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"`+long+`"}`)
	adapter := CLIAdapter{BinaryName: compileAgentBinary(t, "claude"), Timeout: 10 * time.Second}

	_, err := adapter.runBoundedReview(context.Background(), ReviewRequest{Prompt: "audit", SnapshotDir: t.TempDir()}, 10*time.Second)
	if err == nil {
		t.Fatal("runBoundedReview() = nil error, want the failing invocation to surface a bounded detail")
	}
	if strings.Contains(err.Error(), long) {
		t.Errorf("error carries the full %d-rune provider output unbounded, want it cut to the detail bound", len([]rune(long)))
	}
	if !strings.Contains(err.Error(), "…") {
		t.Errorf("error = %q, want the ellipsis marking the bound cut", err)
	}
}
