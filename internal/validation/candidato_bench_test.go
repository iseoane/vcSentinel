package validation

import (
	"path/filepath"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentshell"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/graph"
)

// BenchmarkResolverComando_AlcanceParcialVsCompleto es la evidencia
// reproducible del criterio de salida de F4 "el tiempo de gate --stage
// pre-push en un cambio de un solo paquete baja de forma medible respecto a
// la validación completa": decisión ya tomada, no un test de timing con
// pass/fail (frágil en CI), sino un benchmark de Go. "go test -bench=.
// -run=^$ ./internal/validation/" imprime dos ns/op comparables, uno por
// cada sub-benchmark.
//
// Corre contra el propio repositorio de vas.sentinel (que ya tiene múltiples
// paquetes Go reales), usando el GraphProvider nativo real
// (graph.NuevoProveedorNativo), no un doble: el alcance parcial sale de
// resolverComando con la autorización que ese grafo real produce para un
// cambio en internal/store/store.go, un paquete hoja sin dependientes dentro
// del repo (nadie más en el módulo lo importa), exactamente como lo haría
// EjecutarPerfilSobreCandidato en producción. El snapshot se obtiene con
// git.Congelar + git.CrearSnapshot, el mismo camino que usa
// EjecutarPerfilSobreCandidato en modo worktree: así el benchmark es
// determinista incluso si el worktree real está sucio.
//
// go vet es el comando de referencia: barato y determinista, no hace falta
// go test para demostrar la diferencia de alcance.
func BenchmarkResolverComando_AlcanceParcialVsCompleto(b *testing.B) {
	candidato, err := git.Congelar()
	if err != nil {
		b.Fatalf("no se pudo congelar el candidato del repositorio real: %v", err)
	}
	snapshot, err := git.CrearSnapshot(candidato.Arbol)
	if err != nil {
		b.Fatalf("no se pudo crear el snapshot del repositorio real: %v", err)
	}

	rutaHoja := filepath.ToSlash(filepath.Join("internal", "store", "store.go"))
	proveedor := graph.NuevoProveedorNativo(snapshot, candidato.Arbol)
	resultado, err := proveedor.Analizar([]string{rutaHoja})
	if err != nil {
		b.Fatalf("el grafo nativo falló al analizar %s: %v", rutaHoja, err)
	}
	autorizacion, ok := graph.AutorizarAlcanceParcial(resultado)
	if !ok {
		b.Fatalf("el grafo nativo no autorizó alcance parcial sobre el repositorio real: completo=%v motivo=%q", resultado.Completo(), resultado.MotivoIncompleto())
	}

	capacidad := config.CapabilityConfig{Command: "go vet ./...", SupportsScope: true, ScopedCommand: "go vet {packages}"}
	comandoParcial, alcanceParcial, _ := resolverComando(capacidad, autorizacion)
	if alcanceParcial != AlcanceParcial {
		b.Fatalf("resolverComando no produjo alcance parcial sobre el repositorio real: comando=%q", comandoParcial)
	}
	comandoCompleto, _, _ := resolverComando(capacidad, graph.AutorizacionAlcance{})

	b.Run("parcial", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if exit, salida, err := agentshell.Ejecutar(snapshot, comandoParcial); err != nil || exit != 0 {
				b.Fatalf("%q falló: exit=%d err=%v salida=%s", comandoParcial, exit, err, salida)
			}
		}
	})
	b.Run("completo", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if exit, salida, err := agentshell.Ejecutar(snapshot, comandoCompleto); err != nil || exit != 0 {
				b.Fatalf("%q falló: exit=%d err=%v salida=%s", comandoCompleto, exit, err, salida)
			}
		}
	})
}
