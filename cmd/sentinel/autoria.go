package main

import (
	"context"
	"errors"
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

// agenteObservado envuelve el agente de una dimensión y anota quién respondió
// DESPUÉS de cada petición con éxito. El momento importa: en una cadena con
// `active_agent: auto`, cuál de los hijos atiende solo se sabe una vez ha
// contestado, así que preguntarlo antes daría el mismo dato equivocado que
// H4 registraba.
type agenteObservado struct {
	review.AuditorAgente
	autoria *recolectorAutoria
}

func (a *agenteObservado) EjecutarPrompt(prompt string) (string, error) {
	salida, err := a.AuditorAgente.EjecutarPrompt(prompt)
	if err == nil {
		a.autoria.registrar(a.AuditorAgente)
	}
	return salida, err
}

func (a *agenteObservado) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	reviewer, ok := a.AuditorAgente.(interface {
		EjecutarRevision(string, string, []string) (string, error)
	})
	if !ok {
		return "", errors.New("semantic review unavailable")
	}
	salida, err := reviewer.EjecutarRevision(prompt, sha, paths)
	if err == nil {
		a.autoria.registrar(a.AuditorAgente)
	}
	return salida, err
}

func (a *agenteObservado) ReviewWithPolicy(prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error) {
	reviewer, ok := a.AuditorAgente.(interface {
		ReviewWithPolicy(string, string, []string, reviewcontract.ToolPolicy) (string, error)
	})
	if !ok {
		return "", review.ErrRestrictedRequired
	}
	output, err := reviewer.ReviewWithPolicy(prompt, sha, paths, policy)
	if err == nil {
		a.autoria.registrar(a.AuditorAgente)
	}
	return output, err
}

func (a *agenteObservado) ReviewWithContext(ctx context.Context, prompt, sha string, paths []string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if contextual, ok := a.AuditorAgente.(interface {
		ReviewWithContext(context.Context, string, string, []string) (string, error)
	}); ok {
		salida, err := contextual.ReviewWithContext(ctx, prompt, sha, paths)
		if err == nil {
			a.autoria.registrar(a.AuditorAgente)
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
		a.autoria.registrar(a.AuditorAgente)
	}
	return salida, err
}

func (a *agenteObservado) ReviewWithContextAndPolicy(ctx context.Context, prompt, sha string, paths []string, policy reviewcontract.ToolPolicy) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if contextual, ok := a.AuditorAgente.(interface {
		ReviewWithContextAndPolicy(context.Context, string, string, []string, reviewcontract.ToolPolicy) (string, error)
	}); ok {
		output, err := contextual.ReviewWithContextAndPolicy(ctx, prompt, sha, paths, policy)
		if err == nil {
			a.autoria.registrar(a.AuditorAgente)
		}
		return output, err
	}
	return a.ReviewWithPolicy(prompt, sha, paths, policy)
}

func (a *agenteObservado) OwnedTree() *process.Tree {
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
