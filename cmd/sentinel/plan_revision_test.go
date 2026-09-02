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
// The added line is a comment on purpose, so security_sensitive is the only
// risk characteristic the change has and the attribute is the only thing that
// can remove it. An executable line would also carry behavior_change, which
// never sees the attributes (FU-14) and would leave a plan scheduled for a
// reason this test is not about. That interaction is characterised where the
// characteristics themselves are observable, in
// TestPlanForProfileHonoursAttributesPerDetector.
func TestPlanDeRevisionUsaLosAtributosLeidos(t *testing.T) {
	profile := change.ChangeProfile{Kind: "generated", Symbols: change.ChangeSymbols{Modified: 1, Complete: true}}
	paths := []string{"internal/api/wire.go"}
	diff := "diff --git a/internal/api/wire.go b/internal/api/wire.go\n" +
		"--- a/internal/api/wire.go\n" +
		"+++ b/internal/api/wire.go\n" +
		"@@ -1,0 +2,1 @@\n" +
		"+\t// the caller rotates its accessToken here\n"

	base, err := planDeRevision(nil, profile, paths, diff, func() (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("planDeRevision: %v", err)
	}
	if len(base) == 0 {
		t.Fatalf("without attributes the content must schedule a plan; got none")
	}

	got, err := planDeRevision(nil, profile, paths, diff, func() (string, error) {
		return "internal/api/wire.go linguist-generated\n", nil
	})
	if err != nil {
		t.Fatalf("planDeRevision: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("declaring the path generated scheduled %d bundles against %d without attributes; the attributes never reached the derivation",
			len(got), len(base))
	}
}
