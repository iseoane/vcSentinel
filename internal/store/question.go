package store

import (
	"strconv"
	"strings"
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
// de F2 para los findings. Cada componente va prefijado por su longitud
// decimal (mismo patrón que review.empaquetarConLongitud): un separador
// simple como "#" sería ambiguo si blob o questionID lo contuvieran
// literalmente — p.ej. claveRespuesta("X#question:q1", "q2") colisionaría
// con claveRespuesta("X", "q1#question:q2") sin este prefijo, suprimiendo una
// pregunta distinta de la que realmente se respondió.
func claveRespuesta(blob, questionID string) string {
	var b strings.Builder
	for _, c := range []string{blob, questionID} {
		b.WriteString(strconv.Itoa(len(c)))
		b.WriteByte(':')
		b.WriteString(c)
	}
	return b.String()
}

// RegistrarRespuesta persiste que questionID sobre blob fue respondida por
// actor con respuesta, como una Decision con Fingerprint = claveRespuesta(blob,
// questionID) y Decision = "question_answered".
//
// decisions.jsonl es un registro LOCAL sin autenticar (mismo modo 0644 que el
// resto del store) y Actor es atribución autodeclarada (ver resolverActor en
// cmd/sentinel): cualquiera con permiso de escritura en el repositorio puede
// sembrar una línea question_answered por adelantado y suprimir una pregunta
// real. Aceptable hoy porque nada la lee todavía (ver el párrafo siguiente);
// antes de conectar esto a un flujo interactivo real, revisar si esa
// propiedad basta o si la respuesta necesita algo más fuerte que "está en el
// archivo".
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
