package agentadapter

import "sync"

// AgenteEfectivo identifica quién atendió realmente una petición: el binario,
// el modelo y el esfuerzo con los que se ejecutó.
//
// Existe por H4/T0.2. La ficha de auditoría guardaba el nombre del perfil
// (o "default"), no el agente que respondió, así que con `active_agent: auto`
// un veredicto quedaba sin autor verificable: comprobado en la ficha de
// 6c079a8, que registró "default" tras responder claude.
type AgenteEfectivo struct {
	Binario  string `json:"agent,omitempty"`
	Modelo   string `json:"model,omitempty"`
	Esfuerzo string `json:"effort,omitempty"`
}

// Vacio indica que no hay autor que registrar. Se prefiere dejar el campo
// vacío antes que inventar uno: atribuir mal es el defecto que T0.2 corrige.
func (a AgenteEfectivo) Vacio() bool {
	return a.Binario == "" && a.Modelo == "" && a.Esfuerzo == ""
}

// ReportaAgenteEfectivo lo implementan los adaptadores capaces de decir quién
// atendió su última petición. Es opcional: un adaptador que no lo implemente
// simplemente no aporta autoría.
type ReportaAgenteEfectivo interface {
	AgenteEfectivo() (AgenteEfectivo, bool)
}

// AgenteEfectivo de un CLIAdapter es su propio binario con la configuración
// con la que se construyó: siempre responde él o no responde nadie.
func (c *CLIAdapter) AgenteEfectivo() (AgenteEfectivo, bool) {
	return AgenteEfectivo{
		Binario:  c.nombreBase(),
		Modelo:   c.Config.Model,
		Esfuerzo: c.Config.ReasoningEffort,
	}, true
}

// registroEfectivo guarda el último hijo que respondió dentro de una cadena.
// Lleva mutex porque el motor de auditoría audita varias dimensiones en
// paralelo y cada una puede reintentar con una ronda de aclaración.
type registroEfectivo struct {
	mu       sync.Mutex
	efectivo AgenteEfectivo
	definido bool
}

func (r *registroEfectivo) registrar(a adaptadorCompleto) {
	reporta, ok := a.(ReportaAgenteEfectivo)
	if !ok {
		return
	}
	efectivo, ok := reporta.AgenteEfectivo()
	if !ok || efectivo.Vacio() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.efectivo, r.definido = efectivo, true
}

func (r *registroEfectivo) leer() (AgenteEfectivo, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.efectivo, r.definido
}

// AgenteEfectivo devuelve el hijo que atendió la última petición con éxito.
// Devuelve false mientras ninguno haya respondido: el fallback es por
// petición y nunca se cachea, así que el autor sigue a la última llamada.
func (c *CadenaAdaptador) AgenteEfectivo() (AgenteEfectivo, bool) {
	return c.registro.leer()
}
