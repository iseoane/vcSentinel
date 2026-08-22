package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// fakeRunsAgent satisfies both the base AgentAdapter contract and the prompt
// seam, standing in for the configured agent chain during CLI tests.
type fakeRunsAgent struct {
	output string
	err    error
}

func (a *fakeRunsAgent) ObtenerMensajeCommit([]string, string, int) (string, error) {
	return "", errors.New("fakeRunsAgent: commit messages are not part of these tests")
}

func (a *fakeRunsAgent) EjecutarPrompt(string) (string, error) {
	if a.err != nil {
		return "", a.err
	}
	return a.output, nil
}

var _ agentadapter.AdaptadorPrompt = (*fakeRunsAgent)(nil)

// runsSeedAdapter drives a real controller to seed durable evidence without
// involving the CLI's agent chain.
type runsSeedAdapter struct {
	result execution.AdapterResult
	err    error
}

func (a runsSeedAdapter) Execute(context.Context, agentrun.LogicalJob, agentrun.InvocationEnvelope, string) (execution.AdapterResult, error) {
	return a.result, a.err
}

// replaceRunsAgent swaps the construction seam for a deterministic double.
func replaceRunsAgent(t *testing.T, agent agentadapter.AdaptadorPrompt) {
	t.Helper()
	previous := newAgentForRuns
	newAgentForRuns = func(string) (agentadapter.AdaptadorPrompt, error) { return agent, nil }
	t.Cleanup(func() { newAgentForRuns = previous })
}

// seedDurableRun admits one run through a real controller against the temp
// repository's common-dir store and waits until its state settles.
func seedDurableRun(t *testing.T, worktree, name string, adapter execution.Adapter) agentrun.Identity {
	t.Helper()
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	controller := execution.NewController(store.NuevoStore(commonDir), adapter)
	request := agentrun.NewRunRequest(
		agentrun.Candidate(fmt.Sprintf("seed:%s:%s", strings.ToLower(name), name)),
		agentrun.Prompt("seed prompt for "+name), nil)
	handle, err := controller.Start(context.Background(), request, store.RunPolicy{ID: "policy:test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := observeUntilSettled(context.Background(), controller, handle.RunID); err != nil {
		t.Fatal(err)
	}
	return handle.RunID
}

func captureRunsOutput(t *testing.T, command func(io.Writer) int) (string, int) {
	t.Helper()
	var salida bytes.Buffer
	code := command(&salida)
	return salida.String(), code
}

func decodeRunsJSON(t *testing.T, payload string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, payload)
	}
	return decoded
}

func TestRunExitCodeMapsEveryDocumentedSentinel(t *testing.T) {
	corruption := store.EventCorruptionError{RunID: "r", Sequence: 1, Reason: "bad hash"}
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil is success", nil, runExitSuccess},
		{"missing execution is not found", fmt.Errorf("wrapped: %w", store.ErrExecutionNotFound), runExitRunNotFound},
		{"stale revision", fmt.Errorf("wrapped: %w", execution.ErrStaleRevision), runExitStaleRevision},
		{"not active", execution.ErrRunNotActive, runExitInvalidState},
		{"not retryable", execution.ErrRunNotRetryable, runExitInvalidState},
		{"not recoverable", execution.ErrRunNotRecoverable, runExitInvalidState},
		{"decision not pending", execution.ErrDecisionNotPending, runExitInvalidState},
		{"unsupported action", execution.ErrUnsupportedAction, runExitInvalidState},
		{"duplicate admission", execution.ErrRunAlreadyExists, runExitInvalidState},
		{"event corruption is infrastructure", fmt.Errorf("wrapped: %w", corruption), runExitInfrastructure},
		{"incomplete tail is infrastructure", fmt.Errorf("wrapped: %w", store.ErrIncompleteEventTail), runExitInfrastructure},
		{"plain failures are infrastructure", errors.New("disk exploded"), runExitInfrastructure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runExitCode(tt.err); got != tt.want {
				t.Fatalf("runExitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestExecuteRunsUsageErrorsExitOne(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)

	tests := []struct {
		name    string
		args    []string
		wantUso bool
	}{
		{"bare dispatch prints usage", nil, true},
		{"unknown subcommand", []string{"teleport"}, true},
		{"logs without --run", []string{"logs"}, false},
		{"unknown flag", []string{"status", "--filter", "x"}, false},
		{"flag missing value", []string{"abort", "--run"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			output, code := captureRunsOutput(t, func(w io.Writer) int {
				return executeRuns(w, worktree, tt.args)
			})
			if code != runExitUsage {
				t.Fatalf("exit code = %d, want %d (usage): %s", code, runExitUsage, output)
			}
			if tt.wantUso && !strings.Contains(output, "sentinel runs") {
				t.Fatalf("output lacks a usage line: %s", output)
			}
			if !tt.wantUso && strings.TrimSpace(output) == "" {
				t.Fatal("usage rejection printed nothing; operators need the concrete reason")
			}
		})
	}
}

func TestRunsStartAdmitsAndCompletesThroughConfiguredAgent(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	replaceRunsAgent(t, &fakeRunsAgent{output: "operator output"})

	first, code := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"start", "--prompt", "do the thing", "--json"})
	})
	if code != runExitSuccess {
		t.Fatalf("start exit code = %d, want %d: %s", code, runExitSuccess, first)
	}
	decoded := decodeRunsJSON(t, first)
	for _, field := range []string{"run_id", "job_id", "invocation_id", "state"} {
		if _, ok := decoded[field]; !ok {
			t.Fatalf("start JSON lacks stable field %q: %s", field, first)
		}
	}
	if decoded["state"] != "succeeded" {
		t.Fatalf("start state = %v, want succeeded", decoded["state"])
	}

	second, secondCode := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"start", "--prompt", "do the thing", "--json"})
	})
	if secondCode != runExitSuccess {
		t.Fatalf("second start exit code = %d: %s", secondCode, second)
	}
	if got := decodeRunsJSON(t, second)["run_id"]; got == decoded["run_id"] {
		t.Fatalf("two starts produced the same run identity %v; candidates must be salted per invocation", got)
	}
}

func TestRunsStatusListAndDetailAreShapeStable(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	runID := seedDurableRun(t, worktree, "stable-success",
		runsSeedAdapter{result: execution.AdapterResult{Output: "done"}})

	listOnce, listCode := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"status", "--json"})
	})
	listTwice, listTwiceCode := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, []string{"status", "--json"})
	})
	if listCode != runExitSuccess || listTwiceCode != runExitSuccess {
		t.Fatalf("status exit codes = %d/%d, want success", listCode, listTwiceCode)
	}
	if listOnce != listTwice {
		t.Fatalf("--json shape drifted between identical invocations:\n%s\n---\n%s", listOnce, listTwice)
	}
	var listing struct {
		Runs []runsListEntry `json:"runs"`
	}
	if err := json.Unmarshal([]byte(listOnce), &listing); err != nil {
		t.Fatalf("list JSON invalid: %v", err)
	}
	if len(listing.Runs) != 1 {
		t.Fatalf("listed %d runs, want exactly the seeded one: %s", len(listing.Runs), listOnce)
	}
	entry := listing.Runs[0]
	if entry.RunID != string(runID) || entry.State != "succeeded" ||
		entry.OutcomeClass != "success" || entry.Revision != 4 {
		t.Fatalf("listing entry = %+v, want the seeded succeeded run at revision 4", entry)
	}

	detailArgs := []string{"status", "--run", string(runID), "--json"}
	detailOnce, detailCode := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, detailArgs)
	})
	detailTwice, _ := captureRunsOutput(t, func(w io.Writer) int {
		return executeRuns(w, worktree, detailArgs)
	})
	if detailCode != runExitSuccess {
		t.Fatalf("detail exit code = %d: %s", detailCode, detailOnce)
	}
	if detailOnce != detailTwice {
		t.Fatalf("detail JSON drifted between identical invocations:\n%s\n---\n%s", detailOnce, detailTwice)
	}
	summary := decodeRunsJSON(t, detailOnce)
	for _, field := range []string{"run_id", "state", "sequence", "revision", "event_count", "outcomes", "responses"} {
		if _, ok := summary[field]; !ok {
			t.Fatalf("detail JSON lacks stable field %q: %s", field, detailOnce)
		}
	}
	outcomes, ok := summary["outcomes"].([]any)
	if !ok || len(outcomes) != 1 {
		t.Fatalf("detail outcomes = %#v, want one admitted attempt", summary["outcomes"])
	}
	if summary["state"] != "succeeded" || summary["revision"] != float64(4) {
		t.Fatalf("detail summary = %v, want succeeded at revision 4", summary)
	}
}

func TestRunsLogsPaginationResumesWithoutGapsOrDuplicates(t *testing.T) {
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	runID := seedDurableRun(t, worktree, "paged-run",
		runsSeedAdapter{result: execution.AdapterResult{Output: "paged"}})

	type logsPage struct {
		RunID      string             `json:"run_id"`
		Events     []store.EventFrame `json:"events"`
		HasMore    bool               `json:"has_more"`
		NextCursor *uint64            `json:"next_cursor"`
	}

	var seen []store.EventFrame
	cursor := uint64(0)
	pages := 0
	for {
		args := []string{"logs", "--run", string(runID), "--after", fmt.Sprintf("%d", cursor), "--limit", "2", "--json"}
		output, code := captureRunsOutput(t, func(w io.Writer) int {
			return executeRuns(w, worktree, args)
		})
		if code != runExitSuccess {
			t.Fatalf("logs exit code = %d: %s", code, output)
		}
		var page logsPage
		if err := json.Unmarshal([]byte(output), &page); err != nil {
			t.Fatalf("logs JSON invalid: %v\n%s", err, output)
		}
		if page.RunID != string(runID) {
			t.Fatalf("logs run_id = %q, want %q", page.RunID, runID)
		}
		seen = append(seen, page.Events...)
		pages++
		if !page.HasMore {
			if page.NextCursor != nil {
				t.Fatalf("exhausted page must report next_cursor null, got %d", *page.NextCursor)
			}
			break
		}
		if page.NextCursor == nil || *page.NextCursor <= cursor {
			t.Fatalf("next_cursor = %v must advance past %d", page.NextCursor, cursor)
		}
		cursor = *page.NextCursor
		if pages > 10 {
			t.Fatal("pagination never terminated")
		}
	}
	if pages < 2 {
		t.Fatalf("expected at least two pages with limit 2, got %d", pages)
	}
	// A seeded succeeded run carries exactly four events: created→queued,
	// queued→admitted, admitted→running, running→succeeded.
	wantSequences := make([]uint64, 4)
	for i := range wantSequences {
		wantSequences[i] = uint64(i + 1)
	}
	gotSequences := make([]uint64, 0, len(seen))
	for _, frame := range seen {
		gotSequences = append(gotSequences, frame.Sequence)
	}
	if !reflect.DeepEqual(gotSequences, wantSequences) {
		t.Fatalf("paged sequences = %v, want gap-free %v with no duplicates", gotSequences, wantSequences)
	}
}
