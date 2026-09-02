package main

import (
	"errors"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
)

// TestPlanDeRevisionNoLeeAtributosConDimsExplicitas pins the exception the
// review of ce8316d found untested. Explicit --dims replaces the derived plan
// outright, so reading the evidence that derivation needs would abort a run
// whose bundles the caller already chose, for evidence nothing consumes.
func TestPlanDeRevisionNoLeeAtributosConDimsExplicitas(t *testing.T) {
	leido := false
	leer := func() (string, error) {
		leido = true
		return "", errors.New("must not be called")
	}

	bundles, err := planDeRevision([]string{"logic", "tests"}, change.ChangeProfile{}, nil, "", leer)
	if err != nil {
		t.Fatalf("explicit dims returned %v; the attribute read must be skipped entirely", err)
	}
	if leido {
		t.Error("the attributes were read even though --dims replaced the plan")
	}
	if len(bundles) != 1 || len(bundles[0].Dimensions) != 2 {
		t.Errorf("bundles = %+v, want one bundle carrying both requested dimensions", bundles)
	}
}

// TestPlanDeRevisionPropagaElFalloSinDims is the other half: with no explicit
// dimensions the derived plan is what schedules the audit, and its evidence
// decides whether the security bundle appears at all, so a read failure must
// surface rather than degrade to empty.
func TestPlanDeRevisionPropagaElFalloSinDims(t *testing.T) {
	leer := func() (string, error) { return "", errors.New("simulated read failure") }

	if _, err := planDeRevision(nil, change.ChangeProfile{}, nil, "", leer); err == nil {
		t.Error("a derived plan swallowed an attribute read failure; it must not classify from empty evidence")
	}
}
