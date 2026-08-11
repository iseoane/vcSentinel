package change

import "testing"

func TestCohesionPorComponentesConexas(t *testing.T) {
	casos := []struct {
		nombre           string
		rutas            []string
		historial        string
		quiereClusters   int
		quierePuntuacion float64
		quiereSplit      bool
	}{
		{
			nombre: "tres grupos independientes",
			rutas: []string{
				"internal/review/finding.go",
				"internal/review/ledger.go",
				"internal/setup/github.go",
				".github/workflows/ci.yml",
			},
			quiereClusters:   3,
			quierePuntuacion: 1.0 / 3.0,
			quiereSplit:      true,
		},
		{
			nombre: "cambio grande cohesivo por modulo",
			rutas: []string{
				"internal/change/clases.go",
				"internal/change/perfil.go",
				"internal/change/caracteristicas.go",
				"internal/change/cohesion.go",
				"internal/change/cohesion_test.go",
			},
			quiereClusters:   1,
			quierePuntuacion: 1,
			quiereSplit:      false,
		},
		{
			nombre:           "solo el co-cambio historico conecta",
			rutas:            []string{"internal/auth/token.go", "pkg/session/store.go"},
			historial:        "commit:abc123\n\ninternal/auth/token.go\npkg/session/store.go\n",
			quiereClusters:   1,
			quierePuntuacion: 1,
			quiereSplit:      false,
		},
		{
			nombre: "el co-cambio historico es transitivo",
			rutas:  []string{"internal/auth/token.go", "pkg/session/store.go", "cmd/api/main.go"},
			historial: "commit:abc123\n\ninternal/auth/token.go\npkg/session/store.go\n" +
				"commit:def456\n\npkg/session/store.go\ncmd/api/main.go\n",
			quiereClusters:   1,
			quierePuntuacion: 1,
			quiereSplit:      false,
		},
	}

	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			llamadas := 0
			falso := func(args ...string) (string, error) {
				llamadas++
				if len(args) == 0 || args[0] != "log" || !contains(args, "--name-only") {
					t.Fatalf("lectura git inesperada: %v", args)
				}
				for _, ruta := range caso.rutas {
					if !contains(args, ruta) {
						t.Errorf("git log no recibió la ruta %q: %v", ruta, args)
					}
				}
				return caso.historial, nil
			}

			resultado, err := Cohesion(caso.rutas, falso)
			if err != nil {
				t.Fatalf("Cohesion devolvió error: %v", err)
			}
			if resultado.Clusters != caso.quiereClusters || resultado.Puntuacion != caso.quierePuntuacion || resultado.SugerenciaSplit != caso.quiereSplit {
				t.Errorf("Cohesion = %#v, quiere clusters=%d puntuacion=%v split=%v", resultado, caso.quiereClusters, caso.quierePuntuacion, caso.quiereSplit)
			}
			if llamadas != 1 {
				t.Errorf("llamadas a git = %d, quiere 1", llamadas)
			}
		})
	}
}
