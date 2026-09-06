// Package risk evalúa el riesgo explicable de un perfil de cambio.
package risk

import "github.com/ISeoane-Quental/vas.sentinel/internal/change"

// Nivel es el vocabulario estable del contrato de riesgo.
type Nivel string

const (
	NivelNone     Nivel = "none"
	NivelLow      Nivel = "low"
	NivelStandard Nivel = "standard"
	NivelElevated Nivel = "elevated"
	NivelHigh     Nivel = "high"
)

// Resultado conserva el nivel y la regla que lo decidió.
type Resultado struct {
	Nivel       Nivel
	Explicacion string
}

// Nombres de características y kinds de ChangeProfile que este paquete
// consulta: constantes locales en vez de strings repetidos sueltos, para que
// un typo dentro de ESTE archivo falle en tiempo de compilación (revisión de
// T3.4). No viven en internal/change porque esa migración excede el alcance
// de este arreglo; deben coincidir con los literales que
// caracteristicas.go/perfil.go usan del otro lado.
//
// Las de característica (caracteristica*) tienen test de drift:
// TestConstantesCaracteristicaCoincidenConChange las compara contra el
// Nombre real que devuelve change.DetectarCaracteristicas. Las de kind
// (kind*) NO lo tienen — perfil.go no expone sus kinds como constantes
// exportadas ni una función que los enumere, así que ejercitarlas exigiría
// repos git sintéticos por cada kind (como hace perfil_test.go), fuera de
// proporción para una ADVISORY. Deuda anotada explícitamente para F4/F5, no
// descuido silencioso.
const (
	caracteristicaSecuridadSensible = "security_sensitive"
	caracteristicaBaseDeDatos       = "database"
	caracteristicaAPIPublica        = "public_api"
	caracteristicaCruceDeModulos    = "cross_module"
	caracteristicaConcurrencia      = "concurrency"
	caracteristicaCambioComport     = "behavior_change"
	caracteristicaCoberturaTests    = "test_covered"

	kindDependency    = "dependency"
	kindTestOnly      = "test_only"
	kindDocumentation = "documentation"
	kindGenerated     = "generated"
)

// caracteristicasDeRiesgo son las que descartan la regla de "none": su
// presencia contradice "ninguna característica de riesgo".
var caracteristicasDeRiesgo = []string{
	caracteristicaAPIPublica, caracteristicaCruceDeModulos, caracteristicaConcurrencia,
	caracteristicaCambioComport, caracteristicaSecuridadSensible, caracteristicaBaseDeDatos,
}

// regla es una alternativa de riesgo evaluable de forma aislada: añadir una
// regla nueva en F4 (grafo completo, semver) es agregar un elemento a
// reglasDeRiesgo, no editar el cuerpo de Evaluar (revisión de T3.4: el
// if-chain anterior rompía open/closed en cuanto llegara una regla más).
type regla struct {
	nivel   Nivel
	evaluar func(perfil change.ChangeProfile, caracteristicas []change.Caracteristica) (aplica bool, explicacion string)
}

// reglasDeRiesgo implementa, en cualquier orden, las alternativas de cada
// nivel del informe (docs/design/replanteamiento-objetivo.md, §9.2):
// Evaluar no asume prioridad por posición, calcula el máximo real al final.
//
// Symbols.Complete habilita las reglas F4 de completitud. El análisis semver de
// dependencias sigue fuera de alcance: kind=dependency conserva provisionalmente high.
var reglasDeRiesgo = []regla{
	{NivelHigh, func(perfil change.ChangeProfile, cs []change.Caracteristica) (bool, string) {
		return !perfil.Symbols.Complete && estadoDe(cs, caracteristicaCambioComport) == change.CaracteristicaPresente, "high por behavior_change con grafo incompleto"
	}},
	{NivelHigh, func(perfil change.ChangeProfile, cs []change.Caracteristica) (bool, string) {
		if nombre := primeraPresente(cs, caracteristicaSecuridadSensible, caracteristicaBaseDeDatos); nombre != "" {
			return true, "high por " + nombre + " presente"
		}
		return false, ""
	}},
	{NivelHigh, func(perfil change.ChangeProfile, cs []change.Caracteristica) (bool, string) {
		if perfil.Kind == kindDependency {
			return true, "high por kind=dependency (simplificacion F3)"
		}
		return false, ""
	}},
	{NivelElevated, func(perfil change.ChangeProfile, cs []change.Caracteristica) (bool, string) {
		if nombre := primeraPresente(cs, caracteristicaAPIPublica, caracteristicaCruceDeModulos, caracteristicaConcurrencia); nombre != "" {
			return true, "elevated por " + nombre + " presente"
		}
		return false, ""
	}},
	{NivelElevated, func(perfil change.ChangeProfile, cs []change.Caracteristica) (bool, string) {
		// Por criterio conservador, test_covered indeterminate cuenta como
		// cobertura no confirmada: si no está confirmada, no se resta riesgo.
		if estadoDe(cs, caracteristicaCambioComport) == change.CaracteristicaPresente &&
			estadoDe(cs, caracteristicaCoberturaTests) != change.CaracteristicaPresente {
			return true, "elevated por behavior_change sin test_covered confirmado"
		}
		return false, ""
	}},
	{NivelLow, func(perfil change.ChangeProfile, cs []change.Caracteristica) (bool, string) {
		if perfil.Kind == kindTestOnly {
			return true, "low por kind=test_only"
		}
		return false, ""
	}},
	{NivelLow, func(perfil change.ChangeProfile, cs []change.Caracteristica) (bool, string) {
		return perfil.Kind == "refactor" && perfil.Symbols.Complete && estadoDe(cs, caracteristicaAPIPublica) == change.CaracteristicaAusente, "low por refactor con grafo completo sin public_api"
	}},
	{NivelNone, func(perfil change.ChangeProfile, cs []change.Caracteristica) (bool, string) {
		if (perfil.Kind == kindDocumentation || perfil.Kind == kindGenerated) &&
			primeraPresente(cs, caracteristicasDeRiesgo...) == "" {
			return true, "none por kind=" + perfil.Kind + " sin caracteristicas de riesgo"
		}
		return false, ""
	}},
}

// Evaluar aplica un máximo sobre reglas; standard es solo el fallback cuando
// ninguna regla especial produce un candidato.
func Evaluar(perfil change.ChangeProfile, caracteristicas []change.Caracteristica) Resultado {
	var candidatos []Resultado
	for _, r := range reglasDeRiesgo {
		if aplica, explicacion := r.evaluar(perfil, caracteristicas); aplica {
			candidatos = append(candidatos, Resultado{r.nivel, explicacion})
		}
	}

	if len(candidatos) == 0 {
		return Resultado{NivelStandard, "standard por defecto: ninguna regla de none/low/elevated/high aplica"}
	}
	maximo := candidatos[0]
	for _, candidato := range candidatos[1:] {
		if rango(candidato.Nivel) > rango(maximo.Nivel) {
			maximo = candidato
		}
	}
	return maximo
}

func primeraPresente(caracteristicas []change.Caracteristica, nombres ...string) string {
	for _, nombre := range nombres {
		if estadoDe(caracteristicas, nombre) == change.CaracteristicaPresente {
			return nombre
		}
	}
	return ""
}

// estadoDe prioriza Presente si aparece en cualquier posición, en vez de
// devolver solo la primera coincidencia por nombre: si el llamador llegara a
// insertar entradas duplicadas para el mismo nombre, un duplicado Ausente
// antes que uno Presente ya no subestima el riesgo en silencio (revisión de
// T3.4).
func estadoDe(caracteristicas []change.Caracteristica, nombre string) change.EstadoCaracteristica {
	encontrada := false
	estado := change.CaracteristicaIndeterminada
	for _, caracteristica := range caracteristicas {
		if caracteristica.Nombre != nombre {
			continue
		}
		if caracteristica.Estado == change.CaracteristicaPresente {
			return change.CaracteristicaPresente
		}
		if !encontrada {
			estado = caracteristica.Estado
			encontrada = true
		}
	}
	return estado
}

func rango(nivel Nivel) int {
	switch nivel {
	case NivelNone:
		return 0
	case NivelLow:
		return 1
	case NivelStandard:
		return 2
	case NivelElevated:
		return 3
	case NivelHigh:
		return 4
	default:
		return -1
	}
}
