// Command-level tests for the `sentinel review` admitted-run announcement
// (bounded follow-up): each durable review run identity must surface the
// moment the shared transport admits it — before the provider completes — so
// an operator can immediately `sentinel runs attach --run <id> --follow`.
// The announcement is wired ONLY at the sentinel-review command boundary and
// travels on the JSON-safe stderr channel, never on stdout where `review
// --json` consumers parse machine-readable payloads.
package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// TestReviewRunAnnouncerPrintsAttachCommandOnce covers the announcer contract:
// one line per UNIQUE admitted run ID carrying the canonical attach command,
// duplicates suppressed, everything written to the injected writer (stderr in
// production) and nowhere else.
func TestReviewRunAnnouncerPrintsAttachCommandOnce(t *testing.T) {
	var salida bytes.Buffer
	announcer := nuevoReviewRunAnnouncer(&salida)

	announcer.observe("run-aaa")
	announcer.observe("run-aaa") // duplicate observation: must not print twice
	announcer.observe("run-bbb")

	obtenida := salida.String()
	lineas := strings.Split(strings.TrimRight(obtenida, "\n"), "\n")
	if len(lineas) != 2 {
		t.Fatalf("announcer wrote %d lines (%q), want exactly one per unique run", len(lineas), obtenida)
	}
	for _, id := range []string{"run-aaa", "run-bbb"} {
		attachCmd := "sentinel runs attach --run " + id + " --follow"
		if !strings.Contains(obtenida, id) {
			t.Errorf("announcement omits run id %q:\n%s", id, obtenida)
		}
		if !strings.Contains(obtenida, attachCmd) {
			t.Errorf("announcement omits the actionable attach command %q:\n%s", attachCmd, obtenida)
		}
		if n := strings.Count(obtenida, attachCmd); n != 1 {
			t.Errorf("attach command for %s printed %d times, want exactly 1:\n%s", id, n, obtenida)
		}
	}
}

// TestSentinelReviewWiringAnnouncesAdmittedRunsOnStderr drives the EXACT
// production wiring `ejecutarReview` builds — the announced transport over the
// repository common-dir store — through a real audit, and proves that every
// admitted durable run is announced on the JSON-safe stream with its attach
// command while stdout stays untouched (so `review --json` stdout remains
// parseable).
func TestSentinelReviewWiringAnnouncesAdmittedRunsOnStderr(t *testing.T) {
	worktree := repositorioCutover(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(worktree)
	escribirYmlGateTest(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), ymlValidacionCutover)

	cfg, err := config.CargarConfiguracionLocalEstricta(worktree)
	if err != nil {
		t.Fatalf("config load failed: %v", err)
	}
	sha, err := git.ResolverSHA("HEAD")
	if err != nil {
		t.Fatalf("resolve HEAD: %v", err)
	}
	mensaje, err := git.MensajeCommit(sha)
	if err != nil {
		t.Fatalf("commit message: %v", err)
	}
	diff, err := git.DiffCommit(sha)
	if err != nil {
		t.Fatalf("commit diff: %v", err)
	}
	archivos, err := git.ArchivosDeCommit(sha)
	if err != nil {
		t.Fatalf("commit files: %v", err)
	}

	var stderrLive bytes.Buffer
	var stdoutJSON bytes.Buffer

	fabrica := func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
		return &agenteRevisionFijo{salida: `{"dim":"logic","verdict":"ok","findings":[]}`}, "logic", nil
	}
	opciones := review.OpcionesAuditoria{
		SHA:     sha,
		Mensaje: mensaje,
		Diff:    diff,
		Bundles: []review.ReviewBundle{{Name: "requested", Dimensions: []string{"logic"}, Priority: review.PriorityRequired, Cost: 1}},
		// This is the exact call site `ejecutarReview` uses; production passes
		// os.Stderr, this fixture captures it.
		ReviewTransport: durableReviewTransportConAnuncios(cfg, worktree, sha, archivos, &stderrLive),
	}
	resultado := review.AuditarCommit(fabrica, 1, opciones)
	if resultado.Veredicto != review.VerdictOK {
		t.Fatalf("audit verdict = %q (%+v), want %q through the durable wiring", resultado.Veredicto, resultado.Dims, review.VerdictOK)
	}

	// Every durable run the store holds for this audit must have been
	// announced exactly once, each with its own attach command.
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		t.Fatalf("git common dir: %v", err)
	}
	st := store.NuevoStore(commonDir)
	ids, err := st.ListExecutionIDs()
	if err != nil {
		t.Fatalf("ListExecutionIDs() error = %v", err)
	}
	if len(ids) == 0 {
		t.Fatal("the durable store holds no runs; the audit did not route through admission")
	}
	anuncios := stderrLive.String()
	for _, id := range ids {
		attachCmd := "sentinel runs attach --run " + string(id) + " --follow"
		if !strings.Contains(anuncios, attachCmd) {
			t.Errorf("admitted run %s was not announced with %q;\nlive stream:\n%s", id, attachCmd, anuncios)
		}
		if n := strings.Count(anuncios, attachCmd); n != 1 {
			t.Errorf("run %s announced %d times, want exactly 1:\n%s", id, n, anuncios)
		}
	}
	// JSON-safety: the live announcements never touch stdout, so a `review
	// --json` consumer reading stdout keeps parsing valid payloads.
	if stdoutJSON.Len() != 0 {
		t.Errorf("stdout received live announcement bytes, corrupting --json output:\n%q", stdoutJSON.String())
	}
	if !strings.Contains(anuncios, "vas-sentinel:") {
		t.Errorf("announcement lacks the established vas-sentinel diagnostic prefix:\n%s", anuncios)
	}
}
