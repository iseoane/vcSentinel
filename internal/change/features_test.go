package change

import "testing"

func checkDetector(t *testing.T, detector func(FeaturesInput) Feature, positive, negative FeaturesInput) {
	t.Helper()
	if got := detector(positive).State; got != FeaturePresent {
		t.Errorf("positive case = %q, want %q", got, FeaturePresent)
	}
	if got := detector(negative).State; got != FeatureAbsent {
		t.Errorf("negative case = %q, want %q", got, FeatureAbsent)
	}
}

func TestDetectorPublicAPI(t *testing.T) {
	positive := FeaturesInput{Symbols: ChangeSymbols{ExportedTouched: 1, Complete: true}}
	negative := FeaturesInput{Symbols: ChangeSymbols{Modified: 1, Complete: true}}
	checkDetector(t, detectPublicAPI, positive, negative)
	if detectPublicAPI(positive).Heuristic {
		t.Error("exact public_api must not declare heuristic=true")
	}
	if got := detectPublicAPI(FeaturesInput{}).State; got != FeatureIndeterminate {
		t.Errorf("public_api without complete AST = %q, want indeterminate", got)
	}
}

func TestDetectorDatabase(t *testing.T) {
	checkDetector(t, detectDatabase,
		FeaturesInput{Paths: []string{"db/migrations/001.sql"}},
		FeaturesInput{Paths: []string{"internal/app/app.go"}})
}

func TestDetectorSecuritySensitive(t *testing.T) {
	checkDetector(t, detectSecuritySensitive,
		FeaturesInput{AddedLines: map[string][]string{"app.go": {"token := newToken()"}}},
		FeaturesInput{AddedLines: map[string][]string{"app.go": {"name := user.Name"}}})
}

func TestDetectorConcurrency(t *testing.T) {
	checkDetector(t, detectConcurrency,
		FeaturesInput{AddedLines: map[string][]string{"worker.go": {"go run()"}}},
		FeaturesInput{AddedLines: map[string][]string{"worker.go": {"run()"}}})
}

func TestDetectorBehaviorChange(t *testing.T) {
	positive := FeaturesInput{Paths: []string{"internal/app/app.go"}, AddedLines: map[string][]string{"internal/app/app.go": {"return 2"}}}
	checkDetector(t, detectBehaviorChange, positive,
		FeaturesInput{Paths: []string{"internal/app/app.go"}, AddedLines: map[string][]string{"internal/app/app.go": {"// documentation only"}}})
	if got := detectBehaviorChange(FeaturesInput{Paths: []string{"internal/app/app_test.go"}, AddedLines: map[string][]string{"internal/app/app_test.go": {"t.Fatal()"}}}).State; got != FeatureAbsent {
		t.Errorf("change only in test = %q, want absent", got)
	}
}

func TestDetectorTestCovered(t *testing.T) {
	base := FeaturesInput{Paths: []string{"internal/app/app.go"}}
	positive := base
	positive.TestMap = map[string]bool{"internal/app": true}
	negative := base
	negative.TestMap = map[string]bool{"internal/app": false}
	checkDetector(t, detectTestCoverage, positive, negative)
	if got := detectTestCoverage(base).State; got != FeatureIndeterminate {
		t.Errorf("without test map = %q, want %q", got, FeatureIndeterminate)
	}
	if got := detectTestCoverage(FeaturesInput{Paths: []string{"internal/app/app.go", "internal/app/app_test.go"}}).State; got != FeaturePresent {
		t.Errorf("test in the diff = %q, want present", got)
	}
}

func TestDetectorCrossModule(t *testing.T) {
	checkDetector(t, detectCrossModule,
		FeaturesInput{Paths: []string{"internal/app/app.go", "cmd/sentinel/main.go"}},
		FeaturesInput{Paths: []string{"internal/app/app.go", "internal/app/app_test.go"}})
}

func TestDetectorGeneratedCode(t *testing.T) {
	checkDetector(t, detectGeneratedCode,
		FeaturesInput{Paths: []string{"gen/model.go"}, Gitattributes: "gen/** linguist-generated\n"},
		FeaturesInput{Paths: []string{"internal/app/app.go"}})
}

func TestDetectorCICD(t *testing.T) {
	checkDetector(t, detectCICD,
		FeaturesInput{Paths: []string{".github/workflows/ci.yml"}},
		FeaturesInput{Paths: []string{"config/app.yml"}})
}

func TestDetectorInfrastructure(t *testing.T) {
	checkDetector(t, detectInfrastructure,
		FeaturesInput{Paths: []string{"infra/main.tf"}},
		FeaturesInput{Paths: []string{"internal/app/app.go"}})
}

// TestDetectorConcurrencyDoesNotConfuseSpanishWords covers the real false
// positive of the T3.3 review: "go " as a substring without word boundary
// matched common Spanish words.
func TestDetectorConcurrencyDoesNotConfuseSpanishWords(t *testing.T) {
	cases := []string{"algo cambia aqui", "tengo una duda", "esto es muy largo", "tal vez luego"}
	for _, line := range cases {
		input := FeaturesInput{AddedLines: map[string][]string{"app.go": {line}}}
		if got := detectConcurrency(input).State; got != FeatureAbsent {
			t.Errorf("line %q = %q, want absent (false positive of \"go \")", line, got)
		}
	}
	if got := detectConcurrency(FeaturesInput{AddedLines: map[string][]string{"app.go": {"go run()"}}}).State; got != FeaturePresent {
		t.Errorf("\"go run()\" = %q, want present", got)
	}
	// Same risk as "go ", flagged in the T3.3 review ADVISORY for the rest of
	// the marks: "context." inside a longer identifier must not raise a false
	// positive.
	withoutBoundary := FeaturesInput{AddedLines: map[string][]string{"app.go": {"miscontext.Value = 1"}}}
	if got := detectConcurrency(withoutBoundary).State; got != FeatureAbsent {
		t.Errorf("\"miscontext.\" = %q, want absent (false positive of \"context.\")", got)
	}
	withBoundary := FeaturesInput{AddedLines: map[string][]string{"app.go": {"context.Background()"}}}
	if got := detectConcurrency(withBoundary).State; got != FeaturePresent {
		t.Errorf("\"context.Background()\" = %q, want present", got)
	}
}

// TestDetectorSecurityAndConcurrencyDeclareHeuristic: they match by
// substring, just like public_api, so they must declare themselves equally
// heuristic (T3.3 review).
func TestDetectorSecurityAndConcurrencyDeclareHeuristic(t *testing.T) {
	security := detectSecuritySensitive(FeaturesInput{AddedLines: map[string][]string{"app.go": {"token := 1"}}})
	if !security.Heuristic {
		t.Error("security_sensitive must declare heuristic=true")
	}
	concurrency := detectConcurrency(FeaturesInput{AddedLines: map[string][]string{"app.go": {"go run()"}}})
	if !concurrency.Heuristic {
		t.Error("concurrency must declare heuristic=true")
	}
}

// TestDetectorsUseInjectedRules covers the T3.3 review: before this fix,
// detectCICD (via containsClass) ignored FeaturesInput and always called
// DefaultRules(), so a user rule could never change the classification.
//
// It covers detectCICD and detectInfrastructure, the callers of
// containsClass. Since the helper receives paths and rules instead of the
// whole input, each one resolves its own, and a regression in one stopped
// being covered by the other. A third caller appearing without a case here
// would reopen that asymmetry; nothing detects it automatically.
//
// detectDatabase uses a similar fixture shape but does not go through here:
// it classifies by suffix and by e.DataPatterns, without rulesOf.
func TestDetectorsUseInjectedRules(t *testing.T) {
	// Subtests, not two sequential calls: the guard uses t.Fatalf, which cuts
	// the whole test function. Chained, a stale fixture in ci_cd would leave
	// infrastructure without running and the report would say only one failed
	// — the same asymmetry this test exists to close, one level up.
	for _, c := range []struct {
		name     string
		detector func(FeaturesInput) Feature
		path     string
		rules    []Rule
	}{
		{"ci_cd", detectCICD, "pipelines/build.yaml", []Rule{{Class: ClassCI, Patterns: []string{"pipelines/**"}}}},
		{"infrastructure", detectInfrastructure, "deploy/stack.yaml", []Rule{{Class: ClassInfra, Patterns: []string{"deploy/**"}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			checkInjection(t, c.detector, c.name, c.path, c.rules)
		})
	}
}

// checkInjection verifies that a detector honors the injected rules, and not
// just that the fixture path does not coincidentally match any default.
//
// The guard comes BEFORE the positive case. If the default rules grew to
// cover the path, the positive would pass without the injection doing
// anything and the negative would fail saying "want absent" instead of
// pointing at the stale fixture. It is the same fixture-guard convention the
// attribute tests of this file use, placed here so both halves share it:
// applying it to only one left the asymmetry this test closed open.
func checkInjection(t *testing.T, detector func(FeaturesInput) Feature, name, path string, rules []Rule) {
	t.Helper()
	if got := detector(FeaturesInput{Paths: []string{path}}).State; got != FeatureAbsent {
		t.Fatalf("%s: the defaults already classify %s (%q); the fixture no longer distinguishes rule injection", name, path, got)
	}
	if got := detector(FeaturesInput{Paths: []string{path}, Rules: rules}).State; got != FeaturePresent {
		t.Errorf("%s with injected rule = %q, want %q", name, got, FeaturePresent)
	}
}

func TestDetectFeaturesIncludesAllDetectors(t *testing.T) {
	got := DetectFeatures(FeaturesInput{})
	if len(got) != 10 {
		t.Fatalf("features = %d, want 10", len(got))
	}
}

// TestSecuritySensitiveIgnoresProseAndGeneratedContent pins the false positive
// FU-10 demonstrates. The detector scanned the added lines of every path, so a
// metrics artifact under docs/ holding "cached_input_tokens" raised the
// characteristic and, through it, a high risk level for a documentation-only
// commit. Documentation is prose and generated files are output, so neither is
// evidence of credential handling. Config and infrastructure paths are not
// excluded: they genuinely can hold credentials, so the narrowing is a
// deny-list rather than an allow-list of source alone.
func TestSecuritySensitiveIgnoresProseAndGeneratedContent(t *testing.T) {
	cases := []struct {
		name string
		path string
		line string
		want FeatureStatus
	}{
		{"documentation artifact", "docs/reengineering/evidence/t9-4a-metrics.json", `  "cached_input_tokens": 12,`, FeatureAbsent},
		{"markdown prose", "docs/guide.md", "the reviewer receives an auth token", FeatureAbsent},
		{"generated file", "internal/api/service.pb.go", "type TokenRequest struct {", FeatureAbsent},
		{"source identifier", "internal/session/session.go", "\taccessToken := os.Getenv(\"X\")", FeaturePresent},
		{"config key", "config/app.json", `  "password": "changeme",`, FeaturePresent},
	}
	for _, c := range cases {
		input := FeaturesInput{
			Paths:      []string{c.path},
			AddedLines: map[string][]string{c.path: {c.line}},
		}
		if got := detectSecuritySensitive(input).State; got != c.want {
			t.Errorf("%s (%s) = %q, want %q", c.name, c.path, got, c.want)
		}
	}
}

// TestSecuritySensitivePathPatternsSurviveTheClassFilter keeps the two inputs
// independent: a declared sensitive path marks the characteristic present
// whatever its class and whatever its content says.
func TestSecuritySensitivePathPatternsSurviveTheClassFilter(t *testing.T) {
	input := FeaturesInput{
		Paths:             []string{"docs/auth/notes.md"},
		AddedLines:        map[string][]string{"docs/auth/notes.md": {"nothing interesting here"}},
		SensitivePatterns: []string{"**/auth/**"},
	}
	if got := detectSecuritySensitive(input).State; got != FeaturePresent {
		t.Errorf("declared sensitive path = %q, want %q", got, FeaturePresent)
	}
}

// TestConcurrencyIgnoresProseAndGeneratedContent applies to the concurrency
// detector the filter ticket 02 gave the security one. The divergence
// measurement found the gap: 17 of 52 prose-only commits were unlocked by
// concurrency alone, because this repository's reengineering documents discuss
// Go concurrency constantly and every document mentioning context. read as a
// concurrent change. The word-boundary guard already on these marks is a
// different fix for a different problem: it stops miscontext. matching inside a
// longer identifier, and does nothing about a sentence in a Markdown file.
func TestConcurrencyIgnoresProseAndGeneratedContent(t *testing.T) {
	cases := []struct {
		name string
		path string
		line string
		want FeatureStatus
	}{
		{"markdown prose", "docs/reengineering/f9-observability.md", "the producer reads context.Background() on every attempt", FeatureAbsent},
		{"generated file", "internal/api/service.pb.go", "\tresults chan *Reply", FeatureAbsent},
		{"source goroutine", "internal/worker/worker.go", "\tgo run()", FeaturePresent},
		{"source context", "internal/worker/worker.go", "\tctx := context.Background()", FeaturePresent},
		{"config key", "deploy/values.yaml", "  channel: sync.enabled", FeaturePresent},
	}
	for _, c := range cases {
		input := FeaturesInput{
			Paths:      []string{c.path},
			AddedLines: map[string][]string{c.path: {c.line}},
		}
		if got := detectConcurrency(input).State; got != c.want {
			t.Errorf("%s (%s) = %q, want %q", c.name, c.path, got, c.want)
		}
	}
}

// TestAttributesDoNotSwitchOffCIDetection pins the one exception to FU-14's
// rule, and it is a security boundary rather than a style choice.
//
// Every detector that reasons about the code honours `linguist-generated`,
// because a repository declaring a tree generated is stating something true
// about its own source. ci_cd and infrastructure do not, because they ask which
// surface a change touches rather than whether it is source: a workflow is a
// workflow even when a tool wrote it.
//
// Asserted through detectCICD, not through the containsClass helper it calls.
// The helper is an implementation detail, and a rewrite that classified inside
// the detector would keep a helper-level test green while reopening the hole.
//
// Measured before the exception existed: marking the workflow generated turned
// ci_cd from present to absent, which handed the audited repository a switch for
// the detection of its own CI.
func TestAttributesDoNotSwitchOffCIDetection(t *testing.T) {
	const workflow = ".github/workflows/deploy.yml"
	e := FeaturesInput{Paths: []string{workflow}}
	if detectCICD(e).State != FeaturePresent {
		t.Fatalf("%s is not classified as CI without attributes; the fixture no longer exercises the case", workflow)
	}

	e.Gitattributes = workflow + " linguist-generated\n"
	if state := detectCICD(e).State; state != FeaturePresent {
		t.Errorf("ci_cd = %q once the repository marked %s linguist-generated, want %q; a repository must not be able to switch off the detection of its own CI surface",
			state, workflow, FeaturePresent)
	}
}

// TestAttributesDoNotSwitchOffInfrastructureDetection is the other half of the
// same exception. The comment at containsClass and the debt entry both justify
// it by "ci_cd e infrastructure", and only ci_cd was exercised: infrastructure
// reaches containsClass through the same call, so a change that broke one and
// not the other would have gone unnoticed.
func TestAttributesDoNotSwitchOffInfrastructureDetection(t *testing.T) {
	const terraform = "infra/main.tf"
	e := FeaturesInput{Paths: []string{terraform}}
	if detectInfrastructure(e).State != FeaturePresent {
		t.Fatalf("%s is not classified as infrastructure without attributes; the fixture no longer exercises the case", terraform)
	}

	e.Gitattributes = terraform + " linguist-generated\n"
	if state := detectInfrastructure(e).State; state != FeaturePresent {
		t.Errorf("infrastructure = %q once the repository marked %s linguist-generated, want %q; a repository must not be able to switch off the detection of its own infrastructure surface",
			state, terraform, FeaturePresent)
	}
}

// TestAttributesSwitchOffBehaviorChange holds the other side, so the
// exception above cannot quietly become the rule. A declared-generated source
// path must stop counting as behaviour change: that is FU-14 itself, and it is
// what stops every regeneration of such a tree from being elevated risk.
func TestAttributesSwitchOffBehaviorChange(t *testing.T) {
	const generated = "internal/api/wire.go"
	e := FeaturesInput{
		Paths:      []string{generated},
		AddedLines: map[string][]string{generated: {"\taccessToken := os.Getenv(\"SERVICE_TOKEN\")"}},
	}
	if detectBehaviorChange(e).State != FeaturePresent {
		t.Fatalf("%s is not a behaviour change without attributes; the fixture no longer exercises the case", generated)
	}

	e.Gitattributes = generated + " linguist-generated\n"
	if state := detectBehaviorChange(e).State; state != FeatureAbsent {
		t.Errorf("behavior_change = %q for a declared-generated path, want %q", state, FeatureAbsent)
	}
}
