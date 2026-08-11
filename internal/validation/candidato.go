package validation

import (
	"errors"
	"fmt"
	"path/filepath"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/graph"
)

// ErrCandidatoObsoleto señala que el árbol (o el HEAD) del repositorio
// cambió mientras se ejecutaba la validación en modo worktree: el resultado
// ya no representa el estado que el usuario congeló y no debe usarse como
// evidencia de ningún gate (pre-commit, pre-push, pr review...).
var ErrCandidatoObsoleto = errors.New("el candidato cambió durante la validación: resultado obsoleto, no usable como evidencia")

// EjecutarPerfilSobreCandidato es el punto de entrada de T1.6: envuelve
// EjecutarPerfil (T1.3) con la garantía de aislamiento/vigencia que exige
// config.ValidationConfig.Mode, sin que el llamador tenga que conocer la
// diferencia entre correr sobre un snapshot o sobre el worktree real.
//
//   - inplace: exige el worktree limpio (git.ExigirWorktreeLimpioEnInplace)
//     ANTES de ejecutar nada; si está sucio, aborta sin correr ningún
//     comando. Si está limpio, ejecuta directamente sobre opts.Worktree (el
//     worktree real): no hay snapshot que crear ni vigencia que comprobar,
//     porque la guarda ya exigió que nada esté a medio commitear.
//   - worktree (default, también cuando Mode viene vacío): congela el
//     candidato (git.Congelar) ANTES de ejecutar, corre la validación sobre
//     un snapshot aislado del árbol del candidato congelado (git.CrearSnapshot) y, al
//     terminar, comprueba que el candidato sigue vigente (git.SigueVigente).
//     Si dejó de estarlo —el HEAD o el árbol del worktree real cambiaron
//     durante la ejecución—, el resultado se descarta y se devuelve
//     ErrCandidatoObsoleto: nunca se propagan runs que puedan corresponder a
//     un estado distinto del que el usuario cree estar validando.
//
// Limitación conocida y aceptada del modo worktree: el snapshot es un
// checkout limpio en un directorio nuevo (git.CrearSnapshot), así que no
// tiene node_modules, .env, ni fixtures sin seguimiento (untracked) que el
// worktree real sí tiene; una capability que dependa de eso fallará ahí
// aunque pasaría en el worktree real. El modo inplace existe como
// alternativa configurable exactamente para ese caso.
func EjecutarPerfilSobreCandidato(perfil string, rutasCambiadas []string, opts OpcionesEjecucion) ([]ValidationRun, error) {
	modo := opts.Cfg.Validation.Mode
	if modo == "" {
		modo = config.ModeWorktree
	}

	if modo == config.ModeInplace {
		if err := git.ExigirWorktreeLimpioEnInplace(modo); err != nil {
			return nil, err
		}
		return EjecutarPerfil(perfil, opts)
	}

	candidato, err := git.Congelar()
	if err != nil {
		return nil, fmt.Errorf("no se pudo congelar el candidato antes de validar: %w", err)
	}

	// El snapshot se ancla al árbol de candidato.Arbol, no a un ArbolDe("HEAD")
	// recalculado aparte: Congelar ya decidió el árbol correcto (el de HEAD si
	// el worktree estaba limpio, o el stash-anchor si estaba sucio). Anclar a
	// HEAD por separado ignoraría cambios sin commitear que ya existían ANTES
	// de llamar a esta función, y SigueVigente los daría por buenos si nada
	// más cambiaba durante la ejecución: el resultado parecería vigente pero
	// habría validado un árbol distinto del que el usuario congeló.
	snapshot, err := git.CrearSnapshot(candidato.Arbol)
	if err != nil {
		return nil, fmt.Errorf("no se pudo crear el snapshot de validación: %w", err)
	}

	opts.Worktree = snapshot
	opts.autorizacion = graph.AutorizacionAlcance{}
	if opts.ProveedorGraph != nil && len(rutasCambiadas) > 0 {
		resultado, errorGraph := opts.ProveedorGraph(snapshot, candidato.Arbol).Analizar(rutasCambiadas)
		if errorGraph == nil && resultado.IdentidadSnapshot() == candidato.Arbol && mismasRutas(resultado.RutasAnalizadas(), rutasCambiadas) {
			opts.autorizacion, _ = graph.AutorizarAlcanceParcial(resultado)
		}
	}
	runs, err := EjecutarPerfil(perfil, opts)
	if err != nil {
		return runs, err
	}

	vigente, err := git.SigueVigente(candidato)
	if err != nil {
		return nil, fmt.Errorf("no se pudo comprobar si el candidato seguía vigente tras validar: %w", err)
	}
	if !vigente {
		// El árbol cambió durante la ejecución: los runs ya obtenidos podrían
		// corresponder a un estado distinto del que el usuario congeló, así
		// que se descartan en vez de dejar que un gate los use por error.
		return nil, ErrCandidatoObsoleto
	}
	return runs, nil
}

func mismasRutas(analizadas, cambiadas []string) bool {
	if len(analizadas) != len(cambiadas) {
		return false
	}
	pendientes := make(map[string]int, len(cambiadas))
	for _, ruta := range cambiadas {
		pendientes[filepath.ToSlash(filepath.Clean(filepath.FromSlash(ruta)))]++
	}
	for _, ruta := range analizadas {
		pendientes[ruta]--
	}
	for _, cantidad := range pendientes {
		if cantidad != 0 {
			return false
		}
	}
	return true
}
