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
	positiva := EntradaCaracteristicas{Rutas: []string{"api/handler.go"}, LineasAnadidas: map[string][]string{"api/handler.go": {"func Exportada() {}"}}}
	negativa := EntradaCaracteristicas{Rutas: []string{"internal/app.go"}, LineasAnadidas: map[string][]string{"internal/app.go": {"func privada() {}"}}}
	comprobarDetector(t, detectarAPIPublica, positiva, negativa)
	if !detectarAPIPublica(positiva).Heuristica {
		t.Error("public_api debe declarar heuristic=true")
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

func TestDetectarCaracteristicasIncluyeTodosLosDetectores(t *testing.T) {
	got := DetectarCaracteristicas(EntradaCaracteristicas{})
	if len(got) != 10 {
		t.Fatalf("características = %d, quiere 10", len(got))
	}
}
