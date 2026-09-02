// Production-cutover integration test for the gate durable-runs wiring
// (ticket 11 slice 3, unconditional since ticket 13 R11 removed the
// gate.durable_runs switch). It drives the EXACT cmd-level construction
// path — buildGateOptions plus applyDurableCutover over a strictly loaded
// project configuration — and proves that one gate execution produces
// root+children linkage in ONE store (the same instance backs the root run
// and every routed review invocation), that the settlement suffix enumerates
// validation jobs plus every learned review child, and that the persisted
// ParentRunID scan reproduces exactly that set.
package main

import (
	"bytes"
	"context"
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
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
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

func (a *agenteRevisionFijo) ReviewWithPolicy(prompt, sha string, paths []string, _ reviewcontract.ToolPolicy) (string, error) {
	return a.EjecutarRevision(prompt, sha, paths)
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
	opciones := buildGateOptions(cfg, verificador, worktree, perfilGatePorDefecto, sha, mensaje, diff, "", profile, archivos)
	sink := applyDurableCutover(&opciones, cfg, worktree, stage, sha, archivos)
	opciones.FabricaAuditor = func(_ review.ReviewBundle, _ string) (review.AuditorAgente, string, error) {
		return &agenteRevisionFijo{salida: auditorSalida}, "logic", nil
	}
	opciones.FabricaRefutador = func() (review.AuditorAgente, string, error) {
		return &agenteRevisionFijo{salida: `{"refuted":false,"reason":"the risk remains"}`}, "cheap", nil
	}
	return opciones, cfg, sink
}

func TestGateCutoverLinksReviewChildrenInSharedStore(t *testing.T) {
	worktree := repositorioCutover(t)
	blockingJSON := `{"dim":"logic","verdict":"block","findings":[{"dimension":"logic","file":"a.go","line":1,"severity":"CRITICAL","description":"confirmable risk","evidence":"risk()","confidence":"high"}]}`
	opciones, _, sink := construirOpcionesCutover(t, worktree, "pre-push", "", blockingJSON)

	if sink == nil {
		t.Fatal("applyDurableCutover returned no sink, expected the durable wiring always on")
	}
	if opciones.DurableStore == nil || opciones.DurableReviewTransportFactory == nil {
		t.Fatalf("cutover left the durable seams unwired: store=%v factory=%v",
			opciones.DurableStore != nil, opciones.DurableReviewTransportFactory != nil)
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

// TestGateStrictConfigRejectsRemovedDurableRunsKey proves at cmd level that a
// project yaml still carrying gate.durable_runs (removed in ticket 13 R11)
// fails fast as configuration infrastructure instead of being ignored.
func TestGateStrictConfigRejectsRemovedDurableRunsKey(t *testing.T) {
	worktree := repositorioCutover(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(worktree)
	escribirYmlGateTest(t, filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"),
		ymlValidacionCutover+"gate:\n  durable_runs: false\n")

	var salida bytes.Buffer
	exitCode := ejecutarGate(&salida, worktree, []string{"--stage", "pre-push"})

	if exitCode != 4 {
		t.Fatalf("exit = %d (%q), want 4 for a strict configuration failure", exitCode, salida.String())
	}
	if !strings.Contains(salida.String(), "gate") || !strings.Contains(salida.String(), "not found") {
		t.Fatalf("output = %q, want the unknown-key error to name the removed gate section", salida.String())
	}
}
