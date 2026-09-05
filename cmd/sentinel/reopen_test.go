package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

func reopenValidOptions() reopenOptions {
	return reopenOptions{
		sha: "abc12345", fingerprint: "fp-target", reason: "the guard is bypassed on this path",
		lineStart: 2, lineEnd: 2,
	}
}

// seedHumanRefutation records a human refutation through the real refute path
// so reopen tests start from the cleared state Unit B is about.
func seedHumanRefutation(t *testing.T, deps *refuteDeps, ledger *review.Ledger) {
	t.Helper()
	if err := ledger.GuardarRevision("abc12345", "message", "bucket", "model-a", refuteFichaFixture()); err != nil {
		t.Fatalf("save revision: %v", err)
	}
	if _, err := runRefutation(deps, refuteValidOptions()); err != nil {
		t.Fatalf("seed refutation: %v", err)
	}
}

// A reopen persists every contracted field append-only, leaves the revision
// byte-identical, and makes the finding block again.
func TestRunReopenPersistsAndReblocks(t *testing.T) {
	deps, ledger, st := refuteTestDeps(t, nil)
	seedHumanRefutation(t, deps, ledger)
	before, err := os.ReadFile(ledger.RutaFicha("abc12345"))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := runReopen(deps, reopenValidOptions())
	if err != nil {
		t.Fatalf("reopen rejected: %v", err)
	}
	after, err := os.ReadFile(ledger.RutaFicha("abc12345"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("the persisted revision was mutated by a reopen")
	}
	records, err := st.ReadDispositions()
	if err != nil {
		t.Fatalf("read dispositions: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("dispositions = %+v, want refutation plus reopen", records)
	}
	d := records[1]
	if d.Status != review.StatusReopened || d.Fingerprint != "fp-target" || d.SHA != "abc12345" {
		t.Fatalf("disposition = %+v, want the human reopen", d)
	}
	if d.Actor != review.RefutationActorHuman || d.Source != review.DispositionSourceHuman {
		t.Fatalf("disposition provenance = %+v, want human provenance", d)
	}
	if outcome.disposition == nil || outcome.disposition.Status != review.StatusReopened {
		t.Fatalf("outcome = %+v, want the recorded reopen", outcome)
	}
	// The reopened finding blocks again under the shared rule.
	if !review.IsBlocking(review.SevCritical, d.Status) {
		t.Fatal("a reopened CRITICAL must block again")
	}
}

// An automated refutation can be reopened too: the gate is the cleared
// state, not who cleared it.
func TestRunReopenReopensAutomatedRefutation(t *testing.T) {
	deps, ledger, st := refuteTestDeps(t, nil)
	cleared := refuteFichaFixture()
	cleared.AggregatedFindings[0].Status = review.StatusRefuted
	cleared.AggregatedFindings[0].RefutationActor = review.RefutationActorRefuter
	if err := ledger.GuardarRevision("abc12345", "message", "bucket", "model-a", cleared); err != nil {
		t.Fatalf("save revision: %v", err)
	}
	if _, err := runReopen(deps, reopenValidOptions()); err != nil {
		t.Fatalf("reopen of automated refutation rejected: %v", err)
	}
	records, err := st.ReadDispositions()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Status != review.StatusReopened {
		t.Fatalf("dispositions = %+v, want one reopen", records)
	}
}

// Every failure closes without persisting anything: still-blocking findings,
// findings never cleared, missing or ambiguous fingerprints, ranges the
// evidence gate rejects, unreadable snapshots, and a second reopen.
func TestRunReopenFailsClosedWithoutPersistence(t *testing.T) {
	blocking := refuteFichaFixture()
	ambiguous := refuteFichaFixture()
	ambiguous.AggregatedFindings[1].Fingerprint = "fp-target"
	cases := []struct {
		name     string
		revision *review.Revision
		snapshot review.SnapshotReader
		mutate   func(*reopenOptions)
		setup    func(deps *refuteDeps, ledger *review.Ledger)
	}{
		{"missing review record", nil, nil, func(*reopenOptions) {}, nil},
		{
			"still blocking without answers", &[]review.Revision{blocking}[0], nil,
			func(*reopenOptions) {}, nil,
		},
		{
			"missing fingerprint", &[]review.Revision{refuteFichaFixture()}[0], nil,
			func(o *reopenOptions) { o.fingerprint = "fp-absent" }, nil,
		},
		{
			"ambiguous fingerprint", &[]review.Revision{ambiguous}[0], nil,
			func(*reopenOptions) {}, nil,
		},
		{
			"range outside finding", &[]review.Revision{refuteFichaFixture()}[0], nil,
			func(o *reopenOptions) { o.lineStart, o.lineEnd = 1, 1 },
			func(deps *refuteDeps, ledger *review.Ledger) {
				if _, err := runRefutation(deps, refuteValidOptions()); err != nil {
					panic(err)
				}
			},
		},
		{
			"snapshot unreadable", &[]review.Revision{refuteFichaFixture()}[0],
			func(string, string) (string, error) { return "", errors.New("snapshot unavailable") },
			func(*reopenOptions) {}, nil,
		},
		{
			"second reopen refuses", &[]review.Revision{refuteFichaFixture()}[0], nil,
			func(*reopenOptions) {},
			func(deps *refuteDeps, ledger *review.Ledger) {
				if _, err := runRefutation(deps, refuteValidOptions()); err != nil {
					panic(err)
				}
				if _, err := runReopen(deps, reopenValidOptions()); err != nil {
					panic(err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, ledger, st := refuteTestDeps(t, tc.snapshot)
			if tc.revision != nil {
				if err := ledger.GuardarRevision("abc12345", "message", "bucket", "model-a", *tc.revision); err != nil {
					t.Fatalf("save revision: %v", err)
				}
			}
			base := 0
			if tc.setup != nil {
				tc.setup(deps, ledger)
				records, err := st.ReadDispositions()
				if err != nil {
					t.Fatalf("read dispositions: %v", err)
				}
				base = len(records)
			}
			opts := reopenValidOptions()
			tc.mutate(&opts)
			if _, err := runReopen(deps, opts); err == nil {
				t.Fatal("invalid reopen was recorded")
			}
			records, err := st.ReadDispositions()
			if err != nil {
				t.Fatalf("read dispositions: %v", err)
			}
			if len(records) != base {
				t.Fatalf("dispositions = %+v, want %d (nothing new on failure)", records, base)
			}
		})
	}
}

// A corrupt dispositions log fails the reopen without persisting anything:
// revalidating the cleared state against a partial answer set could reopen
// the wrong finding.
func TestRunReopenRefusesCorruptHistory(t *testing.T) {
	deps, ledger, _ := refuteTestDeps(t, nil)
	if err := ledger.GuardarRevision("abc12345", "message", "bucket", "model-a", refuteFichaFixture()); err != nil {
		t.Fatalf("save revision: %v", err)
	}
	path := filepath.Join(filepath.Dir(filepath.Dir(ledger.RutaFicha("abc12345"))), "vas-sentinel", "dispositions.jsonl")
	const corrupt = `{"sha":"abc12345","fingerprint":"fp-target","status":"ignored","actor":"human","source":"human"}` + "\n"
	if err := os.WriteFile(path, []byte(corrupt), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := runReopen(deps, reopenValidOptions()); err == nil {
		t.Fatal("reopen appended despite corrupt disposition history")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != corrupt {
		t.Fatalf("corrupt log changed after failed reopen: %q", got)
	}
}

func TestParseReopenArgsRejectsBadInput(t *testing.T) {
	valid := []string{"--sha", "abc123", "--fingerprint", "fp-1", "--reason", "applies again", "--line-start", "2", "--line-end", "2"}
	if _, err := parseReopenArgs(valid); err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
	for _, args := range [][]string{
		{"--sha", "abc123", "--fingerprint", "fp-1", "--reason", "x"},
		{"--sha", "abc123", "--fingerprint", "fp-1", "--reason", "x", "--line-start", "2"},
		{"--sha", "abc123", "--fingerprint", "fp-1", "--reason", "x", "--line-start", "3", "--line-end", "2"},
		{"--sha", "abc123", "--fingerprint", "fp-1", "--reason", "x", "--line-start", "2", "--line-end", "2", "--bogus", "y"},
		{},
	} {
		if _, err := parseReopenArgs(args); err == nil {
			t.Fatalf("invalid args were accepted: %v", args)
		}
	}
}

func TestReopenHelpIsRegistered(t *testing.T) {
	if !strings.Contains(construirAyuda(), "reopen") {
		t.Fatal("the top-level help does not list reopen")
	}
	var buf bytes.Buffer
	if !escribirAyudaComando(&buf, "reopen") {
		t.Fatal("no dedicated help for reopen")
	}
	if !strings.Contains(buf.String(), "--fingerprint") {
		t.Fatalf("help = %q, want the addressing flags documented", buf.String())
	}
	buf.Reset()
	if !gestionarAyuda(&buf, &bytes.Buffer{}, "reopen", []string{"--help"}) {
		t.Fatal("reopen --help was not intercepted")
	}
}

func TestEjecutarReopenRejectsBadArgs(t *testing.T) {
	var buf bytes.Buffer
	if code := ejecutarReopen(&buf, t.TempDir(), []string{"--sha", "abc123"}); code != 1 {
		t.Fatalf("code = %d, want 1 for bad args", code)
	}
}
