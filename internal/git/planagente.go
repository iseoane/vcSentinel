package git

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Respuestas admitidas para una decisión pendiente del plan. No hay valor por
// defecto: T0.10 exige una respuesta explícita para cada decisión.
const (
	RespuestaBypass  = "bypass"
	RespuestaAbortar = "abortar"
)

// DecisionPendiente es una pregunta que el modo interactivo haría por stdin y
// que el modo plan registra en su lugar, para que un orquestador la traslade
// al usuario. La decisión sigue siendo del humano: registrar no es aprobar.
type DecisionPendiente struct {
	ID       string   `json:"id"`
	Archivo  string   `json:"archivo"`
	Lineas   int      `json:"lineas"`
	Pregunta string   `json:"pregunta"`
	Opciones []string `json:"opciones"`
}

// LoteSerializado es la proyección estable de un LotePlanificado en el plan
// emitido: solo lo que un consumidor externo necesita para revisarlo.
type LoteSerializado struct {
	Numero    int      `json:"numero"`
	Capa      string   `json:"capa"`
	Rutas     []string `json:"rutas"`
	Lineas    int      `json:"lineas"`
	Mensaje   string   `json:"mensaje"`
	EsGigante bool     `json:"es_gigante"`
}

// PlanSerializado es el plan completo emitido por `sentinel slice plan`. No
// commitea nada y es idempotente: sobre el mismo árbol produce el mismo
// PlanID y el mismo EstadoWorktree.
type PlanSerializado struct {
	PlanID               string              `json:"plan_id"`
	EstadoWorktree       string              `json:"estado_worktree"`
	Lotes                []LoteSerializado   `json:"lotes"`
	DecisionesPendientes []DecisionPendiente `json:"decisiones_pendientes"`
}

// registradorDecisiones implementa la segunda vía del callback de decisión de
// ConstruirPlanFragmentacion: en vez de preguntar por stdin, anota la pregunta
// y deja seguir la construcción para poder emitir el plan completo. Devolver
// true aquí NO aprueba nada: el lote queda marcado como gigante y `slice
// apply` se negará mientras la decisión no tenga respuesta explícita.
type registradorDecisiones struct {
	pendientes []DecisionPendiente
}

func (r *registradorDecisiones) callback(f ArchivoModificado) (bool, error) {
	r.pendientes = append(r.pendientes, DecisionPendiente{
		ID:      IDDecision(f.Ruta),
		Archivo: f.Ruta,
		Lineas:  f.Lineas,
		Pregunta: fmt.Sprintf(
			"%s tiene %d líneas y supera el máximo sugerido de %d. ¿Fragmentarlo tal cual (bypass) o abortar?",
			f.Ruta, f.Lineas, LimiteCodigoGigante),
		Opciones: []string{RespuestaBypass, RespuestaAbortar},
	})
	return true, nil
}

// ConstruirPlanParaAgente calcula el plan de fragmentación y lo emite sin
// crear ningún commit ni leer stdin. Es seguro ejecutarlo tantas veces como
// haga falta.
func ConstruirPlanParaAgente() (*PlanSerializado, error) {
	archivos, err := ObtenerArchivosModificados()
	if err != nil {
		return nil, err
	}
	registrador := &registradorDecisiones{}
	plan, err := ConstruirPlanFragmentacion(archivos, registrador.callback)
	if err != nil {
		return nil, err
	}
	estado, err := HashEstadoWorktree()
	if err != nil {
		return nil, err
	}
	return SerializarPlan(plan, registrador.pendientes, estado), nil
}

// SerializarPlan proyecta el plan y calcula su PlanID a partir de los lotes.
func SerializarPlan(plan *PlanFragmentacion, pendientes []DecisionPendiente, estado string) *PlanSerializado {
	lotes := make([]LoteSerializado, 0, len(plan.Lotes))
	for _, lote := range plan.Lotes {
		mensaje := lote.Mensaje
		if strings.TrimSpace(mensaje) == "" {
			mensaje = lote.MensajeAutomatico
		}
		lotes = append(lotes, LoteSerializado{
			Numero:    lote.Numero,
			Capa:      lote.Capa,
			Rutas:     lote.Rutas,
			Lineas:    lote.LineasTotales,
			Mensaje:   mensaje,
			EsGigante: lote.EsGigante,
		})
	}
	if pendientes == nil {
		pendientes = []DecisionPendiente{}
	}
	return &PlanSerializado{
		PlanID:               calcularPlanID(lotes),
		EstadoWorktree:       estado,
		Lotes:                lotes,
		DecisionesPendientes: pendientes,
	}
}

// calcularPlanID resume la estructura de los lotes: capa, rutas y tamaño. No
// incluye el mensaje, que puede regenerarse sin cambiar qué se commitea.
func calcularPlanID(lotes []LoteSerializado) string {
	h := sha256.New()
	for _, lote := range lotes {
		fmt.Fprintf(h, "%d|%s|%d|%t|", lote.Numero, lote.Capa, lote.Lineas, lote.EsGigante)
		for _, ruta := range lote.Rutas {
			fmt.Fprintf(h, "%s,", ruta)
		}
		fmt.Fprint(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil))
}

// IDDecision identifica una decisión pendiente por el archivo que la provoca,
// de forma estable entre ejecuciones sobre el mismo árbol.
func IDDecision(ruta string) string {
	suma := sha256.Sum256([]byte(ruta))
	return hex.EncodeToString(suma[:8])
}

// HashEstadoWorktree resume el árbol pendiente: las rutas con cambios y el
// contenido de cada una. Es la congelación del candidato aplicada a slice —
// T0.10 rechaza aplicar un plan calculado sobre otro estado. Un archivo
// borrado no tiene contenido que hashear: cuenta solo por su línea de estado.
func HashEstadoWorktree() (string, error) {
	salida, err := ejecutarGitSalida("status", "--porcelain")
	if err != nil {
		return "", err
	}
	lineas := make([]string, 0, 16)
	for _, linea := range strings.Split(salida, "\n") {
		if strings.TrimSpace(linea) != "" {
			lineas = append(lineas, linea)
		}
	}
	sort.Strings(lineas)

	h := sha256.New()
	for _, linea := range lineas {
		fmt.Fprintf(h, "%s\n", linea)
		ruta := strings.TrimSpace(linea)
		if idx := strings.Index(ruta, " "); idx >= 0 {
			ruta = strings.TrimSpace(ruta[idx:])
		}
		if contenido, err := ejecutarGitSalida("hash-object", "--", ruta); err == nil {
			fmt.Fprintf(h, "%s\n", strings.TrimSpace(contenido))
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
