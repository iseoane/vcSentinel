package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/process"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewcontract"
)

// agentesMultiples es el valor que se registra cuando las dimensiones de una
// misma revisión las atendieron agentes distintos. No es un autor: es la
// constancia honesta de que no hay uno solo. Elegir uno de ellos repetiría el
// defecto de H4 con otra cara.
const agentesMultiples = "múltiples"

// recolectorAutoria reúne los agentes que realmente atendieron cada dimensión
// de una revisión. El motor de auditoría corre las dimensiones en paralelo,
// de ahí el mutex.
//
// Vive en cmd/ y no en internal/review a propósito: el motor de auditoría solo
// depende de la interfaz mínima AuditorAgente y no conoce agentadapter.
// Extraer aquí la autoría evita acoplar el motor a la implementación concreta
// de los adaptadores.
type recolectorAutoria struct {
	mu      sync.Mutex
	agentes []agentadapter.AgenteEfectivo
}

// observedAgent wraps one dimension agent and records the responder only
// AFTER a successful request. Timing matters: in an `active_agent: auto`
// chain, the child that serves the request is known only after it responds,
// so asking earlier would reproduce the incorrect H4 record.
type observedAgent struct {
	review.AuditorAgente
	authorship *recolectorAutoria
}

func (a *observedAgent) EjecutarPrompt(prompt string) (string, error) {
	salida, err := a.AuditorAgente.EjecutarPrompt(prompt)
	if err == nil {
		a.authorship.registrar(a.AuditorAgente)
	}
	return salida, err
}

// AgenteEfectivo forwards the wrapped adapter's effective-responder report so
// per-finding producer stamping survives authorship observation: the engine
// sees this wrapper, not the CLIAdapter that knows who answered.
func (a *observedAgent) AgenteEfectivo() (agentadapter.AgenteEfectivo, bool) {
	reporta, ok := a.AuditorAgente.(agentadapter.ReportaAgenteEfectivo)
	if !ok {
		return agentadapter.AgenteEfectivo{}, false
	}
	return reporta.AgenteEfectivo()
}

func (a *observedAgent) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	reviewer, ok := a.AuditorAgente.(interface {
		EjecutarRevision(string, string, []string) (string, error)
	})
	if !ok {
		return "", errors.New("semantic review unavailable")
	}
	salida, err := reviewer.EjecutarRevision(prompt, sha, paths)
	if err == nil {
		a.authorship.registrar(a.AuditorAgente)
	}
	return salida, err
}

func (a *observedAgent) ReviewWithPolicy(prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error) {
	reviewer, ok := a.AuditorAgente.(interface {
		ReviewWithPolicy(string, string, []string, reviewcontract.ToolPolicy) (string, error)
	})
	if !ok {
		// Mismo diagnóstico accionable que el motor: nombra el adaptador en
		// vez de repetir seis veces una capacidad ausente sin dueño.
		return "", fmt.Errorf("%w: the configured agent %T cannot review under a tool policy; configure a CLI agent (claude or opencode) for review", review.ErrRestrictedRequired, a.AuditorAgente)
	}
	output, err := reviewer.ReviewWithPolicy(prompt, sha, paths, policy)
	if err == nil {
		a.authorship.registrar(a.AuditorAgente)
	}
	return output, err
}

func (a *observedAgent) ReviewWithContext(ctx context.Context, prompt, sha string, paths []string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if contextual, ok := a.AuditorAgente.(interface {
		ReviewWithContext(context.Context, string, string, []string) (string, error)
	}); ok {
		salida, err := contextual.ReviewWithContext(ctx, prompt, sha, paths)
		if err == nil {
			a.authorship.registrar(a.AuditorAgente)
		}
		return salida, err
	}
	reviewer, ok := a.AuditorAgente.(interface {
		EjecutarRevision(string, string, []string) (string, error)
	})
	if !ok {
		return "", errors.New("semantic review unavailable")
	}
	salida, err := reviewer.EjecutarRevision(prompt, sha, paths)
	if err == nil {
		a.authorship.registrar(a.AuditorAgente)
	}
	return salida, err
}

func (a *observedAgent) ReviewWithContextAndPolicy(ctx context.Context, prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if contextual, ok := a.AuditorAgente.(interface {
		ReviewWithContextAndPolicy(context.Context, string, string, []string, reviewcontract.ToolPolicy) (string, error)
	}); ok {
		output, err := contextual.ReviewWithContextAndPolicy(ctx, prompt, sha, paths, policy)
		if err == nil {
			a.authorship.registrar(a.AuditorAgente)
		}
		return output, err
	}
	return a.ReviewWithPolicy(prompt, sha, paths, policy)
}

func (a *observedAgent) OwnedTree() *process.Tree {
	if provider, ok := a.AuditorAgente.(interface {
		OwnedTree() *process.Tree
	}); ok {
		return provider.OwnedTree()
	}
	return nil
}

// registrar anota el agente efectivo de un adaptador, si sabe reportarlo.
func (r *recolectorAutoria) registrar(agente any) {
	reporta, ok := agente.(agentadapter.ReportaAgenteEfectivo)
	if !ok {
		return
	}
	efectivo, ok := reporta.AgenteEfectivo()
	if !ok || efectivo.Vacio() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agentes = append(r.agentes, efectivo)
}

// consolidar devuelve el autor único de la revisión: el agente si todas las
// dimensiones coincidieron, la marca agentesMultiples si no, y el valor vacío
// si nadie respondió.
func (r *recolectorAutoria) consolidar() agentadapter.AgenteEfectivo {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.agentes) == 0 {
		return agentadapter.AgenteEfectivo{}
	}
	primero := r.agentes[0]
	for _, agente := range r.agentes[1:] {
		if agente != primero {
			return agentadapter.AgenteEfectivo{Binario: agentesMultiples}
		}
	}
	return primero
}
