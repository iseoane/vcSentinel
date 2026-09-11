package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

func acceptValidOptions() acceptOptions {
	return acceptOptions{
		sha: "abc12345", fingerprint: "fp-target", reason: "residual risk acknowledged for this release",
	}
}

// An acceptance persists every contracted field append-only, leaves the
// persisted revision byte-identical, and never clears the block.
func TestRunAcceptancePersistsWithoutClearing(t *testing.T) {
	deps, ledger, st := refuteTestDeps(t, nil)
	if err := ledger.SaveRevision("abc12345", "message", "bucket", "model-a", refuteRecordFixture()); err != nil {
		t.Fatalf("save revision: %v", err)
	}
	before, err := os.ReadFile(ledger.RecordPath("abc12345"))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := runAcceptance(deps, acceptValidOptions())
	if err != nil {
		t.Fatalf("acceptance rejected: %v", err)
	}
	after, err := os.ReadFile(ledger.RecordPath("abc12345"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("the persisted revision was mutated by an acceptance")
	}
	records, err := st.ReadDispositions()
	if err != nil {
		t.Fatalf("read dispositions: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("dispositions = %+v, want exactly one", records)
	}
	d := records[0]
	if d.Status != review.StatusAcceptedByUser || d.Reason != "residual risk acknowledged for this release" {
		t.Fatalf("disposition = %+v, want the human acceptance", d)
	}
	if d.Fingerprint != "fp-target" || d.SHA != "abc12345" || d.Path != "a.go" {
		t.Fatalf("disposition address = %+v, want the finding identity", d)
	}
	if d.TargetSeverity != review.SevCritical {
		t.Fatalf("persisted target severity = %q, want %q", d.TargetSeverity, review.SevCritical)
	}
	if d.Actor != review.RefutationActorHuman || d.Source != review.DispositionSourceHuman {
		t.Fatalf("disposition provenance = %+v, want human provenance", d)
	}
	if outcome.disposition == nil || outcome.disposition.Status != review.StatusAcceptedByUser {
		t.Fatalf("outcome = %+v, want the recorded acceptance", outcome)
	}
	// Acceptance never clears: the finding still blocks under the shared rule.
	if !review.IsBlocking(review.SevCritical, d.Status) {
		t.Fatal("an accepted CRITICAL must still block")
	}
}

// Every failure closes without persisting anything: missing record, missing
// or ambiguous fingerprint, already-answered finding, unsafe path.
func TestRunAcceptanceFailsClosedWithoutPersistence(t *testing.T) {
	ambiguous := refuteRecordFixture()
	ambiguous.AggregatedFindings[1].Fingerprint = "fp-target"
	answered := refuteRecordFixture()
	answered.AggregatedFindings[0].Status = review.StatusRefuted
	unsafe := refuteRecordFixture()
	unsafe.AggregatedFindings[0].Location.File = "../evil.go"
	cases := []struct {
		name     string
		revision *review.Revision
		mutate   func(*acceptOptions)
		setup    func(*store.Store) error
	}{
		{"missing review record", nil, func(*acceptOptions) {}, nil},
		{
			"missing fingerprint", &[]review.Revision{refuteRecordFixture()}[0],
			func(o *acceptOptions) { o.fingerprint = "fp-absent" }, nil,
		},
		{
			"ambiguous fingerprint", &[]review.Revision{ambiguous}[0],
			func(*acceptOptions) {}, nil,
		},
		{
			"already refuted", &[]review.Revision{answered}[0],
			func(*acceptOptions) {}, nil,
		},
		{
			"already accepted", &[]review.Revision{refuteRecordFixture()}[0],
			func(*acceptOptions) {}, func(st *store.Store) error {
				return st.AppendDisposition(acceptanceFixture("abc12345", "fp-target"))
			},
		},
		{
			"unsafe stored path", &[]review.Revision{unsafe}[0],
			func(*acceptOptions) {}, nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, ledger, st := refuteTestDeps(t, nil)
			if tc.revision != nil {
				if err := ledger.SaveRevision("abc12345", "message", "bucket", "model-a", *tc.revision); err != nil {
					t.Fatalf("save revision: %v", err)
				}
			}
			if tc.setup != nil {
				if err := tc.setup(st); err != nil {
					t.Fatalf("setup: %v", err)
				}
			}
			want := 0
			if tc.setup != nil {
				want = 1
			}
			opts := acceptValidOptions()
			tc.mutate(&opts)
			if _, err := runAcceptance(deps, opts); err == nil {
				t.Fatal("invalid acceptance was recorded")
			}
			records, err := st.ReadDispositions()
			if err != nil {
				t.Fatalf("read dispositions: %v", err)
			}
			if len(records) != want {
				t.Fatalf("dispositions = %+v, want %d (nothing new on failure)", records, want)
			}
		})
	}
}

func acceptanceFixture(sha, fp string) *review.FindingDisposition {
	return &review.FindingDisposition{
		SHA: sha, Fingerprint: fp, Status: review.StatusAcceptedByUser,
		Reason: "acknowledged", Path: "a.go",
		Actor: review.RefutationActorHuman, Source: review.DispositionSourceHuman,
	}
}

func TestParseAcceptArgsRejectsBadInput(t *testing.T) {
	valid := []string{"--sha", "abc123", "--fingerprint", "fp-1", "--reason", "acknowledged"}
	if _, err := parseAcceptArgs(valid); err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
	for _, args := range [][]string{
		{"--sha", "abc123", "--fingerprint", "fp-1"},
		{"--sha", "abc123", "--reason", "x"},
		{"--fingerprint", "fp-1", "--reason", "x"},
		{"--sha", "abc123", "--fingerprint", "fp-1", "--reason", "x", "--bogus", "y"},
		{},
	} {
		if _, err := parseAcceptArgs(args); err == nil {
			t.Fatalf("invalid args were accepted: %v", args)
		}
	}
}

func TestAcceptHelpIsRegistered(t *testing.T) {
	if !strings.Contains(buildHelp(), "accept") {
		t.Fatal("the top-level help does not list accept")
	}
	var buf bytes.Buffer
	if !writeCommandHelp(&buf, "accept") {
		t.Fatal("no dedicated help for accept")
	}
	if !strings.Contains(buf.String(), "--fingerprint") {
		t.Fatalf("help = %q, want the addressing flags documented", buf.String())
	}
	buf.Reset()
	if !handleHelp(&buf, &bytes.Buffer{}, "accept", []string{"--help"}) {
		t.Fatal("accept --help was not intercepted")
	}
}

func TestRunAcceptRejectsBadArgs(t *testing.T) {
	var buf bytes.Buffer
	if code := runAccept(&buf, t.TempDir(), []string{"--sha", "abc123"}); code != 1 {
		t.Fatalf("code = %d, want 1 for bad args", code)
	}
}

// A corrupt dispositions log fails the acceptance without persisting
// anything: answering against a partial history could acknowledge the wrong
// finding.
func TestRunAcceptanceRefusesCorruptHistory(t *testing.T) {
	deps, ledger, _ := refuteTestDeps(t, nil)
	if err := ledger.SaveRevision("abc12345", "message", "bucket", "model-a", refuteRecordFixture()); err != nil {
		t.Fatalf("save revision: %v", err)
	}
	path := filepath.Join(filepath.Dir(filepath.Dir(ledger.RecordPath("abc12345"))), "vas-sentinel", "dispositions.jsonl")
	const corrupt = `{"sha":"abc12345","fingerprint":"fp-target","status":"ignored","actor":"human","source":"human"}` + "\n"
	if err := os.WriteFile(path, []byte(corrupt), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runAcceptance(deps, acceptValidOptions()); err == nil {
		t.Fatal("acceptance appended despite corrupt disposition history")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != corrupt {
		t.Fatalf("corrupt log changed after failed acceptance: %q", got)
	}
}

// A lock cleanup failure after the append is a completed acceptance, not a
// failed mutation callers should retry.
func TestRunAcceptancePreservesCompletedAppendAfterLockCleanupFailure(t *testing.T) {
	deps, ledger, st := refuteTestDeps(t, nil)
	if err := ledger.SaveRevision("abc12345", "message", "bucket", "model-a", refuteRecordFixture()); err != nil {
		t.Fatalf("save revision: %v", err)
	}
	deps.withLockedRecord = func(sha string, fn func(*review.Record) error) error {
		if err := ledger.WithLockedRecord(sha, fn); err != nil {
			return err
		}
		return review.ErrLockNotReleased
	}
	outcome, err := runAcceptance(deps, acceptValidOptions())
	if outcome.disposition == nil || outcome.disposition.Status != review.StatusAcceptedByUser {
		t.Fatalf("completed acceptance was discarded: outcome=%+v err=%v", outcome, err)
	}
	if !errors.Is(outcome.completionWarning, review.ErrLockNotReleased) {
		t.Fatalf("cleanup failure = %v, want ErrLockNotReleased", outcome.completionWarning)
	}
	if err != nil {
		t.Fatalf("completed acceptance failed: %v", err)
	}
	records, readErr := st.ReadDispositions()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(records) != 1 {
		t.Fatalf("dispositions = %+v, want one completed append", records)
	}
}

// Piece 2 (surface findings, never clear): a human acceptance may acknowledge
// the CRITICAL finding a narrow run raised, even though the commit's
// authoritative verdict is OK. Acceptance documents judgement; the block (the
// alarm) stands until refuted or fixed.
func TestRunAcceptanceAcceptsSupplementaryAlarm(t *testing.T) {
	deps, ledger, st := refuteTestDeps(t, nil)
	seedSupplementaryAlarmRecord(t, ledger)

	outcome, err := runAcceptance(deps, acceptValidOptions())
	if err != nil {
		t.Fatalf("accepting a supplementary alarm failed: %v", err)
	}
	if outcome.disposition == nil || outcome.disposition.Status != review.StatusAcceptedByUser {
		t.Fatalf("disposition = %+v, want the human acceptance", outcome.disposition)
	}
	records, err := st.ReadDispositions()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Fingerprint != "fp-target" {
		t.Fatalf("dispositions = %+v, want the single acceptance", records)
	}

	// Acceptance never clears the alarm: it still blocks the branch gate.
	record, err := ledger.ReadRecord("abc12345")
	if err != nil || record == nil {
		t.Fatalf("read record: %+v / %v", record, err)
	}
	if blockers := review.BranchBlockers([]review.Record{*record}, records); len(blockers) != 1 {
		t.Fatalf("blockers after acceptance = %+v, want the alarm to keep blocking", blockers)
	}
}
