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
