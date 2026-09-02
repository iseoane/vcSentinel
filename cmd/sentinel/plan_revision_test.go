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
// Both rows mark the same path linguist-generated and differ only in whether
// the added line is code. That is deliberate, and it is what the attribute
// alone cannot do: FU-14 records that behavior_change classifies through
// ClasificarPorRuta and never sees the attributes, so a generated path adding
// real code still reports it. Reading only the comment row invites the
// conclusion that the attribute silences the whole plan, which it does not.
//
// The comment row is therefore the one that proves the attribute arrived:
// security_sensitive is its only risk characteristic, and only the attribute
// can remove it. The code row pins FU-14's behaviour so that resolving it fails
// here rather than silently.
func TestPlanDeRevisionUsaLosAtributosLeidos(t *testing.T) {
	profile := change.ChangeProfile{Kind: "generated", Symbols: change.ChangeSymbols{Modified: 1, Complete: true}}
	paths := []string{"internal/api/wire.go"}
	cabecera := "diff --git a/internal/api/wire.go b/internal/api/wire.go\n" +
		"--- a/internal/api/wire.go\n" +
		"+++ b/internal/api/wire.go\n" +
		"@@ -1,0 +2,1 @@\n"
	generado := func() (string, error) { return "internal/api/wire.go linguist-generated\n", nil }
	sinAtributos := func() (string, error) { return "", nil }

	casos := []struct {
		nombre       string
		linea        string
		conAtributos int // bundles once the path is declared generated
		razon        string
	}{
		{
			nombre: "comment only", linea: "+\t// the caller rotates its accessToken here",
			conAtributos: 0,
			razon:        "security_sensitive is the only risk characteristic and the attribute removes it",
		},
		{
			nombre: "executable line", linea: "+\taccessToken := os.Getenv(\"SERVICE_TOKEN\")",
			conAtributos: 3,
			razon:        "behavior_change survives the attribute; see FU-14",
		},
	}
	for _, caso := range casos {
		t.Run(caso.nombre, func(t *testing.T) {
			diff := cabecera + caso.linea + "\n"

			base, err := planDeRevision(nil, profile, paths, diff, sinAtributos)
			if err != nil {
				t.Fatalf("planDeRevision: %v", err)
			}
			if len(base) == 0 {
				t.Fatalf("without attributes the content must schedule a plan; got none")
			}

			got, err := planDeRevision(nil, profile, paths, diff, generado)
			if err != nil {
				t.Fatalf("planDeRevision: %v", err)
			}
			if len(got) != caso.conAtributos {
				t.Errorf("declaring the path generated scheduled %d bundles, want %d (%s); against %d without attributes",
					len(got), caso.conAtributos, caso.razon, len(base))
			}
		})
	}
}
