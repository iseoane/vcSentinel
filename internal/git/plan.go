package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	internalchange "github.com/ISeoane-Quental/vas.sentinel/internal/change"
)

// LotePlanificado representa un lote propuesto dentro del plan de fragmentación.
// El mensaje puede nacer vacío (lotes normales) y completarse después con
// GenerarMensajesLotes, o nacer fijo (gigantes).
type LotePlanificado struct {
	Capa                string
	Numero              int
	Rutas               []string
	LineasTotales       int
	Mensaje             string
	MensajeAutomatico   string
	MensajeDeterminista bool
	EsGigante           bool
}

// PlanFragmentacion es la propuesta completa de fragmentación, construida sin
// crear ningún commit: los lotes y sus mensajes se aprueban antes de ejecutar.
type PlanFragmentacion struct {
	Lotes []LotePlanificado
}

// ResultadoCommit resume un commit creado durante la ejecución del plan.
type ResultadoCommit struct {
	Hash     string
	Mensaje  string
	Capa     string
	Archivos int
}

// ConstruirPlanFragmentacion usa la proximidad estructural de Cohesion. La vía
// productiva llama a ConstruirPlanFragmentacionConLector para sumar también el
// co-cambio histórico; este wrapper sin I/O mantiene los tests y consumidores
// que construyen planes a partir de una lista ya materializada.
func ConstruirPlanFragmentacion(archivos []ArchivoModificado, confirmarBypass func(ArchivoModificado) (bool, error)) (*PlanFragmentacion, error) {
	return ConstruirPlanFragmentacionConLector(archivos, confirmarBypass, func(...string) (string, error) { return "", nil })
}

// ConstruirPlanFragmentacionConLector agrupa primero por clúster de cohesión y
// ordena cada clúster por clase (config → source → test → docs → generated).
// Un clúster solo se parte cuando construirLotes alcanza el límite de 400.
func ConstruirPlanFragmentacionConLector(archivos []ArchivoModificado, confirmarBypass func(ArchivoModificado) (bool, error), lector internalchange.LectorGit) (*PlanFragmentacion, error) {
	porRuta := make(map[string]ArchivoModificado, len(archivos))
	rutas := make([]string, 0, len(archivos))
	lineasPorRuta := make(map[string]int, len(archivos))
	for _, f := range archivos {
		porRuta[filepath.ToSlash(f.Ruta)] = f
		rutas = append(rutas, f.Ruta)
		lineasPorRuta[f.Ruta] = f.Lineas
	}
	cohesion, err := internalchange.Cohesion(rutas, lector)
	if err != nil {
		return nil, err
	}
	grupos := make([][]ArchivoModificado, 0, len(cohesion.Grupos))
	for _, rutasGrupo := range cohesion.Grupos {
		grupo := make([]ArchivoModificado, 0, len(rutasGrupo))
		for _, ruta := range rutasGrupo {
			grupo = append(grupo, porRuta[ruta])
		}
		ordenarPorClase(grupo)
		grupos = append(grupos, grupo)
	}
	sort.SliceStable(grupos, func(i, j int) bool {
		claseI, claseJ := rangoClaseGrupo(grupos[i]), rangoClaseGrupo(grupos[j])
		if claseI != claseJ {
			return claseI < claseJ
		}
		return rangoCapaGrupo(grupos[i]) < rangoCapaGrupo(grupos[j])
	})

	var plan PlanFragmentacion
	numero := 1
	for _, grupo := range grupos {
		restantes := make([]ArchivoModificado, 0, len(grupo))
		agregarRestantes := func() {
			for _, archivosLote := range construirLotes(restantes) {
				rutasLote := make([]string, 0, len(archivosLote))
				for _, archivo := range archivosLote {
					rutasLote = append(rutasLote, archivo.Ruta)
				}
				plan.Lotes = append(plan.Lotes, loteNormal(loteConCapa{Capa: capaDelLote(archivosLote), Rutas: rutasLote}, numero, lineasPorRuta))
				numero++
			}
			restantes = restantes[:0]
		}
		for _, f := range grupo {
			switch {
			case esConfigGigante(f):
				agregarRestantes()
				plan.Lotes = append(plan.Lotes, loteGigante(f, mensajeAisladoDeps, numero))
				numero++
			case esDocumentacionExtensa(f):
				agregarRestantes()
				// Un documento largo se aísla como la configuración: nunca
				// entra por la rama de código masivo, que ofrecería dividirlo
				// con IA aplicando SRP.
				plan.Lotes = append(plan.Lotes, loteGigante(f, fmt.Sprintf(mensajeAisladoDocs, filepath.Base(f.Ruta)), numero))
				numero++
			case esCodigoGigante(f):
				agregarRestantes()
				ok, err := confirmarBypass(f)
				if err != nil {
					return nil, err
				}
				if !ok {
					return nil, fmt.Errorf("fragmentación abortada: %s tiene %d líneas y supera el límite de %d", f.Ruta, f.Lineas, LimiteCodigoGigante)
				}
				plan.Lotes = append(plan.Lotes, loteGigante(f, fmt.Sprintf(mensajeBypassGigante, filepath.Base(f.Ruta)), numero))
				numero++
			default:
				restantes = append(restantes, f)
			}
		}
		agregarRestantes()
	}
	return &plan, nil
}

func rangoCapaGrupo(grupo []ArchivoModificado) int {
	mejor := len(ordenCapas)
	for _, archivo := range grupo {
		for i, capa := range ordenCapas {
			if archivo.Capa == capa && i < mejor {
				mejor = i
			}
		}
	}
	return mejor
}

func ordenarPorClase(archivos []ArchivoModificado) {
	sort.SliceStable(archivos, func(i, j int) bool {
		return rangoClase(ClaseArchivo(archivos[i].Ruta)) < rangoClase(ClaseArchivo(archivos[j].Ruta))
	})
}

func rangoClaseGrupo(grupo []ArchivoModificado) int {
	mejor := len(ordenClases)
	for _, archivo := range grupo {
		if rango := rangoClase(ClaseArchivo(archivo.Ruta)); rango < mejor {
			mejor = rango
		}
	}
	return mejor
}

func rangoClase(clase string) int {
	for i, candidata := range ordenClases {
		if clase == candidata {
			return i
		}
	}
	return len(ordenClases)
}

func capaDelLote(archivos []ArchivoModificado) string {
	if len(archivos) == 0 {
		return "cohesion"
	}
	capa := archivos[0].Capa
	for _, archivo := range archivos[1:] {
		if archivo.Capa != capa {
			return "cohesion"
		}
	}
	return capa
}

// GenerarMensajesLotes consulta al adaptador el mensaje de cada lote no gigante
// y lo rellena en el plan. Si el adaptador falla para un lote, usa el mensaje
// automático de respaldo y lo marca como determinista. Devuelve la cantidad de
// lotes que cayeron al respaldo para que la UI ofrezca fallback.
func GenerarMensajesLotes(plan *PlanFragmentacion, adapter agentadapter.AgentAdapter) int {
	fallbacks := 0
	for i := range plan.Lotes {
		lote := &plan.Lotes[i]
		if lote.EsGigante {
			continue
		}
		mensaje, err := obtenerMensajeConDiff(lote.Rutas, lote.Capa, lote.Numero, adapter)
		if err != nil {
			lote.Mensaje = lote.MensajeAutomatico
			lote.MensajeDeterminista = true
			fallbacks++
			continue
		}
		lote.Mensaje = mensaje
		lote.MensajeDeterminista = false
	}
	return fallbacks
}

// AplicarMensajesAutomaticos reemplaza el mensaje de todos los lotes por su
// mensaje determinista: el de respaldo para los lotes normales y el propio del
// gigante para los aislados.
func AplicarMensajesAutomaticos(plan *PlanFragmentacion) {
	for i := range plan.Lotes {
		lote := &plan.Lotes[i]
		lote.Mensaje = lote.MensajeAutomatico
		lote.MensajeDeterminista = true
	}
}

// RegenerarMensajeLote regenera el mensaje de un único lote con el adaptador
// dado. Si el adaptador falla, deja el mensaje automático de respaldo.
func RegenerarMensajeLote(plan *PlanFragmentacion, numero int, adapter agentadapter.AgentAdapter) error {
	lote, err := lotePorNumero(plan, numero)
	if err != nil {
		return err
	}
	mensaje, err := obtenerMensajeConDiff(lote.Rutas, lote.Capa, lote.Numero, adapter)
	if err != nil {
		lote.Mensaje = lote.MensajeAutomatico
		lote.MensajeDeterminista = true
		return nil
	}
	lote.Mensaje = mensaje
	lote.MensajeDeterminista = false
	return nil
}

// AplicarMensajeAutomaticoLote restaura el mensaje determinista de un lote.
func AplicarMensajeAutomaticoLote(plan *PlanFragmentacion, numero int) error {
	lote, err := lotePorNumero(plan, numero)
	if err != nil {
		return err
	}
	lote.Mensaje = lote.MensajeAutomatico
	lote.MensajeDeterminista = true
	return nil
}

// EditarMensajeLote fija manualmente el mensaje de un lote.
func EditarMensajeLote(plan *PlanFragmentacion, numero int, mensaje string) error {
	lote, err := lotePorNumero(plan, numero)
	if err != nil {
		return err
	}
	lote.Mensaje = strings.TrimSpace(mensaje)
	lote.MensajeDeterminista = false
	return nil
}

// VerificarAdaptador prueba un adaptador con una petición sintética mínima y
// devuelve true si responde sin error. Permite detectar adaptadores no
// disponibles o rotos antes de generar los mensajes de todo el plan.
func VerificarAdaptador(adapter agentadapter.AgentAdapter) bool {
	_, err := obtenerMensajeConDiff([]string{"sonda.txt"}, "backend", 0, adapter)
	return err == nil
}

// EjecutarPlanFragmentacion commitea cada lote aprobado con su mensaje
// pre-aprobado, en el orden del plan, y devuelve un resumen por commit creado.
// Todos los commits omiten la verificación de hooks (--no-verify): invocar
// sentinel slice ES el desbloqueo del guardián, cada lote ya está validado
// (≤400 líneas salvo gigantes con bypass explícito) y el hook de volumen
// mediría también los cambios pendientes de los lotes siguientes, rechazando
// por error commits legítimos cuando el total pendiente supera las 400 líneas.
func EjecutarPlanFragmentacion(plan *PlanFragmentacion) ([]ResultadoCommit, error) {
	var resultados []ResultadoCommit
	for _, lote := range plan.Lotes {
		mensaje := lote.Mensaje
		if strings.TrimSpace(mensaje) == "" {
			mensaje = lote.MensajeAutomatico
		}
		hash, err := commitLoteConMensaje(lote.Rutas, mensaje)
		if err != nil {
			return resultados, err
		}
		resultados = append(resultados, ResultadoCommit{
			Hash:     hash,
			Mensaje:  mensaje,
			Capa:     lote.Capa,
			Archivos: len(lote.Rutas),
		})
	}
	return resultados, nil
}

// WorktreeLimpio indica si no quedan cambios pendientes en el worktree.
func WorktreeLimpio() (bool, error) {
	salida, err := ejecutarGitSalida("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(salida) == "", nil
}

func agruparPorCapas(archivos []ArchivoModificado) map[string][]ArchivoModificado {
	porCapas := map[string][]ArchivoModificado{"config": {}, "backend": {}, "frontend": {}, "test": {}}
	for _, f := range archivos {
		porCapas[f.Capa] = append(porCapas[f.Capa], f)
	}
	return porCapas
}

// ordenClases fija el orden de salida de los lotes por clase de archivo:
// config → source → test → docs → generated. Es el eje que agrupa antes que
// la capa, para que ningún lote mezcle clases (T0.12).
var ordenClases = []string{ClaseConfig, ClaseSource, ClaseTest, ClaseDocs, ClaseGenerada}

// agruparPorClases separa los archivos por ClaseArchivo, sin tocar la capa:
// son dos ejes distintos que ConstruirPlanFragmentacion combina en cascada.
func agruparPorClases(archivos []ArchivoModificado) map[string][]ArchivoModificado {
	porClases := make(map[string][]ArchivoModificado, len(ordenClases))
	for _, f := range archivos {
		clase := ClaseArchivo(f.Ruta)
		porClases[clase] = append(porClases[clase], f)
	}
	return porClases
}

func loteGigante(f ArchivoModificado, mensaje string, numero int) LotePlanificado {
	return LotePlanificado{
		Capa:                f.Capa,
		Numero:              numero,
		Rutas:               []string{f.Ruta},
		LineasTotales:       f.Lineas,
		Mensaje:             mensaje,
		MensajeAutomatico:   mensaje,
		MensajeDeterminista: true,
		EsGigante:           true,
	}
}

func loteNormal(lote loteConCapa, numero int, lineasPorRuta map[string]int) LotePlanificado {
	total := 0
	for _, ruta := range lote.Rutas {
		total += lineasPorRuta[ruta]
	}
	return LotePlanificado{
		Capa:              lote.Capa,
		Numero:            numero,
		Rutas:             lote.Rutas,
		LineasTotales:     total,
		MensajeAutomatico: fmt.Sprintf("chore(slice): auto-fragmented %s batch #%d", lote.Capa, numero),
	}
}

func lotePorNumero(plan *PlanFragmentacion, numero int) (*LotePlanificado, error) {
	for i := range plan.Lotes {
		if plan.Lotes[i].Numero == numero {
			return &plan.Lotes[i], nil
		}
	}
	return nil, fmt.Errorf("no existe el lote #%d en el plan", numero)
}

// commitLoteConMensaje añade las rutas y crea el commit con el mensaje
// aprobado. Omite los hooks (--no-verify) porque el flujo de slice ya validó
// el tamaño de cada lote y es el mecanismo de fragmentación del guardián.
//
// El add usa -f: las rutas de un lote siempre vienen de
// ObtenerArchivosModificados, que solo reporta archivos trackeados
// modificados o untracked NO ignorados, así que forzar el add nunca cuela un
// ignorado genuino. Lo que sí cubre es el caso real de un archivo que ya
// estaba trackeado cuando .gitignore empezó a afectarle después (p. ej.
// .atl/): sin -f, git add avisa y sale con código 1 aunque de todos modos deja
// el archivo en stage, y ese error abortaba el lote entero sin dejar rastro
// del motivo real (B13: antes de este cambio solo se veía "exit status 1").
func commitLoteConMensaje(rutas []string, mensaje string) (string, error) {
	argsAdd := append([]string{"add", "-f", "--"}, rutas...)
	if salida, err := exec.Command("git", argsAdd...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add falló: %w: %s", err, strings.TrimSpace(string(salida)))
	}
	if salida, err := exec.Command("git", "commit", "-m", mensaje, "--no-verify").CombinedOutput(); err != nil {
		return "", fmt.Errorf("git commit falló: %w: %s", err, strings.TrimSpace(string(salida)))
	}
	hash, err := ejecutarGitSalida("rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(hash), nil
}
