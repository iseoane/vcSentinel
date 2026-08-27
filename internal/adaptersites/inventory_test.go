package adaptersites

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// siteByPath indexes the curated inventory for cross-checks.
func siteByPath(t *testing.T) map[string]Site {
	t.Helper()
	byPath := make(map[string]Site)
	for _, site := range Sites() {
		if _, dup := byPath[site.Path]; dup {
			t.Fatalf("duplicate inventory entry for %s", site.Path)
		}
		byPath[site.Path] = site
	}
	return byPath
}

// readFile returns the repository file as text.
func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("inventory entry %s is unreadable: %v", rel, err)
	}
	return string(content)
}

// functionBody extracts the source of the first function whose declaration
// contains needle, from that declaration line through its matching closing
// brace. It is deliberately textual: brace counting over gofmt-clean files
// (no braces inside raw comments at declaration level) is deterministic and
// keeps this audit free of AST dependencies.
func functionBody(t *testing.T, text, needle string) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "func ") && strings.Contains(line, needle) {
			start = i
			break
		}
	}
	if start == -1 {
		t.Fatalf("function declaration containing %q not found", needle)
	}
	depth := 0
	opened := false
	for i := start; i < len(lines); i++ {
		for _, r := range lines[i] {
			switch r {
			case '{':
				depth++
				opened = true
			case '}':
				depth--
			}
		}
		if opened && depth == 0 {
			return strings.Join(lines[start:i+1], "\n")
		}
	}
	t.Fatalf("unbalanced braces in function containing %q", needle)
	return ""
}

// TestAdapterExecutionSitesInventory proves the curated enumeration matches
// reality: every declared entry exists and still pins its anchored site, and
// every scanned canary-bearing file is classified. A new execution or
// lifecycle-mutation site appearing anywhere in the module fails here until
// it is added to the inventory with an explicit class and reason.
func TestAdapterExecutionSitesInventory(t *testing.T) {
	root, err := ModuleRoot()
	if err != nil {
		t.Fatalf("module root not found: %v", err)
	}
	sites := Sites()
	if len(sites) < 40 {
		t.Fatalf("curated inventory shrank unexpectedly: %d entries", len(sites))
	}
	byPath := siteByPath(t)

	scanned, err := ScanMarkerFiles(root)
	if err != nil {
		t.Fatalf("canary scan failed: %v", err)
	}

	// Every scanned marker-bearing file must be declared.
	for path := range scanned {
		site, ok := byPath[path]
		if !ok {
			t.Errorf("UNCLASSIFIED execution/lifecycle site: %s carries markers %v; add it to Sites() with a class and reason", path, scanned[path])
			continue
		}
		declared := map[string]bool{}
		if site.Marker != "" {
			declared[site.Marker] = true
		}
		matchedAny := false
		for _, marker := range scanned[path] {
			if marker == site.Marker || declared[marker] || site.Marker == "" {
				matchedAny = true
			}
		}
		if !matchedAny && len(scanned[path]) > 0 {
			t.Errorf("stale inventory entry %s: pins marker %q but the file now only carries %v", path, site.Marker, scanned[path])
		}
	}

	// Every declared entry must exist on disk and pin a real line.
	for _, site := range sites {
		text := readFile(t, root, site.Path)
		if site.Marker == "" {
			continue // documentation-only row
		}
		if site.Anchor == "" {
			t.Errorf("inventory entry %s (%s) declares marker %q with no anchor: the entry cannot be pinned to the file", site.Path, site.Symbol, site.Marker)
			continue
		}
		if !strings.Contains(site.Anchor, site.Marker) {
			t.Errorf("inventory entry %s (%s): anchor %q does not contain its own marker %q; the entry pins the wrong text", site.Path, site.Symbol, site.Anchor, site.Marker)
			continue
		}
		if !strings.Contains(text, site.Anchor) {
			t.Errorf("inventory entry %s (%s): the file no longer contains the anchored site %q; re-read the site and refresh the anchor, class and reason", site.Path, site.Symbol, site.Anchor)
		}
	}
}

// TestStorePrimitiveCanariesCatchUndeclaredMutations proves the ticket 13
// hardening pool canary set (R10 M2): the store-primitive mutation tokens
// are part of the scan, a synthetic undeclared file carrying one of them is
// detected by ScanMarkerFiles (and therefore fails the completeness
// invariant of TestAdapterExecutionSitesInventory), and every real module
// file carrying those tokens today is declared as durable-controller.
func TestStorePrimitiveCanariesCatchUndeclaredMutations(t *testing.T) {
	primitives := []string{"CreateRun(", "AppendTerminalEvent(", "SaveAttemptOutcome("}
	for _, token := range primitives {
		found := false
		for _, marker := range canaryMarkers {
			if marker == token {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("store-primitive token %q is not a canary marker; direct durable-state mutations could appear unclassified", token)
		}
	}

	root := t.TempDir()
	undeclared := filepath.Join(root, "stray.go")
	if err := os.WriteFile(undeclared, []byte("package main\n\nfunc f(s interface{ CreateRun() error }) { s.CreateRun() }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	scanned, err := ScanMarkerFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if markers := scanned["stray.go"]; len(markers) != 1 || markers[0] != "CreateRun(" {
		t.Fatalf("ScanMarkerFiles on an undeclared file = %v, want exactly [CreateRun(]", markers)
	}

	moduleRoot, err := ModuleRoot()
	if err != nil {
		t.Fatalf("module root not found: %v", err)
	}
	byPath := siteByPath(t)
	moduleScan, err := ScanMarkerFiles(moduleRoot)
	if err != nil {
		t.Fatalf("canary scan failed: %v", err)
	}
	for path, markers := range moduleScan {
		carriesPrimitive := false
		for _, marker := range markers {
			for _, primitive := range primitives {
				if marker == primitive {
					carriesPrimitive = true
				}
			}
		}
		if !carriesPrimitive {
			continue
		}
		site, ok := byPath[path]
		if !ok {
			t.Errorf("UNDECLARED store-primitive site: %s carries %v; add it to Sites()", path, markers)
			continue
		}
		if site.Class != ClassDurable {
			t.Errorf("store-primitive site %s classified %q; mutating durable run state is exclusively durable-controller", path, site.Class)
		}
	}
}

// TestInScopeAdapterSitesCarryAdmittedEnvelope asserts envelope totality for
// every durable-controller provider-execution site: each one routes its
// provider call through the execution controller with an admitted
// agentrun.InvocationEnvelope present in the dispatch signature or admission
// flow.
func TestInScopeAdapterSitesCarryAdmittedEnvelope(t *testing.T) {
	root, err := ModuleRoot()
	if err != nil {
		t.Fatalf("module root not found: %v", err)
	}

	transport := readFile(t, root, "internal/reviewexec/durable_transport.go")
	runBody := functionBody(t, transport, ") run(reviewer any")
	for _, seam := range []string{
		"agentrun.NewRunRequest(", // request admitted...
		"controller.Start(",       // ...through the controller lifecycle authority
		"validateSnapshotBinding", // fail-fast binding before waiting
		"t.verifyEvidence(",       // output admitted only against durable evidence
	} {
		if !strings.Contains(runBody, seam) {
			t.Errorf("DurableTransport.run lost its admission seam %q", seam)
		}
	}

	reviewAdapter := readFile(t, root, "internal/reviewexec/reviewexec.go")
	execBody := functionBody(t, reviewAdapter, ") Execute(ctx context.Context, job agentrun.LogicalJob")
	if !strings.Contains(execBody, "InvocationEnvelope") {
		t.Errorf("ReviewAdapter.Execute must receive the admitted InvocationEnvelope in its dispatch signature")
	}

	runsCLI := readFile(t, root, "cmd/sentinel/comandos_runs.go")
	promptBody := functionBody(t, runsCLI, "(a promptRunAdapter) Execute(")
	if !strings.Contains(promptBody, "InvocationEnvelope") {
		t.Errorf("promptRunAdapter.Execute must receive the admitted InvocationEnvelope in its dispatch signature")
	}
	builderBody := functionBody(t, runsCLI, "func buildRunsController(")
	if !strings.Contains(builderBody, "execution.NewController(backing, promptAdapter)") {
		t.Errorf("buildRunsController must route promptRunAdapter exclusively through execution.NewController")
	}

	adapters := readFile(t, root, "internal/gate/gate_durable_adapters.go")
	for _, adapterFn := range []string{"(a rootRunAdapter) Execute(", "(a settledValidationAdapter) Execute("} {
		body := functionBody(t, adapters, adapterFn)
		if !strings.Contains(body, "InvocationEnvelope") {
			t.Errorf("%s must receive the admitted InvocationEnvelope in its dispatch signature", adapterFn)
		}
	}

	gateDurable := readFile(t, root, "internal/gate/gate_durable.go")
	if admissions := strings.Count(gateDurable, "controller.Start("); admissions != 2 {
		t.Errorf("gate durable orchestration must admit exactly through controller.Start (root + validation jobs), found %d call sites", admissions)
	}

	// The refuter influences verdicts (it can downgrade a confirmed CRITICAL
	// blocker), so with a transport present it must never call the reviewer
	// directly.
	engine := readFile(t, root, "internal/review/engine.go")
	refuteBody := functionBody(t, engine, "func refutarHallazgosCriticos(")
	if !strings.Contains(refuteBody, `transport("refutation"`) {
		t.Errorf("refutarHallazgosCriticos must route refutations through the admitted ReviewTransport when one is present")
	}
}

// TestNoCompatibilityGatedSitesRemain proves the ticket 13 (R11) cutover
// completion: the two release-bounded switches were removed, so ZERO
// compatibility-gated entries are permitted in the inventory and the config
// parser no longer declares any durable_runs yaml key.
func TestNoCompatibilityGatedSitesRemain(t *testing.T) {
	root, err := ModuleRoot()
	if err != nil {
		t.Fatalf("module root not found: %v", err)
	}

	lifecycleMarkers := map[string]bool{"NewController(": true, "AppendEvent(": true}
	controllerCore := map[string]bool{
		"internal/execution/controller.go":       true,
		"internal/execution/controller_retry.go": true,
		"internal/store/execution_events.go":     true,
		"internal/agentrun/contracts.go":         true,
	}
	for _, site := range Sites() {
		if site.Class == ClassGated {
			t.Errorf("compatibility-gated entry %s survived the R11 switch removal; every site must now be durably admitted or explicitly out-of-scope", site.Path)
		}
		if site.Marker != "" && lifecycleMarkers[site.Marker] && !controllerCore[site.Path] {
			if site.Class != ClassDurable {
				t.Errorf("lifecycle-mutating site %s must be class %q, got %q", site.Path, ClassDurable, site.Class)
			}
		}
	}

	parser := readFile(t, root, "internal/config/parser.go")
	if got := strings.Count(parser, `yaml:"durable_runs"`); got != 0 {
		t.Errorf("review.durable_runs and gate.durable_runs were removed in R11, found %d remaining yaml tags", got)
	}
}
