package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// prepararRepositorioConCommits crea un repo de prueba con estado inicial
// (base.txt), un commit que añade a.go y otro que añade b.go.
func prepararRepositorioConCommits(t *testing.T) string {
	t.Helper()
	dir := prepararRepositorioPrueba(t, map[string]string{"base.txt": "base\n"})
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0644); err != nil {
		t.Fatalf("no se pudo crear a.go: %v", err)
	}
	ejecutarGit(t, dir, "add", "a.go")
	ejecutarGit(t, dir, "commit", "-m", "feat(a): primer commit")
	if err := os.WriteFile(filepath.Join(dir, "b.go"), []byte("package b\n"), 0644); err != nil {
		t.Fatalf("no se pudo crear b.go: %v", err)
	}
	ejecutarGit(t, dir, "add", "b.go")
	ejecutarGit(t, dir, "commit", "-m", "feat(b): segundo commit")
	return dir
}

func TestMensajeCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioConCommits(t)
	t.Chdir(dir)

	head, err := SHAHead()
	if err != nil {
		t.Fatalf("SHAHead devolvió error: %v", err)
	}
	mensaje, err := MensajeCommit(head)
	if err != nil {
		t.Fatalf("MensajeCommit devolvió error: %v", err)
	}
	if mensaje != "feat(b): segundo commit" {
		t.Errorf("mensaje = %q, esperado 'feat(b): segundo commit'", mensaje)
	}
}

func TestDiffCommitContieneArchivo(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioConCommits(t)
	t.Chdir(dir)

	head, _ := SHAHead()
	diff, err := DiffCommit(head)
	if err != nil {
		t.Fatalf("DiffCommit devolvió error: %v", err)
	}
	if !strings.Contains(diff, "b.go") {
		t.Errorf("el diff debería mencionar b.go, obtenido: %s", diff)
	}
}

func TestSHAsRangoCronologico(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioConCommits(t)
	t.Chdir(dir)

	todos, err := SHAsRango("", "HEAD")
	if err != nil {
		t.Fatalf("SHAsRango devolvió error: %v", err)
	}
	if len(todos) != 3 {
		t.Fatalf("SHAsRango(\"\", HEAD) = %d commits, esperado 3 (inicial + 2)", len(todos))
	}

	// Rango desde el estado inicial: solo los dos commits de trabajo.
	shas, err := SHAsRango(todos[0], "HEAD")
	if err != nil {
		t.Fatalf("SHAsRango devolvió error: %v", err)
	}
	if len(shas) != 2 {
		t.Fatalf("SHAsRango = %d commits, esperado 2", len(shas))
	}
	primero, _ := MensajeCommit(shas[0])
	segundo, _ := MensajeCommit(shas[1])
	if primero != "feat(a): primer commit" || segundo != "feat(b): segundo commit" {
		t.Errorf("orden cronológico roto: %q, %q", primero, segundo)
	}
}

func TestArchivosDeCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioConCommits(t)
	t.Chdir(dir)

	shas, _ := SHAsRango("", "HEAD")
	// shas[1] es "feat(a): primer commit" (shas[0] es el estado inicial).
	archivos, err := ArchivosDeCommit(shas[1])
	if err != nil {
		t.Fatalf("ArchivosDeCommit devolvió error: %v", err)
	}
	if len(archivos) != 1 || archivos[0] != "a.go" {
		t.Errorf("archivos = %+v, esperado [a.go]", archivos)
	}
}

func TestSHAsHastaCronologico(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioConCommits(t)
	t.Chdir(dir)

	shas, err := SHAsHasta("HEAD")
	if err != nil {
		t.Fatalf("SHAsHasta devolvió error: %v", err)
	}
	if len(shas) != 3 {
		t.Fatalf("SHAsHasta = %d commits, esperado 3", len(shas))
	}
	primero, _ := MensajeCommit(shas[0])
	ultimo, _ := MensajeCommit(shas[2])
	if primero != "estado inicial" || ultimo != "feat(b): segundo commit" {
		t.Errorf("orden roto: %q ... %q", primero, ultimo)
	}
}

func TestRamaActual(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioConCommits(t)
	t.Chdir(dir)

	ejecutarGit(t, dir, "checkout", "-b", "feature/x")
	rama, err := RamaActual()
	if err != nil {
		t.Fatalf("RamaActual devolvió error: %v", err)
	}
	if rama != "feature/x" {
		t.Errorf("RamaActual = %q, esperado feature/x", rama)
	}
}

func TestResolverSHA(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioConCommits(t)
	t.Chdir(dir)

	head, _ := SHAHead()
	resuelto, err := ResolverSHA("HEAD")
	if err != nil {
		t.Fatalf("ResolverSHA(HEAD) devolvió error: %v", err)
	}
	if resuelto != head {
		t.Errorf("ResolverSHA(HEAD) = %q, esperado %q", resuelto, head)
	}

	if _, err := ResolverSHA("HEAD~1"); err != nil {
		t.Errorf("ResolverSHA(HEAD~1) devolvió error: %v", err)
	}
	if _, err := ResolverSHA("no-existe-esta-ref"); err == nil {
		t.Error("ResolverSHA(ref inválida) debería fallar")
	}
}

func TestExisteCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioConCommits(t)
	t.Chdir(dir)

	head, _ := SHAHead()
	if !ExisteCommit(head) {
		t.Error("ExisteCommit(HEAD) = false, esperado true")
	}
	if ExisteCommit(strings.Repeat("0", 40)) {
		t.Error("ExisteCommit(sha inexistente) = true, esperado false")
	}
}

func TestContenidoDeArchivoEnCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioConCommits(t)
	t.Chdir(dir)

	shas, _ := SHAsRango("", "HEAD")
	// shas[1] es "feat(a): primer commit", que añade a.go con "package a\n".
	contenido, err := ContenidoDeArchivoEnCommit(shas[1], "a.go")
	if err != nil {
		t.Fatalf("ContenidoDeArchivoEnCommit devolvió error: %v", err)
	}
	if contenido != "package a\n" {
		t.Errorf("contenido = %q, esperado %q", contenido, "package a\n")
	}

	if _, err := ContenidoDeArchivoEnCommit(shas[1], "no-existe.go"); err == nil {
		t.Error("ContenidoDeArchivoEnCommit con archivo inexistente en ese commit debería devolver error")
	}

	if c, p, e := ReadPathAtRevision(shas[1], "a.go"); !p || e != nil || c != "package a\n" {
		t.Errorf("ReadPathAtRevision present = %q/%v/%v", c, p, e)
	}
	if _, p, e := ReadPathAtRevision(shas[1], "no-existe.go"); p || e != nil {
		t.Errorf("ReadPathAtRevision absent = %v %v", p, e)
	}
	if _, _, e := ReadPathAtRevision("not-a-rev", "a.go"); e == nil {
		t.Error("invalid revision must fail, never report absence")
	}
}

func TestBlobDeArchivoEnCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioConCommits(t)
	t.Chdir(dir)

	shas, _ := SHAsRango("", "HEAD")
	// shas[1] es "feat(a): primer commit", que añade a.go con "package a\n".
	blob, err := BlobDeArchivoEnCommit(shas[1], "a.go")
	if err != nil {
		t.Fatalf("BlobDeArchivoEnCommit devolvió error: %v", err)
	}
	if blob == "" {
		t.Fatal("BlobDeArchivoEnCommit devolvió un hash vacío")
	}

	// Verificación independiente: git cat-file -p <blob> debe devolver
	// exactamente el contenido que se escribió en el commit.
	contenido, err := ejecutarGitSalida("cat-file", "-p", blob)
	if err != nil {
		t.Fatalf("git cat-file -p %s falló: %v", blob, err)
	}
	if contenido != "package a\n" {
		t.Errorf("contenido del blob = %q, esperado %q", contenido, "package a\n")
	}

	// b.go tiene contenido distinto: su blob debe ser distinto al de a.go
	// (confirma que no es un hash fijo, sino del contenido real).
	blobB, err := BlobDeArchivoEnCommit(shas[2], "b.go")
	if err != nil {
		t.Fatalf("BlobDeArchivoEnCommit devolvió error: %v", err)
	}
	if blobB == blob {
		t.Errorf("blob de b.go coincide con el de a.go: %s", blob)
	}

	if _, err := BlobDeArchivoEnCommit(shas[1], "no-existe.go"); err == nil {
		t.Error("BlobDeArchivoEnCommit con archivo inexistente en ese commit debería devolver error")
	}
}

func TestUpstreamOMainEligeMain(t *testing.T) {
	if testing.Short() {
		t.Skip("salta la integración con repositorio git real en modo -short")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git no está disponible en el PATH")
	}

	dir := prepararRepositorioConCommits(t)
	t.Chdir(dir)

	// Sin upstream: debe caer en la rama main o master local.
	ejecutarGit(t, dir, "checkout", "-b", "main")
	base, err := UpstreamOMain()
	if err != nil {
		t.Fatalf("UpstreamOMain devolvió error: %v", err)
	}
	if base != "main" {
		t.Errorf("UpstreamOMain = %q, esperado main", base)
	}
}

// prepareRepoWithMerge builds a repo with a real two-parent merge commit:
// master changes base.txt and a feat branch adds feature.go; the --no-ff
// merge joins them without conflict.
func prepareRepoWithMerge(t *testing.T) string {
	t.Helper()
	dir := prepararRepositorioPrueba(t, map[string]string{"base.txt": "base\n"})
	ejecutarGit(t, dir, "checkout", "-q", "-b", "feat")
	if err := os.WriteFile(filepath.Join(dir, "feature.go"), []byte("package feature\n"), 0644); err != nil {
		t.Fatalf("could not create feature.go: %v", err)
	}
	ejecutarGit(t, dir, "add", "feature.go")
	ejecutarGit(t, dir, "commit", "-q", "-m", "feat(f): branch change")
	ejecutarGit(t, dir, "checkout", "-q", "master")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base v2\n"), 0644); err != nil {
		t.Fatalf("could not modify base.txt: %v", err)
	}
	ejecutarGit(t, dir, "commit", "-qam", "chore(m): master change")
	ejecutarGit(t, dir, "merge", "-q", "--no-ff", "feat", "-m", "merge: two parents")
	return dir
}

func TestDiffCommitOnMergeReturnsFirstParentDiff(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in -short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available on PATH")
	}

	dir := prepareRepoWithMerge(t)
	t.Chdir(dir)

	head, _ := SHAHead()
	// Verify HEAD really is a two-parent merge commit: if the repo setup
	// changes, the test must fail here instead of misleading us.
	if parents := strings.Fields(ejecutarGit(t, dir, "rev-list", "--parents", "-n", "1", head)); len(parents) != 3 {
		t.Fatalf("HEAD should be a two-parent merge commit, rev-list gave %d fields", len(parents))
	}

	diff, err := DiffCommit(head)
	if err != nil {
		t.Fatalf("DiffCommit returned an error: %v", err)
	}
	if !strings.Contains(diff, "feature.go") || !strings.Contains(diff, "+package feature") {
		t.Errorf("merge diff should show the merged-branch change (feature.go), got: %q", diff)
	}
	if strings.Contains(diff, "base v2") {
		t.Errorf("merge diff must not include the first parent's own change (base v2), got: %q", diff)
	}
}

func TestCommitFilesOnMergeListsBranchFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in -short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available on PATH")
	}

	dir := prepareRepoWithMerge(t)
	t.Chdir(dir)

	head, _ := SHAHead()
	// Verify HEAD really is a two-parent merge commit: if the repo setup
	// changes, the test must fail here instead of misleading us.
	if parents := strings.Fields(ejecutarGit(t, dir, "rev-list", "--parents", "-n", "1", head)); len(parents) != 3 {
		t.Fatalf("HEAD should be a two-parent merge commit, rev-list gave %d fields", len(parents))
	}

	archivos, err := ArchivosDeCommit(head)
	if err != nil {
		t.Fatalf("ArchivosDeCommit returned an error: %v", err)
	}
	if len(archivos) != 1 || archivos[0] != "feature.go" {
		t.Errorf("merge ArchivosDeCommit should list exactly feature.go, got: %+v", archivos)
	}
}
