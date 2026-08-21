package git

import (
	"errors"
	"fmt"
)

// Motivos de rechazo de `slice apply`. Son errores centinela para que la CLI
// distinga «te falta aprobar» de «esto ya no es aplicable».
var (
	// ErrArbolCambiado: el plan se calculó sobre otro estado del worktree.
	ErrArbolCambiado = errors.New("el árbol de trabajo cambió desde que se calculó el plan")
	// ErrPlanNoCoincide: las respuestas aprueban un plan distinto.
	ErrPlanNoCoincide = errors.New("las respuestas no corresponden a este plan")
	// ErrDecisionSinRespuesta: falta una respuesta explícita.
	ErrDecisionSinRespuesta = errors.New("hay decisiones pendientes sin respuesta explícita")
	// ErrDecisionAbortada: el usuario respondió abortar.
	ErrDecisionAbortada = errors.New("el usuario abortó la fragmentación")
)

// RespuestasPlan es el artefacto de aprobación: respuestas explícitas ligadas
// a un plan concreto. No admite valores por defecto ni un «aprobar todo»
// implícito.
type RespuestasPlan struct {
	PlanID     string            `json:"plan_id"`
	Respuestas map[string]string `json:"respuestas"`
}

// AplicarPlanAprobado ejecuta un plan emitido por `slice plan` solo si las
// tres ligaduras se cumplen: el árbol es el mismo, las respuestas son de este
// plan y toda decisión pendiente tiene respuesta explícita. Cualquier fallo
// corta antes de crear un solo commit.
//
// Lo que esto elimina es el accidente de B9: ya no hay lectura de stdin que
// pueda confundir «nadie al teclado» con «el humano aprobó». Lo que NO
// promete es impedir que un agente deliberado llame a `git commit` por su
// cuenta: eso queda fuera del modelo de amenaza.
func AplicarPlanAprobado(plan *PlanSerializado, respuestas RespuestasPlan) ([]ResultadoCommit, error) {
	if err := ValidarAplicacion(plan, respuestas); err != nil {
		return nil, err
	}
	ejecutable := deserializarPlan(plan)
	if len(ejecutable.Changes) > 0 {
		return ejecutarPlanConSelecciones(ejecutable)
	}
	return EjecutarPlanFragmentacion(ejecutable)
}

// ValidarAplicacion comprueba las tres ligaduras sin tocar el repositorio más
// allá de releer el estado del árbol. Separarla de la ejecución permite
// verificar una aprobación sin arriesgar ningún commit.
func ValidarAplicacion(plan *PlanSerializado, respuestas RespuestasPlan) error {
	if err := ValidateSerializedPlan(plan); err != nil {
		return err
	}
	var estadoActual string
	var err error
	if len(plan.Changes) > 0 {
		estadoActual, err = hashDraftStateForChanges(plan.Changes)
	} else {
		estadoActual, err = HashEstadoWorktree(RutasDelPlan(plan))
	}
	if err != nil {
		return err
	}
	if estadoActual != plan.EstadoWorktree {
		return fmt.Errorf("%w: vuelve a ejecutar 'sentinel slice plan'", ErrArbolCambiado)
	}
	if respuestas.PlanID != plan.PlanID {
		return fmt.Errorf("%w: aprobaron el plan %q y este es el %q", ErrPlanNoCoincide, respuestas.PlanID, plan.PlanID)
	}
	for _, decision := range plan.DecisionesPendientes {
		respuesta, ok := respuestas.Respuestas[decision.ID]
		switch {
		case !ok:
			return fmt.Errorf("%w: falta la decisión %s (%s)", ErrDecisionSinRespuesta, decision.ID, decision.Archivo)
		case respuesta == RespuestaAbortar:
			return fmt.Errorf("%w: %s", ErrDecisionAbortada, decision.Archivo)
		case respuesta != RespuestaBypass:
			return fmt.Errorf("%w: respuesta %q no admitida para la decisión %s", ErrDecisionSinRespuesta, respuesta, decision.ID)
		}
	}
	return nil
}

// deserializarPlan reconstruye el plan ejecutable desde su proyección. El
// mensaje ya viene aprobado, así que se fija como definitivo.
func deserializarPlan(plan *PlanSerializado) *PlanFragmentacion {
	ejecutable := &PlanFragmentacion{Lotes: make([]LotePlanificado, 0, len(plan.Lotes))}
	for _, lote := range plan.Lotes {
		ejecutable.Lotes = append(ejecutable.Lotes, LotePlanificado{
			Capa:              lote.Capa,
			Numero:            lote.Numero,
			Rutas:             lote.Rutas,
			Selectors:         lote.Selectors,
			LineasTotales:     lote.Lineas,
			Mensaje:           lote.Mensaje,
			MensajeAutomatico: lote.Mensaje,
			EsGigante:         lote.EsGigante,
		})
	}
	ejecutable.Changes = cloneChanges(plan.Changes)
	return ejecutable
}
