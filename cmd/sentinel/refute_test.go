package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// FU-6: the refute command is non-interactive and fully flagged. Every
// missing or malformed argument fails with usage, never with a partial
// record.
func TestParseRefuteArgsRejectsBadInput(t *testing.T) {
	valid := []string{"--sha", "abc123", "--fingerprint", "fp-1", "--reason", "safe", "--line-start", "2", "--line-end", "2"}
	if _, err := parseRefuteArgs(valid); err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
	cases := []struct {
		name string
		args []string
	}{
		{"no args", nil},
		{"missing sha", []string{"--fingerprint", "fp-1", "--reason", "safe", "--line-start", "2", "--line-end", "2"}},
		{"missing fingerprint", []string{"--sha", "abc123", "--reason", "safe", "--line-start", "2", "--line-end", "2"}},
		{"missing reason", []string{"--sha", "abc123", "--fingerprint", "fp-1", "--line-start", "2", "--line-end", "2"}},
		{"missing line-start", []string{"--sha", "abc123", "--fingerprint", "fp-1", "--reason", "safe", "--line-end", "2"}},
		{"missing line-end", []string{"--sha", "abc123", "--fingerprint", "fp-1", "--reason", "safe", "--line-start", "2"}},
		{"reversed range", []string{"--sha", "abc123", "--fingerprint", "fp-1", "--reason", "safe", "--line-start", "3", "--line-end", "2"}},
		{"zero line-start", []string{"--sha", "abc123", "--fingerprint", "fp-1", "--reason", "safe", "--line-start", "0", "--line-end", "2"}},
		{"non-numeric lines", []string{"--sha", "abc123", "--fingerprint", "fp-1", "--reason", "safe", "--line-start", "x", "--line-end", "2"}},
		{"removed --file flag", append(append([]string{}, valid...), "--file", "a.go")},
		{"dangling value", []string{"--sha"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseRefuteArgs(tc.args); err == nil {
				t.Fatal("invalid args were accepted")
			}
		})
	}
}

const refuteSnapshotContent = "const unrelated = true\ncriticalCall()\n"

func refuteTestDeps(t *testing.T, snapshot review.SnapshotReader) (*refuteDeps, *review.Ledger, *store.Store) {
	t.Helper()
	commonDir := t.TempDir()
	ledger := review.NuevoLedger(commonDir)
	st := store.NuevoStore(commonDir)
	if snapshot == nil {
		snapshot = func(sha, file string) (string, error) {
			if sha != "abc12345" || file != "a.go" {
				t.Errorf("snapshot read sha=%q file=%q", sha, file)
			}
			return refuteSnapshotContent, nil
		}
	}
	return &refuteDeps{
		ledger: ledger, store: st, snapshot: snapshot,
		now: func() time.Time { return time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC) },
	}, ledger, st
}

func refuteFichaFixture() review.Revision {
	return review.Revision{
		At: time.Date(2026, 9, 4, 11, 0, 0, 0, time.UTC), Result: "block",
		Dims: []review.DimensionResult{{Dim: review.DimLogic, Verdict: review.VerdictBlock, Findings: []review.ReviewFinding{
			{File: "a.go", Line: 2, Severity: review.SevCritical, Description: "bug", Status: review.StatusConfirmed},
			{File: "b.go", Line: 30, Severity: review.SevCritical, Description: "other", Status: review.StatusConfirmed},
		}}},
		AggregatedFindings: []review.Hallazgo{
			{
				Dimension: review.DimLogic, Severity: review.SevCritical, Status: review.StatusConfirmed,
				Description: "bug", Fingerprint: "fp-target",
				Location: review.Ubicacion{Archivo: "a.go", LineaInicio: 2},
				Evidence: "criticalCall()", Title: "unsafe call",
				Producer: review.Productor{Agente: "agent-a", Modelo: "model-a"},
			},
			{
				Dimension: review.DimLogic, Severity: review.SevCritical, Status: review.StatusConfirmed,
				Description: "other", Fingerprint: "fp-other",
				Location: review.Ubicacion{Archivo: "b.go", LineaInicio: 30},
				Evidence: "other call here", Title: "unsafe call",
				Producer: review.Productor{Agente: "agent-a", Modelo: "model-a"},
			},
		},
	}
}

func refuteValidOptions() refuteOptions {
	return refuteOptions{
		sha: "abc12345", fingerprint: "fp-target", reason: "the committed implementation is safe",
		lineStart: 2, lineEnd: 2,
	}
}

// FU-6: a valid human refutation persists every contracted field append-only
// and leaves the persisted revision byte-identical.
func TestRunRefutationPersistsWithoutMutatingRevisions(t *testing.T) {
	deps, ledger, st := refuteTestDeps(t, nil)
	if err := ledger.GuardarRevision("abc12345", "message", "bucket", "model-a", refuteFichaFixture()); err != nil {
		t.Fatalf("save revision: %v", err)
	}
	before, err := os.ReadFile(ledger.RutaFicha("abc12345"))
	if err != nil {
		t.Fatalf("read ficha: %v", err)
	}

	outcome, err := runRefutation(deps, refuteValidOptions())
	if err != nil {
		t.Fatalf("refutation rejected: %v", err)
	}
	if outcome.completionWarning != nil {
		t.Fatalf("completion warning = %v, want nil", outcome.completionWarning)
	}
	disposition := outcome.disposition
	if disposition.SHA != "abc12345" || disposition.Fingerprint != "fp-target" {
		t.Fatalf("address = %q/%q, want sha/fingerprint", disposition.SHA, disposition.Fingerprint)
	}
	if disposition.Status != review.StatusRefuted || disposition.Reason != "the committed implementation is safe" {
		t.Fatalf("disposition = %+v, want the human answer", disposition)
	}
	if disposition.Path != "a.go" || disposition.LineStart != 2 || disposition.LineEnd != 2 {
		t.Fatalf("range = %+v, want the snapshot range", disposition)
	}
	if disposition.Evidence != "criticalCall()" {
		t.Fatalf("evidence = %q, want the exact snapshot extract", disposition.Evidence)
	}
	if want := "1f4902c8f5b87a9dc694f279ef8bd2e6d491934eb00169644a05462d7f11b34f"; disposition.RangeHash != want {
		t.Fatalf("hash = %q, want the exact range hash", disposition.RangeHash)
	}
	if disposition.Actor != review.RefutationActorHuman || disposition.Source != review.DispositionSourceHuman {
		t.Fatalf("provenance = %+v, want human", disposition)
	}
	if disposition.TargetDimension != review.DimLogic || disposition.TargetLine != 2 || disposition.TargetDescription != "bug" {
		t.Fatalf("target = %+v, want the finding identity", disposition)
	}

	after, err := os.ReadFile(ledger.RutaFicha("abc12345"))
	if err != nil {
		t.Fatalf("read ficha: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("the persisted revision was mutated by the refutation")
	}

	records, err := st.ReadDispositions()
	if err != nil {
		t.Fatalf("read dispositions: %v", err)
	}
	if len(records) != 1 || records[0].Fingerprint != "fp-target" {
		t.Fatalf("dispositions = %+v, want one append-only record", records)
	}
}

// FU-6 defect 2: a finding without a line (LineaInicio == 0, e.g. a
// deterministic check citing a bare file path) could never be refuted: the
// shared gate requires the finding line inside the evidence range. The
// command accepts a file-scoped range instead; the extract must still match
// the audited Git object exactly, so the gate stays fail-closed.
func TestRunRefutationAcceptsLinelessFinding(t *testing.T) {
	deps, ledger, st := refuteTestDeps(t, nil)
	ficha := refuteFichaFixture()
	ficha.AggregatedFindings[0].Location.LineaInicio = 0
	if err := ledger.GuardarRevision("abc12345", "message", "bucket", "model-a", ficha); err != nil {
		t.Fatalf("save revision: %v", err)
	}
	opts := refuteValidOptions()
	opts.lineStart, opts.lineEnd = 1, 1
	outcome, err := runRefutation(deps, opts)
	if err != nil {
		t.Fatalf("line-less finding rejected a valid file-scoped evidence range: %v", err)
	}
	disposition := outcome.disposition
	if disposition.TargetLine != 0 || disposition.Path != "a.go" || disposition.LineStart != 1 || disposition.LineEnd != 1 {
		t.Fatalf("range = %+v, want the file-scoped range on a.go", disposition)
	}
	if disposition.Evidence != "const unrelated = true" {
		t.Fatalf("evidence = %q, want the exact snapshot extract", disposition.Evidence)
	}
	if want := "fc5d94369ff09844867f729602ecd2ed2a50239fffe6d789181680f58cd2f257"; disposition.RangeHash != want {
		t.Fatalf("hash = %q, want the exact range hash", disposition.RangeHash)
	}
	records, err := st.ReadDispositions()
	if err != nil {
		t.Fatalf("read dispositions: %v", err)
	}
	if len(records) != 1 || records[0].Fingerprint != "fp-target" || records[0].Status != review.StatusRefuted {
		t.Fatalf("dispositions = %+v, want one append-only record", records)
	}
}

// FU-6 defect 2: the line-less path still fails closed: an extract without
// evidence content is rejected and nothing is persisted.
func TestRunRefutationLinelessStillFailsClosed(t *testing.T) {
	deps, ledger, st := refuteTestDeps(t, nil)
	ficha := refuteFichaFixture()
	ficha.AggregatedFindings[0].Location.LineaInicio = 0
	if err := ledger.GuardarRevision("abc12345", "message", "bucket", "model-a", ficha); err != nil {
		t.Fatalf("save revision: %v", err)
	}
	opts := refuteValidOptions()
	opts.lineStart, opts.lineEnd = 3, 3 // the empty tail line below the snapshot content
	if _, err := runRefutation(deps, opts); err == nil {
		t.Fatal("empty evidence was accepted for a line-less finding")
	}
	records, err := st.ReadDispositions()
	if err != nil {
		t.Fatalf("read dispositions: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("dispositions = %+v, want nothing persisted", records)
	}
}

// FU-6: every failure closes without persisting anything: missing review
// record, missing or ambiguous fingerprint, already-cleared finding, unsafe
// path, range outside the gate, and unreadable snapshot.
func TestRunRefutationFailsClosedWithoutPersistence(t *testing.T) {
	ambiguous := refuteFichaFixture()
	ambiguous.AggregatedFindings[1].Fingerprint = "fp-target"
	cleared := refuteFichaFixture()
	cleared.AggregatedFindings[0].Status = review.StatusRefuted
	unsafe := refuteFichaFixture()
	unsafe.AggregatedFindings[0].Location.Archivo = "../evil.go"
	cases := []struct {
		name     string
		revision *review.Revision
		snapshot review.SnapshotReader
		mutate   func(*refuteOptions)
	}{
		{"missing review record", nil, nil, func(*refuteOptions) {}},
		{
			"missing fingerprint", &[]review.Revision{refuteFichaFixture()}[0], nil,
			func(o *refuteOptions) { o.fingerprint = "fp-absent" },
		},
		{
			"ambiguous fingerprint", &[]review.Revision{ambiguous}[0], nil,
			func(*refuteOptions) {},
		},
		{
			"already cleared", &[]review.Revision{cleared}[0], nil,
			func(*refuteOptions) {},
		},
		{
			"unsafe stored path", &[]review.Revision{unsafe}[0], nil,
			func(*refuteOptions) {},
		},
		{
			"range outside finding", &[]review.Revision{refuteFichaFixture()}[0], nil,
			func(o *refuteOptions) { o.lineStart, o.lineEnd = 1, 1 },
		},
		{
			"snapshot unreadable", &[]review.Revision{refuteFichaFixture()}[0],
			func(string, string) (string, error) { return "", errors.New("snapshot unavailable") },
			func(*refuteOptions) {},
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
			opts := refuteValidOptions()
			tc.mutate(&opts)
			if _, err := runRefutation(deps, opts); err == nil {
				t.Fatal("invalid refutation was accepted")
			}
			records, err := st.ReadDispositions()
			if err != nil {
				t.Fatalf("read dispositions: %v", err)
			}
			if len(records) != 0 {
				t.Fatalf("dispositions = %+v, want nothing persisted on failure", records)
			}
		})
	}
}

// FU-6: the command is discoverable and self-documenting.
func TestRefuteHelpIsRegistered(t *testing.T) {
	if !strings.Contains(construirAyuda(), "refute") {
		t.Fatal("the top-level help does not list refute")
	}
	var buf bytes.Buffer
	if !escribirAyudaComando(&buf, "refute") {
		t.Fatal("no dedicated help for refute")
	}
	if !strings.Contains(buf.String(), "--fingerprint") {
		t.Fatalf("help = %q, want the addressing flags documented", buf.String())
	}
	buf.Reset()
	if !gestionarAyuda(&buf, &bytes.Buffer{}, "refute", []string{"--help"}) {
		t.Fatal("refute --help was not intercepted")
	}
}

// FU-6: ejecutarRefute reports success and failure through its exit code
// without touching the process.
func TestEjecutarRefuteRejectsBadArgs(t *testing.T) {
	var buf bytes.Buffer
	if code := ejecutarRefute(&buf, t.TempDir(), []string{"--sha", "abc123"}); code != 1 {
		t.Fatalf("exit = %d, want 1 for missing flags (output %q)", code, buf.String())
	}
}

// FU-6: the evidence comes from the audited Git object, never from the
// caller or the worktree. The file is dirtied after the commit; the
// refutation still validates against the committed content.
func TestRefuteReadsEvidenceFromTheAuditedGitObject(t *testing.T) {
	if testing.Short() {
		t.Skip("uses a temporary Git repository")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	repo := t.TempDir()
	runRefuteGit(t, repo, "init")
	runRefuteGit(t, repo, "config", "user.email", "refute@example.test")
	runRefuteGit(t, repo, "config", "user.name", "Refute Test")
	committed := "const unrelated = true\ncriticalCall()\n"
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte(committed), 0o600); err != nil {
		t.Fatal(err)
	}
	runRefuteGit(t, repo, "add", "a.go")
	runRefuteGit(t, repo, "commit", "-m", "audited commit")
	sha := strings.TrimSpace(runRefuteGit(t, repo, "rev-parse", "HEAD"))
	// Dirty the worktree: only the committed object may satisfy the gate.
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("tampered worktree content\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	commonDir, err := git.ObtenerGitCommonDir(repo)
	if err != nil {
		t.Fatalf("common dir: %v", err)
	}
	ledger := review.NuevoLedger(commonDir)
	revision := refuteFichaFixture()
	if err := ledger.GuardarRevision(sha, "audited commit", "bucket", "model-a", revision); err != nil {
		t.Fatalf("save revision: %v", err)
	}

	var buf bytes.Buffer
	args := []string{
		"--sha", sha, "--fingerprint", "fp-target",
		"--reason", "the committed implementation is safe",
		"--line-start", "2", "--line-end", "2",
	}
	if code := ejecutarRefute(&buf, repo, args); code != 0 {
		t.Fatalf("exit = %d, want 0 (output %q)", code, buf.String())
	}
	records, err := store.NuevoStore(commonDir).ReadDispositions()
	if err != nil {
		t.Fatalf("read dispositions: %v", err)
	}
	if len(records) != 1 || records[0].Evidence != "criticalCall()" {
		t.Fatalf("dispositions = %+v, want the committed evidence recorded", records)
	}
	if want := "1f4902c8f5b87a9dc694f279ef8bd2e6d491934eb00169644a05462d7f11b34f"; records[0].RangeHash != want {
		t.Fatalf("hash = %q, want the committed range hash", records[0].RangeHash)
	}
}

func runRefuteGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

// FU-6: the review --gate escalation sees the same effective disposition:
// a human-cleared CRITICAL no longer escalates the exit, a confirmed one
// still does.
func TestGateEscalationHonoursHumanRefutations(t *testing.T) {
	build := func(status, actor string) review.ResultadoAuditoria {
		return review.ResultadoAuditoria{
			Dims: []review.ResultadoDimension{{
				Dim: review.DimSecurity,
				Resultado: &review.DimensionResult{
					Dim: review.DimSecurity, Verdict: review.VerdictWarn,
					Findings: []review.ReviewFinding{{
						Severity: review.SevCritical, File: "a.go",
						Status: status, RefutationActor: actor,
					}},
				},
			}},
		}
	}
	if !tieneHallazgosCriticos(build(review.StatusConfirmed, "")) {
		t.Fatal("a confirmed CRITICAL must escalate")
	}
	if tieneHallazgosCriticos(build(review.StatusRefuted, review.RefutationActorHuman)) {
		t.Fatal("a human-refuted CRITICAL must not escalate")
	}
	if tieneHallazgosCriticos(build(review.StatusFixed, "")) {
		t.Fatal("a fixed CRITICAL must not escalate")
	}
	if !tieneHallazgosCriticos(build(review.StatusAcceptedByUser, review.RefutationActorHuman)) {
		t.Fatal("an accepted CRITICAL must still escalate")
	}
}

// FU-6 race fix: a re-audit landing between fingerprint resolution and the
// dispositions append must fail the refutation closed. The seam lands a
// concurrent revision that retires the resolved fingerprint; the command
// must persist nothing and report the record changed.
func TestRunRefutationRefusesConcurrentRevision(t *testing.T) {
	deps, ledger, st := refuteTestDeps(t, nil)
	if err := ledger.GuardarRevision("abc12345", "message", "bucket", "model-a", refuteFichaFixture()); err != nil {
		t.Fatalf("save revision: %v", err)
	}
	deps.beforeLockedAppend = func() {
		next := refuteFichaFixture()
		next.AggregatedFindings[0].Fingerprint = "fp-retired"
		next.AggregatedFindings[0].Description = "rewritten premise"
		if err := ledger.GuardarRevision("abc12345", "message", "bucket", "model-a", next); err != nil {
			t.Errorf("concurrent revision: %v", err)
		}
	}

	if _, err := runRefutation(deps, refuteValidOptions()); err == nil {
		t.Fatal("a refutation resolved against a superseded revision was accepted")
	}
	records, err := st.ReadDispositions()
	if err != nil {
		t.Fatalf("read dispositions: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("dispositions = %+v, want nothing persisted for a stale resolution", records)
	}
}

// FU-6: corrupt history and an already-effective refutation both fail before
// append. A second answer must not accumulate against a finding no longer
// blocking.
func TestRunRefutationRefusesCorruptOrRepeatedDisposition(t *testing.T) {
	t.Run("corrupt log", func(t *testing.T) {
		deps, ledger, _ := refuteTestDeps(t, nil)
		if err := ledger.GuardarRevision("abc12345", "message", "bucket", "model-a", refuteFichaFixture()); err != nil {
			t.Fatalf("save revision: %v", err)
		}
		path := filepath.Join(filepath.Dir(filepath.Dir(ledger.RutaFicha("abc12345"))), "vas-sentinel", "dispositions.jsonl")
		const corrupt = `{"sha":"abc12345","fingerprint":"fp-target","status":"ignored","actor":"human","source":"human"}` + "\n"
		if err := os.WriteFile(path, []byte(corrupt), 0600); err != nil {
			t.Fatal(err)
		}

		if _, err := runRefutation(deps, refuteValidOptions()); err == nil {
			t.Fatal("refutation appended despite corrupt disposition history")
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != corrupt {
			t.Fatalf("corrupt log changed after failed refutation: %q", got)
		}
	})

	t.Run("already refuted", func(t *testing.T) {
		deps, ledger, st := refuteTestDeps(t, nil)
		if err := ledger.GuardarRevision("abc12345", "message", "bucket", "model-a", refuteFichaFixture()); err != nil {
			t.Fatalf("save revision: %v", err)
		}
		if _, err := runRefutation(deps, refuteValidOptions()); err != nil {
			t.Fatalf("first refutation: %v", err)
		}
		if _, err := runRefutation(deps, refuteValidOptions()); err == nil {
			t.Fatal("repeated refutation was appended")
		}
		records, err := st.ReadDispositions()
		if err != nil {
			t.Fatalf("read dispositions: %v", err)
		}
		if len(records) != 1 {
			t.Fatalf("dispositions = %+v, want one record after duplicate", records)
		}
	})
}

// FU-6: the command boundary rejects an out-of-snapshot range in a real Git
// repository and leaves the append-only log untouched.
func TestEjecutarRefuteRejectsOutOfSnapshotRange(t *testing.T) {
	if testing.Short() {
		t.Skip("uses a temporary Git repository")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available")
	}
	repo := t.TempDir()
	runRefuteGit(t, repo, "init")
	runRefuteGit(t, repo, "config", "user.email", "refute@example.test")
	runRefuteGit(t, repo, "config", "user.name", "Refute Test")
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte(refuteSnapshotContent), 0600); err != nil {
		t.Fatal(err)
	}
	runRefuteGit(t, repo, "add", "a.go")
	runRefuteGit(t, repo, "commit", "-m", "audited commit")
	sha := strings.TrimSpace(runRefuteGit(t, repo, "rev-parse", "HEAD"))
	commonDir, err := git.ObtenerGitCommonDir(repo)
	if err != nil {
		t.Fatal(err)
	}
	ledger := review.NuevoLedger(commonDir)
	if err := ledger.GuardarRevision(sha, "audited commit", "bucket", "model-a", refuteFichaFixture()); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if exit := ejecutarRefute(&output, repo, []string{
		"--sha", sha, "--fingerprint", "fp-target",
		"--reason", "the committed implementation is safe",
		"--line-start", "4", "--line-end", "4",
	}); exit != 1 {
		t.Fatalf("refute exit = %d, want 1; output: %q", exit, output.String())
	}
	records, err := store.NuevoStore(commonDir).ReadDispositions()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("dispositions = %+v, want no append after invalid range", records)
	}
}

// FU-6: a lock cleanup failure after the append is a completed refutation,
// not a failed mutation callers should retry. The command must preserve that
// result and report success with the lock warning.
func TestRunRefutationPreservesCompletedAppendAfterLockCleanupFailure(t *testing.T) {
	deps, ledger, st := refuteTestDeps(t, nil)
	if err := ledger.GuardarRevision("abc12345", "message", "bucket", "model-a", refuteFichaFixture()); err != nil {
		t.Fatalf("save revision: %v", err)
	}
	deps.withLockedFicha = func(sha string, fn func(*review.Ficha) error) error {
		if err := ledger.WithLockedFicha(sha, fn); err != nil {
			return err
		}
		return review.ErrBloqueoNoLiberado
	}

	outcome, err := runRefutation(deps, refuteValidOptions())
	if outcome.disposition == nil || outcome.disposition.Fingerprint != "fp-target" {
		t.Fatalf("completed refutation was discarded: outcome=%+v err=%v", outcome, err)
	}
	if !errors.Is(outcome.completionWarning, review.ErrBloqueoNoLiberado) {
		t.Fatalf("cleanup failure = %v, want ErrBloqueoNoLiberado", outcome.completionWarning)
	}
	if err != nil {
		t.Fatalf("completed refutation failed: %v", err)
	}
	records, readErr := st.ReadDispositions()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(records) != 1 {
		t.Fatalf("dispositions = %+v, want one completed append", records)
	}
}

// FU-6: the command boundary preserves a completed append when only cleanup
// fails: it exits successfully, confirms the record, and warns about cleanup.
func TestEjecutarRefuteReportsCompletedAppendWithLockCleanupWarning(t *testing.T) {
	deps, ledger, st := refuteTestDeps(t, nil)
	if err := ledger.GuardarRevision("abc12345", "message", "bucket", "model-a", refuteFichaFixture()); err != nil {
		t.Fatalf("save revision: %v", err)
	}
	deps.withLockedFicha = func(sha string, fn func(*review.Ficha) error) error {
		if err := ledger.WithLockedFicha(sha, fn); err != nil {
			return err
		}
		return review.ErrBloqueoNoLiberado
	}
	previousResolver := refuteDepsResolver
	refuteDepsResolver = func(string) (*refuteDeps, error) {
		return deps, nil
	}
	t.Cleanup(func() { refuteDepsResolver = previousResolver })

	var output bytes.Buffer
	if exit := ejecutarRefute(&output, t.TempDir(), []string{
		"--sha", "abc12345", "--fingerprint", "fp-target",
		"--reason", "the committed implementation is safe",
		"--line-start", "2", "--line-end", "2",
	}); exit != 0 {
		t.Fatalf("completed refutation exit = %d, want 0; output: %q", exit, output.String())
	}
	if !strings.Contains(output.String(), "recorded") || !strings.Contains(output.String(), "lock cleanup") {
		t.Fatalf("completed refutation output = %q, want success and cleanup warning", output.String())
	}
	records, err := st.ReadDispositions()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("dispositions = %+v, want one completed append", records)
	}
}

// FU-6: a ficha lock failure before the callback runs is a hard failure, not
// a completed append. The sentinel alone carries no completion signal, so the
// command must return an empty outcome, surface the sentinel as an error, and
// persist nothing.
func TestRunRefutationRejectsLockErrorBeforeAppend(t *testing.T) {
	deps, ledger, st := refuteTestDeps(t, nil)
	if err := ledger.GuardarRevision("abc12345", "message", "bucket", "model-a", refuteFichaFixture()); err != nil {
		t.Fatalf("save revision: %v", err)
	}
	deps.withLockedFicha = func(string, func(*review.Ficha) error) error {
		return review.ErrBloqueoNoLiberado
	}

	outcome, err := runRefutation(deps, refuteValidOptions())
	if err == nil || !errors.Is(err, review.ErrBloqueoNoLiberado) {
		t.Fatalf("err = %v, want a hard error wrapping ErrBloqueoNoLiberado", err)
	}
	if outcome.disposition != nil || outcome.completionWarning != nil {
		t.Fatalf("outcome = %+v, want an empty outcome on a pre-append lock failure", outcome)
	}
	records, readErr := st.ReadDispositions()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(records) != 0 {
		t.Fatalf("dispositions = %+v, want zero persisted dispositions", records)
	}
}
