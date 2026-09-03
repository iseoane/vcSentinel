package change

import "testing"

func comprobarDetector(t *testing.T, detector func(EntradaCaracteristicas) Caracteristica, positiva, negativa EntradaCaracteristicas) {
	t.Helper()
	if got := detector(positiva).Estado; got != CaracteristicaPresente {
		t.Errorf("caso positivo = %q, quiere %q", got, CaracteristicaPresente)
	}
	if got := detector(negativa).Estado; got != CaracteristicaAusente {
		t.Errorf("caso negativo = %q, quiere %q", got, CaracteristicaAusente)
	}
}

func TestDetectorPublicAPI(t *testing.T) {
	positiva := EntradaCaracteristicas{Symbols: ChangeSymbols{ExportedTouched: 1, Complete: true}}
	negativa := EntradaCaracteristicas{Symbols: ChangeSymbols{Modified: 1, Complete: true}}
	comprobarDetector(t, detectarAPIPublica, positiva, negativa)
	if detectarAPIPublica(positiva).Heuristica {
		t.Error("public_api exacta no debe declarar heuristic=true")
	}
	if got := detectarAPIPublica(EntradaCaracteristicas{}).Estado; got != CaracteristicaIndeterminada {
		t.Errorf("public_api sin AST completo = %q, quiere indeterminate", got)
	}
}

func TestDetectorDatabase(t *testing.T) {
	comprobarDetector(t, detectarBaseDeDatos,
		EntradaCaracteristicas{Rutas: []string{"db/migrations/001.sql"}},
		EntradaCaracteristicas{Rutas: []string{"internal/app/app.go"}})
}

func TestDetectorSecuritySensitive(t *testing.T) {
	comprobarDetector(t, detectarSeguridadSensible,
		EntradaCaracteristicas{LineasAnadidas: map[string][]string{"app.go": {"token := nuevoToken()"}}},
		EntradaCaracteristicas{LineasAnadidas: map[string][]string{"app.go": {"nombre := usuario.Nombre"}}})
}

func TestDetectorConcurrency(t *testing.T) {
	comprobarDetector(t, detectarConcurrencia,
		EntradaCaracteristicas{LineasAnadidas: map[string][]string{"worker.go": {"go ejecutar()"}}},
		EntradaCaracteristicas{LineasAnadidas: map[string][]string{"worker.go": {"ejecutar()"}}})
}

func TestDetectorBehaviorChange(t *testing.T) {
	positiva := EntradaCaracteristicas{Rutas: []string{"internal/app/app.go"}, LineasAnadidas: map[string][]string{"internal/app/app.go": {"return 2"}}}
	comprobarDetector(t, detectarCambioDeComportamiento, positiva,
		EntradaCaracteristicas{Rutas: []string{"internal/app/app.go"}, LineasAnadidas: map[string][]string{"internal/app/app.go": {"// solo documentación"}}})
	if got := detectarCambioDeComportamiento(EntradaCaracteristicas{Rutas: []string{"internal/app/app_test.go"}, LineasAnadidas: map[string][]string{"internal/app/app_test.go": {"t.Fatal()"}}}).Estado; got != CaracteristicaAusente {
		t.Errorf("cambio solo en test = %q, quiere ausente", got)
	}
}

func TestDetectorTestCovered(t *testing.T) {
	base := EntradaCaracteristicas{Rutas: []string{"internal/app/app.go"}}
	positiva := base
	positiva.MapaTests = map[string]bool{"internal/app": true}
	negativa := base
	negativa.MapaTests = map[string]bool{"internal/app": false}
	comprobarDetector(t, detectarCoberturaDeTests, positiva, negativa)
	if got := detectarCoberturaDeTests(base).Estado; got != CaracteristicaIndeterminada {
		t.Errorf("sin mapa de tests = %q, quiere %q", got, CaracteristicaIndeterminada)
	}
	if got := detectarCoberturaDeTests(EntradaCaracteristicas{Rutas: []string{"internal/app/app.go", "internal/app/app_test.go"}}).Estado; got != CaracteristicaPresente {
		t.Errorf("test en el diff = %q, quiere presente", got)
	}
}

func TestDetectorCrossModule(t *testing.T) {
	comprobarDetector(t, detectarCruceDeModulos,
		EntradaCaracteristicas{Rutas: []string{"internal/app/app.go", "cmd/sentinel/main.go"}},
		EntradaCaracteristicas{Rutas: []string{"internal/app/app.go", "internal/app/app_test.go"}})
}

func TestDetectorGeneratedCode(t *testing.T) {
	comprobarDetector(t, detectarCodigoGenerado,
		EntradaCaracteristicas{Rutas: []string{"gen/model.go"}, Gitattributes: "gen/** linguist-generated\n"},
		EntradaCaracteristicas{Rutas: []string{"internal/app/app.go"}})
}

func TestDetectorCICD(t *testing.T) {
	comprobarDetector(t, detectarCICD,
		EntradaCaracteristicas{Rutas: []string{".github/workflows/ci.yml"}},
		EntradaCaracteristicas{Rutas: []string{"config/app.yml"}})
}

func TestDetectorInfrastructure(t *testing.T) {
	comprobarDetector(t, detectarInfraestructura,
		EntradaCaracteristicas{Rutas: []string{"infra/main.tf"}},
		EntradaCaracteristicas{Rutas: []string{"internal/app/app.go"}})
}

// TestDetectorConcurrencyNoConfundePalabrasEnCastellano cubre el falso
// positivo real de la revisión de T3.3: "go " como subcadena sin límite de
// palabra coincidía con palabras habituales en castellano.
func TestDetectorConcurrencyNoConfundePalabrasEnCastellano(t *testing.T) {
	casos := []string{"algo cambia aqui", "tengo una duda", "esto es muy largo", "tal vez luego"}
	for _, linea := range casos {
		entrada := EntradaCaracteristicas{LineasAnadidas: map[string][]string{"app.go": {linea}}}
		if got := detectarConcurrencia(entrada).Estado; got != CaracteristicaAusente {
			t.Errorf("linea %q = %q, quiere ausente (falso positivo de \"go \")", linea, got)
		}
	}
	if got := detectarConcurrencia(EntradaCaracteristicas{LineasAnadidas: map[string][]string{"app.go": {"go ejecutar()"}}}).Estado; got != CaracteristicaPresente {
		t.Errorf("\"go ejecutar()\" = %q, quiere presente", got)
	}
	// mismo riesgo que "go ", señalado en la ADVISORY de la revisión de T3.3
	// para el resto de marcas: "context." dentro de un identificador más
	// largo no debe disparar un falso positivo.
	sinLimite := EntradaCaracteristicas{LineasAnadidas: map[string][]string{"app.go": {"miscontext.Valor = 1"}}}
	if got := detectarConcurrencia(sinLimite).Estado; got != CaracteristicaAusente {
		t.Errorf("\"miscontext.\" = %q, quiere ausente (falso positivo de \"context.\")", got)
	}
	conLimite := EntradaCaracteristicas{LineasAnadidas: map[string][]string{"app.go": {"context.Background()"}}}
	if got := detectarConcurrencia(conLimite).Estado; got != CaracteristicaPresente {
		t.Errorf("\"context.Background()\" = %q, quiere presente", got)
	}
}

// TestDetectorSecurityYConcurrencyDeclaranHeuristica: coinciden por
// subcadena, igual que public_api, así que deben declararse igual de
// heurísticos (revisión de T3.3).
func TestDetectorSecurityYConcurrencyDeclaranHeuristica(t *testing.T) {
	seguridad := detectarSeguridadSensible(EntradaCaracteristicas{LineasAnadidas: map[string][]string{"app.go": {"token := 1"}}})
	if !seguridad.Heuristica {
		t.Error("security_sensitive debe declarar heuristic=true")
	}
	concurrencia := detectarConcurrencia(EntradaCaracteristicas{LineasAnadidas: map[string][]string{"app.go": {"go ejecutar()"}}})
	if !concurrencia.Heuristica {
		t.Error("concurrency debe declarar heuristic=true")
	}
}

// TestDetectoresUsanReglasInyectadas cubre la revisión de T3.3: antes de
// este fix, detectarCICD (vía contieneClase) ignoraba EntradaCaracteristicas
// y llamaba siempre a ReglasPorDefecto(), así que una regla de usuario nunca
// podía cambiar la clasificación.
//
// Cubre detectarCICD y detectarInfraestructura, los llamantes de contieneClase.
// Desde que el helper recibe rutas y reglas en vez de la entrada completa, cada
// uno resuelve las suyas, y una regresión en uno dejó de estar cubierta por el
// otro. Un tercer llamante que apareciera sin caso aquí reabriría esa asimetría;
// no hay nada que lo detecte automáticamente.
//
// detectarBaseDeDatos usa una forma de fixture parecida pero no pasa por aquí:
// clasifica por sufijo y por e.PatronesData, sin reglasDe.
func TestDetectoresUsanReglasInyectadas(t *testing.T) {
	// Subtests, no dos llamadas seguidas: el guard usa t.Fatalf, que corta la
	// función de test entera. Encadenadas, un fixture caducado en ci_cd dejaría
	// infraestructura sin ejecutar y el informe diría que solo falló uno — la
	// misma asimetría que este test existe para cerrar, un nivel más arriba.
	for _, caso := range []struct {
		nombre   string
		detector func(EntradaCaracteristicas) Caracteristica
		ruta     string
		reglas   []Regla
	}{
		{"ci_cd", detectarCICD, "pipelines/build.yaml", []Regla{{Clase: ClaseCI, Patrones: []string{"pipelines/**"}}}},
		{"infrastructure", detectarInfraestructura, "despliegue/stack.yaml", []Regla{{Clase: ClaseInfra, Patrones: []string{"despliegue/**"}}}},
	} {
		t.Run(caso.nombre, func(t *testing.T) {
			comprobarInyeccion(t, caso.detector, caso.nombre, caso.ruta, caso.reglas)
		})
	}
}

// comprobarInyeccion verifica que un detector honra las reglas inyectadas, y no
// solo que la ruta del fixture no coincide por casualidad con ningún default.
//
// El guard va ANTES del caso positivo. Si las reglas por defecto crecieran para
// cubrir la ruta, el positivo pasaría sin que la inyección hiciera nada y el
// negativo fallaría diciendo "quiere ausente" en vez de que el fixture caducó.
// Es la misma convención de fixture-guard que usan los tests de atributos de
// este fichero, y está aquí para que las dos mitades la compartan: aplicarla a
// una sola dejaba abierta la asimetría que este test cerró.
func comprobarInyeccion(t *testing.T, detector func(EntradaCaracteristicas) Caracteristica, nombre, ruta string, reglas []Regla) {
	t.Helper()
	if got := detector(EntradaCaracteristicas{Rutas: []string{ruta}}).Estado; got != CaracteristicaAusente {
		t.Fatalf("%s: los defaults ya clasifican %s (%q); el fixture ya no distingue la inyección de reglas", nombre, ruta, got)
	}
	if got := detector(EntradaCaracteristicas{Rutas: []string{ruta}, Reglas: reglas}).Estado; got != CaracteristicaPresente {
		t.Errorf("%s con regla inyectada = %q, quiere %q", nombre, got, CaracteristicaPresente)
	}
}

func TestDetectarCaracteristicasIncluyeTodosLosDetectores(t *testing.T) {
	got := DetectarCaracteristicas(EntradaCaracteristicas{})
	if len(got) != 10 {
		t.Fatalf("características = %d, quiere 10", len(got))
	}
}

// TestSecuritySensitiveIgnoresProseAndGeneratedContent pins the false positive
// FU-10 demonstrates. The detector scanned the added lines of every path, so a
// metrics artifact under docs/ holding "cached_input_tokens" raised the
// characteristic and, through it, a high risk level for a documentation-only
// commit. Documentation is prose and generated files are output, so neither is
// evidence of credential handling. Config and infrastructure paths are not
// excluded: they genuinely can hold credentials, so the narrowing is a
// deny-list rather than an allow-list of source alone.
func TestSecuritySensitiveIgnoresProseAndGeneratedContent(t *testing.T) {
	casos := []struct {
		name string
		path string
		line string
		want EstadoCaracteristica
	}{
		{"documentation artifact", "docs/reingenieria/evidence/t9-4a-metrics.json", `  "cached_input_tokens": 12,`, CaracteristicaAusente},
		{"markdown prose", "docs/guia.md", "the reviewer receives an auth token", CaracteristicaAusente},
		{"generated file", "internal/api/service.pb.go", "type TokenRequest struct {", CaracteristicaAusente},
		{"source identifier", "internal/session/session.go", "\taccessToken := os.Getenv(\"X\")", CaracteristicaPresente},
		{"config key", "config/app.json", `  "password": "changeme",`, CaracteristicaPresente},
	}
	for _, caso := range casos {
		entrada := EntradaCaracteristicas{
			Rutas:          []string{caso.path},
			LineasAnadidas: map[string][]string{caso.path: {caso.line}},
		}
		if got := detectarSeguridadSensible(entrada).Estado; got != caso.want {
			t.Errorf("%s (%s) = %q, want %q", caso.name, caso.path, got, caso.want)
		}
	}
}

// TestSecuritySensitivePathPatternsSurviveTheClassFilter keeps the two inputs
// independent: a declared sensitive path marks the characteristic present
// whatever its class and whatever its content says.
func TestSecuritySensitivePathPatternsSurviveTheClassFilter(t *testing.T) {
	entrada := EntradaCaracteristicas{
		Rutas:             []string{"docs/auth/notas.md"},
		LineasAnadidas:    map[string][]string{"docs/auth/notas.md": {"nothing interesting here"}},
		PatronesSensibles: []string{"**/auth/**"},
	}
	if got := detectarSeguridadSensible(entrada).Estado; got != CaracteristicaPresente {
		t.Errorf("declared sensitive path = %q, want %q", got, CaracteristicaPresente)
	}
}

// TestConcurrencyIgnoresProseAndGeneratedContent applies to the concurrency
// detector the filter ticket 02 gave the security one. The divergence
// measurement found the gap: 17 of 52 prose-only commits were unlocked by
// concurrency alone, because this repository's reengineering fichas discuss Go
// concurrency constantly and every document mentioning context. read as a
// concurrent change. The word-boundary guard already on these marks is a
// different fix for a different problem: it stops miscontext. matching inside a
// longer identifier, and does nothing about a sentence in a Markdown file.
func TestConcurrencyIgnoresProseAndGeneratedContent(t *testing.T) {
	casos := []struct {
		name string
		path string
		line string
		want EstadoCaracteristica
	}{
		{"markdown prose", "docs/reingenieria/f9-observabilidad.md", "the producer reads context.Background() on every attempt", CaracteristicaAusente},
		{"generated file", "internal/api/service.pb.go", "\tresults chan *Reply", CaracteristicaAusente},
		{"source goroutine", "internal/worker/worker.go", "\tgo ejecutar()", CaracteristicaPresente},
		{"source context", "internal/worker/worker.go", "\tctx := context.Background()", CaracteristicaPresente},
		{"config key", "deploy/values.yaml", "  channel: sync.enabled", CaracteristicaPresente},
	}
	for _, caso := range casos {
		entrada := EntradaCaracteristicas{
			Rutas:          []string{caso.path},
			LineasAnadidas: map[string][]string{caso.path: {caso.line}},
		}
		if got := detectarConcurrencia(entrada).Estado; got != caso.want {
			t.Errorf("%s (%s) = %q, want %q", caso.name, caso.path, got, caso.want)
		}
	}
}

// TestAtributosNoApaganLaDeteccionDeCI pins the one exception to FU-14's rule,
// and it is a security boundary rather than a style choice.
//
// Every detector that reasons about the code honours `linguist-generated`,
// because a repository declaring a tree generated is stating something true
// about its own source. ci_cd and infrastructure do not, because they ask which
// surface a change touches rather than whether it is source: a workflow is a
// workflow even when a tool wrote it.
//
// Asserted through detectarCICD, not through the contieneClase helper it calls.
// The helper is an implementation detail, and a rewrite that classified inside
// the detector would keep a helper-level test green while reopening the hole.
//
// Measured before the exception existed: marking the workflow generated turned
// ci_cd from present to absent, which handed the audited repository a switch for
// the detection of its own CI.
func TestAtributosNoApaganLaDeteccionDeCI(t *testing.T) {
	const workflow = ".github/workflows/deploy.yml"
	e := EntradaCaracteristicas{Rutas: []string{workflow}}
	if detectarCICD(e).Estado != CaracteristicaPresente {
		t.Fatalf("%s is not classified as CI without attributes; the fixture no longer exercises the case", workflow)
	}

	e.Gitattributes = workflow + " linguist-generated\n"
	if estado := detectarCICD(e).Estado; estado != CaracteristicaPresente {
		t.Errorf("ci_cd = %q once the repository marked %s linguist-generated, want %q; a repository must not be able to switch off the detection of its own CI surface",
			estado, workflow, CaracteristicaPresente)
	}
}

// TestAtributosNoApaganLaDeteccionDeInfraestructura is the other half of the
// same exception. The comment at contieneClase and the debt entry both justify
// it by "ci_cd e infrastructure", and only ci_cd was exercised: infrastructure
// reaches contieneClase through the same call, so a change that broke one and
// not the other would have gone unnoticed.
func TestAtributosNoApaganLaDeteccionDeInfraestructura(t *testing.T) {
	const terraform = "infra/main.tf"
	e := EntradaCaracteristicas{Rutas: []string{terraform}}
	if detectarInfraestructura(e).Estado != CaracteristicaPresente {
		t.Fatalf("%s is not classified as infrastructure without attributes; the fixture no longer exercises the case", terraform)
	}

	e.Gitattributes = terraform + " linguist-generated\n"
	if estado := detectarInfraestructura(e).Estado; estado != CaracteristicaPresente {
		t.Errorf("infrastructure = %q once the repository marked %s linguist-generated, want %q; a repository must not be able to switch off the detection of its own infrastructure surface",
			estado, terraform, CaracteristicaPresente)
	}
}

// TestAtributosApaganElCambioDeComportamiento holds the other side, so the
// exception above cannot quietly become the rule. A declared-generated source
// path must stop counting as behaviour change: that is FU-14 itself, and it is
// what stops every regeneration of such a tree from being elevated risk.
func TestAtributosApaganElCambioDeComportamiento(t *testing.T) {
	const generado = "internal/api/wire.go"
	e := EntradaCaracteristicas{
		Rutas:          []string{generado},
		LineasAnadidas: map[string][]string{generado: {"\taccessToken := os.Getenv(\"SERVICE_TOKEN\")"}},
	}
	if detectarCambioDeComportamiento(e).Estado != CaracteristicaPresente {
		t.Fatalf("%s is not a behaviour change without attributes; the fixture no longer exercises the case", generado)
	}

	e.Gitattributes = generado + " linguist-generated\n"
	if estado := detectarCambioDeComportamiento(e).Estado; estado != CaracteristicaAusente {
		t.Errorf("behavior_change = %q for a declared-generated path, want %q", estado, CaracteristicaAusente)
	}
}
