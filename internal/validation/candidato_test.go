package validation

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/graph"
)

type proveedorGraphConError struct{}

func (proveedorGraphConError) Nombre() string { return "error" }
func (proveedorGraphConError) Analizar([]string) (graph.ResultadoAnalisis, error) {
	return graph.ResultadoAnalisis{}, errors.New("graph no disponible")
}

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
	if err := os.MkdirAll(filepath.Join(dir, "internal", "a"), 0755); err != nil {
		t.Fatalf("no se pudo crear paquete de prueba: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/candidato\n\ngo 1.26\n"), 0644); err != nil {
		t.Fatalf("no se pudo escribir go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "a", "a.go"), []byte("package a\n"), 0644); err != nil {
		t.Fatalf("no se pudo escribir a.go: %v", err)
	}
	correr("add", ".")
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

// TestEjecutarPerfilSobreCandidato_WorktreeSucioAlCongelar_ElSnapshotRefleja
// ElCandidatoNoSoloHEAD reproduce el hallazgo de la revisión de fase F1: si
// el worktree YA estaba sucio antes de llamar a esta función (no durante la
// ejecución, que es el caso que ya cubre SigueVigente), el snapshot debe
// reflejar ese contenido sin commitear —el que Congelar() ancla vía
// stash-anchor—, no el árbol limpio de HEAD. Antes del fix, el snapshot se
// creaba siempre con ArbolDe("HEAD") y este test habría visto "inicial" en
// vez de "sucio".
func TestEjecutarPerfilSobreCandidato_WorktreeSucioAlCongelar_ElSnapshotRefleja(t *testing.T) {
	dir := repoDeCandidatoTest(t)

	if err := os.WriteFile(filepath.Join(dir, "archivo.txt"), []byte("sucio\n"), 0644); err != nil {
		t.Fatalf("no se pudo ensuciar el worktree: %v", err)
	}

	opts := OpcionesEjecucion{Worktree: dir, Cfg: cfgPerfilCandidatoTest(config.ModeWorktree)}
	opts.Ejecutar = func(comando string) (int, string, error) { return 0, "ok", nil }

	if _, err := EjecutarPerfilSobreCandidato("perfil", nil, opts); err != nil {
		t.Fatalf("no esperaba error, obtuve: %v", err)
	}

	// Inspeccionar el snapshot que la función bajo prueba REALMENTE creó (no
	// uno recalculado aparte, que CrearSnapshot generaría igual sin importar
	// lo que hiciera la función: eso no probaría nada sobre su comportamiento
	// real). Un repo de prueba recién creado solo puede tener un snapshot.
	commonDir, err := git.ObtenerGitCommonDir(dir)
	if err != nil {
		t.Fatalf("no se pudo obtener el common-dir: %v", err)
	}
	dirSnapshots := filepath.Join(commonDir, "vas-sentinel", "snapshots")
	entradas, err := os.ReadDir(dirSnapshots)
	if err != nil {
		t.Fatalf("no se pudo leer el directorio de snapshots: %v", err)
	}
	if len(entradas) != 1 {
		t.Fatalf("esperaba exactamente 1 snapshot, hay %d", len(entradas))
	}
	contenido, err := os.ReadFile(filepath.Join(dirSnapshots, entradas[0].Name(), "archivo.txt"))
	if err != nil {
		t.Fatalf("no se pudo leer archivo.txt del snapshot real: %v", err)
	}
	if string(contenido) != "sucio\n" {
		t.Fatalf("el snapshot real contiene %q, esperaba el contenido sucio del candidato congelado, no el de HEAD", contenido)
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

func TestEjecutarPerfilSobreCandidato_AlcanceSoloConGrafoCompleto(t *testing.T) {
	dir := repoDeCandidatoTest(t)
	cfg := cfgPerfilCandidatoTest(config.ModeWorktree)
	cfg.Validation.Capabilities["cap1"] = config.CapabilityConfig{
		Command:       "go test ./...",
		SupportsScope: true,
		ScopedCommand: "go test {packages}",
	}

	casos := []struct {
		nombre, ruta, comando, alcance, scoped string
		errorGraph                             bool
	}{
		{"incompleto ejecuta el comando completo exacto", "archivo.txt", "go test ./...", AlcanceCompleto, "go test {packages}", false},
		{"completo sustituye el paquete autorizado", "internal/a/a.go", "go test example.com/candidato/internal/a", AlcanceParcial, "go test {packages}", false},
		{"scoped_command inválido ejecuta el completo exacto", "internal/a/a.go", "go test ./...", AlcanceCompleto, "go test ./internal/...", false},
		{"error ejecuta el comando completo exacto", "internal/a/a.go", "go test ./...", AlcanceCompleto, "go test {packages}", true},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			cfgCaso := cfg
			capacidad := cfg.Validation.Capabilities["cap1"]
			capacidad.ScopedCommand = caso.scoped
			cfgCaso.Validation.Capabilities = map[string]config.CapabilityConfig{"cap1": capacidad}
			var ejecutado string
			runs, err := EjecutarPerfilSobreCandidato("perfil", []string{caso.ruta}, OpcionesEjecucion{
				Worktree: dir,
				Cfg:      cfgCaso,
				ProveedorGraph: func(snapshot, treeOID string) graph.GraphProvider {
					if snapshot == dir || treeOID == "" {
						t.Fatalf("el grafo recibió el worktree vivo o un tree OID vacío: snapshot=%q tree=%q", snapshot, treeOID)
					}
					if caso.errorGraph {
						return proveedorGraphConError{}
					}
					return graph.NuevoProveedorNativo(snapshot, treeOID)
				},
				Ejecutar: func(comando string) (int, string, error) {
					ejecutado = comando
					return 0, "", nil
				},
			})
			if err != nil {
				t.Fatalf("EjecutarPerfilSobreCandidato falló: %v", err)
			}
			if ejecutado != caso.comando || runs[0].Comando != caso.comando {
				t.Fatalf("comando = %q, run = %q; esperado exacto %q", ejecutado, runs[0].Comando, caso.comando)
			}
			if runs[0].Alcance != caso.alcance || runs[0].MotivoAlcance == "" {
				t.Fatalf("decisión de alcance no explicada: %+v", runs[0])
			}
		})
	}
}

// TestEjecutarPerfilSobreCandidato_SinGraphProviderUsaCommandCompleto es el
// criterio de salida de F4 "sin GraphProvider (nil), el sistema funciona
// igual y valida completo": aunque la capability declare supports_scope y
// haya un archivo cambiado real, sin ProveedorGraph configurado (nil) nunca
// se llega a analizar el grafo, así que la autorización de alcance se queda
// en su valor cero (no autorizada) y el comando ejecutado es el completo
// exacto, nunca el acotado.
func TestEjecutarPerfilSobreCandidato_SinGraphProviderUsaCommandCompleto(t *testing.T) {
	dir := repoDeCandidatoTest(t)
	cfg := cfgPerfilCandidatoTest(config.ModeWorktree)
	cfg.Validation.Capabilities["cap1"] = config.CapabilityConfig{
		Command:       "go test ./...",
		SupportsScope: true,
		ScopedCommand: "go test {packages}",
	}

	var ejecutado string
	runs, err := EjecutarPerfilSobreCandidato("perfil", []string{"internal/a/a.go"}, OpcionesEjecucion{
		Worktree:       dir,
		Cfg:            cfg,
		ProveedorGraph: nil,
		Ejecutar: func(comando string) (int, string, error) {
			ejecutado = comando
			return 0, "", nil
		},
	})
	if err != nil {
		t.Fatalf("EjecutarPerfilSobreCandidato falló: %v", err)
	}
	if ejecutado != "go test ./..." || runs[0].Comando != "go test ./..." {
		t.Fatalf("comando = %q, run = %q; esperado el completo exacto sin GraphProvider", ejecutado, runs[0].Comando)
	}
	if runs[0].Alcance != AlcanceCompleto || runs[0].MotivoAlcance == "" {
		t.Fatalf("decisión de alcance no explicada: %+v", runs[0])
	}
}

// TestEjecutarPerfilSobreCandidato_BarraSnapshotsAntiguos cierra el cable de
// limpieza: cada flujo que crea un snapshot barre los que superan la
// retención, sin tocar los frescos reutilizables.
func TestEjecutarPerfilSobreCandidato_BarraSnapshotsAntiguos(t *testing.T) {
	dir := repoDeCandidatoTest(t)

	arbolHEAD, err := git.ArbolDe("HEAD")
	if err != nil {
		t.Fatalf("no se pudo resolver el árbol de HEAD: %v", err)
	}
	emptyTree := exec.Command("git", "mktree")
	emptyTree.Dir = dir
	emptyTree.Stdin = strings.NewReader("")
	output, err := emptyTree.Output()
	if err != nil {
		t.Fatalf("no se pudo materializar el árbol vacío: %v", err)
	}
	antiguo, err := git.CrearSnapshot(strings.TrimSpace(string(output)))
	if err != nil {
		t.Fatalf("no se pudo crear el snapshot envejecido: %v", err)
	}
	pasado := time.Now().Add(-(git.RetencionSnapshots + time.Hour))
	if err := os.Chtimes(antiguo, pasado, pasado); err != nil {
		t.Fatalf("no se pudo envejecer el snapshot: %v", err)
	}

	opts := OpcionesEjecucion{Worktree: dir, Cfg: cfgPerfilCandidatoTest(config.ModeWorktree)}
	opts.Ejecutar = func(string) (int, string, error) { return 0, "ok", nil }
	if _, err := EjecutarPerfilSobreCandidato("perfil", nil, opts); err != nil {
		t.Fatalf("no esperaba error, obtuve: %v", err)
	}

	commonDir, err := git.ObtenerGitCommonDir(dir)
	if err != nil {
		t.Fatalf("no se pudo obtener el common-dir: %v", err)
	}
	entradas, err := os.ReadDir(filepath.Join(commonDir, "vas-sentinel", "snapshots"))
	if err != nil {
		t.Fatalf("no se pudo leer el directorio de snapshots: %v", err)
	}
	if len(entradas) != 1 || entradas[0].Name() != arbolHEAD {
		var nombres []string
		for _, entrada := range entradas {
			nombres = append(nombres, entrada.Name())
		}
		t.Fatalf("tras el barrido quedaron %v, esperaba solo el snapshot fresco %s", nombres, arbolHEAD)
	}
}
