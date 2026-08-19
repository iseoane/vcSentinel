package store

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

// Decision es una decisión humana registrada en decisions.jsonl. El mismo
// esquema de cinco campos cubre tres usos distintos (T7.5), distinguidos por
// el valor de Decision en vez de un campo "tipo" separado:
//   - Decisión sobre un hallazgo concreto: Fingerprint = fingerprint del
//     hallazgo, Decision = p.ej. "accept"/"reject".
//   - Bypass de --force (informe M3): Fingerprint vacío (no hay un hallazgo
//     único que se esté superando, sino la validación de todo un run),
//     Decision = "force_bypass", Motivo = el --reason del usuario,
//     Alcance = dónde ocurrió el bypass (p.ej. "pr-create").
//   - Respuesta a una pregunta del agente: Fingerprint = clave determinista
//     "blob:<blob>#question:<questionID>" (ver RegistrarRespuesta),
//     Decision = "question_answered", Motivo = el texto de la respuesta.
type Decision struct {
	Fingerprint string    `json:"fingerprint"`
	Decision    string    `json:"decision"`
	Actor       string    `json:"actor"`
	At          time.Time `json:"at"`
	Motivo      string    `json:"motivo,omitempty"`
	Alcance     string    `json:"alcance,omitempty"`
}

// DecisionForceBypass y AlcancePrCreate son el vocabulario exportado del
// bypass de --force (informe M3), para que un caller fuera de este paquete
// (cmd/sentinel) no componga los literales a mano: un typo en un literal
// suelto produciría una fila que ningún lector de decisions.jsonl reconoce,
// sin ningún error de compilación que lo delate.
const (
	DecisionForceBypass = "force_bypass"
	AlcancePrCreate     = "pr-create"
)

// RegistrarDecision añade una línea a decisions.jsonl. A diferencia de
// units/runs/findings/commits, este archivo es append-only por naturaleza:
// cada línea es un hecho inmutable del pasado, nunca se relee para sustituir
// un registro anterior. Por eso NO usa el patrón temp+rename de guardarJSON
// (que asume "hay un último estado válido que reemplazar"): se abre con
// O_APPEND|O_CREATE|O_WRONLY, que en POSIX garantiza que cada write() se
// coloca al final del archivo como operación atómica, sin entrelazarse con
// otro escritor concurrente.
func (s *Store) RegistrarDecision(d *Decision) error {
	if err := os.MkdirAll(s.dir, 0755); err != nil {
		return err
	}
	datos, err := json.Marshal(d)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.rutaDecisiones(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(datos, '\n'))
	return err
}

// LeerDecisiones devuelve todas las decisiones registradas, en orden de
// escritura. Un archivo inexistente devuelve (nil, nil); una línea corrupta
// es un error explícito, igual que en el resto del store.
func (s *Store) LeerDecisiones() ([]Decision, error) {
	datos, err := os.ReadFile(s.rutaDecisiones())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var decisiones []Decision
	for _, linea := range strings.Split(strings.TrimRight(string(datos), "\n"), "\n") {
		if linea == "" {
			continue
		}
		var d Decision
		if err := json.Unmarshal([]byte(linea), &d); err != nil {
			return nil, err
		}
		decisiones = append(decisiones, d)
	}
	return decisiones, nil
}
