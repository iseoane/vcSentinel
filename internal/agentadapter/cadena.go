package agentadapter

import (
	"context"
	"fmt"
	"strings"
)

// adaptadorCompleto es la interfaz interna que CadenaAdaptador exige a sus
// hijos: generar mensajes de commit (AgentAdapter) y responder prompts
// arbitrarios (AdaptadorPrompt).
type adaptadorCompleto interface {
	AgentAdapter
	EjecutarPrompt(prompt string) (string, error)
}

// CadenaAdaptador envuelve una lista ordenada de adaptadores y, en cada
// petición, prueba el primero; si falla, prueba el siguiente (fallback por
// petición, nunca cacheado). Implementa AgentAdapter, AdapterConDiff,
// AdapterRefactor y AdaptadorPrompt.
type CadenaAdaptador struct {
	adaptadores []adaptadorCompleto
	// registro anota qué hijo atendió la última petición, para que la ficha
	// de auditoría pueda registrar el autor real y no el perfil pedido (H4).
	registro registroEfectivo
}

// EjecutarPrompt prueba cada adaptador en orden y devuelve la primera salida
// sin error. Si todos fallan, devuelve un error agregado con el de cada uno.
func (c *CadenaAdaptador) EjecutarPrompt(prompt string) (string, error) {
	return c.primeroExitoso(func(a adaptadorCompleto) (string, error) {
		return a.EjecutarPrompt(prompt)
	})
}

// EjecutarRevision preserves per-request fallback while retaining tool limits.
func (c *CadenaAdaptador) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	return c.primeroExitoso(func(a adaptadorCompleto) (string, error) {
		if reviewer, ok := a.(interface {
			EjecutarRevision(string, string, []string) (string, error)
		}); ok {
			return reviewer.EjecutarRevision(prompt, sha, paths)
		}
		return "", fmt.Errorf("semantic review unavailable: adapter %s does not implement EjecutarRevision", nombreAdaptador(a))
	})
}

// ReviewWithContext preserves per-request fallback while forwarding the
// cancellation context: children that accept a context receive it so aborts
// reach their spawned provider processes; children that only implement the
// legacy contract keep answering exactly as before.
func (c *CadenaAdaptador) ReviewWithContext(ctx context.Context, prompt, sha string, paths []string) (string, error) {
	return c.primeroExitoso(func(a adaptadorCompleto) (string, error) {
		if contextual, ok := a.(interface {
			ReviewWithContext(context.Context, string, string, []string) (string, error)
		}); ok {
			return contextual.ReviewWithContext(ctx, prompt, sha, paths)
		}
		if reviewer, ok := a.(interface {
			EjecutarRevision(string, string, []string) (string, error)
		}); ok {
			return reviewer.EjecutarRevision(prompt, sha, paths)
		}
		return "", fmt.Errorf("semantic review unavailable: adapter %s does not implement EjecutarRevision", nombreAdaptador(a))
	})
}

// ObtenerMensajeCommit genera el mensaje de commit probando cada adaptador en
// orden hasta obtener una salida sin error.
func (c *CadenaAdaptador) ObtenerMensajeCommit(rutasArchivos []string, capa string, batchNum int) (string, error) {
	return c.primeroExitoso(func(a adaptadorCompleto) (string, error) {
		return a.ObtenerMensajeCommit(rutasArchivos, capa, batchNum)
	})
}

// ObtenerMensajeCommitConDiff genera el mensaje de commit usando el micro-diff
// cuando el hijo implementa AdapterConDiff; si no, usa el método base del hijo.
func (c *CadenaAdaptador) ObtenerMensajeCommitConDiff(rutasArchivos []string, capa string, batchNum int, diff string) (string, error) {
	return c.primeroExitoso(func(a adaptadorCompleto) (string, error) {
		if conDiff, ok := a.(AdapterConDiff); ok {
			return conDiff.ObtenerMensajeCommitConDiff(rutasArchivos, capa, batchNum, diff)
		}
		return a.ObtenerMensajeCommit(rutasArchivos, capa, batchNum)
	})
}

// ProponerPlanRefactor pide el plan de división probando solo los hijos que
// implementan AdapterRefactor, en orden.
func (c *CadenaAdaptador) ProponerPlanRefactor(rutaArchivo string) (string, error) {
	return c.primeroExitoso(func(a adaptadorCompleto) (string, error) {
		if refactor, ok := a.(AdapterRefactor); ok {
			return refactor.ProponerPlanRefactor(rutaArchivo)
		}
		return "", fmt.Errorf("el adaptador %s no implementa AdapterRefactor", nombreAdaptador(a))
	})
}

// AplicarPlanRefactor ordena aplicar un plan de refactorización probando solo
// los hijos que implementan AdapterRefactor, en orden.
func (c *CadenaAdaptador) AplicarPlanRefactor(rutaArchivo string, plan string) (string, error) {
	return c.primeroExitoso(func(a adaptadorCompleto) (string, error) {
		if refactor, ok := a.(AdapterRefactor); ok {
			return refactor.AplicarPlanRefactor(rutaArchivo, plan)
		}
		return "", fmt.Errorf("el adaptador %s no implementa AdapterRefactor", nombreAdaptador(a))
	})
}

// primeroExitoso recorre los adaptadores en orden ejecutando intentar con cada
// uno; devuelve la primera salida sin error o un error agregado si todos fallan.
func (c *CadenaAdaptador) primeroExitoso(intentar func(adaptadorCompleto) (string, error)) (string, error) {
	if len(c.adaptadores) == 0 {
		return "", fmt.Errorf("la cadena de adaptadores esta vacia")
	}
	errores := make([]string, 0, len(c.adaptadores))
	for _, adaptador := range c.adaptadores {
		salida, err := intentar(adaptador)
		if err == nil {
			// Solo el que respondió queda registrado como autor.
			c.registro.registrar(adaptador)
			return salida, nil
		}
		errores = append(errores, fmt.Sprintf("%s: %v", nombreAdaptador(adaptador), err))
	}
	return "", fmt.Errorf("ningun agente de la cadena respondio: %s", strings.Join(errores, "; "))
}

// nombreAdaptador devuelve un identificador legible de un adaptador para los
// mensajes de error: el nombre base del binario para CLIAdapter, el String()
// si el adaptador lo define y el tipo como último recurso.
func nombreAdaptador(a adaptadorCompleto) string {
	if cli, ok := a.(*CLIAdapter); ok {
		return cli.nombreBase()
	}
	if s, ok := a.(fmt.Stringer); ok {
		return s.String()
	}
	return fmt.Sprintf("%T", a)
}
