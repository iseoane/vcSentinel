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
	return ConstruirPlanParaAgenteConAdapter(nil)
}

// ConstruirPlanParaAgenteConAdapter intenta generar mensajes semánticos antes
// de serializar; un adaptador ausente o fallido conserva el fallback del plan.
type generadorMensajesCommit interface {
	ObtenerMensajeCommit(rutasArchivos []string, capa string, batchNum int) (string, error)
}

func ConstruirPlanParaAgenteConAdapter(adapter generadorMensajesCommit) (*PlanSerializado, error) {
	archivos, err := ObtenerArchivosModificados()
	if err != nil {
		return nil, err
	}
	registrador := &registradorDecisiones{}
	plan, err := ConstruirPlanFragmentacionConLector(archivos, registrador.callback, ejecutarGitSalida)
	if err != nil {
		return nil, err
	}
	if adapter != nil {
		GenerarMensajesLotes(plan, adapter)
	}
	serializado := SerializarPlan(plan, registrador.pendientes, "")
	estado, err := HashEstadoWorktree(RutasDelPlan(serializado))
	if err != nil {
		return nil, err
	}
	serializado.EstadoWorktree = estado
	return serializado, nil
}

// RutasDelPlan devuelve, ordenadas, todas las rutas que el plan commitearía.
func RutasDelPlan(plan *PlanSerializado) []string {
	var rutas []string
	for _, lote := range plan.Lotes {
		rutas = append(rutas, lote.Rutas...)
	}
	sort.Strings(rutas)
	return rutas
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

// HashEstadoWorktree congela el estado exacto de las rutas que el plan
// commitearía: su línea de estado en git y el hash de su contenido. T0.10 lo
// usa para negarse a aplicar un plan calculado sobre otro estado.
//
// Se ata a las rutas del plan y no al worktree entero a propósito: el propio
// flujo escribe plan.json y respuestas.json, y con un hash global cualquier
// artefacto de aprobación invalidaría el plan que aprueba. La garantía se
// mantiene, porque apply solo commitea rutas del plan y cada una se verifica;
// un archivo nuevo ajeno al plan no se commitea y por eso no lo invalida.
func HashEstadoWorktree(rutas []string) (string, error) {
	h := sha256.New()
	for _, ruta := range rutas {
		estado, err := ejecutarGitSalida("status", "--porcelain", "--", ruta)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s|%s|", ruta, strings.TrimSpace(estado))
		contenido, err := ejecutarGitSalida("hash-object", "--", ruta)
		if err != nil {
			// El archivo ya no existe: cuenta como ausente, que es un estado
			// distinto de cualquier contenido y por tanto invalida el plan.
			fmt.Fprint(h, "ausente\n")
			continue
		}
		fmt.Fprintf(h, "%s\n", strings.TrimSpace(contenido))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
