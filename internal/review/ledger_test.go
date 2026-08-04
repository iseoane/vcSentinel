package review

import (
	"os"
	"path/filepath"
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

func TestLedgerMarcarCorregidaSinFichaEsNoOp(t *testing.T) {
	dir := t.TempDir()
	ledger := NuevoLedger(dir)
	if err := ledger.MarcarCorregida("noexiste", "bbb222"); err != nil {
		t.Fatalf("MarcarCorregida sobre SHA sin ficha devolvió error: %v", err)
	}
}
