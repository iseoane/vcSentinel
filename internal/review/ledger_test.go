package review

import (
	"errors"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLedgerGuardarYLeerFicha(t *testing.T) {
	dir := t.TempDir()
	ledger := NuevoLedger(dir)

	rev := Revision{At: time.Now().UTC(), Result: VerdictOK, Dims: []DimensionResult{{Dim: DimSpec, Verdict: VerdictOK}}}
	if err := ledger.GuardarRevision("abc123", "feat(x): cosa", "backend", "modelo-test", rev); err != nil {
		t.Fatalf("GuardarRevision devolvió error: %v", err)
	}

	ficha, err := ledger.LeerFicha("abc123")
	if err != nil {
		t.Fatalf("LeerFicha devolvió error: %v", err)
	}
	if ficha == nil {
		t.Fatal("LeerFicha devolvió nil para una ficha existente")
	}
	if ficha.SHA != "abc123" || ficha.Message != "feat(x): cosa" || ficha.Bucket != "backend" || ficha.Model != "modelo-test" {
		t.Errorf("ficha = %+v, no coincide con lo guardado", ficha)
	}
	if len(ficha.Revisions) != 1 || ficha.Revisions[0].Result != VerdictOK {
		t.Errorf("revisions = %+v, esperado 1 con resultado ok", ficha.Revisions)
	}
}

func TestLedgerRevisionesAppend(t *testing.T) {
	dir := t.TempDir()
	ledger := NuevoLedger(dir)

	rev1 := Revision{At: time.Now().UTC(), Result: VerdictBlock}
	rev2 := Revision{At: time.Now().UTC(), Result: VerdictOK}
	if err := ledger.GuardarRevision("def456", "msg", "", "", rev1); err != nil {
		t.Fatalf("primera revisión: %v", err)
	}
	if err := ledger.GuardarRevision("def456", "msg", "", "", rev2); err != nil {
		t.Fatalf("segunda revisión: %v", err)
	}

	ficha, err := ledger.LeerFicha("def456")
	if err != nil {
		t.Fatalf("LeerFicha devolvió error: %v", err)
	}
	if len(ficha.Revisions) != 2 {
		t.Fatalf("revisions = %d, esperado 2 (append, no pisado)", len(ficha.Revisions))
	}
	if ficha.Revisions[0].Result != VerdictBlock || ficha.Revisions[1].Result != VerdictOK {
		t.Errorf("el orden append no se respetó: %+v", ficha.Revisions)
	}
}

// TestLedgerPersistsAggregatedFindings verifies that Revision.AggregatedFindings
// (T6.5) round-trips through the JSON ledger file: it is the aggregated
// review.AuditarCommit result (ResultadoAuditoria.Findings), not the raw
// per-dimension DimensionResult.Findings already covered by Dims, and the
// renderer needs it to survive persistence to render it later. Location and
// EvidenceSet (with several FindingEvidence) round-trip too: they are the
// "fused evidence" the commit this test guards is actually named after, and
// renderMergedFinding consumes both directly (T6.5 review finding: tests).
func TestLedgerPersistsAggregatedFindings(t *testing.T) {
	dir := t.TempDir()
	ledger := NuevoLedger(dir)

	ubicacion := Ubicacion{Archivo: "auth.go", LineaInicio: 42, LineaFin: 44, Simbolo: "checkToken"}
	evidencias := []FindingEvidence{
		{Dimension: DimSecurity, Evidence: "token == expected", Confidence: 0.8},
		{Dimension: DimLogic, Evidence: "no hmac.Equal usage found", Confidence: 0.95},
	}
	rev := Revision{
		At:     time.Now().UTC(),
		Result: VerdictBlock,
		AggregatedFindings: []Hallazgo{
			{
				Dimension:   DimSecurity,
				Severity:    SevCritical,
				Source:      SourceReview,
				Confidence:  0.9,
				Description: "exposed secret",
				Location:    ubicacion,
				EvidenceSet: &FindingEvidenceSet{Values: evidencias},
			},
		},
	}
	if err := ledger.GuardarRevision("aggfind1", "feat(x): thing", "", "", rev); err != nil {
		t.Fatalf("GuardarRevision returned error: %v", err)
	}

	ficha, err := ledger.LeerFicha("aggfind1")
	if err != nil {
		t.Fatalf("LeerFicha returned error: %v", err)
	}
	if len(ficha.Revisions) != 1 || len(ficha.Revisions[0].AggregatedFindings) != 1 {
		t.Fatalf("AggregatedFindings did not round-trip: %+v", ficha.Revisions)
	}
	got := ficha.Revisions[0].AggregatedFindings[0]
	if got.Source != SourceReview || got.Confidence != 0.9 || got.Description != "exposed secret" {
		t.Errorf("AggregatedFindings[0] = %+v, values changed across persistence", got)
	}
	if got.Location != ubicacion {
		t.Errorf("Location did not round-trip: got %+v, want %+v", got.Location, ubicacion)
	}
	if got.EvidenceSet == nil || len(got.EvidenceSet.Values) != len(evidencias) {
		t.Fatalf("EvidenceSet did not round-trip: %+v", got.EvidenceSet)
	}
	for i, esperada := range evidencias {
		if got.EvidenceSet.Values[i] != esperada {
			t.Errorf("EvidenceSet.Values[%d] = %+v, want %+v", i, got.EvidenceSet.Values[i], esperada)
		}
	}
}

// TestRevisionHallazgosEfectivosPrefersAggregated: when AggregatedFindings is
// non-empty, HallazgosEfectivos returns it as-is and ignores Dims entirely.
// It is the single selection point riesgos() and BloqueantesDeRama must both
// consume (T6.5 review finding: design — before this method existed,
// BloqueantesDeRama read only Dims and could still block on a semantic
// finding T6.2 had already superseded by a deterministic one).
func TestRevisionHallazgosEfectivosPrefersAggregated(t *testing.T) {
	rev := Revision{
		AggregatedFindings: []Hallazgo{{Dimension: DimSecurity, Severity: SevCritical, Description: "aggregated"}},
		Dims: []DimensionResult{{Dim: DimSpec, Findings: []ReviewFinding{
			{Dimension: DimSpec, Severity: SevWarning, Description: "must be ignored"},
		}}},
	}
	got := rev.HallazgosEfectivos()
	if len(got) != 1 || got[0].Description != "aggregated" {
		t.Errorf("HallazgosEfectivos() = %+v, expected only AggregatedFindings", got)
	}
}

// TestRevisionHallazgosEfectivosConvertsDimsWithoutAggregated: with no
// AggregatedFindings, HallazgosEfectivos converts every raw v1 ReviewFinding
// from Dims to the v2 Hallazgo shape, so callers get one uniform type
// regardless of a Revision's origin (a Revision saved before T6.5, or by a
// caller that never propagated AggregatedFindings).
func TestRevisionHallazgosEfectivosConvertsDimsWithoutAggregated(t *testing.T) {
	rev := Revision{
		Dims: []DimensionResult{{
			Dim: DimSpec,
			Findings: []ReviewFinding{
				{Dimension: DimSpec, File: "a.go", Line: 7, Severity: SevWarning, Description: "converted"},
			},
		}},
	}
	got := rev.HallazgosEfectivos()
	if len(got) != 1 {
		t.Fatalf("HallazgosEfectivos() = %d hallazgos, expected 1", len(got))
	}
	if got[0].Dimension != DimSpec || got[0].Severity != SevWarning || got[0].Description != "converted" ||
		got[0].Location.Archivo != "a.go" || got[0].Location.LineaInicio != 7 {
		t.Errorf("HallazgosEfectivos() converted = %+v, values lost in conversion", got[0])
	}
}

// TestRevisionHallazgosEfectivosNeverCarriesLegacySourceWithoutConfidence:
// a v1 ReviewFinding can have Source populated (e.g. SourceReview, stamped
// by the T5.7 critical-refutation path at engine.go:305) without ever
// having had a real Confidence — v1 has no such field. If
// hallazgoDesdeReviewFinding copied that Source as-is, the converted
// Hallazgo would pass renderMergedFinding's "h.Source != \"\"" gate and
// render a fabricated "(review, confidence 0.00)" — exactly the datum T6.5
// omits the whole segment to avoid (T6.5bis review finding: logic WARNING).
// So the conversion must never carry a Source without its matching real
// Confidence: HallazgosEfectivos leaves Source empty for every Dims-derived
// Hallazgo, regardless of what the underlying ReviewFinding.Source held.
func TestRevisionHallazgosEfectivosNeverCarriesLegacySourceWithoutConfidence(t *testing.T) {
	rev := Revision{
		Dims: []DimensionResult{{
			Dim: DimSecurity,
			Findings: []ReviewFinding{
				{Dimension: DimSecurity, File: "a.go", Line: 7, Severity: SevCritical, Description: "refuted", Source: SourceReview},
			},
		}},
	}
	got := rev.HallazgosEfectivos()
	if len(got) != 1 {
		t.Fatalf("HallazgosEfectivos() = %d hallazgos, expected 1", len(got))
	}
	if got[0].Source != "" {
		t.Errorf("HallazgosEfectivos()[0].Source = %q, want \"\" (no real Confidence backs it)", got[0].Source)
	}
}

func TestLedgerFichaInexistente(t *testing.T) {
	dir := t.TempDir()
	ledger := NuevoLedger(dir)

	ficha, err := ledger.LeerFicha("noexiste")
	if err != nil {
		t.Fatalf("LeerFicha devolvió error: %v", err)
	}
	if ficha != nil {
		t.Error("LeerFicha debería devolver nil para un SHA sin ficha")
	}
}

func TestLedgerArchivoCorruptoEsError(t *testing.T) {
	dir := t.TempDir()
	ledger := NuevoLedger(dir)

	ruta := ledger.RutaFicha("abc123")
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ruta, []byte("no es json"), 0644); err != nil {
		t.Fatal(err)
	}

	if _, err := ledger.LeerFicha("abc123"); err == nil {
		t.Error("un archivo corrupto debería devolver error, no nil")
	}
}

func TestLedgerEliminarFichaInexistenteEsNoOp(t *testing.T) {
	dir := t.TempDir()
	ledger := NuevoLedger(dir)
	if err := ledger.EliminarFicha("ffffffffffffffffffffffffffffffffffffffff"); err != nil {
		t.Fatalf("EliminarFicha de ficha inexistente devolvió error: %v", err)
	}
}

func TestLedgerPurgarHuerfanas(t *testing.T) {
	dir := t.TempDir()
	ledger := NuevoLedger(dir)

	rev := Revision{At: time.Now().UTC(), Result: VerdictOK}
	for _, sha := range []string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	} {
		if err := ledger.GuardarRevision(sha, "feat(x): cosa", "backend", "modelo-test", rev); err != nil {
			t.Fatalf("GuardarRevision(%s) devolvió error: %v", sha, err)
		}
	}

	// Sin un repositorio Git detrás, cat-file falla y ambas fichas son
	// huérfanas: el purge debe vaciar el ledger.
	eliminados, err := ledger.PurgarHuerfanas(func(sha string) (bool, error) { return git.ContenidoEnAlgunRef(sha), nil })
	if err != nil {
		t.Fatalf("PurgarHuerfanas devolvió error: %v", err)
	}
	if len(eliminados) != 2 {
		t.Errorf("PurgarHuerfanas eliminó %d fichas, esperado 2", len(eliminados))
	}
	restantes, err := ledger.ListarFichas()
	if err != nil {
		t.Fatalf("ListarFichas devolvió error: %v", err)
	}
	if len(restantes) != 0 {
		t.Errorf("tras purgar quedan %d fichas, esperado 0: %v", len(restantes), restantes)
	}
}

// TestLedgerPurgarHuerfanasDangling cubre el caso real: un commit reescrito
// con amend sigue existiendo en el object store como dangling, pero ya no es
// alcanzable desde ningún ref. ContenidoEnAlgunRef lo detecta y la ficha se
// purga; un commit vivo se conserva.
func TestLedgerPurgarHuerfanasDangling(t *testing.T) {
	repo := t.TempDir()
	ejecutarGitEn := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		salida, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v devolvió error: %v\n%s", args, err, salida)
		}
		return strings.TrimSpace(string(salida))
	}

	ejecutarGitEn("init", "-q")
	ejecutarGitEn("config", "user.email", "test@local")
	ejecutarGitEn("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("uno\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ejecutarGitEn("add", "a.txt")
	ejecutarGitEn("commit", "-q", "-m", "primero")
	shaVivo := ejecutarGitEn("rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("uno\ndos\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ejecutarGitEn("add", "a.txt")
	ejecutarGitEn("commit", "-q", "-m", "segundo")
	shaDangling := ejecutarGitEn("rev-parse", "HEAD")
	// Amend reescribe el commit: el SHA anterior queda dangling pero sigue
	// siendo un objeto válido en el almacén.
	ejecutarGitEn("commit", "--amend", "-q", "-m", "segundo corregido")
	if shaVivo == shaDangling {
		t.Fatal("los SHAs no pueden coincidir")
	}

	// El ledger se apunta al repo real para que ContenidoEnAlgunRef resuelva
	// contra sus refs.
	t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
	t.Setenv("GIT_WORK_TREE", repo)
	ledger := NuevoLedger(t.TempDir())
	rev := Revision{At: time.Now().UTC(), Result: VerdictOK}
	for _, sha := range []string{shaVivo, shaDangling} {
		if err := ledger.GuardarRevision(sha, "msg", "backend", "modelo", rev); err != nil {
			t.Fatalf("GuardarRevision(%s) devolvió error: %v", sha, err)
		}
	}

	eliminados, err := ledger.PurgarHuerfanas(func(sha string) (bool, error) { return git.ContenidoEnAlgunRef(sha), nil })
	if err != nil {
		t.Fatalf("PurgarHuerfanas devolvió error: %v", err)
	}
	if len(eliminados) != 1 || eliminados[0] != shaDangling {
		t.Errorf("PurgarHuerfanas eliminó %v, esperado solo %s (el dangling)", eliminados, shaDangling)
	}
	if ficha, _ := ledger.LeerFicha(shaVivo); ficha == nil {
		t.Errorf("la ficha del commit vivo %s no debería haberse purgado", shaVivo)
	}
}

func TestLedgerEscrituraAtomica(temp *testing.T) {
	// Varias escrituras consecutivas nunca dejan un archivo a medias: al final
	// siempre hay JSON válido con la última revisión.
	temp.Run("secuencial", func(t *testing.T) {
		dir := t.TempDir()
		ledger := NuevoLedger(dir)
		for i := 0; i < 20; i++ {
			rev := Revision{At: time.Now().UTC(), Result: VerdictOK}
			if err := ledger.GuardarRevision("sha1", "m", "", "", rev); err != nil {
				t.Fatalf("escritura %d: %v", i, err)
			}
		}
		ficha, err := ledger.LeerFicha("sha1")
		if err != nil {
			t.Fatalf("tras 20 escrituras el archivo quedó corrupto: %v", err)
		}
		if len(ficha.Revisions) != 20 {
			t.Errorf("revisions = %d, esperado 20", len(ficha.Revisions))
		}
	})
}

func TestLedgerMarcarCorregida(t *testing.T) {
	dir := t.TempDir()
	ledger := NuevoLedger(dir)

	rev := Revision{At: time.Now().UTC(), Result: VerdictBlock}
	if err := ledger.GuardarRevision("aaa111", "feat(x): con bug", "backend", "m", rev); err != nil {
		t.Fatalf("GuardarRevision devolvió error: %v", err)
	}

	if err := ledger.MarcarCorregida("aaa111", "bbb222"); err != nil {
		t.Fatalf("MarcarCorregida devolvió error: %v", err)
	}
	ficha, err := ledger.LeerFicha("aaa111")
	if err != nil {
		t.Fatalf("LeerFicha devolvió error: %v", err)
	}
	if ficha.FixedIn != "bbb222" {
		t.Errorf("FixedIn = %q, esperado bbb222", ficha.FixedIn)
	}

	// La primera corrección gana: no se sobreescribe con una posterior.
	if err := ledger.MarcarCorregida("aaa111", "ccc333"); err != nil {
		t.Fatalf("segunda MarcarCorregida devolvió error: %v", err)
	}
	ficha, _ = ledger.LeerFicha("aaa111")
	if ficha.FixedIn != "bbb222" {
		t.Errorf("FixedIn = %q, esperado que bbb222 gane", ficha.FixedIn)
	}
}

// TestLedgerAdoptarFicha cubre el fix de T2.7: adoptar la ficha de un SHA
// origen bajo un SHA destino nuevo debe conservar Message/Bucket/Model y
// TODAS las revisiones (incluidos los hallazgos reales), recuperables con
// LeerFicha(hacia).
func TestLedgerAdoptarFicha(t *testing.T) {
	dir := t.TempDir()
	ledger := NuevoLedger(dir)

	rev := Revision{
		At:     time.Now().UTC(),
		Result: VerdictWarn,
		Dims: []DimensionResult{{
			Dim:     DimLogic,
			Verdict: VerdictWarn,
			Findings: []ReviewFinding{{
				Dimension:   DimLogic,
				File:        "b.txt",
				Severity:    SevWarning,
				Description: "hallazgo real de prueba",
			}},
		}},
	}
	if err := ledger.GuardarRevision("sha-viejo", "feat(b): cosa", "pr", "modelo-test", rev); err != nil {
		t.Fatalf("GuardarRevision: %v", err)
	}

	if err := ledger.AdoptarFicha("sha-viejo", "sha-nuevo"); err != nil {
		t.Fatalf("AdoptarFicha: %v", err)
	}

	adoptada, err := ledger.LeerFicha("sha-nuevo")
	if err != nil {
		t.Fatalf("LeerFicha(sha-nuevo): %v", err)
	}
	if adoptada == nil {
		t.Fatal("LeerFicha(sha-nuevo) devolvió nil tras AdoptarFicha")
	}
	if adoptada.SHA != "sha-nuevo" || adoptada.Message != "feat(b): cosa" || adoptada.Bucket != "pr" || adoptada.Model != "modelo-test" {
		t.Errorf("ficha adoptada = %+v, no coincide con la de origen", adoptada)
	}
	if len(adoptada.Revisions) != 1 || len(adoptada.Revisions[0].Dims) != 1 || len(adoptada.Revisions[0].Dims[0].Findings) != 1 {
		t.Fatalf("revisiones adoptadas = %+v, esperado el hallazgo real intacto", adoptada.Revisions)
	}
	if adoptada.Revisions[0].Dims[0].Findings[0].Description != "hallazgo real de prueba" {
		t.Errorf("hallazgo adoptado = %+v, esperado conservar la descripción original", adoptada.Revisions[0].Dims[0].Findings[0])
	}

	// La ficha origen sigue existiendo: adoptar no es mover.
	if origen, _ := ledger.LeerFicha("sha-viejo"); origen == nil {
		t.Error("la ficha origen no debería desaparecer al adoptarla")
	}
}

// TestLedgerAdoptarFichaSHAOrigenInexistenteEsError: a diferencia de
// MarcarCorregida, adoptar de un SHA sin ficha es un error real, no un
// no-op silencioso — el SHA origen debería existir siempre porque
// commitCubiertoPorBlobs lo obtiene del store, que solo registra commits ya
// auditados.
func TestLedgerAdoptarFichaSHAOrigenInexistenteEsError(t *testing.T) {
	dir := t.TempDir()
	ledger := NuevoLedger(dir)

	if err := ledger.AdoptarFicha("no-existe", "sha-nuevo"); err == nil {
		t.Error("AdoptarFicha de un SHA origen sin ficha debería devolver error")
	}
	if ficha, _ := ledger.LeerFicha("sha-nuevo"); ficha != nil {
		t.Error("AdoptarFicha que falla no debería crear ninguna ficha destino")
	}
}

// TestLedgerAdoptarFichaEsIdempotente: llamarla dos veces con los mismos
// argumentos no falla ni duplica nada raro, solo sobrescribe con el mismo
// contenido.
func TestLedgerAdoptarFichaEsIdempotente(t *testing.T) {
	dir := t.TempDir()
	ledger := NuevoLedger(dir)

	rev := Revision{At: time.Now().UTC(), Result: VerdictOK}
	if err := ledger.GuardarRevision("sha-viejo", "feat(x)", "pr", "m", rev); err != nil {
		t.Fatalf("GuardarRevision: %v", err)
	}

	if err := ledger.AdoptarFicha("sha-viejo", "sha-nuevo"); err != nil {
		t.Fatalf("primera adopción: %v", err)
	}
	if err := ledger.AdoptarFicha("sha-viejo", "sha-nuevo"); err != nil {
		t.Fatalf("segunda adopción (idempotente): %v", err)
	}

	adoptada, err := ledger.LeerFicha("sha-nuevo")
	if err != nil {
		t.Fatalf("LeerFicha: %v", err)
	}
	if adoptada == nil || len(adoptada.Revisions) != 1 {
		t.Errorf("ficha tras adoptar dos veces = %+v, esperado 1 revisión (sin duplicar)", adoptada)
	}
}

func TestLedgerMarcarCorregidaSinFichaEsNoOp(t *testing.T) {
	dir := t.TempDir()
	ledger := NuevoLedger(dir)
	if err := ledger.MarcarCorregida("noexiste", "bbb222"); err != nil {
		t.Fatalf("MarcarCorregida sobre SHA sin ficha devolvió error: %v", err)
	}
}

// TestPurgarHuerfanasAbortaCuandoElCriterioFalla pins the ledger's own half of
// the contract, which the migrated tests do not reach: they wrap a helper that
// converts every git error into false and then force a nil error, so a
// regression that swallowed query failures would still pass there.
//
// The contract is that a question the purge cannot answer never authorises a
// deletion, and that what was already removed is reported rather than lost, so
// the caller knows the ledger is half-purged instead of assuming nothing
// happened.
func TestPurgarHuerfanasAbortaCuandoElCriterioFalla(t *testing.T) {
	ledger := NuevoLedger(t.TempDir())
	// ListarFichas sorts, so these names fix the traversal order: the orphan is
	// visited first, the unresolvable one second, and the third exists only to
	// prove the purge stopped. Without it an implementation could return the
	// failure, preserve the SHA it could not resolve, and delete everything
	// after it while satisfying every other assertion.
	huerfana := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ilegible := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	posterior := "cccccccccccccccccccccccccccccccccccccccc"
	for _, sha := range []string{huerfana, ilegible, posterior} {
		if err := ledger.GuardarRevision(sha, "fixture", "b", "m", Revision{At: time.Now(), Result: "ok"}); err != nil {
			t.Fatal(err)
		}
	}

	// The order the predicate is actually asked in is recorded, so a change in
	// traversal fails here instead of quietly weakening the test.
	var consultados []string
	fallo := errors.New("the object store cannot be read")
	eliminados, err := ledger.PurgarHuerfanas(func(sha string) (bool, error) {
		consultados = append(consultados, sha)
		if sha == ilegible {
			return false, fallo
		}
		return false, nil
	})
	if !slices.Equal(consultados, []string{huerfana, ilegible}) {
		t.Fatalf("the predicate was asked for %v, want exactly %v; the purge did not stop at the failure",
			consultados, []string{huerfana, ilegible})
	}

	if !errors.Is(err, fallo) {
		t.Fatalf("PurgarHuerfanas() error = %v, want the predicate's failure; a query that cannot be answered must not authorise a deletion", err)
	}
	if ficha, lerr := ledger.LeerFicha(ilegible); lerr != nil || ficha == nil {
		t.Errorf("the ficha whose existence could not be resolved was deleted (ficha=%v, err=%v)", ficha, lerr)
	}
	// Whatever was already removed must come back with the error: the ledger is
	// half-purged, and a caller told only "it failed" would believe otherwise.
	// The whole list is asserted, not just membership: a purge that reported the
	// SHA it could not resolve, or one ordered after it, as deleted would satisfy
	// a containment check while lying about what it destroyed.
	if !slices.Equal(eliminados, []string{huerfana}) {
		t.Errorf("PurgarHuerfanas() = %v, want exactly %v: only the ficha it had already deleted before aborting", eliminados, []string{huerfana})
	}
	if ficha, lerr := ledger.LeerFicha(huerfana); lerr != nil || ficha != nil {
		t.Errorf("the genuinely orphaned ficha was not deleted before the abort (ficha=%v, err=%v)", ficha, lerr)
	}
	if ficha, lerr := ledger.LeerFicha(posterior); lerr != nil || ficha == nil {
		t.Errorf("a ficha ordered after the failure was deleted anyway (ficha=%v, err=%v); the purge continued past a question it could not answer", ficha, lerr)
	}
}

// TestListarFichasFailsWhenTheLedgerDirectoryCannotBeRead pins FU-16. The
// listing enumerated with filepath.Glob, which reports only ErrBadPattern and
// swallows every I/O error it meets while reading a directory, so an
// unreadable ledger came back as an empty list and a nil error. The callers
// that decide what a prune may destroy read that as "this ledger cites
// nothing": collectProvenanceReferences, through anotarReferenciasDeLedger,
// then treats the execution streams those fichas reference as unreferenced and
// deletes them, which is exactly what its own contract forbids.
//
// The fault is staged with a file shape and not with a permission bit. A mode
// change is a no-op under root, so a permission-based fixture would pass
// without exercising anything.
func TestListarFichasFailsWhenTheLedgerDirectoryCannotBeRead(t *testing.T) {
	gitDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(gitDir, "vas-sentinel"), []byte("not a directory"), 0644); err != nil {
		t.Fatal(err)
	}

	shas, err := NuevoLedger(gitDir).ListarFichas()
	if err == nil {
		t.Fatalf("ListarFichas() = %v, nil; want an error: a ledger that cannot be enumerated must never be reported as one holding no fichas", shas)
	}
	if len(shas) != 0 {
		t.Errorf("ListarFichas() returned %v next to its error, want no SHAs", shas)
	}
}

// TestListarFichasFailsOnADanglingLedgerSymlink covers the shape that reports
// the same ErrNotExist as a ledger nobody ever wrote: os.ReadDir resolves the
// link and cannot separate a broken one from an absent path. Only absence is a
// real answer, so the distinction has to be made before the read.
//
// It matters on the destructive path in particular: directoriosLedgerV1 guards
// every linked worktree with Lstat before Stat, but appends the common
// directory unconditionally, so a broken link there reaches this listing with
// no check in front of it.
// This test is the only one that pins the Lstat guard, and the skip below is
// therefore a real coverage limit rather than a formality: removing the guard
// and keeping ReadDir's own ErrNotExist check leaves the regular-file test green,
// because ReadDir answers ENOTDIR there. Measured, and recorded under FU-16 in
// docs/reingenieria/f0-deuda.md.
func TestListarFichasFailsOnADanglingLedgerSymlink(t *testing.T) {
	gitDir := t.TempDir()
	if err := os.Symlink(filepath.Join(gitDir, "ledger-that-was-removed"), filepath.Join(gitDir, "vas-sentinel")); err != nil {
		t.Skipf("this platform refuses to create a symlink without extra privileges: %v", err)
	}

	shas, err := NuevoLedger(gitDir).ListarFichas()
	if err == nil {
		t.Fatalf("ListarFichas() = %v, nil; want an error: a ledger path pointing nowhere is a broken ledger, not an empty one", shas)
	}
	if len(shas) != 0 {
		t.Errorf("ListarFichas() returned %v next to its error, want no SHAs", shas)
	}
}

// TestListarFichasTreatsAMissingLedgerDirectoryAsEmpty holds the other side of
// FU-16 down. NuevoLedger does not create the directory — the first saved
// revision does — so its absence is a real answer and not a failure. A fix that
// propagated every ReadDir error would break `sentinel status`, the metrics
// reader and the prune's own provenance scan on any repository that never saved
// a review.
func TestListarFichasTreatsAMissingLedgerDirectoryAsEmpty(t *testing.T) {
	shas, err := NuevoLedger(t.TempDir()).ListarFichas()
	if err != nil {
		t.Fatalf("ListarFichas() error = %v, want nil: a ledger nobody has written to yet holds no fichas", err)
	}
	if len(shas) != 0 {
		t.Errorf("ListarFichas() = %v, want no SHAs", shas)
	}
}
