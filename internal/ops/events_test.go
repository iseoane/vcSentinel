package ops

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

var _ func(string, string, int, []string, EventDetail, string) error = RecordEvent

func TestRecordAndReadEvents(t *testing.T) {
	dir := t.TempDir()
	writeEventLines(t, dir,
		`{"at":"2026-01-01T00:00:00Z","cmd":"check","exit":0,"detail":{}}`,
		`{"at":"2026-01-01T00:00:01Z","cmd":"review","exit":4,"shas":["abc123"],"detail":"provider_unavailable","worktree":"C:\\repo"}`,
	)

	events, err := RecentEvents(dir, 10)
	if err != nil {
		t.Fatalf("RecentEvents returned error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	// Newest first.
	if events[0].Cmd != "review" || events[0].Exit != 4 || events[0].Detail != "provider_unavailable" {
		t.Errorf("newest event = %+v, does not match", events[0])
	}
	if len(events[0].Shas) != 1 || events[0].Shas[0] != "abc123" {
		t.Errorf("shas = %+v, want [abc123]", events[0].Shas)
	}
	if events[1].Cmd != "check" {
		t.Errorf("previous event = %+v, want check", events[1])
	}
}

func TestRecentEventsLimit(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := RecordEvent(dir, "status", 0, nil, EventDetail{}, ""); err != nil {
			t.Fatal(err)
		}
	}

	events, err := RecentEvents(dir, 3)
	if err != nil {
		t.Fatalf("RecentEvents returned error: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
}

func TestRecentEventsNoLog(t *testing.T) {
	dir := t.TempDir()

	events, err := RecentEvents(dir, 10)
	if err != nil {
		t.Fatalf("RecentEvents returned error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("events = %d, want 0 without log", len(events))
	}
}

func TestRecordManyEventsWithoutCorruption(t *testing.T) {
	dir := t.TempDir()

	for i := 0; i < 1000; i++ {
		if err := RecordEvent(dir, "check", 0, nil, EventDetail{}, ""); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	events, err := RecentEvents(dir, 0)
	if err != nil {
		t.Fatalf("RecentEvents returned error: %v", err)
	}
	if len(events) != 1000 {
		t.Errorf("events = %d, want 1000", len(events))
	}
}

func TestRotateEventsKeepsLastLines(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := RecordEvent(dir, "status", 0, nil, EventDetail{}, ""); err != nil {
			t.Fatal(err)
		}
	}

	if err := RotateEvents(dir, 2); err != nil {
		t.Fatalf("RotateEvents returned error: %v", err)
	}
	events, err := RecentEvents(dir, 10)
	if err != nil {
		t.Fatalf("RecentEvents returned error: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("events = %d, want 2 after rotating", len(events))
	}

	// The log is still appendable after the rotation.
	if err := RecordEvent(dir, "review", 1, nil, EventDetail{}, ""); err != nil {
		t.Fatalf("RecordEvent after rotating returned error: %v", err)
	}
	events, err = RecentEvents(dir, 10)
	if err != nil {
		t.Fatalf("RecentEvents returned error: %v", err)
	}
	if len(events) != 3 || events[0].Cmd != "review" {
		t.Errorf("events = %d, the newest %q, want 3 with review", len(events), events[0].Cmd)
	}
}

func TestRotateEventsNoLogIsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := RotateEvents(dir, 100); err != nil {
		t.Fatalf("RotateEvents without log returned error: %v", err)
	}
}

func TestRotateEventsDiscardsCorruptLines(t *testing.T) {
	dir := t.TempDir()
	if err := RecordEvent(dir, "status", 0, nil, EventDetail{}, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, eventsRelPath)
	if err := appendLine(path, "{\"corrupto\""); err != nil {
		t.Fatal(err)
	}
	if err := RecordEvent(dir, "review", 0, nil, EventDetail{}, ""); err != nil {
		t.Fatal(err)
	}

	if err := RotateEvents(dir, 10); err != nil {
		t.Fatalf("RotateEvents returned error: %v", err)
	}
	// The rotation discards corrupt lines: only the events that parse
	// remain, in order.
	events, err := RecentEvents(dir, 10)
	if err != nil {
		t.Fatalf("RecentEvents returned error: %v", err)
	}
	if len(events) != 2 {
		t.Errorf("events = %d, want 2 (the corrupt one was discarded during rotation)", len(events))
	}
	// Newest first (review was recorded after status).
	if events[0].Cmd != "review" || events[1].Cmd != "status" {
		t.Errorf("order = %q, %q; want review, status", events[0].Cmd, events[1].Cmd)
	}
	// The file no longer contains the corrupt line.
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "corrupto") {
		t.Errorf("the corrupt line is still in the file after the rotation")
	}
}

func TestPurgeEventsOfDeletesOnlyRequestedSHAs(t *testing.T) {
	dir := t.TempDir()
	if err := RecordEvent(dir, "review", 4, []string{"aaa111"}, EventDetail{"reason": "provider_unavailable"}, ""); err != nil {
		t.Fatal(err)
	}
	if err := RecordEvent(dir, "review", 0, []string{"bbb222"}, EventDetail{}, ""); err != nil {
		t.Fatal(err)
	}
	if err := RecordEvent(dir, "status", 0, nil, EventDetail{}, ""); err != nil {
		t.Fatal(err)
	}

	removed, err := PurgeEventsOf(dir, []string{"aaa111"})
	if err != nil {
		t.Fatalf("PurgeEventsOf returned error: %v", err)
	}
	if removed != 1 {
		t.Errorf("removed lines = %d, want 1", removed)
	}

	events, err := RecentEvents(dir, 10)
	if err != nil {
		t.Fatalf("RecentEvents returned error: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 (the ones of still-alive commits are kept)", len(events))
	}
	// The aaa111 event disappeared; the bbb222 one and the status remain.
	if events[0].Cmd != "status" || events[1].Cmd != "review" || events[1].Shas[0] != "bbb222" {
		t.Errorf("remaining events = %+v, do not match", events)
	}

	// The log is still appendable after the purge.
	if err := RecordEvent(dir, "check", 0, nil, EventDetail{}, ""); err != nil {
		t.Fatalf("RecordEvent after purge returned error: %v", err)
	}
}

func TestPurgeEventsOfNoSHAsIsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := RecordEvent(dir, "review", 0, []string{"aaa111"}, EventDetail{}, ""); err != nil {
		t.Fatal(err)
	}
	removed, err := PurgeEventsOf(dir, nil)
	if err != nil || removed != 0 {
		t.Errorf("PurgeEventsOf(nil) = %d, %v; want 0, nil", removed, err)
	}
	events, _ := RecentEvents(dir, 10)
	if len(events) != 1 {
		t.Errorf("events = %d, want 1 (unchanged)", len(events))
	}
}

func TestPurgeEventsOfNoLogIsNoOp(t *testing.T) {
	dir := t.TempDir()
	removed, err := PurgeEventsOf(dir, []string{"aaa111"})
	if err != nil || removed != 0 {
		t.Errorf("PurgeEventsOf without log = %d, %v; want 0, nil", removed, err)
	}
}

func appendLine(path, line string) error {
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(line + "\n")
	return err
}

func writeEventLines(t *testing.T, gitDir string, lines ...string) {
	t.Helper()
	path := filepath.Join(gitDir, eventsRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	contents := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
}

// writeTestPrCreateEvent appends a test pr-create event with a PR number.
func writeTestPrCreateEvent(t *testing.T, gitDir string, number string, hasURL bool) {
	t.Helper()
	detail := EventDetail{"fallback": !hasURL, "chain_pr": false}
	if hasURL {
		detail["pr_url"] = "https://github.com/demo/repo/pull/" + number
	}
	if err := RecordEvent(gitDir, "pr-create", 0, nil, detail, "C:\\repo"); err != nil {
		t.Fatalf("could not record the event: %v", err)
	}
}

// TestPurgeEventsOfResolvedPRs: merged or closed PRs are purged from the
// log; open or draft ones are kept.
func TestPurgeEventsOfResolvedPRs(t *testing.T) {
	gitDir := t.TempDir()
	writeTestPrCreateEvent(t, gitDir, "10", true) // MERGED → purge
	writeTestPrCreateEvent(t, gitDir, "11", true) // OPEN → keep
	writeTestPrCreateEvent(t, gitDir, "12", true) // CLOSED → purge
	writeTestPrCreateEvent(t, gitDir, "13", true) // DRAFT → keep

	states := map[string]string{
		"10": "MERGED",
		"11": "OPEN",
		"12": "CLOSED",
		"13": "DRAFT",
	}
	res, err := PurgeEventsOfResolvedPRs(gitDir, func(number int) (string, error) {
		return states[strconv.Itoa(number)], nil
	})
	if err != nil {
		t.Fatalf("Purge... failed: %v", err)
	}
	if res.Purged != 2 {
		t.Errorf("Purged = %d, want 2 (10 and 12)", res.Purged)
	}
	if res.Kept != 2 {
		t.Errorf("Kept = %d, want 2 (11 and 13)", res.Kept)
	}

	events, err := RecentEvents(gitDir, 0)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("%d events remained, want 2", len(events))
	}
	for _, ev := range events {
		text := detailText(t, ev.Detail)
		if !strings.Contains(text, "/pull/11") && !strings.Contains(text, "/pull/13") {
			t.Errorf("a wrong event survived: %s", text)
		}
	}

	// The log is still appendable after the PR purge.
	if err := RecordEvent(gitDir, "pr-review", 0, []string{"zzz999"}, EventDetail{}, ""); err != nil {
		t.Fatalf("RecordEvent after PR purge returned error: %v", err)
	}
	events, err = RecentEvents(gitDir, 10)
	if err != nil || len(events) != 3 {
		t.Errorf("events = %d, %v; want 3 after re-appending", len(events), err)
	}
}

// TestPurgeFallbackWithoutURLIsKept: a fallback record (without URL) is not
// verifiable with gh and is kept intact, with a warning.
func TestPurgeFallbackWithoutURLIsKept(t *testing.T) {
	gitDir := t.TempDir()
	writeTestPrCreateEvent(t, gitDir, "0", false)

	res, err := PurgeEventsOfResolvedPRs(gitDir, func(number int) (string, error) {
		t.Error("must not consult gh for a record without URL")
		return "OPEN", nil
	})
	if err != nil {
		t.Fatalf("Purge... failed: %v", err)
	}
	if res.Kept != 1 || res.Purged != 0 {
		t.Errorf("Kept = %d Purged = %d, want 1/0", res.Kept, res.Purged)
	}
	if len(res.Warnings) != 1 {
		t.Errorf("Warnings = %d, want 1 (unverifiable record)", len(res.Warnings))
	}
}

// TestPurgeGHErrorKeeps: without gh or without network, the purge is
// best-effort: the failure is warned and the records are kept (never destroyed
// by uncertainty).
func TestPurgeGHErrorKeeps(t *testing.T) {
	gitDir := t.TempDir()
	writeTestPrCreateEvent(t, gitDir, "20", true)
	writeTestPrCreateEvent(t, gitDir, "21", true)

	res, err := PurgeEventsOfResolvedPRs(gitDir, func(number int) (string, error) {
		return "", errors.New("gh missing or no network")
	})
	if err != nil {
		t.Fatalf("Purge... must not fail when gh is absent: %v", err)
	}
	if res.Purged != 0 || res.Kept != 2 {
		t.Errorf("Purged = %d Kept = %d, want 0/2", res.Purged, res.Kept)
	}
	if len(res.Warnings) != 2 {
		t.Errorf("Warnings = %d, want 2", len(res.Warnings))
	}
	if _, err := os.Stat(filepath.Join(gitDir, eventsRelPath)); err != nil {
		t.Fatalf("the log must still exist: %v", err)
	}
}

// TestWriteTemporaryLogNoPreviousLog: without a previous log the temporary
// file becomes the log (no .bak is created).
func TestWriteTemporaryLogNoPreviousLog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}

	if err := writeTemporaryLogRenaming(path, [][]byte{[]byte("a"), []byte("b")}, os.Rename); err != nil {
		t.Fatalf("writeTemporaryLog without previous log failed: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the log must exist after the write: %v", err)
	}
	if string(data) != "a\nb\n" {
		t.Errorf("log = %q, want %q", data, "a\nb\n")
	}
	if _, err := os.Stat(path + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("without previous log no .bak must remain: %v", err)
	}
}

// TestWriteTemporaryLogRestoresBackup: if the rename temp→path fails, the
// .bak is restored and the original log remains intact.
func TestWriteTemporaryLogRestoresBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}

	err := writeTemporaryLogRenaming(path, [][]byte{[]byte("new")}, func(source, dest string) error {
		if strings.HasSuffix(source, ".tmp") {
			return errors.New("simulated: rename temp→path fails")
		}
		return os.Rename(source, dest)
	})
	if err == nil {
		t.Fatal("expected an error on failed rename")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original\n" {
		t.Errorf("the original log must be restored, got %q, %v", data, err)
	}
	if _, err := os.Stat(path + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the .bak must be consumed in the restoration: %v", err)
	}
}

// TestWriteTemporaryLogDoubleFailureReportsBak: if the restoration also
// fails, the error reports where the backup ended up (manual recovery).
func TestWriteTemporaryLogDoubleFailureReportsBak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}

	err := writeTemporaryLogRenaming(path, [][]byte{[]byte("new")}, func(source, dest string) error {
		if strings.HasSuffix(source, ".tmp") || strings.HasSuffix(source, ".bak") {
			return errors.New("simulated: rename fails")
		}
		return os.Rename(source, dest)
	})
	if err == nil {
		t.Fatal("expected an error on double rename failure")
	}
	if !strings.Contains(err.Error(), ".bak") {
		t.Errorf("the error must indicate where the backup is: %v", err)
	}
	if _, err := os.Stat(path + ".bak"); err != nil {
		t.Errorf("the original must remain safe as .bak: %v", err)
	}
}

// TestWriteTemporaryLogToleratesResidualBak: a residual .bak from an
// interrupted execution does not block the next write (Windows does not
// rename over an existing destination) and is cleaned up at the end.
func TestWriteTemporaryLogToleratesResidualBak(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("original\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".bak", []byte("old data\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := writeTemporaryLogRenaming(path, [][]byte{[]byte("new")}, os.Rename); err != nil {
		t.Fatalf("a residual .bak must not block the write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new\n" {
		t.Errorf("log = %q, %v; want %q", data, err, "new\n")
	}
	if _, err := os.Stat(path + ".bak"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the residual .bak must be cleaned up: %v", err)
	}
}

// TestPurgeCorruptDetailKeepsAndDoesNotAbort: a record with an invalid detail
// is kept with a warning and the rest of the purge moves on (best-effort).
func TestPurgeCorruptDetailKeepsAndDoesNotAbort(t *testing.T) {
	gitDir := t.TempDir()
	writeTestPrCreateEvent(t, gitDir, "30", true) // MERGED → purge
	// Corrupt records (not PRCreateDetail JSON), seeded as historical JSONL.
	path := filepath.Join(gitDir, eventsRelPath)
	if err := appendLine(path, `{"at":"2026-01-01T00:00:01Z","cmd":"pr-create","exit":0,"detail":"not json"}`); err != nil {
		t.Fatal(err)
	}
	if err := appendLine(path, `{"at":"2026-01-01T00:00:02Z","cmd":"pr-create","exit":0,"detail":"{\"pr_url\": \"incomplete\"}"}`); err != nil {
		t.Fatal(err)
	}

	res, err := PurgeEventsOfResolvedPRs(gitDir, func(number int) (string, error) {
		return "MERGED", nil
	})
	if err != nil {
		t.Fatalf("a corrupt detail must not abort the purge: %v", err)
	}
	if res.Purged != 1 || res.Kept != 2 {
		t.Errorf("Purged = %d Kept = %d, want 1/2", res.Purged, res.Kept)
	}
	if len(res.Warnings) != 2 {
		t.Fatalf("Warnings = %d, want 2 (the invalid records are warned)", len(res.Warnings))
	}
	warningText := strings.Join(res.Warnings, "\n")
	if !strings.Contains(warningText, "invalid") || !strings.Contains(warningText, "kept") {
		t.Errorf("the warnings must explain why records are kept: %v", res.Warnings)
	}
	events, err := RecentEvents(gitDir, 0)
	if err != nil || len(events) != 2 {
		t.Errorf("events = %d, %v; want 2 kept", len(events), err)
	}
}

// TestPullRequestNumber: tolerates suffixes after the number (/pull/10/files).
func TestPullRequestNumber(t *testing.T) {
	cases := map[string]int{
		"https://github.com/a/b/pull/42":         42,
		"https://github.com/a/b/pull/42/":        42,
		"https://github.com/a/b/pull/42/files":   42,
		"https://github.com/a/b/pull/42/commits": 42,
	}
	for url, want := range cases {
		n, ok := pullRequestNumber(url)
		if !ok || n != want {
			t.Errorf("pullRequestNumber(%q) = %d, %v; want %d", url, n, ok, want)
		}
	}
	if _, ok := pullRequestNumber("https://github.com/a/b/issues/7"); ok {
		t.Error("pullRequestNumber accepted a URL without /pull/")
	}
	if _, ok := pullRequestNumber("https://github.com/a/b/pull/abc"); ok {
		t.Error("pullRequestNumber accepted a non-numeric number")
	}
}

// TestVerifyRecordsPrVerify: with GitDir, Verify records the pr-verify event
// in the three modes with the detail of schema §13.
func TestVerifyRecordsPrVerify(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		gitDir := t.TempDir()
		_, err := Verify(VerifyOptions{
			GitDir: gitDir,
			Cfg: config.Config{
				LintCommands: []string{"go vet ./..."},
				TestCommands: []string{"go test ./..."},
			},
			Run: func(command string) (int, error) {
				if strings.Contains(command, "test") {
					return 1, nil
				}
				return 0, nil
			},
		})
		if err != nil {
			t.Fatalf("Verify failed: %v", err)
		}
		events, _ := RecentEvents(gitDir, 1)
		if len(events) != 1 || events[0].Cmd != "pr-verify" {
			t.Fatalf("no pr-verify event: %+v", events)
		}
		if events[0].Exit != 1 {
			t.Errorf("Exit = %d, want 1 (the worst exit code)", events[0].Exit)
		}
		if !strings.Contains(detailText(t, events[0].Detail), `"exit":1`) {
			t.Errorf("the detail does not report the per-command exit: %s", detailText(t, events[0].Detail))
		}
	})

	t.Run("delegated", func(t *testing.T) {
		gitDir := t.TempDir()
		_, err := Verify(VerifyOptions{
			GitDir: gitDir,
			Agent:  &fakeVerifyAgent{output: "tested: make test"},
			Questionr: func(warning string) (string, error) {
				return "delegate", nil
			},
		})
		if err != nil {
			t.Fatalf("Verify failed: %v", err)
		}
		events, err := RecentEvents(gitDir, 1)
		if err != nil || len(events) != 1 {
			t.Fatalf("no pr-verify event: %v %+v", err, events)
		}
		if !strings.Contains(detailText(t, events[0].Detail), `"tested"`) || !strings.Contains(detailText(t, events[0].Detail), `make test`) {
			t.Errorf("the detail does not report the tested contract: %s", detailText(t, events[0].Detail))
		}
	})

	t.Run("reason", func(t *testing.T) {
		gitDir := t.TempDir()
		_, err := Verify(VerifyOptions{
			GitDir: gitDir,
			Questionr: func(warning string) (string, error) {
				return "skip", nil
			},
		})
		if err != nil {
			t.Fatalf("Verify failed: %v", err)
		}
		events, err := RecentEvents(gitDir, 1)
		if err != nil || len(events) != 1 {
			t.Fatalf("pr-verify event: %+v", events)
		}
		if !strings.Contains(detailText(t, events[0].Detail), `"reason":"omitido"`) {
			t.Errorf("the detail does not report the reason: %s", detailText(t, events[0].Detail))
		}
	})

	t.Run("without gitDir does not record", func(t *testing.T) {
		dir := t.TempDir()
		t.Chdir(dir)
		_, err := Verify(VerifyOptions{
			Cfg: config.Config{LintCommands: []string{"echo hello"}},
			Run: func(command string) (int, error) {
				return 0, nil
			},
		})
		if err != nil {
			t.Fatalf("Verify without GitDir must not fail on recording: %v", err)
		}
		// Without GitDir there are no side effects: the log is not created.
		if _, err := os.Stat(filepath.Join(dir, eventsRelPath)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("without GitDir events.jsonl must not be created: %v", err)
		}
	})
}

func TestRecordEventWritesStructuredObjectDetail(t *testing.T) {
	dir := t.TempDir()
	detail := EventDetail{
		"reason":  "ripgrep execution failed",
		"unknown": map[string]any{"kept": true},
	}
	if err := RecordEvent(dir, "review", 4, nil, detail, ""); err != nil {
		t.Fatalf("RecordEvent failed: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, eventsRelPath))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	var envelope struct {
		Detail json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("event is not valid JSON: %v", err)
	}
	if !strings.HasPrefix(string(envelope.Detail), "{") {
		t.Fatalf("detail = %s, want a JSON object rather than an encoded string", envelope.Detail)
	}
	object := map[string]any{}
	if err := json.Unmarshal(envelope.Detail, &object); err != nil {
		t.Fatalf("structured detail is not an object: %v", err)
	}
	if object["reason"] != "ripgrep execution failed" {
		t.Errorf("reason = %v, want the producer value", object["reason"])
	}
	unknown, ok := object["unknown"].(map[string]any)
	if !ok || unknown["kept"] != true {
		t.Errorf("unknown fields = %#v, want them preserved", object["unknown"])
	}
}

func TestLatestEventsReadsMixedLegacyAndStructuredDetails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"at":"2026-01-01T00:00:00Z","cmd":"legacy-json","exit":0,"detail":"{\"legacy\":\"value\",\"unknown\":{\"kept\":true}}"}`,
		`{"at":"2026-01-01T00:00:01Z","cmd":"legacy-text","exit":4,"detail":"provider_unavailable"}`,
		`{"at":"2026-01-01T00:00:02Z","cmd":"new-object","exit":0,"detail":{"new":"value","unknown":{"number":7}}}`,
	}
	original := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	events, err := RecentEvents(dir, 0)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if len(events) != len(lines) {
		t.Fatalf("events = %d, want %d mixed lines", len(events), len(lines))
	}
	// RecentEvents presents the newest event first; restore the stream order
	// here to verify that decoding never loses or reorders a historical line.
	chronological := make([]Event, len(events))
	for i := range events {
		chronological[len(events)-1-i] = events[i]
	}
	if chronological[0].Cmd != "legacy-json" || chronological[1].Cmd != "legacy-text" || chronological[2].Cmd != "new-object" {
		t.Fatalf("commands = %q, %q, %q; want original stream order", chronological[0].Cmd, chronological[1].Cmd, chronological[2].Cmd)
	}
	legacyObject := structuredDetail(t, chronological[0].Detail)
	if legacyObject["legacy"] != "value" {
		t.Errorf("legacy object detail = %#v, want its fields decoded", legacyObject)
	}
	legacyUnknown, ok := legacyObject["unknown"].(map[string]any)
	if !ok || legacyUnknown["kept"] != true {
		t.Errorf("legacy unknown fields = %#v, want them preserved", legacyObject["unknown"])
	}
	if chronological[1].Detail != "provider_unavailable" {
		t.Errorf("legacy non-JSON detail = %#v, want the original text", chronological[1].Detail)
	}
	newObject := structuredDetail(t, chronological[2].Detail)
	if newObject["new"] != "value" {
		t.Errorf("new object detail = %#v, want its fields decoded", newObject)
	}
	newUnknown, ok := newObject["unknown"].(map[string]any)
	if !ok || newUnknown["number"] != float64(7) {
		t.Errorf("new unknown fields = %#v, want them preserved", newObject["unknown"])
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != original {
		t.Fatal("reading mixed events must not rewrite events.jsonl")
	}
}

func TestPrVerifyProducerWritesStructuredDetail(t *testing.T) {
	gitDir := t.TempDir()
	_, err := Verify(VerifyOptions{
		GitDir: gitDir,
		Cfg:    config.Config{TestCommands: []string{"go test ./..."}},
		Run: func(string) (int, error) {
			return 1, nil
		},
	})
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(gitDir, eventsRelPath))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	var envelope struct {
		Detail json.RawMessage `json:"detail"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("event is not valid JSON: %v", err)
	}
	if !strings.HasPrefix(string(envelope.Detail), "{") {
		t.Fatalf("producer detail = %s, want a JSON object", envelope.Detail)
	}
}

func structuredDetail(t *testing.T, detail any) map[string]any {
	t.Helper()
	object, ok := detail.(map[string]any)
	if !ok {
		t.Fatalf("detail = %#v (%T), want a decoded object", detail, detail)
	}
	return object
}

func detailText(t *testing.T, detail any) string {
	t.Helper()
	if text, ok := detail.(string); ok {
		return text
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("marshal detail: %v", err)
	}
	return string(encoded)
}

func TestRotateAndPurgePreserveRetainedLineBytes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	keep := []byte(`{"at":"2026-01-01T00:00:00Z","cmd":"keep","exit":0,"shas":["keep"],"detail":{"unknown":{"kept":true}}}` + "\r\n")
	remove := []byte(`{"at":"2026-01-01T00:00:01Z","cmd":"remove","exit":0,"shas":["remove"],"detail":{"unknown":{"number":7}}}`)
	original := append(append([]byte(nil), keep...), remove...)
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}

	if err := RotateEvents(dir, 2); err != nil {
		t.Fatalf("RotateEvents failed: %v", err)
	}
	afterRotate, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterRotate) != string(original) {
		t.Fatalf("rotation changed retained bytes: got %q, want %q", afterRotate, original)
	}

	deleted, err := PurgeEventsOf(dir, []string{"remove"})
	if err != nil {
		t.Fatalf("PurgeEventsOf failed: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("eliminated = %d, want 1", deleted)
	}
	afterPurge, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterPurge) != string(keep) {
		t.Fatalf("purge changed retained bytes: got %q, want %q", afterPurge, keep)
	}
}

func TestRecordEventSeparatesAnUnterminatedLegacyRecord(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, eventsRelPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	legacy := `{"at":"2026-01-01T00:00:00Z","cmd":"legacy","exit":0,"detail":"text"}`
	if err := os.WriteFile(path, []byte(legacy), 0644); err != nil {
		t.Fatal(err)
	}

	if err := RecordEvent(dir, "new", 0, nil, EventDetail{"kind": "object"}, ""); err != nil {
		t.Fatalf("RecordEvent failed: %v", err)
	}
	events, err := RecentEvents(dir, 0)
	if err != nil {
		t.Fatalf("RecentEvents failed: %v", err)
	}
	if len(events) != 2 || events[0].Cmd != "new" || events[1].Cmd != "legacy" {
		t.Fatalf("events = %#v, want separate new and legacy records", events)
	}
}

func TestRecordEventRejectsInvalidDetailBeforeCreatingLog(t *testing.T) {
	dir := t.TempDir()
	if err := RecordEvent(dir, "invalid", 1, nil, EventDetail{"channel": make(chan int)}, ""); err == nil {
		t.Fatal("RecordEvent accepted a detail that JSON cannot encode")
	}
	if _, err := os.Stat(filepath.Join(dir, eventsRelPath)); !os.IsNotExist(err) {
		t.Fatalf("events.jsonl exists after marshal failure: %v", err)
	}
}

func TestRecordEventWritesObjectDetailsAndOmitsNil(t *testing.T) {
	tests := []struct {
		name   string
		detail EventDetail
	}{
		{name: "nil", detail: nil},
		{name: "empty", detail: EventDetail{}},
		{name: "non-empty", detail: EventDetail{"reason": "provider_unavailable"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := RecordEvent(dir, "record", 0, nil, tt.detail, ""); err != nil {
				t.Fatalf("RecordEvent failed: %v", err)
			}

			raw, err := os.ReadFile(filepath.Join(dir, eventsRelPath))
			if err != nil {
				t.Fatalf("read events.jsonl: %v", err)
			}
			if strings.Contains(string(raw), `"detail":null`) {
				t.Fatalf("writer output contains detail:null: %s", raw)
			}

			var envelope map[string]json.RawMessage
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatalf("event is not valid JSONL: %v", err)
			}
			rawDetail, present := envelope["detail"]
			if tt.detail == nil {
				if present {
					t.Fatalf("nil detail must be omitted, got %s", rawDetail)
				}
				return
			}
			if !present {
				t.Fatal("present detail must be written")
			}
			if !strings.HasPrefix(string(rawDetail), "{") {
				t.Fatalf("detail = %s, want a JSON object", rawDetail)
			}
			var object map[string]any
			if err := json.Unmarshal(rawDetail, &object); err != nil {
				t.Fatalf("detail is not a JSON object: %v", err)
			}
		})
	}
}
