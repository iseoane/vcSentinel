package agentadapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// TimeoutComando es el límite de una llamada al agente (300 s). La fase 1 lo
// hace configurable vía review.timeout en vassentinel.yml; este es el fallback
// cuando un adaptador no define Timeout.
const TimeoutComando = 300 * time.Second

var patronMensajeCommit = regexp.MustCompile(`^(build|chore|ci|docs|feat|fix|perf|refactor|revert|style|test)(\([a-zA-Z0-9._/-]+\))?!?: .+$`)

type CLIAdapter struct {
	BinaryName string
	Config     config.AgentConfig
	// CommitLanguage fija el idioma de los mensajes de commit generados. Si
	// está vacío se usa IdiomaPorDefecto.
	CommitLanguage string
	// Timeout es el límite por llamada; si es 0 se usa TimeoutComando.
	Timeout time.Duration
}

// ReviewRequest limits an agent review to read-only exploration of planned paths.
type ReviewRequest struct {
	Prompt       string
	SHA          string
	Paths        []string
	SnapshotDir  string
	MaxToolCalls int
}

const defaultReviewToolCalls = 8

// EjecutarPrompt ejecuta el binario con un prompt arbitrario y devuelve la
// salida. Es la vía pública del motor de auditoría hacia el agente.
func (c *CLIAdapter) EjecutarPrompt(prompt string) (string, error) {
	return c.ejecutarComando(prompt)
}

// EjecutarRevision runs a semantic review with the bounded tool profile.
func (c *CLIAdapter) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = TimeoutComando
	}
	snapshot, safePaths, cleanup, err := createReviewSnapshot("", sha, paths)
	if err != nil {
		return "", err
	}
	defer cleanup()
	return c.ejecutarRevisionConTimeout(ReviewRequest{Prompt: prompt, SHA: sha, Paths: safePaths, SnapshotDir: snapshot, MaxToolCalls: defaultReviewToolCalls}, timeout)
}

func (c *CLIAdapter) ObtenerMensajeCommit(rutasArchivos []string, capa string, batchNum int) (string, error) {
	if c.esOpenCode() {
		return "", fmt.Errorf("opencode requiere la via consentida con micro-diff para generar mensajes de commit")
	}
	salida, err := c.ejecutarComando(construirPromptAgente(capa, batchNum, rutasArchivos, c.idiomaCommit()))
	if err != nil {
		return "", err
	}
	return validarMensajeCommit(salida)
}

// ObtenerMensajeCommitConDiff incorpora el cambio preparado al prompt para que
// el agente no necesite leer el repositorio durante la generación del mensaje.
func (c *CLIAdapter) ObtenerMensajeCommitConDiff(rutasArchivos []string, capa string, batchNum int, diff string) (string, error) {
	prompt := construirPromptAgenteConDiff(capa, batchNum, rutasArchivos, diff, c.idiomaCommit())
	return c.ejecutarMensajeCommit(prompt)
}

// ProponerPlanRefactor pide al agente un plan de división para un archivo de
// código masivo. Implementa AdapterRefactor.
func (c *CLIAdapter) ProponerPlanRefactor(rutaArchivo string) (string, error) {
	return c.ejecutarComando(construirPromptRefactor(rutaArchivo))
}

// AplicarPlanRefactor ordena al agente ejecutar el plan de refactorización
// directamente sobre el working tree, sin hacer commits. Implementa
// AdapterRefactor.
func (c *CLIAdapter) AplicarPlanRefactor(rutaArchivo string, plan string) (string, error) {
	return c.ejecutarComando(construirPromptAplicarRefactor(rutaArchivo, plan))
}

// ejecutarComando ejecuta el binario del agente con el prompt dado y devuelve
// la salida estándar completa (recortada). Para opencode y claude el prompt
// viaja por stdin (ver comandoPrompt); para el resto de binarios se pasa como
// argumento de "-p". El proceso se corta con TimeoutComando si el agente no
// responde: un agente que espera entrada interactiva no debe colgar la
// auditoría.
func (c *CLIAdapter) ejecutarComando(prompt string) (string, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = TimeoutComando
	}
	return c.ejecutarComandoConTimeout(prompt, timeout)
}

func (c *CLIAdapter) ejecutarMensajeCommit(prompt string) (string, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = TimeoutComando
	}
	if !c.esOpenCode() {
		salida, err := c.ejecutarComandoConTimeout(prompt, timeout)
		if err != nil {
			return "", err
		}
		return validarMensajeCommit(salida)
	}

	ctx, cancelar := context.WithTimeout(context.Background(), timeout)
	defer cancelar()
	cmd, limpiar, err := c.prepararComandoCommit(ctx, prompt)
	if err != nil {
		return "", err
	}
	defer limpiar()

	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return extraerMensajeCommitOpenCode(out.String())
}

// prepararComandoCommit ejecuta OpenCode fuera del repositorio y sin plugins
// externos. El micro-diff ya viaja en el prompt, por lo que no pierde contexto.
func (c *CLIAdapter) prepararComandoCommit(ctx context.Context, prompt string) (*exec.Cmd, func(), error) {
	dir, err := os.MkdirTemp("", "vas-sentinel-commit-")
	if err != nil {
		return nil, nil, fmt.Errorf("crear directorio neutral para opencode: %w", err)
	}
	limpiar := func() { _ = os.RemoveAll(dir) }
	args := []string{"run", "--pure", "--agent", "title", "--format", "json"}
	if c.Config.Model != "" {
		args = append(args, "--model", c.Config.Model)
	}
	if c.Config.ReasoningEffort != "" {
		args = append(args, "--variant", c.Config.ReasoningEffort)
	}
	args = append(args, "--dir", dir)
	cmd := exec.CommandContext(ctx, c.BinaryName, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("OPENCODE_MODEL=%s", c.Config.Model),
		fmt.Sprintf("OPENCODE_REASONING_EFFORT=%s", c.Config.ReasoningEffort),
	)
	cmd.Stdin = strings.NewReader(prompt)
	return cmd, limpiar, nil
}

func extraerMensajeCommitOpenCode(salida string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(salida))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	mensaje := ""
	textos := 0
	lineas := 0
	for scanner.Scan() {
		linea := scanner.Bytes()
		if len(bytes.TrimSpace(linea)) == 0 {
			return "", fmt.Errorf("salida JSONL invalida de opencode: linea vacia")
		}
		lineas++
		var evento struct {
			Type string `json:"type"`
			Part struct {
				Text string `json:"text"`
			} `json:"part"`
		}
		if err := json.Unmarshal(linea, &evento); err != nil {
			return "", fmt.Errorf("salida JSONL invalida de opencode: %w", err)
		}
		if evento.Type != "text" {
			continue
		}
		textos++
		if textos > 1 {
			return "", fmt.Errorf("opencode devolvio multiples eventos de texto")
		}
		mensaje = evento.Part.Text
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("salida JSONL invalida de opencode: %w", err)
	}
	if lineas == 0 {
		return "", fmt.Errorf("salida JSONL vacia de opencode")
	}
	if textos != 1 {
		return "", fmt.Errorf("opencode no devolvio un evento de texto")
	}
	return validarMensajeCommit(mensaje)
}

func validarMensajeCommit(salida string) (string, error) {
	mensaje := strings.TrimSpace(salida)
	if strings.ContainsAny(mensaje, "\r\n") || !patronMensajeCommit.MatchString(mensaje) {
		return "", fmt.Errorf("el agente no devolvio una unica linea Conventional Commit")
	}
	return mensaje, nil
}

// ejecutarComandoConTimeout es la variante parametrizada de ejecutarComando;
// permite a los tests acortar la espera sin tocar la constante de producción.
func (c *CLIAdapter) ejecutarComandoConTimeout(prompt string, timeout time.Duration) (string, error) {
	ctx, cancelar := context.WithTimeout(context.Background(), timeout)
	defer cancelar()

	args, viaStdin := c.comandoPrompt(prompt)
	cmd := exec.CommandContext(ctx, c.BinaryName, args...)
	env := os.Environ()

	if c.esClaude() {
		env = append(env, fmt.Sprintf("CLAUDE_CODE_MODEL=%s", c.Config.Model))
		env = append(env, fmt.Sprintf("CLAUDE_CODE_REASONING=%s", c.Config.ReasoningEffort))
	} else if c.esOpenCode() {
		env = append(env, fmt.Sprintf("OPENCODE_MODEL=%s", c.Config.Model))
		env = append(env, fmt.Sprintf("OPENCODE_REASONING_EFFORT=%s", c.Config.ReasoningEffort))
	}
	cmd.Env = env

	var out bytes.Buffer
	cmd.Stdout = &out
	// opencode y claude leen el prompt de stdin; los demás binarios lo reciben
	// como argumento. Pasar el prompt por stdin evita el límite de 32.767
	// caracteres de la línea de comandos de Windows.
	if viaStdin {
		cmd.Stdin = strings.NewReader(prompt)
	}
	if err := cmd.Run(); err != nil {
		return "", err
	}

	return strings.TrimSpace(out.String()), nil
}

func (c *CLIAdapter) ejecutarRevisionConTimeout(request ReviewRequest, timeout time.Duration) (string, error) {
	ctx, cancelar := context.WithTimeout(context.Background(), timeout)
	defer cancelar()

	args, restrictions, err := c.reviewCommand(request)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, c.BinaryName, args...)
	cmd.Env = reviewEnvironment(restrictions["OPENCODE_CONFIG_CONTENT"], request.SnapshotDir)

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stdin = strings.NewReader(request.Prompt)
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(out.String()), nil
}

func (c *CLIAdapter) reviewCommand(request ReviewRequest) ([]string, map[string]string, error) {
	maxToolCalls := request.MaxToolCalls
	if maxToolCalls <= 0 {
		maxToolCalls = defaultReviewToolCalls
	}
	if !c.esOpenCode() {
		return nil, nil, fmt.Errorf("semantic review is unavailable: path-confined tool permissions are not configured for this provider")
	}
	if request.SnapshotDir == "" {
		return nil, nil, fmt.Errorf("semantic review requires an immutable snapshot directory")
	}
	safePaths := rutasRevisionSeguras(request.Paths)
	permissions := map[string]map[string]string{
		"bash":     {"*": "deny"},
		"edit":     {"*": "deny"},
		"write":    {"*": "deny"},
		"webfetch": {"*": "deny"},
		"read":     {"*": "deny"},
		"grep":     {"*": "deny"},
		"glob":     {"*": "deny"},
	}
	for _, ruta := range safePaths {
		permissions["read"][ruta] = "allow"
		permissions["grep"][ruta] = "allow"
		permissions["glob"][ruta] = "allow"
	}
	permission := make(map[string]any, len(permissions)+1)
	permission["*"] = "deny"
	for tool, rules := range permissions {
		permission[tool] = rules
	}
	configuration := struct {
		Agent map[string]struct {
			Steps      int            `json:"steps"`
			Permission map[string]any `json:"permission"`
		} `json:"agent"`
	}{Agent: map[string]struct {
		Steps      int            `json:"steps"`
		Permission map[string]any `json:"permission"`
	}{"reviewer": {Steps: maxToolCalls, Permission: permission}}}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		panic(fmt.Sprintf("review configuration cannot be serialized: %v", err))
	}
	return []string{"run", "--pure", "--agent", "reviewer", "--dir", request.SnapshotDir}, map[string]string{"OPENCODE_CONFIG_CONTENT": string(encoded)}, nil
}

func rutasRevisionSeguras(rutas []string) []string {
	seguras := make([]string, 0, len(rutas))
	for _, ruta := range rutas {
		normalizada := strings.ReplaceAll(ruta, "\\", "/")
		limpia := path.Clean(normalizada)
		drive := len(limpia) >= 2 && limpia[1] == ':'
		if ruta == "" || path.IsAbs(limpia) || drive || limpia == "." || limpia == ".." || strings.HasPrefix(limpia, "../") || strings.HasPrefix(ruta, "-") || strings.ContainsAny(ruta, "\x00\r\n*?[]{}!") {
			continue
		}
		seguras = append(seguras, limpia)
	}
	return seguras
}

func reviewEnvironment(configuration, snapshot string) []string {
	isolationRoot := snapshot
	blocked := map[string]bool{
		"OPENCODE_CONFIG": true, "OPENCODE_CONFIG_CONTENT": true, "OPENCODE_CONFIG_DIR": true,
		"OPENCODE_TEST_HOME": true, "OPENCODE_PURE": true, "OPENCODE_DISABLE_PROJECT_CONFIG": true, "OPENCODE_AUTH_CONTENT": true,
		"HOME": true, "USERPROFILE": true, "XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true,
		"XDG_STATE_HOME": true, "XDG_CACHE_HOME": true,
	}
	env := make([]string, 0, len(os.Environ())+10)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && !blocked[key] {
			env = append(env, entry)
		}
	}
	env = append(env,
		"OPENCODE_CONFIG_CONTENT="+configuration,
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"OPENCODE_PURE=1",
		"OPENCODE_AUTH_CONTENT={}",
		"OPENCODE_TEST_HOME="+isolationRoot,
		"HOME="+isolationRoot,
		"XDG_CONFIG_HOME="+filepath.Join(isolationRoot, ".config"),
		"XDG_DATA_HOME="+filepath.Join(isolationRoot, ".local", "share"),
		"XDG_STATE_HOME="+filepath.Join(isolationRoot, ".local", "state"),
		"XDG_CACHE_HOME="+filepath.Join(isolationRoot, ".cache"),
	)
	env = append(env, "USERPROFILE="+isolationRoot)
	return env
}

// comandoPrompt devuelve los argumentos de invocación según el binario y si el
// prompt viaja por stdin: opencode usa el subcomando "run" y claude "-p", ambos
// leyendo el prompt de stdin (sin límite de longitud); cualquier otro binario
// recibe el prompt como argumento de "-p" (comportamiento anterior).
func (c *CLIAdapter) comandoPrompt(prompt string) ([]string, bool) {
	if c.esOpenCode() {
		return []string{"run"}, true
	}
	if c.esClaude() {
		return []string{"-p"}, true
	}
	return []string{"-p", prompt}, false
}

func (c *CLIAdapter) esClaude() bool {
	return c.nombreBase() == "claude"
}

func (c *CLIAdapter) esOpenCode() bool {
	return c.nombreBase() == "opencode"
}

// nombreBase extrae el nombre del binario sin ruta ni extensión, para tolerar
// rutas completas o sufijos de plataforma (p. ej. "opencode.exe").
func (c *CLIAdapter) nombreBase() string {
	return strings.TrimSuffix(filepath.Base(c.BinaryName), filepath.Ext(c.BinaryName))
}

// construirPromptAgente arma el prompt del mensaje de commit. El idioma se
// fija explícitamente y con un ejemplo (T0.13): sin decirlo, el modelo lo
// elegía al azar y mezclaba idiomas dentro de la misma ejecución.
func construirPromptAgente(capa string, batchNum int, archivos []string, idioma string) string {
	archivosStr := strings.Join(archivos, " ")
	instruccion, ejemplo := instruccionDeIdioma(idioma)
	return fmt.Sprintf(
		"Analiza estos archivos modificados de la capa [%s] (Lote #%d): %s. Genera un mensaje de commit semántico bajo el estándar Conventional Commits. %s Ejemplo del formato esperado: %s. Devuelve ÚNICAMENTE la línea del mensaje, sin marcas de markdown ni comillas.",
		capa, batchNum, archivosStr, instruccion, ejemplo,
	)
}

func construirPromptAgenteConDiff(capa string, batchNum int, archivos []string, diff string, idioma string) string {
	return fmt.Sprintf("%s\n\nMicro-diff preparado:\n%s", construirPromptAgente(capa, batchNum, archivos, idioma), diff)
}

func construirPromptRefactor(ruta string) string {
	return fmt.Sprintf(
		"Analiza el archivo %s. Propón un plan detallado para dividirlo en archivos más pequeños y cohesivos, respetando el principio de responsabilidad única (SRP). Devuelve el plan en texto plano: qué archivos crear, qué contenido debería moverse a cada uno y el orden sugerido. Sin marcas de markdown.",
		ruta,
	)
}

func construirPromptAplicarRefactor(ruta string, plan string) string {
	return fmt.Sprintf(
		"Aplica el siguiente plan de refactorización sobre el archivo %s:\n%s\nRealiza los cambios directamente en el working tree: crea, mueve y edita los archivos necesarios. NO hagas commits ni ejecutes git. Devuelve un resumen breve de los archivos creados, modificados o eliminados. Sin marcas de markdown.",
		ruta, plan,
	)
}
