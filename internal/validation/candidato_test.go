package validation

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

// repoDeCandidatoTest crea un repositorio git real (con un commit inicial) en
// un directorio temporal y posiciona ahí el cwd del proceso de test: las
// funciones de internal/git (Congelar, ArbolDe, CrearSnapshot...) ejecutan
// git contra el cwd del proceso, no reciben una ruta explícita, así que no
// hay otra forma de dirigirlas a un repo aislado por test.
func repoDeCandidatoTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	correr := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if salida, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, salida)
		}
	}
	correr("init")
	correr("config", "user.email", "test@example.com")
	correr("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "archivo.txt"), []byte("inicial\n"), 0644); err != nil {
		t.Fatalf("no se pudo escribir archivo.txt: %v", err)
	}
	correr("add", "archivo.txt")
	correr("commit", "-m", "inicial")

	t.Chdir(dir)
	return dir
}

// cfgPerfilCandidatoTest devuelve una config mínima con un único perfil
// ("perfil") que apunta a una única capability ("cap1"): basta para que
// EjecutarPerfil no caiga en la delegación al agente (perfil sin
// capabilities configuradas), que no es lo que T1.6 quiere probar aquí.
func cfgPerfilCandidatoTest(modo string) config.Config {
	var cfg config.Config
	cfg.Validation.Mode = modo
	cfg.Validation.Profiles = map[string][]string{"perfil": {"cap1"}}
	cfg.Validation.Capabilities = map[string]config.CapabilityConfig{
		"cap1": {Command: "true", FailsWhen: config.FailsWhenExitCode},
	}
	return cfg
}

// contarSnapshots cuenta las entradas del directorio de snapshots del repo en
// dir (0 si el directorio todavía no existe): sirve para comprobar que el
// modo inplace nunca crea uno.
func contarSnapshots(t *testing.T, dir string) int {
	t.Helper()
	commonDir, err := git.ObtenerGitCommonDir(dir)
	if err != nil {
		t.Fatalf("no se pudo obtener el common-dir: %v", err)
	}
	entradas, err := os.ReadDir(filepath.Join(commonDir, "vas-sentinel", "snapshots"))
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("no se pudo leer el directorio de snapshots: %v", err)
	}
	return len(entradas)
}

func TestEjecutarPerfilSobreCandidato_WorktreeSinCambios_EjecutaSobreSnapshotYNoQuedaObsoleto(t *testing.T) {
	dir := repoDeCandidatoTest(t)

	opts := OpcionesEjecucion{Worktree: dir, Cfg: cfgPerfilCandidatoTest(config.ModeWorktree)}
	var comandosRecibidos []string
	opts.Ejecutar = func(comando string) (int, string, error) {
		comandosRecibidos = append(comandosRecibidos, comando)
		return 0, "ok", nil
	}

	runs, err := EjecutarPerfilSobreCandidato("perfil", nil, opts)
	if err != nil {
		t.Fatalf("no esperaba error, obtuve: %v", err)
	}
	if len(runs) != 1 || runs[0].Comando != "true" {
		t.Fatalf("runs inesperados: %+v", runs)
	}
	if len(comandosRecibidos) != 1 {
		t.Fatalf("esperaba exactamente 1 comando ejecutado, obtuve %d", len(comandosRecibidos))
	}

	// El snapshot del árbol de HEAD debe existir tras la llamada (CrearSnapshot
	// es idempotente: si ya existe, esta segunda llamada solo lo reutiliza).
	arbol, err := git.ArbolDe("HEAD")
	if err != nil {
		t.Fatalf("no se pudo resolver el árbol de HEAD: %v", err)
	}
	snapshot, err := git.CrearSnapshot(arbol)
	if err != nil {
		t.Fatalf("no se pudo verificar el snapshot: %v", err)
	}
	info, err := os.Stat(snapshot)
	if err != nil || !info.IsDir() {
		t.Fatalf("esperaba que el snapshot %q existiera como directorio", snapshot)
	}
}

func TestEjecutarPerfilSobreCandidato_WorktreeModificadoDuranteEjecucion_QuedaObsoleto(t *testing.T) {
	dir := repoDeCandidatoTest(t)

	opts := OpcionesEjecucion{Worktree: dir, Cfg: cfgPerfilCandidatoTest(config.ModeWorktree)}
	opts.Ejecutar = func(comando string) (int, string, error) {
		// Simula que, mientras corre la validación, algo modifica el
		// worktree real (p. ej. un agente concurrente): el árbol congelado
		// deja de coincidir con el actual.
		if err := os.WriteFile(filepath.Join(dir, "archivo.txt"), []byte("modificado\n"), 0644); err != nil {
			t.Fatalf("no se pudo modificar archivo.txt: %v", err)
		}
		return 0, "ok", nil
	}

	runs, err := EjecutarPerfilSobreCandidato("perfil", nil, opts)
	if !errors.Is(err, ErrCandidatoObsoleto) {
		t.Fatalf("esperaba ErrCandidatoObsoleto, obtuve err=%v", err)
	}
	if runs != nil {
		t.Fatalf("esperaba runs nil cuando el candidato queda obsoleto, obtuve %+v", runs)
	}
}

func TestEjecutarPerfilSobreCandidato_InplaceConWorktreeSucio_AbortaSinEjecutar(t *testing.T) {
	dir := repoDeCandidatoTest(t)
	if err := os.WriteFile(filepath.Join(dir, "archivo.txt"), []byte("sucio\n"), 0644); err != nil {
		t.Fatalf("no se pudo ensuciar el worktree: %v", err)
	}

	opts := OpcionesEjecucion{Worktree: dir, Cfg: cfgPerfilCandidatoTest(config.ModeInplace)}
	opts.Ejecutar = func(comando string) (int, string, error) {
		t.Fatalf("no debía ejecutarse ningún comando en inplace con worktree sucio")
		return 0, "", nil
	}

	runs, err := EjecutarPerfilSobreCandidato("perfil", nil, opts)
	if err == nil {
		t.Fatal("esperaba un error por worktree sucio en modo inplace")
	}
	if runs != nil {
		t.Fatalf("esperaba runs nil al abortar, obtuve %+v", runs)
	}
}

func TestEjecutarPerfilSobreCandidato_InplaceConWorktreeLimpio_EjecutaSobreElWorktreeRealSinSnapshot(t *testing.T) {
	dir := repoDeCandidatoTest(t)
	antes := contarSnapshots(t, dir)

	opts := OpcionesEjecucion{Worktree: dir, Cfg: cfgPerfilCandidatoTest(config.ModeInplace)}
	var comandosRecibidos []string
	opts.Ejecutar = func(comando string) (int, string, error) {
		comandosRecibidos = append(comandosRecibidos, comando)
		return 0, "ok", nil
	}

	runs, err := EjecutarPerfilSobreCandidato("perfil", nil, opts)
	if err != nil {
		t.Fatalf("no esperaba error, obtuve: %v", err)
	}
	if len(runs) != 1 || runs[0].Comando != "true" {
		t.Fatalf("runs inesperados: %+v", runs)
	}
	if len(comandosRecibidos) != 1 {
		t.Fatalf("esperaba exactamente 1 comando ejecutado, obtuve %d", len(comandosRecibidos))
	}

	despues := contarSnapshots(t, dir)
	if despues != antes {
		t.Fatalf("el modo inplace no debía crear ningún snapshot: antes=%d despues=%d", antes, despues)
	}
}
