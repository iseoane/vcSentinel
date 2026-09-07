package change

import (
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// FeatureStatus avoids representing a signal that could not be computed with
// the available information as a plain false.
type FeatureStatus string

const (
	FeaturePresent       FeatureStatus = "present"
	FeatureAbsent        FeatureStatus = "absent"
	FeatureIndeterminate FeatureStatus = "indeterminate"
)

type Feature struct {
	Name      string        `json:"name"`
	State     FeatureStatus `json:"state"`
	Heuristic bool          `json:"heuristic,omitempty"`
}

// FeaturesInput holds signals already derived from the diff and the policy.
// The maps are indexed by Git paths normalized with "/".
type FeaturesInput struct {
	Symbols           ChangeSymbols
	Paths             []string
	AddedLines        map[string][]string
	Contents          map[string]string
	Gitattributes     string
	DataPatterns      []string
	SensitivePatterns []string
	TestMap           map[string]bool
	// Rules are the classification rules to use; if empty, the detectors fall
	// back to DefaultRules(). A single configuration point for all of them,
	// instead of some detectors accepting injected rules while others call the
	// global directly (T3.3 review).
	Rules []Rule
}

// concurrencyMarks use word boundaries in all four, not only in "go ": a
// loose substring ("context." inside a longer identifier, for instance) is the
// same false-positive risk already fixed for "go " (common in Spanish: "algo
// ", "tengo ", "luego ") (T3.3 review).
var concurrencyMarks = []*regexp.Regexp{
	regexp.MustCompile(`\bgo `),
	regexp.MustCompile(`\bsync\.`),
	regexp.MustCompile(`\bchan `),
	regexp.MustCompile(`\bcontext\.`),
}

// classesWithoutContentEvidence lists the path classes whose added text is not
// evidence of anything a detector should act on: documentation is prose and
// generated files are output. Before this filter detectSecuritySensitive and
// detectConcurrency read the lines of EVERY path, so a metrics artifact
// under docs/ holding "cached_input_tokens" marked security_sensitive, and any
// document discussing context.Background() marked concurrency. Both raised
// documentation commits above their real risk (FU-10, ticket 03b).
//
// It is a deny-list and not an allow-list on purpose: config and infra paths
// genuinely can hold credentials, and admitting only ClassSource would lose
// them. For a risk detector, excluding less is the conservative direction.
//
// A word boundary is not the fix here: \btoken\b rejects cached_input_tokens,
// but it also rejects accessToken and refreshTokens, which is how credential
// identifiers are actually spelled in Go.
var classesWithoutContentEvidence = map[string]bool{
	ClassDocs:      true,
	ClassGenerated: true,
}

// admitsContentEvidence reports whether the added lines of path may be
// read as evidence at all. One helper for every content-reading detector: two
// detectors excluding the same classes through two mechanisms is how the FU-10
// defect started.
func admitsContentEvidence(e FeaturesInput, rules []Rule, path string) bool {
	return !classesWithoutContentEvidence[Classify(path, rules, e.Gitattributes)]
}

// rulesOf returns input.Rules if they were injected, or the defaults if not:
// the same criterion in every detector that needs to classify paths.
func rulesOf(e FeaturesInput) []Rule {
	if len(e.Rules) > 0 {
		return e.Rules
	}
	return DefaultRules()
}

// DetectFeatures runs all the detectors in a stable order.
func DetectFeatures(input FeaturesInput) []Feature {
	return []Feature{
		detectPublicAPI(input), detectDatabase(input),
		detectSecuritySensitive(input), detectConcurrency(input),
		detectBehaviorChange(input), detectTestCoverage(input),
		detectCrossModule(input), detectGeneratedCode(input),
		detectCICD(input), detectInfrastructure(input),
	}
}
func detectPublicAPI(e FeaturesInput) Feature {
	if !e.Symbols.Complete {
		return Feature{"public_api", FeatureIndeterminate, false}
	}
	return result("public_api", e.Symbols.ExportedTouched > 0)
}
func detectDatabase(e FeaturesInput) Feature {
	present := pathsMatch(e.Paths, e.DataPatterns)
	for _, p := range e.Paths {
		p = strings.ToLower(filepath.ToSlash(p))
		present = present || strings.HasSuffix(p, ".sql") || strings.Contains("/"+p+"/", "/migrations/") || strings.Contains("/"+p+"/", "/migration/")
	}
	return result("database", present)
}
func detectSecuritySensitive(e FeaturesInput) Feature {
	keys := []string{"auth", "token", "crypto", "password", "secret"}
	rules := rulesOf(e)
	present := pathsMatch(e.Paths, e.SensitivePatterns) || anyLineOfPath(e.AddedLines, func(path, line string) bool {
		if !admitsContentEvidence(e, rules, path) {
			return false
		}
		line = strings.ToLower(line)
		for _, key := range keys {
			if strings.Contains(line, key) {
				return true
			}
		}
		return false
	})
	return heuristicResult("security_sensitive", present)
}
func detectConcurrency(e FeaturesInput) Feature {
	rules := rulesOf(e)
	present := anyLineOfPath(e.AddedLines, func(path, line string) bool {
		if !admitsContentEvidence(e, rules, path) {
			return false
		}
		for _, mark := range concurrencyMarks {
			if mark.MatchString(line) {
				return true
			}
		}
		return false
	})
	return heuristicResult("concurrency", present)
}

// Which classification function each detector uses is no longer an accident
// (FU-14). Before it was: a tree declared generated was generated for the
// content detectors and kept being source for behavior change, for test
// coverage and for ci_cd/infrastructure, so every regeneration marked
// `behavior_change` present, which is `elevated` at minimum. The two groups
// were not chosen, they diverged.
//
// The rule, decided and recorded: a detector that reasons about the NATURE of
// the code uses Classify and honors `linguist-generated`, because there the
// repository states something true about its own source. Those are
// admitsContentEvidence, detectGeneratedCode, detectBehaviorChange and
// detectTestCoverage.
//
// A detector that reasons about WHICH SURFACE the change touches uses
// ClassifyByPath and does not honor it. It is containsClass, and only
// containsClass, that feeds ci_cd and infrastructure: a workflow is a workflow
// even when a tool generates it, and honoring the attribute there would hand
// the audited repository a switch to turn off the detection of its own CI. The
// full rationale, with the measurement, lives at its call site.
//
// The criterion is NOT "decides how much review the change receives". That
// would prove too much: behavior_change also decides it and does honor the
// attribute. It is what question the detector answers.
func detectBehaviorChange(e FeaturesInput) Feature {
	rules := rulesOf(e)
	for _, p := range e.Paths {
		if Classify(p, rules, e.Gitattributes) != ClassSource {
			continue
		}
		for _, line := range linesOfPath(e.AddedLines, p) {
			if isCode(line) {
				return result("behavior_change", true)
			}
		}
	}
	return result("behavior_change", false)
}
func detectTestCoverage(e FeaturesInput) Feature {
	packages, tests := map[string]bool{}, map[string]bool{}
	rules := rulesOf(e)
	for _, p := range e.Paths {
		class := Classify(p, rules, e.Gitattributes)
		if class != ClassSource && class != ClassTest {
			continue
		}
		pkg := path.Dir(filepath.ToSlash(p))
		packages[pkg] = true
		if class == ClassTest {
			tests[pkg] = true
		}
	}
	if len(packages) == 0 {
		return result("test_covered", false)
	}
	indeterminate := false
	for pkg := range packages {
		if tests[pkg] {
			continue
		}
		covered, known := testInMap(e.TestMap, pkg)
		if !known {
			indeterminate = true
			continue
		}
		if !covered {
			return result("test_covered", false)
		}
	}
	if indeterminate {
		return Feature{Name: "test_covered", State: FeatureIndeterminate}
	}
	return result("test_covered", true)
}
func detectCrossModule(e FeaturesInput) Feature {
	return result("cross_module", len(modulesOfPaths(e.Paths)) >= 2)
}
func detectGeneratedCode(e FeaturesInput) Feature {
	rules := rulesOf(e)
	for _, p := range e.Paths {
		if Classify(p, rules, e.Gitattributes) == ClassGenerated {
			return result("generated_code", true)
		}
	}
	marker := func(content string) bool {
		content = strings.ToLower(content)
		return strings.Contains(content, "code generated") || strings.Contains(content, "@generated")
	}
	for _, content := range e.Contents {
		if marker(content) {
			return result("generated_code", true)
		}
	}
	return result("generated_code", anyLine(e.AddedLines, marker))
}
func detectCICD(e FeaturesInput) Feature {
	return result("ci_cd", containsClass(e.Paths, rulesOf(e), ClassCI))
}
func detectInfrastructure(e FeaturesInput) Feature {
	return result("infrastructure", containsClass(e.Paths, rulesOf(e), ClassInfra))
}
func result(name string, present bool) Feature {
	return Feature{Name: name, State: state(present)}
}

// heuristicResult is result() for detectors that match by
// substring/identifier instead of an exact signal (security_sensitive and
// concurrency): a single construction point for the heuristic case too,
// instead of each heuristic detector assembling its own Feature{...} by hand
// (T3.3 review).
func heuristicResult(name string, present bool) Feature {
	f := result(name, present)
	f.Heuristic = true
	return f
}
func state(present bool) FeatureStatus {
	if present {
		return FeaturePresent
	}
	return FeatureAbsent
}

// containsClass classifies BY PATH on purpose, without honoring the
// attributes, and is the only exception to FU-14's rule. Its two consumers,
// detectCICD and detectInfrastructure, do not ask "is this source code whose
// behavior matters?" but "does this change touch that surface?", and a
// workflow is a workflow even when a tool generates it.
//
// Measured: with `.github/workflows/deploy.yml linguist-generated`, honoring
// the attribute made ci_cd go from present to absent. That turns a
// presentation mark into a switch with which the audited repository turns off
// the detection of its own CI and infrastructure.
//
// It receives the paths and the rules, NOT the whole FeaturesInput. The
// exception stops being a promise in a comment this way: without `e` at hand,
// this function does not hold the attributes it must not look at, and
// honoring them would require changing the signature on purpose instead of
// adding a third argument without thinking.
func containsClass(paths []string, rules []Rule, class string) bool {
	for _, p := range paths {
		if ClassifyByPath(p, rules) == class {
			return true
		}
	}
	return false
}
func pathsMatch(paths, patterns []string) bool {
	for _, p := range paths {
		for _, pattern := range patterns {
			if matchesGlob(pattern, filepath.ToSlash(p)) {
				return true
			}
		}
	}
	return false
}
func anyLine(lines map[string][]string, matches func(string) bool) bool {
	return anyLineOfPath(lines, func(_, line string) bool { return matches(line) })
}

// anyLineOfPath keeps the path alongside the line: a detector that
// classifies by path cannot use anyLine, which discards the map key.
func anyLineOfPath(lines map[string][]string, matches func(path, line string) bool) bool {
	for path, fileLines := range lines {
		for _, line := range fileLines {
			if matches(path, line) {
				return true
			}
		}
	}
	return false
}
func linesOfPath(lines map[string][]string, path string) []string {
	path = filepath.ToSlash(path)
	for candidate, content := range lines {
		if filepath.ToSlash(candidate) == path {
			return content
		}
	}
	return nil
}
func isCode(line string) bool {
	line = strings.TrimSpace(line)
	return line != "" && !strings.HasPrefix(line, "//") && !strings.HasPrefix(line, "/*") && !strings.HasPrefix(line, "*") && !strings.HasPrefix(line, "*/")
}
func testInMap(testMap map[string]bool, pkg string) (bool, bool) {
	for path, covered := range testMap {
		if filepath.ToSlash(path) == pkg {
			return covered, true
		}
	}
	return false, false
}
