package review

import (
	"os"
	"os/exec"
	"path/filepath"
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
// renderer needs it to survive persistence to render it later.
func TestLedgerPersistsAggregatedFindings(t *testing.T) {
	dir := t.TempDir()
	ledger := NuevoLedger(dir)

	rev := Revision{
		At:     time.Now().UTC(),
		Result: VerdictBlock,
		AggregatedFindings: []Hallazgo{
			{Dimension: DimSecurity, Severity: SevCritical, Source: SourceReview, Confidence: 0.9, Description: "exposed secret"},
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
	eliminados, err := ledger.PurgarHuerfanas()
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

	eliminados, err := ledger.PurgarHuerfanas()
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
