package risk

import (
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
)

func TestEvaluar(t *testing.T) {
	casos := []struct {
		nombre  string
		kind    string
		estados map[string]change.EstadoCaracteristica
		want    Nivel
		regla   string
	}{
		{
			nombre: "generated sin caracteristicas de riesgo",
			kind:   "generated",
			want:   NivelNone,
			regla:  "kind=generated",
		},
		{
			nombre:  "security sensitive eleva un cambio pequeno",
			kind:    "feature",
			estados: map[string]change.EstadoCaracteristica{"security_sensitive": change.CaracteristicaPresente},
			want:    NivelHigh,
			regla:   "security_sensitive",
		},
		{
			nombre:  "database es high",
			kind:    "feature",
			estados: map[string]change.EstadoCaracteristica{"database": change.CaracteristicaPresente},
			want:    NivelHigh,
			regla:   "database",
		},
		{
			nombre: "test only es low",
			kind:   "test_only",
			want:   NivelLow,
			regla:  "kind=test_only",
		},
		{nombre: "refactor completo sin API es low", kind: "refactor", want: NivelLow, regla: "refactor"},
		{
			nombre:  "public api es elevated",
			kind:    "feature",
			estados: map[string]change.EstadoCaracteristica{"public_api": change.CaracteristicaPresente},
			want:    NivelElevated,
			regla:   "public_api",
		},
		{
			nombre:  "cross module es elevated",
			kind:    "feature",
			estados: map[string]change.EstadoCaracteristica{"cross_module": change.CaracteristicaPresente},
			want:    NivelElevated,
			regla:   "cross_module",
		},
		{
			nombre:  "concurrency es elevated",
			kind:    "feature",
			estados: map[string]change.EstadoCaracteristica{"concurrency": change.CaracteristicaPresente},
			want:    NivelElevated,
			regla:   "concurrency",
		},
		{
			nombre:  "behavior sin tests es elevated",
			kind:    "bugfix",
			estados: map[string]change.EstadoCaracteristica{"behavior_change": change.CaracteristicaPresente},
			want:    NivelElevated,
			regla:   "behavior_change sin test_covered",
		},
		{
			nombre: "behavior sin cobertura confirmada es elevated",
			kind:   "bugfix",
			estados: map[string]change.EstadoCaracteristica{
				"behavior_change": change.CaracteristicaPresente,
				"test_covered":    change.CaracteristicaIndeterminada,
			},
			want:  NivelElevated,
			regla: "behavior_change sin test_covered",
		},
		{
			nombre: "sin regla especial usa standard",
			kind:   "feature",
			want:   NivelStandard,
			regla:  "por defecto",
		},
		{nombre: "behavior con grafo incompleto es high", kind: "feature", estados: map[string]change.EstadoCaracteristica{"behavior_change": change.CaracteristicaPresente}, want: NivelHigh, regla: "grafo incompleto"},
		{
			nombre: "high gana al competir con elevated",
			kind:   "feature",
			estados: map[string]change.EstadoCaracteristica{
				"public_api":         change.CaracteristicaPresente,
				"security_sensitive": change.CaracteristicaPresente,
			},
			want:  NivelHigh,
			regla: "security_sensitive",
		},
		{
			nombre: "dependency se simplifica a high",
			kind:   "dependency",
			want:   NivelHigh,
			regla:  "kind=dependency",
		},
	}

	vistos := make(map[Nivel]bool)
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			perfil := change.ChangeProfile{Kind: caso.kind, Symbols: change.ChangeSymbols{Complete: !strings.Contains(caso.nombre, "incompleto")}}
			resultado := Evaluar(perfil, caracteristicasCon(caso.estados))
			if resultado.Nivel != caso.want {
				t.Fatalf("Nivel = %q; want %q", resultado.Nivel, caso.want)
			}
			if resultado.Explicacion == "" || !strings.Contains(resultado.Explicacion, caso.regla) {
				t.Fatalf("Explicacion = %q; debe identificar %q", resultado.Explicacion, caso.regla)
			}
			vistos[resultado.Nivel] = true
		})
	}

	for _, nivel := range []Nivel{NivelNone, NivelLow, NivelStandard, NivelElevated, NivelHigh} {
		if !vistos[nivel] {
			t.Errorf("el nivel %q no fue cubierto con explicacion", nivel)
		}
	}
}

// TestEstadoDeNoSubestimaConDuplicados cubre la revisión de T3.4: un
// "security_sensitive" ausente ANTES de uno presente en el slice no debe
// esconder el presente y hacer que el riesgo real (high) se subestime.
func TestEstadoDeNoSubestimaConDuplicados(t *testing.T) {
	duplicadas := []change.Caracteristica{
		{Nombre: "security_sensitive", Estado: change.CaracteristicaAusente},
		{Nombre: "security_sensitive", Estado: change.CaracteristicaPresente},
	}
	resultado := Evaluar(change.ChangeProfile{Kind: "feature"}, duplicadas)
	if resultado.Nivel != NivelHigh {
		t.Fatalf("Nivel con duplicados = %q; want %q (security_sensitive presente en cualquier posición)", resultado.Nivel, NivelHigh)
	}
}

// TestConstantesCaracteristicaCoincidenConChange detecta drift entre las
// constantes locales de nombre de característica y los Nombre reales que
// devuelve change.DetectarCaracteristicas: sin esta prueba, un cambio de
// literal en internal/change rompería este paquete en silencio, sin fallo de
// compilación (revisión de T3.4).
func TestConstantesCaracteristicaCoincidenConChange(t *testing.T) {
	nombresReales := map[string]bool{}
	for _, c := range change.DetectarCaracteristicas(change.EntradaCaracteristicas{}) {
		nombresReales[c.Nombre] = true
	}
	locales := []string{
		caracteristicaSecuridadSensible, caracteristicaBaseDeDatos, caracteristicaAPIPublica,
		caracteristicaCruceDeModulos, caracteristicaConcurrencia, caracteristicaCambioComport,
		caracteristicaCoberturaTests,
	}
	for _, nombre := range locales {
		if !nombresReales[nombre] {
			t.Errorf("la constante %q ya no coincide con ningún Nombre real de change.DetectarCaracteristicas: revisar drift con internal/change", nombre)
		}
	}
}

func caracteristicasCon(estados map[string]change.EstadoCaracteristica) []change.Caracteristica {
	nombres := []string{
		"public_api", "database", "security_sensitive", "concurrency", "behavior_change",
		"test_covered", "cross_module", "generated_code", "ci_cd", "infrastructure",
	}
	resultado := make([]change.Caracteristica, 0, len(nombres))
	for _, nombre := range nombres {
		estado := change.CaracteristicaAusente
		if indicado, ok := estados[nombre]; ok {
			estado = indicado
		}
		resultado = append(resultado, change.Caracteristica{Nombre: nombre, Estado: estado})
	}
	return resultado
}
