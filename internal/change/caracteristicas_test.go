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
func TestDetectoresUsanReglasInyectadas(t *testing.T) {
	reglasPersonalizadas := []Regla{{Clase: ClaseCI, Patrones: []string{"pipelines/**"}}}
	entrada := EntradaCaracteristicas{Rutas: []string{"pipelines/build.yaml"}, Reglas: reglasPersonalizadas}
	if got := detectarCICD(entrada).Estado; got != CaracteristicaPresente {
		t.Errorf("ci_cd con regla inyectada = %q, quiere presente", got)
	}
	if got := detectarCICD(EntradaCaracteristicas{Rutas: []string{"pipelines/build.yaml"}}).Estado; got != CaracteristicaAusente {
		t.Errorf("ci_cd sin la regla inyectada (solo defaults) = %q, quiere ausente", got)
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
