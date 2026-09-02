package main

import (
	"reflect"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/risk"
)

// TestContarKeepsDuplicateDimensions is the reason the cost unit is agent
// invocations. BundlesForRisk schedules DimSpec in both the correctness and the
// contracts bundle at high risk, and AuditarCommit dedupes by bundle name, not
// by dimension, so that dimension is audited twice. Collapsing the duplicate
// here would understate the cost of ticket 05 exactly where it lands.
func TestContarKeepsDuplicateDimensions(t *testing.T) {
	caracteristicas := []change.Caracteristica{{Nombre: "public_api", Estado: change.CaracteristicaPresente}}
	bundles := review.BundlesForRisk(risk.Resultado{Nivel: risk.NivelHigh}, caracteristicas)

	invocaciones, dimensiones := contar(bundles)
	if invocaciones != len(dimensiones) {
		t.Fatalf("invocations %d and dimensions %d must agree", invocaciones, len(dimensiones))
	}
	repetidas := map[string]int{}
	for _, d := range dimensiones {
		repetidas[d]++
	}
	if repetidas[review.DimSpec] < 2 {
		t.Errorf("spec scheduled %d times, want at least 2 (correctness plus contracts)", repetidas[review.DimSpec])
	}
	if invocaciones <= len(repetidas) {
		t.Errorf("invocations %d must exceed distinct dimensions %d at high risk", invocaciones, len(repetidas))
	}
}

// TestContarOnNoBundles pins the none-risk arm: no bundle means no invocation,
// which is the state every documentation commit is in today.
func TestContarOnNoBundles(t *testing.T) {
	invocaciones, dimensiones := contar(nil)
	if invocaciones != 0 || len(dimensiones) != 0 {
		t.Errorf("empty plan = (%d, %v), want (0, [])", invocaciones, dimensiones)
	}
}

func TestDesbloqueadasReportsOnlyNewlyPresent(t *testing.T) {
	planner := []change.Caracteristica{
		{Nombre: "security_sensitive", Estado: change.CaracteristicaAusente},
		{Nombre: "public_api", Estado: change.CaracteristicaPresente},
		{Nombre: "concurrency", Estado: change.CaracteristicaAusente},
	}
	compartidas := []change.Caracteristica{
		{Nombre: "security_sensitive", Estado: change.CaracteristicaPresente},
		{Nombre: "public_api", Estado: change.CaracteristicaPresente},
		{Nombre: "concurrency", Estado: change.CaracteristicaPresente},
	}
	got := desbloqueadas(planner, compartidas)
	want := []string{"concurrency", "security_sensitive"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unlocked = %v, want %v", got, want)
	}
	if got := desbloqueadas(compartidas, compartidas); len(got) != 0 {
		t.Errorf("identical inputs unlocked %v, want none", got)
	}
}

// TestContieneFuenteSeparatesTheStrata guards the headline split. profile.Kind
// is not usable here: a commit changing two Go files alongside several
// documents classifies as kind=documentation.
func TestContieneFuenteSeparatesTheStrata(t *testing.T) {
	casos := []struct {
		nombre string
		rutas  []string
		quiere bool
	}{
		{"go source among documents", []string{"docs/a.md", "internal/change/x.go", "README.md"}, true},
		{"documents only", []string{"docs/a.md", "docs/b.md"}, false},
		{"tests only", []string{"internal/change/x_test.go"}, false},
		{"config only", []string{"config/app.json", "go.mod"}, false},
		{"nothing changed", nil, false},
	}
	for _, caso := range casos {
		if got := contieneFuente(caso.rutas); got != caso.quiere {
			t.Errorf("%s = %t, want %t", caso.nombre, got, caso.quiere)
		}
	}
}

// TestSumarAccumulatesBothArms keeps the stratum totals honest: a stratum must
// carry both arms so the delta is readable per stratum, not only overall.
func TestSumarAccumulatesBothArms(t *testing.T) {
	var e estrato
	sumar(&e, medida{RiskChanged: true, PlannerInvocations: 3, SharedInvocations: 6})
	sumar(&e, medida{RiskChanged: false, PlannerInvocations: 0, SharedInvocations: 4})
	if e.Commits != 2 || e.RiskChanged != 1 || e.PlannerInvocations != 3 || e.SharedInvocations != 10 {
		t.Errorf("stratum = %+v, want commits 2, changed 1, 3 -> 10", e)
	}
}
