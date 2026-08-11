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

// Evaluar aplica un máximo sobre reglas; standard es solo el fallback cuando
// ninguna regla especial produce un candidato.
//
// Decisiones deliberadas de alcance para F3: como Symbols todavía siempre vale
// cero y no expresa completitud del grafo, se omiten tanto «refactor con grafo
// completo y sin API pública tocada» (low) como «grafo incompleto con
// behavior_change» (high); ambas alternativas quedan para F4. Al no analizar
// semver todavía, cualquier kind=dependency satisface provisionalmente la
// alternativa high de cambio de versión mayor. Por criterio conservador,
// test_covered indeterminate cuenta como cobertura no confirmada.
func Evaluar(perfil change.ChangeProfile, caracteristicas []change.Caracteristica) Resultado {
	var candidatos []Resultado

	if nombre := primeraPresente(caracteristicas, "security_sensitive", "database"); nombre != "" {
		candidatos = append(candidatos, Resultado{NivelHigh, "high por " + nombre + " presente"})
	}
	if perfil.Kind == "dependency" {
		candidatos = append(candidatos, Resultado{NivelHigh, "high por kind=dependency (simplificacion F3)"})
	}

	if nombre := primeraPresente(caracteristicas, "public_api", "cross_module", "concurrency"); nombre != "" {
		candidatos = append(candidatos, Resultado{NivelElevated, "elevated por " + nombre + " presente"})
	}
	if estadoDe(caracteristicas, "behavior_change") == change.CaracteristicaPresente &&
		estadoDe(caracteristicas, "test_covered") != change.CaracteristicaPresente {
		candidatos = append(candidatos, Resultado{NivelElevated, "elevated por behavior_change sin test_covered confirmado"})
	}

	if perfil.Kind == "test_only" {
		candidatos = append(candidatos, Resultado{NivelLow, "low por kind=test_only"})
	}

	if (perfil.Kind == "documentation" || perfil.Kind == "generated") &&
		primeraPresente(caracteristicas, caracteristicasDeRiesgo...) == "" {
		candidatos = append(candidatos, Resultado{NivelNone, "none por kind=" + perfil.Kind + " sin caracteristicas de riesgo"})
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

var caracteristicasDeRiesgo = []string{
	"public_api", "cross_module", "concurrency", "behavior_change",
	"security_sensitive", "database",
}

func primeraPresente(caracteristicas []change.Caracteristica, nombres ...string) string {
	for _, nombre := range nombres {
		if estadoDe(caracteristicas, nombre) == change.CaracteristicaPresente {
			return nombre
		}
	}
	return ""
}

func estadoDe(caracteristicas []change.Caracteristica, nombre string) change.EstadoCaracteristica {
	for _, caracteristica := range caracteristicas {
		if caracteristica.Nombre == nombre {
			return caracteristica.Estado
		}
	}
	return change.CaracteristicaIndeterminada
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
