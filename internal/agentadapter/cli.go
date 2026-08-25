package agentadapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/process"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
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

	// activeTree holds the restricted reviewer's currently owned process
	// tree, or nil when no review child is running. The execution controller
	// reads it at abort time to escalate against the whole tree.
	activeTree atomic.Pointer[process.Tree]
}

// OwnedTree reports the live owned review process tree, or nil.
func (c *CLIAdapter) OwnedTree() *process.Tree {
	return c.activeTree.Load()
}

// ReviewRequest limits an agent review to read-only exploration of planned paths.
type ReviewRequest struct {
	Prompt       string
	SHA          string
	Paths        []string
	SnapshotDir  string
	MaxToolCalls int
	ToolPolicy   reviewcontract.ToolPolicy
}

const defaultReviewToolCalls = 8

// EjecutarPrompt ejecuta el binario con un prompt arbitrario y devuelve la
// salida. Es la vía pública del motor de auditoría hacia el agente.
func (c *CLIAdapter) EjecutarPrompt(prompt string) (string, error) {
	return c.ejecutarComando(prompt)
}

// EjecutarRevision runs a semantic review with the bounded tool profile.
// The legacy contract carries no context: the review runs under the adapter's
// own timeout budget only. Context-carrying callers go through
// ReviewWithContext so cooperative cancellation reaches the provider process.
func (c *CLIAdapter) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	return c.EjecutarRevisionConPolitica(prompt, sha, paths, reviewcontract.DefaultToolPolicy())
}

// EjecutarRevisionConPolitica translates one provider-neutral contract policy
// into this provider's command and configuration syntax.
func (c *CLIAdapter) EjecutarRevisionConPolitica(prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error) {
	return c.reviewWithContextPolicy(context.Background(), prompt, sha, paths, policy)
}

func (c *CLIAdapter) ObtenerMensajeCommit(rutasArchivos []string, capa string, batchNum int) (string, error) {
	if c.esOpenCode() || c.esClaude() {
		return "", fmt.Errorf("%s requiere la via consentida con micro-diff para generar mensajes de commit", c.nombreBase())
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
	if !c.esOpenCode() && !c.esClaude() {
		salida, err := c.ejecutarComandoConTimeout(prompt, timeout)
		if err != nil {
			return "", err
		}
		return validarMensajeCommit(salida)
	}

	ctx, cancelar := context.WithTimeout(context.Background(), timeout)
	defer cancelar()

	if c.esClaude() {
		cmd, limpiar, err := c.prepararComandoCommitClaude(ctx, prompt)
		if err != nil {
			return "", err
		}
		defer limpiar()

		var out, stderr bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			if detail := strings.TrimSpace(stderr.String()); detail != "" {
				return "", fmt.Errorf("generar mensaje de commit con claude: %w: %s", err, detail)
			}
			return "", err
		}
		// Claude Code's plain-text "-p" output already IS the final message
		// (unlike OpenCode's "--format json" NDJSON stream), so it goes
		// straight through the same format check as the unrestricted path.
		return validarMensajeCommit(out.String())
	}

	cmd, limpiar, err := c.prepararComandoCommit(ctx, prompt)
	if err != nil {
		return "", err
	}
	defer limpiar()

	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return "", fmt.Errorf("generar mensaje de commit con opencode: %w: %s", err, detail)
		}
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

// prepararComandoCommitClaude runs Claude Code in an empty temporary working
// directory with declarative CLI permission flags. The micro-diff already
// travels complete in the prompt. Tests verify the generated arguments and
// working directory only: local `claude --help` documents these flags, but no
// integration test proves a live path matcher or an OS filesystem sandbox.
func (c *CLIAdapter) prepararComandoCommitClaude(ctx context.Context, prompt string) (*exec.Cmd, func(), error) {
	dir, err := os.MkdirTemp("", "vas-sentinel-commit-")
	if err != nil {
		return nil, nil, fmt.Errorf("create neutral directory for claude: %w", err)
	}
	limpiar := func() { _ = os.RemoveAll(dir) }
	args := []string{"-p", "--safe-mode", "--tools", ""}
	if c.Config.Model != "" {
		args = append(args, "--model", c.Config.Model)
	}
	if c.Config.ReasoningEffort != "" {
		args = append(args, "--effort", c.Config.ReasoningEffort)
	}
	cmd := exec.CommandContext(ctx, c.BinaryName, args...)
	cmd.Dir = dir
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

func (c *CLIAdapter) reviewCommand(request ReviewRequest) ([]string, map[string]string, error) {
	policy := request.ToolPolicy
	if policy == (reviewcontract.ToolPolicy{}) {
		policy = reviewcontract.DefaultToolPolicy()
	}
	if !policy.AllowRead || !policy.AllowSearch || !policy.RequireImmutableSnapshot || policy.AllowMutation || policy.AllowShell || policy.AllowNetwork {
		return nil, nil, fmt.Errorf("semantic review requires the canonical read/search-only immutable snapshot tool policy")
	}
	maxToolCalls := request.MaxToolCalls
	if maxToolCalls <= 0 {
		maxToolCalls = defaultReviewToolCalls
	}
	if c.esClaude() {
		if request.SnapshotDir == "" {
			return nil, nil, fmt.Errorf("semantic review requires an immutable snapshot directory")
		}
		snapshotPattern := filepath.ToSlash(filepath.Join(request.SnapshotDir, "**"))
		allowedTools := strings.Join([]string{
			"Read(" + snapshotPattern + ")",
			"Grep(" + snapshotPattern + ")",
			"Glob(" + snapshotPattern + ")",
		}, ",")
		// These flags declaratively select read/search tools, snapshot patterns,
		// and non-interactive permission handling. Tests assert only the emitted
		// command shape; they do not establish an OS sandbox or live matching.
		// There is no confirmed Claude Code flag/env var to cap tool-call
		// count (OpenCode's "Steps"), so maxToolCalls is intentionally unused
		// here; do not invent one.
		args := []string{"-p", "--safe-mode", "--permission-mode", "dontAsk", "--tools", "Read,Grep,Glob", "--allowed-tools", allowedTools, "--disallowed-tools", "Bash,Edit,Write"}
		if c.Config.Model != "" {
			args = append(args, "--model", c.Config.Model)
		}
		if c.Config.ReasoningEffort != "" {
			args = append(args, "--effort", c.Config.ReasoningEffort)
		}
		return args, nil, nil
	}
	if !c.esOpenCode() {
		return nil, nil, fmt.Errorf("semantic review is unavailable: path-confined tool permissions are not configured for this provider")
	}
	if request.SnapshotDir == "" {
		return nil, nil, fmt.Errorf("semantic review requires an immutable snapshot directory")
	}
	permissions := map[string]map[string]string{
		"bash":  {"*": "deny"},
		"edit":  {"*": "deny"},
		"write": {"*": "deny"},
		"read":  {"*": "deny", filepath.ToSlash(filepath.Join(request.SnapshotDir, "**")): "allow"},
		// Grep and Glob receive user-supplied search expressions, not the paths
		// found by those searches. Restricting them to audited filenames would
		// reject ordinary expressions while contributing no snapshot containment.
		"grep": {"*": "allow"},
		"glob": {"*": "allow"},
	}
	permission := make(map[string]any, len(permissions)+1)
	permission["*"] = "deny"
	for tool, rules := range permissions {
		permission[tool] = rules
	}
	configuration := struct {
		Agent map[string]struct {
			Model           string         `json:"model,omitempty"`
			ReasoningEffort string         `json:"reasoningEffort,omitempty"`
			Steps           int            `json:"steps"`
			Permission      map[string]any `json:"permission"`
		} `json:"agent"`
	}{Agent: map[string]struct {
		Model           string         `json:"model,omitempty"`
		ReasoningEffort string         `json:"reasoningEffort,omitempty"`
		Steps           int            `json:"steps"`
		Permission      map[string]any `json:"permission"`
	}{"reviewer": {Model: c.Config.Model, ReasoningEffort: c.Config.ReasoningEffort, Steps: maxToolCalls, Permission: permission}}}
	encoded, err := json.Marshal(configuration)
	if err != nil {
		panic(fmt.Sprintf("review configuration cannot be serialized: %v", err))
	}
	args := []string{"run", "--pure", "--agent", "reviewer"}
	if c.Config.Model != "" {
		args = append(args, "--model", c.Config.Model)
	}
	args = append(args, "--dir", request.SnapshotDir)
	return args, map[string]string{"OPENCODE_CONFIG_CONTENT": string(encoded)}, nil
}

func reviewEnvironment(configuration, snapshot, model string) []string {
	isolationRoot := snapshot
	blocked := map[string]bool{
		"OPENCODE_CONFIG": true, "OPENCODE_CONFIG_CONTENT": true, "OPENCODE_CONFIG_DIR": true,
		"OPENCODE_TEST_HOME": true, "OPENCODE_PURE": true, "OPENCODE_DISABLE_PROJECT_CONFIG": true,
		"HOME": true, "USERPROFILE": true, "XDG_CONFIG_HOME": true,
		"XDG_STATE_HOME": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true,
		"OPENCODE_AUTH_CONTENT": true,
	}
	env := make([]string, 0, len(os.Environ())+10)
	authContent := ""
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if key == "OPENCODE_AUTH_CONTENT" {
			authContent = value
		}
		if !blocked[key] {
			env = append(env, entry)
		}
	}
	env = append(env,
		"OPENCODE_CONFIG_CONTENT="+configuration,
		"OPENCODE_DISABLE_PROJECT_CONFIG=1",
		"OPENCODE_PURE=1",
		"OPENCODE_TEST_HOME="+isolationRoot,
		"HOME="+isolationRoot,
		"XDG_CONFIG_HOME="+filepath.Join(isolationRoot, ".config"),
		"XDG_STATE_HOME="+filepath.Join(isolationRoot, ".local", "state"),
		"XDG_CACHE_HOME="+filepath.Join(isolationRoot, ".cache"),
		// XDG_DATA_HOME is isolated too, not just left blocked: if it were
		// inherited unblocked (or simply absent) it would still resolve to
		// the host's real auth.json directory, giving OpenCode an unscoped
		// fallback whenever scopeCredentialToProvider below yields nothing
		// (malformed or non-matching credentials). Pointing it at the empty
		// snapshot closes that fallback path.
		"XDG_DATA_HOME="+filepath.Join(isolationRoot, ".local", "share"),
	)
	// Isolating HOME strands OpenCode's real auth.json (it lives under the
	// host's data dir). Whatever the credential source — an inherited
	// OPENCODE_AUTH_CONTENT or the host's auth.json — scopeCredentialToProvider
	// is applied uniformly below, so the sandboxed reviewer only ever sees the
	// one provider entry this review actually uses, never a whole
	// multi-provider credential store.
	if authContent == "" {
		authContent = hostAuthContent()
	}
	if scoped := scopeCredentialToProvider(authContent, model); scoped != "" {
		env = append(env, "OPENCODE_AUTH_CONTENT="+scoped)
	}
	env = append(env, "USERPROFILE="+isolationRoot)
	return env
}

// hostAuthContentPath resolves the real OpenCode auth.json path outside the
// isolated sandbox, following the same XDG data dir convention OpenCode uses.
// Returns "" if no host home directory can be resolved: a relative fallback
// would read auth.json from whatever the process's cwd happens to be, which
// under review may be the audited project itself.
func hostAuthContentPath() string {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		home := os.Getenv("HOME")
		if home == "" {
			home = os.Getenv("USERPROFILE")
		}
		if home == "" {
			return ""
		}
		dataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataHome, "opencode", "auth.json")
}

// hostAuthContent reads the host's auth.json, or "" if it cannot be resolved
// or read.
func hostAuthContent() string {
	path := hostAuthContentPath()
	if path == "" {
		return ""
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}

// scopeCredentialToProvider parses a multi-provider credential payload
// (auth.json's layout, keyed by provider name) and returns a JSON object
// containing only the entry for model's provider (the segment before "/").
// Applied the same way regardless of where the credential came from —
// inherited OPENCODE_AUTH_CONTENT or the host's auth.json — so neither source
// can bypass provider scoping. Any parse failure or missing entry returns "":
// fail closed rather than let an unscoped credential reach the sandbox.
func scopeCredentialToProvider(raw, model string) string {
	if raw == "" {
		return ""
	}
	provider, _, ok := strings.Cut(model, "/")
	if !ok || provider == "" {
		return ""
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &all); err != nil {
		return ""
	}
	entry, ok := all[provider]
	if !ok {
		return ""
	}
	scoped, err := json.Marshal(map[string]json.RawMessage{provider: entry})
	if err != nil {
		return ""
	}
	return string(scoped)
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
