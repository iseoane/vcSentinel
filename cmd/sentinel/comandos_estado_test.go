package main

import (
	"fmt"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// TestHelperProcess NO es un test real: es el proceso hijo que arrancan los
// tests de exit code de este paquete para ejercer funciones que llaman a
// os.Exit sin matar el proceso `go test` padre (patrón estándar de Go, el
// mismo que usa la librería os/exec para probarse a sí misma). Sin el guard
// de la variable de entorno, una corrida normal de `go test` la trata como un
// test vacío que pasa.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("VAS_SENTINEL_HELPER_PROCESS") != "1" {
		return
	}
	worktree := os.Getenv("VAS_SENTINEL_HELPER_WORKTREE")
	switch os.Getenv("VAS_SENTINEL_HELPER_FN") {
	case "ejecutarLint":
		ejecutarLint(worktree)
	case "ejecutarReview":
		ejecutarReview(worktree, []string{"HEAD"})
	case "ejecutarPrReview":
		ejecutarPrReview(worktree, nil)
	}
	os.Exit(0)
}

// ejecutarComoSubproceso relanza este mismo binario de test para invocar fn
// (una de las ramas de TestHelperProcess) sobre worktree, y captura su salida
// combinada y su exit code. home fija HOME/USERPROFILE del subproceso: el
// yml global del usuario real de la máquina nunca debe filtrarse al test.
// cmd.Dir se fija a worktree (nunca al cwd real del repo de vas.sentinel):
// si el fix bajo prueba regresara y la función siguiera de largo tras el
// error de config, cualquier comando git/agente que intente lanzar debe
// operar sobre el tmpdir aislado, no sobre este repositorio real.
func ejecutarComoSubproceso(t *testing.T, fn, worktree, home string) (salida string, exitCode int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Dir = worktree
	env := []string{
		"VAS_SENTINEL_HELPER_PROCESS=1",
		"VAS_SENTINEL_HELPER_FN=" + fn,
		"VAS_SENTINEL_HELPER_WORKTREE=" + worktree,
		"HOME=" + home,
		"USERPROFILE=" + home,
	}
	for _, kv := range os.Environ() {
		clave := strings.SplitN(kv, "=", 2)[0]
		if clave == "HOME" || clave == "USERPROFILE" || strings.HasPrefix(kv, "VAS_SENTINEL_HELPER_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = env

	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err == nil {
		return buf.String(), 0
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("no se pudo ejecutar el subproceso de %s: %v", fn, err)
	}
	return buf.String(), exitErr.ExitCode()
}

// escribirYmlConClaveDesconocida escribe un vassentinel.yml per-proyecto con
// una clave fuera del esquema, para los tests de propagación de error de F1.
func escribirYmlConClaveDesconocida(t *testing.T, worktree string) {
	t.Helper()
	ruta := filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml")
	if err := os.MkdirAll(filepath.Dir(ruta), 0755); err != nil {
		t.Fatalf("no se pudo crear %s: %v", filepath.Dir(ruta), err)
	}
	contenido := "active_agent: \"claude\"\nclave_inexistente: true\n"
	if err := os.WriteFile(ruta, []byte(contenido), 0644); err != nil {
		t.Fatalf("no se pudo escribir %s: %v", ruta, err)
	}
}

// TestEjecutarLint_ClaveDesconocidaEnYml_Exit1ConLinea cubre el Fix 1 (F1,
// hallazgo del orquestador): antes de este fix, ejecutarLint usaba
// CargarConfiguracionLocal (sin error), así que una clave desconocida en el
// yml se ignoraba en silencio y, al no quedar lint_commands configurados por
// el yml roto, el comando terminaba con "no hay comandos de lint" y exit 0 —
// exactamente lo contrario de lo que exige la ficha. Con
// CargarConfiguracionLocalEstricta debe cortar con exit 1 y el error visible.
func TestEjecutarLint_ClaveDesconocidaEnYml_Exit1ConLinea(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	escribirYmlConClaveDesconocida(t, worktree)

	salida, exit := ejecutarComoSubproceso(t, "ejecutarLint", worktree, home)

	if exit != 1 {
		t.Errorf("exit esperado 1, obtuve %d (salida: %q)", exit, salida)
	}
	if !strings.Contains(salida, "line") {
		t.Errorf("la salida debe incluir la línea del error del yml, obtuve: %q", salida)
	}
}

func TestParsearFlagsAuditoriaDefault(t *testing.T) {
	// Sin argumentos, targets queda vacío (B17): el default a "HEAD" ya no
	// vive aquí, porque esta función la comparten status (sin concepto de
	// target) y review (que sí lo aplica en resolverShasAuditoria).
	flags, err := parsearFlagsAuditoria(nil)
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria(nil) devolvió error: %v", err)
	}
	if len(flags.targets) != 0 {
		t.Errorf("targets = %+v, esperado vacío", flags.targets)
	}
	if flags.all || flags.chain || flags.gate || flags.jsonOut || flags.prune {
		t.Error("flags booleanos deberían estar apagados por defecto")
	}
}

func TestParsearFlagsAuditoriaCompletas(t *testing.T) {
	flags, err := parsearFlagsAuditoria([]string{
		"abc123", "--dims", "logic, security", "--profile", "deep",
		"--chain", "--answer", "no aplica aqui", "--gate",
	})
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria devolvió error: %v", err)
	}
	if len(flags.targets) != 1 || flags.targets[0] != "abc123" {
		t.Errorf("targets = %+v, esperado [abc123]", flags.targets)
	}
	if len(flags.dims) != 2 || flags.dims[0] != "logic" || flags.dims[1] != "security" {
		t.Errorf("dims = %+v, esperado [logic security]", flags.dims)
	}
	if flags.profile != "deep" || flags.answer != "no aplica aqui" {
		t.Errorf("profile/answer = %q/%q", flags.profile, flags.answer)
	}
	if !flags.chain || !flags.gate {
		t.Error("chain/gate deberían estar activos")
	}
}

func TestParsearFlagsAuditoriaMultiplesTargets(t *testing.T) {
	flags, err := parsearFlagsAuditoria([]string{"abc123", "def456", "HEAD~2"})
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria devolvió error: %v", err)
	}
	if !reflect.DeepEqual(flags.targets, []string{"abc123", "def456", "HEAD~2"}) {
		t.Errorf("targets = %+v, esperado [abc123 def456 HEAD~2]", flags.targets)
	}
}

func TestParsearFlagsAuditoriaErrores(t *testing.T) {
	pruebas := []struct {
		nombre string
		args   []string
	}{
		{"flag sin valor", []string{"--profile"}},
		{"opción desconocida", []string{"--nada"}},
	}
	for _, prueba := range pruebas {
		if _, err := parsearFlagsAuditoria(prueba.args); err == nil {
			t.Errorf("%s: debería devolver error", prueba.nombre)
		}
	}
}

func TestParsearFlagsAuditoriaHeadExplicito(t *testing.T) {
	flags, err := parsearFlagsAuditoria([]string{"HEAD"})
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria(HEAD) devolvió error: %v", err)
	}
	if !reflect.DeepEqual(flags.targets, []string{"HEAD"}) {
		t.Errorf("targets = %+v, esperado [HEAD]", flags.targets)
	}
}

func TestStatusRechazaFlagsNoAplicables(t *testing.T) {
	pruebas := []struct {
		nombre string
		args   []string
	}{
		{"--dims", []string{"--dims", "logic"}},
		{"--profile", []string{"--profile", "deep"}},
		{"--chain", []string{"--chain"}},
		{"--gate", []string{"--gate"}},
		{"--all", []string{"--all"}},
		{"target", []string{"abc123"}},
	}
	for _, prueba := range pruebas {
		flags, err := parsearFlagsAuditoria(prueba.args)
		if err != nil {
			t.Fatalf("%s: parsearFlagsAuditoria devolvió error: %v", prueba.nombre, err)
		}
		if !flagsNoAplicablesAStatus(flags) {
			t.Errorf("%s: debería detectarse como no aplicable a status", prueba.nombre)
		}
	}
}

// TestStatusAceptaInvocacionDesnuda reproduce el bug real: "sentinel status"
// sin ningún argumento siempre se rechazaba, porque parsearFlagsAuditoria
// rellenaba targets con ["HEAD"] incondicionalmente (para que review/lint no
// tuvieran que repetir ese default), y flagsNoAplicablesAStatus no podía
// distinguir "el usuario no pasó nada" de "el usuario pasó un target". status
// no tiene ningún target que aceptar, así que la invocación desnuda debe
// pasar limpia.
func TestStatusAceptaInvocacionDesnuda(t *testing.T) {
	flags, err := parsearFlagsAuditoria(nil)
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria(nil) devolvió error: %v", err)
	}
	if flagsNoAplicablesAStatus(flags) {
		t.Error("sentinel status sin argumentos debería aceptarse, no rechazarse")
	}
}

func TestParsearFlagsAuditoriaTimeout(t *testing.T) {
	flags, err := parsearFlagsAuditoria([]string{"--timeout", "900"})
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria(--timeout 900) devolvió error: %v", err)
	}
	if flags.timeout != 900*time.Second {
		t.Errorf("timeout = %v, esperado 900s", flags.timeout)
	}
}

func TestParsearFlagsAuditoriaTimeoutAusenteEsCero(t *testing.T) {
	flags, err := parsearFlagsAuditoria([]string{"HEAD"})
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria devolvió error: %v", err)
	}
	if flags.timeout != 0 {
		t.Errorf("timeout = %v, esperado 0 (sin override)", flags.timeout)
	}
}

func TestParsearFlagsAuditoriaTimeoutInvalido(t *testing.T) {
	pruebas := []struct {
		nombre string
		args   []string
	}{
		{"sin valor", []string{"--timeout"}},
		{"no numérico", []string{"--timeout", "mucho"}},
		{"cero", []string{"--timeout", "0"}},
		{"negativo", []string{"--timeout", "-30"}},
	}
	for _, prueba := range pruebas {
		if _, err := parsearFlagsAuditoria(prueba.args); err == nil {
			t.Errorf("--timeout %s: debería devolver error", prueba.nombre)
		}
	}
}

func TestStatusRechazaTimeout(t *testing.T) {
	flags, err := parsearFlagsAuditoria([]string{"--timeout", "900"})
	if err != nil {
		t.Fatalf("parsearFlagsAuditoria devolvió error: %v", err)
	}
	if !flagsNoAplicablesAStatus(flags) {
		t.Error("--timeout debería detectarse como no aplicable a status")
	}
}

func TestAplicarTimeoutFlag(t *testing.T) {
	base := config.Config{Review: config.ReviewConfig{Timeout: 600 * time.Second}}

	sinFlag := aplicarTimeoutFlag(base, flagsAuditoria{})
	if sinFlag.Review.Timeout != 600*time.Second {
		t.Errorf("sin --timeout: Timeout = %v, esperado el de la config (600s)", sinFlag.Review.Timeout)
	}

	conFlag := aplicarTimeoutFlag(base, flagsAuditoria{timeout: 900 * time.Second})
	if conFlag.Review.Timeout != 900*time.Second {
		t.Errorf("con --timeout: Timeout = %v, esperado 900s", conFlag.Review.Timeout)
	}
	if base.Review.Timeout != 600*time.Second {
		t.Errorf("aplicarTimeoutFlag mutó la config original: %v", base.Review.Timeout)
	}
}

// TestPurgarHuerfanasAlcanzaLosLedgersDeWorktree is the second half of FU-12,
// and the reason it must land before T9.5 rather than after.
//
// PurgarHuerfanas is a deletion primitive, and T9.5 builds its retention
// cascade on it together with collectProvenanceReferences. That collector was
// already taught to enumerate every ledger in the repository; this one still
// purged the current checkout's ledger alone. A cascade assembled from the two
// would decide what to keep by consulting thirteen ledgers and then delete from
// one, which leaks every ficha a delegated writer produced and makes the
// cascade's own accounting wrong.
// repoConWorktreeEnlazado builds a repository with one commit and one linked
// worktree named "linked", as a sibling of the main checkout, and returns the
// main checkout.
// contenidoUnicoDeRepo evita que dos repositorios de prueba creados en el mismo
// segundo, con el mismo árbol, mensaje e identidad, produzcan commits
// byte-idénticos y por tanto el MISMO SHA. Sin esto un test que dice "este
// commit no existe en el otro repositorio" puede estar mintiendo, y pasar por
// ese motivo en vez de por el que declara.
var contenidoUnicoDeRepo atomic.Int64

func repoConWorktreeEnlazado(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	worktree := filepath.Join(base, "main")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	correr := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + base,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	correr("init", "-q", "-b", "main")
	contenido := fmt.Sprintf("repo %d\n", contenidoUnicoDeRepo.Add(1))
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte(contenido), 0o644); err != nil {
		t.Fatal(err)
	}
	correr("add", "a.txt")
	correr("commit", "-qm", "first")
	correr("worktree", "add", "-q", "--detach", filepath.Join(base, "linked"))
	return worktree
}

func TestPurgarHuerfanasAlcanzaLosLedgersDeWorktree(t *testing.T) {
	worktree := t.TempDir()
	correr := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + worktree,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	correr("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	correr("add", "a.txt")
	correr("commit", "-qm", "first")

	enlazado := filepath.Join(t.TempDir(), "linked")
	correr("worktree", "add", "-q", "--detach", enlazado)

	gitDirPrincipal, err := git.ObtenerGitDirDe(worktree)
	if err != nil {
		t.Fatal(err)
	}
	gitDirEnlazado, err := git.ObtenerGitDirDe(enlazado)
	if err != nil {
		t.Fatal(err)
	}

	// Two fichas in the linked worktree's ledger: one for a SHA the repository
	// does not contain, one for its real HEAD. Both are needed. Without the
	// live one the test would pass against a purge that simply deleted
	// everything it found, which is the opposite failure.
	huerfana := "0123456789abcdef0123456789abcdef01234567"
	viva := revisionDeWorktree(t, worktree, "HEAD")
	ledgerEnlazado := review.NuevoLedger(gitDirEnlazado)
	for _, sha := range []string{huerfana, viva} {
		if err := ledgerEnlazado.GuardarRevision(sha, "fixture", "bucket", "model", review.Revision{
			At: time.Now(), Result: "ok",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// No working-directory change: orphanhood is now resolved against worktree
	// itself. That the live SHA survives while the test binary runs from an
	// unrelated repository is precisely the property under test.
	eliminados, err := purgarHuerfanas(worktree, gitDirPrincipal)
	if err != nil {
		t.Fatalf("purgarHuerfanas() error = %v", err)
	}
	if !slices.Contains(eliminados, huerfana) {
		t.Fatalf("purgarHuerfanas() = %v, missing the orphan %q held in the linked worktree ledger %q; T9.5 would decide from every ledger and delete from one",
			eliminados, huerfana, gitDirEnlazado)
	}
	if ficha, err := ledgerEnlazado.LeerFicha(huerfana); err != nil || ficha != nil {
		t.Errorf("the orphan ficha survives in the linked worktree ledger (ficha=%v, err=%v)", ficha, err)
	}
	if slices.Contains(eliminados, viva) {
		t.Errorf("purgarHuerfanas() deleted %q, whose commit exists; it is purging by reach and not by orphanhood", viva)
	}
	if ficha, err := ledgerEnlazado.LeerFicha(viva); err != nil || ficha == nil {
		t.Errorf("the ficha of a live commit was deleted from the linked worktree ledger (ficha=%v, err=%v)", ficha, err)
	}
}

// revisionDeWorktree resolves a revision inside worktree without depending on
// the process working directory.
func revisionDeWorktree(t *testing.T, worktree, revision string) string {
	t.Helper()
	salida, err := exec.Command("git", "-C", worktree, "rev-parse", revision).Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", revision, err)
	}
	return strings.TrimSpace(string(salida))
}

// TestPurgarHuerfanasLimpiaLosEventosDondeEstabanSusFichas pins the pairing the
// review found broken. events.jsonl lives per gitDir exactly as the ledger
// does, so purging fichas across every checkout while cleaning events in one
// leaves entries pointing at deleted SHAs precisely where the command reports
// having cleaned them.
func TestPurgarHuerfanasLimpiaLosEventosDondeEstabanSusFichas(t *testing.T) {
	worktree := repoConWorktreeEnlazado(t)
	gitDirPrincipal, err := git.ObtenerGitDirDe(worktree)
	if err != nil {
		t.Fatal(err)
	}
	enlazado := filepath.Join(worktree, "..", "linked")
	gitDirEnlazado, err := git.ObtenerGitDirDe(enlazado)
	if err != nil {
		t.Fatal(err)
	}

	huerfana := "0123456789abcdef0123456789abcdef01234567"
	viva := revisionDeWorktree(t, worktree, "HEAD")
	if err := review.NuevoLedger(gitDirEnlazado).GuardarRevision(huerfana, "gone", "b", "m", review.Revision{At: time.Now(), Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	// One event per SHA in the linked worktree's own stream.
	for _, sha := range []string{huerfana, viva} {
		if err := ops.RegistrarEvento(gitDirEnlazado, "review", 0, []string{sha}, ops.EventDetail{}, ""); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := purgarHuerfanasConEventos(worktree, gitDirPrincipal); err != nil {
		t.Fatalf("purgarHuerfanasConEventos() error = %v", err)
	}

	eventos, err := ops.UltimosEventos(gitDirEnlazado, 100)
	if err != nil {
		t.Fatalf("UltimosEventos: %v", err)
	}
	var quedanHuerfano, quedanVivo bool
	for _, evento := range eventos {
		for _, sha := range evento.Shas {
			if sha == huerfana {
				quedanHuerfano = true
			}
			if sha == viva {
				quedanVivo = true
			}
		}
	}
	if quedanHuerfano {
		t.Errorf("the event of the purged ficha survives in the linked worktree stream; the command reports having cleaned it")
	}
	if !quedanVivo {
		t.Errorf("the event of a live commit was deleted; the purge is removing by reach and not by orphanhood")
	}
}

// TestPurgarHuerfanasIgnoraGitDirDelEntorno pins the CRITICAL that blocked the
// previous commit. GIT_DIR takes priority over "-C": with it set, a
// worktree-scoped query answers for the repository GIT_DIR names instead.
// Sentinel runs inside its own pre-commit hook, which is exactly a context
// where Git exports these variables, so a query that decides deletions cannot
// trust "-C" without clearing them. Redirected, every live commit of the target
// repository looks orphaned and the purge empties the ledgers.
func TestPurgarHuerfanasIgnoraGitDirDelEntorno(t *testing.T) {
	worktree := repoConWorktreeEnlazado(t)
	gitDirPrincipal, err := git.ObtenerGitDirDe(worktree)
	if err != nil {
		t.Fatal(err)
	}
	viva := revisionDeWorktree(t, worktree, "HEAD")
	if err := review.NuevoLedger(gitDirPrincipal).GuardarRevision(viva, "live", "b", "m", review.Revision{At: time.Now(), Result: "ok"}); err != nil {
		t.Fatal(err)
	}

	// An unrelated repository, which knows nothing about the SHA above.
	ajeno := repoConWorktreeEnlazado(t)
	gitDirAjeno, err := git.ObtenerGitDirDe(ajeno)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", gitDirAjeno)

	eliminados, err := purgarHuerfanas(worktree, gitDirPrincipal)
	if err != nil {
		t.Fatalf("purgarHuerfanas() error = %v", err)
	}
	if slices.Contains(eliminados, viva) {
		t.Errorf("purgarHuerfanas() deleted %q while GIT_DIR pointed at an unrelated repository; the containment query was redirected away from the worktree it was told to purge", viva)
	}
	if ficha, err := review.NuevoLedger(gitDirPrincipal).LeerFicha(viva); err != nil || ficha == nil {
		t.Errorf("the ficha of a live commit was deleted (ficha=%v, err=%v)", ficha, err)
	}
}

// TestPurgarHuerfanasNoMezclaRepositorios pins the defect that sanitizing half
// the purge introduced. Containment was queried against the requested worktree
// while ledger discovery still inherited an ambient GIT_DIR, so with GIT_DIR
// naming repository B its ledgers were enumerated and then classified against
// repository A's refs. Every live ficha in B reads as orphaned there. Half a
// fix was worse than none, because inconsistency deletes across repositories
// while consistency merely looks at the wrong one.
func TestPurgarHuerfanasNoMezclaRepositorios(t *testing.T) {
	objetivo := repoConWorktreeEnlazado(t)
	gitDirObjetivo, err := git.ObtenerGitDirDe(objetivo)
	if err != nil {
		t.Fatal(err)
	}

	ajeno := repoConWorktreeEnlazado(t)
	gitDirAjeno, err := git.ObtenerGitDirDe(ajeno)
	if err != nil {
		t.Fatal(err)
	}
	// A live ficha in the OTHER repository, which the purge must never reach.
	vivaAjena := revisionDeWorktree(t, ajeno, "HEAD")
	if err := review.NuevoLedger(gitDirAjeno).GuardarRevision(vivaAjena, "live elsewhere", "b", "m", review.Revision{At: time.Now(), Result: "ok"}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GIT_DIR", gitDirAjeno)

	if _, err := purgarHuerfanas(objetivo, gitDirObjetivo); err != nil {
		t.Fatalf("purgarHuerfanas() error = %v", err)
	}

	if ficha, err := review.NuevoLedger(gitDirAjeno).LeerFicha(vivaAjena); err != nil || ficha == nil {
		t.Errorf("purging %q deleted a live ficha belonging to the unrelated repository %q (ficha=%v, err=%v); ledger discovery and containment were resolving different repositories",
			objetivo, ajeno, ficha, err)
	}
}

// TestPurgarHuerfanasNoBorraCuandoNoPuedePreguntar is the property the previous
// four rounds kept missing one hole at a time. While any command failure meant
// "absent", every environment variable that could break the query became a
// deletion of live records, and closing them one by one only changed which
// failure reached the wrong rule.
//
// An unrelated object store is the case the review named: rev-parse and the
// common-dir lookup both still succeed, so the repository looks perfectly
// usable, and only the object read fails. The purge must refuse to decide
// rather than decide wrongly.
func TestPurgarHuerfanasNoBorraCuandoNoPuedePreguntar(t *testing.T) {
	worktree := repoConWorktreeEnlazado(t)
	gitDir, err := git.ObtenerGitDirDe(worktree)
	if err != nil {
		t.Fatal(err)
	}
	viva := revisionDeWorktree(t, worktree, "HEAD")
	if err := review.NuevoLedger(gitDir).GuardarRevision(viva, "live", "b", "m", review.Revision{At: time.Now(), Result: "ok"}); err != nil {
		t.Fatal(err)
	}

	// An empty object store: the repository resolves, its objects do not.
	vacio := filepath.Join(t.TempDir(), "sin-objetos")
	if err := os.MkdirAll(vacio, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_OBJECT_DIRECTORY", vacio)

	_, err = purgarHuerfanas(worktree, gitDir)
	if err == nil {
		t.Errorf("purgarHuerfanas() returned no error while it could not read the repository's objects; a question it cannot answer must never authorise a deletion")
	}
	if ficha, lerr := review.NuevoLedger(gitDir).LeerFicha(viva); lerr != nil || ficha == nil {
		t.Errorf("the ficha of a live commit was deleted because the object store was unreadable (ficha=%v, err=%v)", ficha, lerr)
	}
}

// TestPurgarHuerfanasPorLedgerDevuelveLoYaBorradoAlFallar pins the half of the
// partial-result contract the outer function could not fix on its own. The
// per-ledger loop deletes fichas directory by directory, so when one of them
// fails the earlier ones are already gone. Returning nil there dropped them:
// purgarHuerfanasConEventos cleans events from that very list, so the events of
// the deleted fichas survived pointing at records the command had removed, and
// the operator was told only that the purge failed.
//
// The failure is staged with a file shape rather than a permission bit, which
// is a no-op under root: a ficha whose path is a non-empty directory is listed
// like any other and refuses to be removed.
func TestPurgarHuerfanasPorLedgerDevuelveLoYaBorradoAlFallar(t *testing.T) {
	worktree := t.TempDir()
	correr := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + worktree,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	correr("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	correr("add", "a.txt")
	correr("commit", "-qm", "first")
	enlazado := filepath.Join(t.TempDir(), "linked")
	correr("worktree", "add", "-q", "--detach", enlazado)

	gitDirPrincipal, err := git.ObtenerGitDirDe(worktree)
	if err != nil {
		t.Fatal(err)
	}
	gitDirEnlazado, err := git.ObtenerGitDirDe(enlazado)
	if err != nil {
		t.Fatal(err)
	}

	// The common directory is visited first, so this one is deleted before the
	// loop reaches the ledger it cannot purge.
	const borrable = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := review.NuevoLedger(gitDirPrincipal).GuardarRevision(borrable, "fixture", "b", "m",
		review.Revision{At: time.Now(), Result: review.VerdictOK}); err != nil {
		t.Fatal(err)
	}
	const irremovible = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	ruta := review.NuevoLedger(gitDirEnlazado).RutaFicha(irremovible)
	if err := os.MkdirAll(filepath.Join(ruta, "ocupado"), 0o755); err != nil {
		t.Fatal(err)
	}

	porDirectorio, err := purgarHuerfanasPorLedger(worktree, gitDirPrincipal)
	if err == nil {
		t.Fatalf("purgarHuerfanasPorLedger() error = nil; want the ledger it could not purge reported")
	}
	if !slices.Contains(porDirectorio[gitDirPrincipal], borrable) {
		t.Errorf("purgarHuerfanasPorLedger() = %v, want %q under %q: it was deleted before the failure, and its events are cleaned from this very result",
			porDirectorio, borrable, gitDirPrincipal)
	}
	if ficha, lerr := review.NuevoLedger(gitDirPrincipal).LeerFicha(borrable); lerr != nil || ficha != nil {
		t.Errorf("the ficha reported as deleted is still readable (ficha=%v, err=%v); the fixture no longer exercises the case", ficha, lerr)
	}
}
