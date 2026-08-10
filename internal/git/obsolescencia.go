package git

import (
	"fmt"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// CandidatoCongelado fija, en el instante de Congelar(), la identidad exacta
// que una validación posterior asume seguir vigente: el commit HEAD y el
// árbol (tree OID) del worktree en ese momento. Ambos campos participan en
// la comparación de SigueVigente: si cualquiera de los dos cambió, el
// candidato deja de representar el estado que el usuario cree estar
// validando.
type CandidatoCongelado struct {
	SHA   string
	Arbol string
}

// Congelar captura el estado actual del repositorio: el SHA de HEAD y el
// tree OID del worktree.
//
// Decisión de diseño: si el worktree está limpio, el árbol es directamente
// el de HEAD (ArbolDe("HEAD")), que es barato y no toca el índice ni el
// worktree. Si está sucio, se usa "git stash create": genera un commit
// colgante que representa el contenido actual de los archivos con
// seguimiento (tracked), sin modificar el índice, el worktree ni la lista de
// stashes real (no se ejecuta "stash store"). Limitación conocida y
// aceptada: "stash create" no incluye archivos sin seguimiento (untracked);
// si eso importa para un caso de uso concreto, esa llamada debe apoyarse
// primero en WorktreeLimpio()/ExigirWorktreeLimpioEnInplace, como ya exige
// el modo inplace más abajo en este archivo.
func Congelar() (CandidatoCongelado, error) {
	sha, err := SHAHead()
	if err != nil {
		return CandidatoCongelado{}, fmt.Errorf("no se pudo congelar el candidato: %w", err)
	}

	limpio, err := WorktreeLimpio()
	if err != nil {
		return CandidatoCongelado{}, fmt.Errorf("no se pudo congelar el candidato: %w", err)
	}

	var arbol string
	if limpio {
		arbol, err = ArbolDe("HEAD")
	} else {
		arbol, err = arbolDeWorktreeSucio()
	}
	if err != nil {
		return CandidatoCongelado{}, fmt.Errorf("no se pudo congelar el candidato: %w", err)
	}

	return CandidatoCongelado{SHA: sha, Arbol: arbol}, nil
}

// arbolDeWorktreeSucio obtiene el tree OID del contenido actual del
// worktree (archivos con seguimiento) sin mutar el índice ni el worktree,
// vía "git stash create". Ver la nota de diseño en Congelar.
func arbolDeWorktreeSucio() (string, error) {
	salida, err := ejecutarGitSalida("stash", "create")
	if err != nil {
		return "", fmt.Errorf("no se pudo capturar el árbol del worktree sucio: %w", err)
	}
	commitAncla := strings.TrimSpace(salida)
	if commitAncla == "" {
		// No debería ocurrir si WorktreeLimpio() ya detectó suciedad, pero
		// por robustez ante una carrera (otro proceso commiteó entre medio)
		// se cae al árbol de HEAD en vez de fallar.
		return ArbolDe("HEAD")
	}
	return ArbolDe(commitAncla)
}

// SigueVigente recomprueba el estado actual del repositorio y lo compara con
// el capturado en Congelar(). Sigue vigente solo si el SHA de HEAD y el tree
// OID coinciden ambos con los del candidato: un commit nuevo invalida el
// candidato aunque el árbol resultante sea idéntico, porque referencia un
// HEAD distinto al que el usuario congeló.
func SigueVigente(candidato CandidatoCongelado) (bool, error) {
	actual, err := Congelar()
	if err != nil {
		return false, fmt.Errorf("no se pudo comprobar si el candidato sigue vigente: %w", err)
	}
	return actual.SHA == candidato.SHA && actual.Arbol == candidato.Arbol, nil
}

// ExigirWorktreeLimpioEnInplace aborta antes de cualquier validación en modo
// inplace si el worktree tiene cambios sin commitear: sin esta guarda, la
// validación correría igual y reportaría el resultado de un árbol distinto
// al que el usuario cree que está validando. Fuera del modo inplace no
// aplica (no aborta): el modo worktree ya opera sobre un snapshot aislado.
func ExigirWorktreeLimpioEnInplace(modo string) error {
	if modo != config.ModeInplace {
		return nil
	}
	limpio, err := WorktreeLimpio()
	if err != nil {
		return fmt.Errorf("no se pudo comprobar el estado del worktree: %w", err)
	}
	if !limpio {
		return fmt.Errorf("modo inplace requiere el worktree limpio antes de validar: hay cambios sin commitear")
	}
	return nil
}
