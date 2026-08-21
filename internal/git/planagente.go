package git

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	Numero    int              `json:"numero"`
	Capa      string           `json:"capa"`
	Rutas     []string         `json:"rutas"`
	Selectors []ChangeSelector `json:"selectors,omitempty"`
	Lineas    int              `json:"lineas"`
	Mensaje   string           `json:"mensaje"`
	EsGigante bool             `json:"es_gigante"`
}

// PlanSerializado es el plan completo emitido por `sentinel slice plan`. No
// commitea nada y es idempotente: sobre el mismo árbol produce el mismo
// PlanID y el mismo EstadoWorktree.
type PlanSerializado struct {
	PlanID               string              `json:"plan_id"`
	EstadoWorktree       string              `json:"estado_worktree"`
	Lotes                []LoteSerializado   `json:"lotes"`
	Changes              []PlannedChange     `json:"changes,omitempty"`
	DecisionesPendientes []DecisionPendiente `json:"decisiones_pendientes"`
	Explanation          string              `json:"explanation,omitempty"`
}

// registradorDecisiones implementa la segunda vía del callback de decisión de
// ConstruirPlanFragmentacion: en vez de preguntar por stdin, anota la pregunta
// y deja seguir la construcción para poder emitir el plan completo. Devolver
// true aquí NO aprueba nada: el lote queda marcado como gigante y `slice
// apply` se negará mientras la decisión no tenga respuesta explícita.
type registradorDecisiones struct {
	pendientes []DecisionPendiente
}

func (r *registradorDecisiones) semanticCallback(unit SemanticOversizedUnit) (bool, error) {
	r.pendientes = append(r.pendientes, DecisionPendiente{
		ID:      unit.ID,
		Archivo: strings.Join(unit.Paths, ", "),
		Lineas:  unit.AddedLines,
		Pregunta: fmt.Sprintf(
			"The semantic unit %s has %d authored additions and cannot be safely subdivided by exact diff atoms. Bypass it as one reviewable slice or abort?",
			strings.Join(unit.Paths, ", "), unit.AddedLines),
		Opciones: []string{RespuestaBypass, RespuestaAbortar},
	})
	return true, nil
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
	return ConstruirPlanParaAgenteConOpciones(adapter, SemanticSliceOptions{})
}

// ConstruirPlanParaAgenteConOpciones exposes validated ticket boundaries and
// optional proposal input without changing the non-committing contract.
func ConstruirPlanParaAgenteConOpciones(adapter generadorMensajesCommit, options SemanticSliceOptions) (*PlanSerializado, error) {
	changes, err := CaptureDraftChanges()
	if err != nil {
		return nil, err
	}
	registrador := &registradorDecisiones{}
	if options.ConfirmOversized == nil {
		options.ConfirmOversized = registrador.semanticCallback
	}
	plan, err := BuildSemanticSlicePlan(changes, options)
	if err != nil {
		return nil, err
	}
	if adapter != nil {
		GenerarMensajesLotes(plan, adapter)
	}
	plan.Changes = changes
	serializado := SerializarPlan(plan, registrador.pendientes, "")
	serializado.EstadoWorktree = hashPlannedChangesState(changes)
	if err := RecalculatePlanID(serializado); err != nil {
		return nil, err
	}
	return serializado, nil
}

func archivosParaPlan(changes []PlannedChange) []ArchivoModificado {
	archivos := make([]ArchivoModificado, 0, len(changes))
	for _, change := range changes {
		archivos = append(archivos, ArchivoModificado{
			Ruta:   change.Path,
			Lineas: change.AddedLines,
			Capa:   ClasificarCapa(change.Path),
		})
	}
	return archivos
}

func asignarSelectoresDeArchivo(plan *PlanFragmentacion) {
	for i := range plan.Lotes {
		lote := &plan.Lotes[i]
		if len(lote.Selectors) > 0 {
			continue
		}
		for _, ruta := range lote.Rutas {
			selector := ChangeSelector{Path: ruta, Mode: SelectorWholeFile}
			for _, change := range plan.Changes {
				if change.Path == ruta {
					selector.Path = change.Path
					selector.OldPath = change.OldPath
					break
				}
			}
			lote.Selectors = append(lote.Selectors, selector)
		}
	}
}

// RutasDelPlan devuelve, ordenadas, todas las rutas que el plan commitearía.
func RutasDelPlan(plan *PlanSerializado) []string {
	var rutas []string
	for _, lote := range plan.Lotes {
		if len(lote.Selectors) == 0 {
			rutas = append(rutas, lote.Rutas...)
			continue
		}
		for _, selector := range lote.Selectors {
			rutas = append(rutas, selector.Path)
			if selector.OldPath != "" {
				rutas = append(rutas, selector.OldPath)
			}
		}
	}
	return sortedUnique(rutas)
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
			Rutas:     append([]string(nil), lote.Rutas...),
			Selectors: cloneSelectors(lote.Selectors),
			Lineas:    lote.LineasTotales,
			Mensaje:   mensaje,
			EsGigante: lote.EsGigante,
		})
	}
	serializado := &PlanSerializado{
		EstadoWorktree:       estado,
		Lotes:                lotes,
		Changes:              cloneChanges(plan.Changes),
		DecisionesPendientes: append([]DecisionPendiente(nil), pendientes...),
		Explanation:          plan.Explanation,
	}
	asignarSelectoresSerializados(serializado)
	if pendientes == nil {
		serializado.DecisionesPendientes = []DecisionPendiente{}
	}
	serializado.PlanID = calcularPlanIDPlan(serializado)
	return serializado
}

func asignarSelectoresSerializados(plan *PlanSerializado) {
	changes := make(map[string]PlannedChange, len(plan.Changes))
	for _, change := range plan.Changes {
		changes[changeKey(change.Path, change.OldPath)] = change
	}
	for i := range plan.Lotes {
		lote := &plan.Lotes[i]
		if len(lote.Selectors) > 0 {
			continue
		}
		for _, ruta := range lote.Rutas {
			selector := ChangeSelector{Path: ruta, Mode: SelectorWholeFile}
			if change, ok := changes[changeKey(ruta, "")]; ok {
				selector.Path = change.Path
				selector.OldPath = change.OldPath
			} else {
				for _, change := range plan.Changes {
					if change.Path == ruta {
						selector.OldPath = change.OldPath
						break
					}
				}
			}
			lote.Selectors = append(lote.Selectors, selector)
		}
	}
}

// calcularPlanID resume la estructura de los lotes: capa, rutas y tamaño. No
// incluye el mensaje, que puede regenerarse sin cambiar qué se commitea.
func calcularPlanID(lotes []LoteSerializado) string {
	return calcularPlanIDPlan(&PlanSerializado{Lotes: lotes})
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
	identity := make([]map[string]string, 0, len(rutas))
	for _, ruta := range sortedUnique(rutas) {
		estado, err := ejecutarGitSalida("status", "--porcelain", "-z", "-uall", "--", literalPathspec(ruta))
		if err != nil {
			return "", err
		}
		head, err := hashHeadPath(ruta)
		if err != nil {
			return "", err
		}
		index, err := hashIndexPath(ruta)
		if err != nil {
			return "", err
		}
		worktree, err := hashWorktreePath(ruta)
		if err != nil {
			return "", err
		}
		identity = append(identity, map[string]string{
			"path":     ruta,
			"status":   strings.TrimSpace(estado),
			"head":     head,
			"index":    index,
			"worktree": worktree,
		})
	}
	encoded := mustMarshal(identity)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
