package main

import (
	"errors"
	"slices"
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
	if len(bundles) != 1 {
		t.Fatalf("bundles = %+v, want exactly one", bundles)
	}
	if !slices.Equal(bundles[0].Dimensions, []string{"logic", "tests"}) {
		t.Errorf("dimensions = %v, want exactly the requested [logic tests]", bundles[0].Dimensions)
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

// TestPlanDeRevisionUsaLosAtributosLeidos closes the gap the other two leave: a
// regression that read the attributes and then discarded them would keep both
// of them green while the derivation classified from empty evidence.
//
// The observable is the scheduled plan itself. The added line is a comment, so
// it carries a security keyword and no executable change: without attributes
// the content makes the change high risk and schedules a full plan, and marking
// the path generated removes the only risk characteristic, leaving nothing to
// schedule. Only the attributes can move it.
func TestPlanDeRevisionUsaLosAtributosLeidos(t *testing.T) {
	profile := change.ChangeProfile{Kind: "generated", Symbols: change.ChangeSymbols{Modified: 1, Complete: true}}
	paths := []string{"internal/api/wire.go"}
	diff := "diff --git a/internal/api/wire.go b/internal/api/wire.go\n" +
		"--- a/internal/api/wire.go\n" +
		"+++ b/internal/api/wire.go\n" +
		"@@ -1,0 +2,1 @@\n" +
		"+\t// the caller rotates its accessToken here\n"

	sinAtributos, err := planDeRevision(nil, profile, paths, diff, func() (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("planDeRevision: %v", err)
	}
	if len(sinAtributos) == 0 {
		t.Fatalf("without attributes the content must still schedule a plan; got none")
	}

	conAtributos, err := planDeRevision(nil, profile, paths, diff, func() (string, error) {
		return "internal/api/wire.go linguist-generated\n", nil
	})
	if err != nil {
		t.Fatalf("planDeRevision: %v", err)
	}
	if len(conAtributos) != 0 {
		t.Errorf("marking the path generated still scheduled %d bundles; the attributes never reached the derivation", len(conAtributos))
	}
}
