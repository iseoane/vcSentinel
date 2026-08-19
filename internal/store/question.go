package store

import (
	"fmt"
	"time"
)

// decisionRespuestaPregunta es el valor discriminador de Decision para las
// respuestas a preguntas del agente (ver el comentario de Decision).
const decisionRespuestaPregunta = "question_answered"

// alcanceRespuestaPregunta identifica el contexto de una respuesta
// registrada por RegistrarRespuesta. Es un valor fijo (no hace falta más
// granularidad hoy: la clave blob+questionID en Fingerprint ya identifica de
// forma única qué se respondió); si en el futuro se necesita distinguir
// dimensión u otro contexto, este es el campo a especializar.
const alcanceRespuestaPregunta = "question"

// claveRespuesta construye la clave determinista de Fingerprint para una
// respuesta: combina blob + questionID para que la misma pregunta sobre el
// mismo contenido se reconozca como ya respondida incluso si el SHA del
// commit cambia (p.ej. tras un rebase), igual que hacen los índices de blob
// de F2 para los findings.
func claveRespuesta(blob, questionID string) string {
	return fmt.Sprintf("blob:%s#question:%s", blob, questionID)
}

// RegistrarRespuesta persiste que questionID sobre blob fue respondida por
// actor con respuesta, como una Decision con Fingerprint
// "blob:<blob>#question:<questionID>" y Decision = "question_answered".
//
// Deliberadamente NO está conectada todavía a cmd/sentinel/review, gate ni
// pr review: hoy no existe ningún bucle interactivo de preguntas en la CLI
// (internal/review/engine.go rellena ResultadoAuditoria.Preguntas, pero
// ningún comando de cmd/sentinel lo lee ni lo imprime; --answer es solo una
// ronda extra dentro del mismo proceso, no algo que la CLI dispare al
// detectar una pregunta pendiente entre invocaciones distintas). Esta
// función es la primitiva de persistencia lista para conectarse cuando esa
// superficie interactiva exista, siguiendo el mismo patrón de "sin segundo
// consumidor todavía" ya usado para varias piezas de T7.1-T7.4.
func (s *Store) RegistrarRespuesta(blob, questionID, respuesta, actor string) error {
	return s.RegistrarDecision(&Decision{
		Fingerprint: claveRespuesta(blob, questionID),
		Decision:    decisionRespuestaPregunta,
		Actor:       actor,
		At:          time.Now().UTC(),
		Motivo:      respuesta,
		Alcance:     alcanceRespuestaPregunta,
	})
}

// RespuestaRegistrada informa si questionID sobre blob ya fue respondida en
// una ejecución anterior, y devuelve esa respuesta si es así. Si la misma
// pregunta se respondió más de una vez (no se espera en el flujo normal,
// pero decisions.jsonl es append-only y no lo impide), se devuelve la
// respuesta más reciente.
//
// Ver el comentario de RegistrarRespuesta: este par es una primitiva de
// persistencia, deliberadamente no conectada todavía a ningún comando de la
// CLI.
func (s *Store) RespuestaRegistrada(blob, questionID string) (respuesta string, ok bool, err error) {
	decisiones, err := s.LeerDecisiones()
	if err != nil {
		return "", false, err
	}
	clave := claveRespuesta(blob, questionID)
	for _, d := range decisiones {
		if d.Decision == decisionRespuestaPregunta && d.Fingerprint == clave {
			respuesta, ok = d.Motivo, true
		}
	}
	return respuesta, ok, nil
}
