// Production-cutover integration tests for the gate durable-runs wiring
// (ticket 11 slice 3). These tests drive the EXACT cmd-level construction
// path — buildGateOptions plus applyDurableCutover over a strictly loaded
// project configuration — and then prove both sides of the seam:
//
//   - With gate.durable_runs at its default (true), one gate execution
//     produces root+children linkage in ONE store (the same instance backs
//     the root run and every routed review invocation), the settlement
//     suffix enumerates validation jobs plus every learned review child,
//     and the persisted ParentRunID scan reproduces exactly that set.
//   - With gate.durable_runs: false, the legacy orchestration renders
//     byte-identical facade output to the cutover path on equivalent
//     fixtures and writes ZERO new executions into the store.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
	"github.com/ISeoane-Quental/vas.sentinel/internal/gate"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

const ymlValidacionCutover = "version: \"2.0\"\nvalidation:\n  capabilities:\n    lint:\n      command: \"echo ok\"\n      fails_when: \"exit_code\"\n  profiles:\n    standard: [\"lint\"]\n"

// agenteRevisionFijo implements AuditorAgente AND RestrictedReviewer with a
// canned raw verdict, mirroring internal/gate's auditorFalso but local to the
// cmd package.
type agenteRevisionFijo struct{ salida string }

func (a *agenteRevisionFijo) EjecutarPrompt(string) (string, error) { return a.salida, nil }
func (a *agenteRevisionFijo) EjecutarRevision(string, string, []string) (string, error) {
	return a.salida, nil
}

// repositorioCutover creates a real repository with one commit so HEAD
// resolution, change profiling, and the git common dir all resolve against a
// deterministic fixture.
func repositorioCutover(t *testing.T) string {
	t.Helper()
	worktree := t.TempDir()
	initGitRepo(t, worktree)
	if err := os.WriteFile(filepath.Join(worktree, "fixture.go"), []byte("package fixture\n"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	for _, args := range [][]string{
		{"config", "user.name", "fixture"},
		{"config", "user.email", "fixture@example.com"},
		{"add", "."},
		{"commit", "-m", "feat: gate cutover fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = worktree
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return worktree
}

// construirOpcionesCutover loads the strict project configuration for the
// fixture repo, resolves HEAD inputs through the real git seams, builds the
// production gate options, applies the durable cutover, and finally overrides
// ONLY the reviewer agent seams with canned verdicts (the review transport
// stays whatever production construction produced). t.Chdir pins every
// pathless git call to the fixture repository for the whole subtest.
func construirOpcionesCutover(t *testing.T, worktree, stage, ymlExtra string, auditorSalida string) (gate.Opciones, config.Config, *reviewChildSink) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(worktree)
	escribirYmlGateTest(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), ymlValidacionCutover+ymlExtra)

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
	profile, err := change.PerfilDeCommit(sha)
	if err != nil {
		t.Fatalf("change profile: %v", err)
	}

	verificador := nuevoVerificadorModelo(worktree)
	opciones := buildGateOptions(cfg, verificador, worktree, perfilGatePorDefecto, sha, mensaje, diff, profile, archivos)
	sink := applyDurableCutover(&opciones, cfg, worktree, stage, sha, archivos)
	opciones.FabricaAuditor = func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
		return &agenteRevisionFijo{salida: auditorSalida}, "logic", nil
	}
	opciones.FabricaRefutador = func() (review.AuditorAgente, string, error) {
		return &agenteRevisionFijo{salida: `{"refuted":false,"reason":"the risk remains"}`}, "cheap", nil
	}
	return opciones, cfg, sink
}

// renderSalidaGate reproduces byte-for-byte what ejecutarGate prints for a
// result (header line plus verbatim messages).
func renderSalidaGate(stage, perfil string, resultado gate.Resultado) (string, int) {
	var salida bytes.Buffer
	fmt.Fprintf(&salida, "🚦 gate [%s] perfil=%s → %s\n", stage, perfil, resultado.Estado)
	for _, m := range resultado.Mensajes {
		fmt.Fprintln(&salida, m)
	}
	return salida.String(), gate.CodigoSalida(resultado.Estado)
}

// digestArbolEjecuciones hashes the whole executions subtree of the store
// rooted at commonDir ("absent" when nothing has been written yet), so a test
// can prove an execution wrote zero new runs.
func digestArbolEjecuciones(t *testing.T, commonDir string) string {
	t.Helper()
	raiz := filepath.Join(commonDir, "vas-sentinel", "executions")
	if _, err := os.Stat(raiz); err != nil {
		if os.IsNotExist(err) {
			return "absent"
		}
		t.Fatalf("stat executions tree: %v", err)
	}
	h := sha256.New()
	err := filepath.WalkDir(raiz, func(ruta string, entrada fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(raiz, ruta)
		if err != nil {
			return err
		}
		h.Write([]byte(rel))
		if !entrada.IsDir() {
			datos, err := os.ReadFile(ruta)
			if err != nil {
				return err
			}
			h.Write(datos)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk executions tree: %v", err)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func TestGateCutoverLinksReviewChildrenInSharedStore(t *testing.T) {
	worktree := repositorioCutover(t)
	bloqueoJSON := `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"riesgo confirmable"}]}`
	opciones, _, sink := construirOpcionesCutover(t, worktree, "pre-push", "", bloqueoJSON)

	if sink == nil {
		t.Fatal("applyDurableCutover returned no sink, expected the cutover ON by default")
	}
	if !opciones.DurableRuns || opciones.DurableStore == nil || opciones.DurableReviewTransportFactory == nil {
		t.Fatalf("cutover left the durable seams unwired: runs=%v store=%v factory=%v",
			opciones.DurableRuns, opciones.DurableStore != nil, opciones.DurableReviewTransportFactory != nil)
	}

	resultado := gate.EjecutarGate(opciones)
	if resultado.Estado != gate.EstadoCodeReviewFailed {
		t.Fatalf("estado = %q (%v), expected %q", resultado.Estado, resultado.Mensajes, gate.EstadoCodeReviewFailed)
	}
	if len(sink.learned()) == 0 {
		t.Fatal("the production sink learned no review child identity")
	}

	st := opciones.DurableStore
	ids, err := st.ListExecutionIDs()
	if err != nil {
		t.Fatalf("ListExecutionIDs() error = %v", err)
	}
	rootID := ""
	children := map[string]string{}
	for _, id := range ids {
		request, err := st.ReadExecutionRequest(id)
		if err != nil {
			t.Fatalf("ReadExecutionRequest(%s) error = %v", id, err)
		}
		if request.ParentRunID == "" {
			if rootID != "" {
				t.Fatalf("store holds multiple parentless runs (%s, %s)", rootID, id)
			}
			rootID = id
			continue
		}
		children[id] = request.ParentRunID
	}
	if rootID == "" {
		t.Fatal("store holds no parentless root run")
	}
	scanned := make([]string, 0, len(children))
	for id, parent := range children {
		if parent != rootID {
			t.Fatalf("run %s links to foreign parent %s", id, parent)
		}
		scanned = append(scanned, id)
	}

	// The learned review identities must be part of the persisted scan, and
	// together with the single validation job they must account for EVERY
	// scanned child.
	aprendidos := sink.learned()
	if len(aprendidos) < 1 {
		t.Fatal("sink drained empty after the audit")
	}
	if len(scanned) != 1+len(aprendidos) {
		t.Fatalf("scanned children = %d (%v), want %d (1 validation job + %d review runs)",
			len(scanned), scanned, 1+len(aprendidos), len(aprendidos))
	}
	for _, id := range aprendidos {
		if !slices.Contains(scanned, string(id)) {
			t.Fatalf("learned review run %s missing from the ParentRunID scan %v", id, scanned)
		}
	}

	// Root reconstruction from store contents alone: failed naming the review
	// layer, with a machine-parseable suffix enumerating exactly the scanned
	// set.
	inspeccion, err := execution.NewController(st, nil).Inspect(context.Background(), agentrun.Identity(rootID))
	if err != nil {
		t.Fatalf("root inspection failed: %v", err)
	}
	if inspeccion.Projection.State != agentrun.StateFailed {
		t.Fatalf("root state = %q, expected failed", inspeccion.Projection.State)
	}
	var detalle string
	for _, outcome := range inspeccion.Outcomes {
		if strings.HasPrefix(outcome.Error, "gate: failing layer:") {
			detalle = outcome.Error
		}
	}
	if !strings.Contains(detalle, "gate: failing layer: review") {
		t.Fatalf("root detail %q does not name the review layer", detalle)
	}
	marker := "|children="
	index := strings.Index(detalle, marker)
	if index < 0 {
		t.Fatalf("root detail %q carries no children enumeration", detalle)
	}
	enumerated := strings.Split(detalle[index+len(marker):], ",")
	slices.Sort(enumerated)
	slices.Sort(scanned)
	if !slices.Equal(enumerated, scanned) {
		t.Fatalf("enumerated children %v != ParentRunID scan %v", enumerated, scanned)
	}

	// Sensible classification: only the ROOT carries the failure; every child
	// (validation job included) reached a terminal success attempt.
	for _, id := range scanned {
		hijoInspeccion, err := execution.NewController(st, nil).Inspect(context.Background(), agentrun.Identity(id))
		if err != nil {
			t.Fatalf("child %s inspection failed: %v", id, err)
		}
		if len(hijoInspeccion.Outcomes) != 1 {
			t.Fatalf("child %s recorded %d outcomes, expected exactly one terminal attempt", id, len(hijoInspeccion.Outcomes))
		}
		if hijoInspeccion.Outcomes[0].Class != agentrun.OutcomeSuccess {
			t.Fatalf("child %s settled class=%s, expected success (only the root names the failing layer)", id, hijoInspeccion.Outcomes[0].Class)
		}
	}
}

// sortedNonEmpty returns the non-empty lines sorted, so set-equality of
// message bodies can ignore the engine's process-random dimension ordering.
func sortedNonEmpty(lineas []string) []string {
	filtradas := make([]string, 0, len(lineas))
	for _, linea := range lineas {
		if linea != "" {
			filtradas = append(filtradas, linea)
		}
	}
	slices.Sort(filtradas)
	return filtradas
}

func TestGateRollbackFlagWritesZeroExecutionsAndLegacyBytes(t *testing.T) {
	bloqueoJSON := `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"riesgo confirmable"}]}`

	ejecutarModo := func(t *testing.T, worktree string, ymlExtra string) (salida string, exitCode int, commonDir string, digest string) {
		opciones, _, _ := construirOpcionesCutover(t, worktree, "pre-push", ymlExtra, bloqueoJSON)
		commonDir, err := git.ObtenerGitCommonDir(worktree)
		if err != nil {
			t.Fatalf("git common dir: %v", err)
		}
		digest = digestArbolEjecuciones(t, commonDir)
		resultado := gate.EjecutarGate(opciones)
		salida, exitCode = renderSalidaGate("pre-push", perfilGatePorDefecto, resultado)
		return salida, exitCode, commonDir, digest
	}

	t.Run("explicit false restores the legacy orchestration byte-for-byte", func(t *testing.T) {
		// ONE repository for both runs so even HEAD-derived text (the audit
		// summary embeds the commit SHA) is identical input.
		worktree := repositorioCutover(t)
		salidaLegado, exitLegado, _, _ := ejecutarModo(t, worktree, "gate:\n  durable_runs: false\n")
		salidaDurable, exitDurable, _, _ := ejecutarModo(t, worktree, "")

		if exitDurable != exitLegado {
			t.Fatalf("exit codes diverged: legacy=%d durable=%d", exitLegado, exitDurable)
		}
		lineasLegado := strings.Split(salidaLegado, "\n")
		lineasDurable := strings.Split(salidaDurable, "\n")
		// The header line (estado + stage + perfil) must match verbatim; the
		// message body must be set-equal. Line ORDER inside the body is not
		// part of the seam under test: per-dimension workers append results
		// under a mutex, so the shared engine emits the summary lines in
		// goroutine completion order on BOTH paths — a pre-existing property
		// this comparison must not mistake for cutover drift.
		if lineasLegado[0] != lineasDurable[0] {
			t.Fatalf("header diverged:\nlegacy:  %q\ndurable: %q", lineasLegado[0], lineasDurable[0])
		}
		cuerpoLegado := sortedNonEmpty(lineasLegado[1:])
		cuerpoDurable := sortedNonEmpty(lineasDurable[1:])
		if !slices.Equal(cuerpoLegado, cuerpoDurable) {
			t.Fatalf("facade messages diverged from the legacy pin:\nlegacy:  %q\ndurable: %q", cuerpoLegado, cuerpoDurable)
		}
	})

	t.Run("flag false writes zero new executions", func(t *testing.T) {
		worktree := repositorioCutover(t)
		salida, exit, commonDir, antes := ejecutarModo(t, worktree, "gate:\n  durable_runs: false\n")
		despues := digestArbolEjecuciones(t, commonDir)
		if antes != despues {
			t.Fatalf("rollback mode wrote executions: digest before=%q after=%q", antes, despues)
		}
		if antes != "absent" {
			t.Fatalf("rollback mode found pre-existing executions (digest %q), expected a clean fixture", antes)
		}
		if exit != 1 { // CODE_REVIEW_FAILED contract
			t.Fatalf("exit = %d, want 1 (salida %q)", exit, salida)
		}
	})
}
