// Package risk evaluates the explainable risk of a change profile.
package risk

import "github.com/ISeoane-Quental/vas.sentinel/internal/change"

// Level is the stable vocabulary of the risk contract.
type Level string

const (
	LevelNone     Level = "none"
	LevelLow      Level = "low"
	LevelStandard Level = "standard"
	LevelElevated Level = "elevated"
	LevelHigh     Level = "high"
)

// Result keeps the level and the rule that decided it.
type Result struct {
	Level       Level
	Explanation string
}

// Names of the ChangeProfile characteristics and kinds this package
// consults: local constants instead of loose repeated strings, so that a
// typo within THIS file fails at compile time (T3.4 review). They do not
// live in internal/change because that migration exceeds the scope of this
// fix; they must match the literals the change package's feature detectors
// use on the other side.
//
// The feature ones (feature*) have a drift test:
// TestFeatureConstantsMatchChange compares them against the actual Name
// returned by change.DetectFeatures. The kind ones (kind*) do NOT have
// one — the change package's profile code does not expose its kinds as
// exported constants or a function enumerating them, so exercising them
// would require synthetic git repos per kind, out of proportion for an
// ADVISORY. Debt explicitly annotated for F4/F5, not a silent oversight.
const (
	featureSecuritySensitive = "security_sensitive"
	featureDatabase          = "database"
	featurePublicAPI         = "public_api"
	featureCrossModule       = "cross_module"
	featureConcurrency       = "concurrency"
	featureBehaviorChange    = "behavior_change"
	featureTestCoverage      = "test_covered"

	kindDependency    = "dependency"
	kindTestOnly      = "test_only"
	kindDocumentation = "documentation"
	kindGenerated     = "generated"
)

// riskFeatures are the ones that discard the "none" rule: their presence
// contradicts "no risk feature".
var riskFeatures = []string{
	featurePublicAPI, featureCrossModule, featureConcurrency,
	featureBehaviorChange, featureSecuritySensitive, featureDatabase,
}

// rule is a risk alternative that can be evaluated in isolation: adding a
// new rule in F4 (full graph, semver) is appending an element to
// riskRules, not editing the body of Evaluate (T3.4 review: the previous
// if-chain broke open/closed as soon as one more rule arrived).
type rule struct {
	level    Level
	evaluate func(profile change.ChangeProfile, features []change.Feature) (applies bool, explanation string)
}

// riskRules implements, in any order, the alternatives of each level of
// the report (docs/design/replanteamiento-objetivo.md, §9.2):
// Evaluate does not assume priority by position, it computes the real
// maximum at the end.
//
// Symbols.Complete enables the F4 completeness rules. The semver analysis
// of dependencies remains out of scope: kind=dependency provisionally keeps high.
var riskRules = []rule{
	{LevelHigh, func(profile change.ChangeProfile, cs []change.Feature) (bool, string) {
		return !profile.Symbols.Complete && statusOf(cs, featureBehaviorChange) == change.FeaturePresent, "high due to behavior_change with incomplete graph"
	}},
	{LevelHigh, func(profile change.ChangeProfile, cs []change.Feature) (bool, string) {
		if name := firstPresent(cs, featureSecuritySensitive, featureDatabase); name != "" {
			return true, "high due to " + name + " present"
		}
		return false, ""
	}},
	{LevelHigh, func(profile change.ChangeProfile, cs []change.Feature) (bool, string) {
		if profile.Kind == kindDependency {
			return true, "high due to kind=dependency (F3 simplification)"
		}
		return false, ""
	}},
	{LevelElevated, func(profile change.ChangeProfile, cs []change.Feature) (bool, string) {
		if name := firstPresent(cs, featurePublicAPI, featureCrossModule, featureConcurrency); name != "" {
			return true, "elevated due to " + name + " present"
		}
		return false, ""
	}},
	{LevelElevated, func(profile change.ChangeProfile, cs []change.Feature) (bool, string) {
		// By conservative criterion, test_covered indeterminate counts as
		// unconfirmed coverage: if it is not confirmed, no risk is subtracted.
		if statusOf(cs, featureBehaviorChange) == change.FeaturePresent &&
			statusOf(cs, featureTestCoverage) != change.FeaturePresent {
			return true, "elevated due to behavior_change without confirmed test_covered"
		}
		return false, ""
	}},
	{LevelLow, func(profile change.ChangeProfile, cs []change.Feature) (bool, string) {
		if profile.Kind == kindTestOnly {
			return true, "low due to kind=test_only"
		}
		return false, ""
	}},
	{LevelLow, func(profile change.ChangeProfile, cs []change.Feature) (bool, string) {
		return profile.Kind == "refactor" && profile.Symbols.Complete && statusOf(cs, featurePublicAPI) == change.FeatureAbsent, "low due to refactor with complete graph and no public_api"
	}},
	{LevelNone, func(profile change.ChangeProfile, cs []change.Feature) (bool, string) {
		if (profile.Kind == kindDocumentation || profile.Kind == kindGenerated) &&
			firstPresent(cs, riskFeatures...) == "" {
			return true, "none due to kind=" + profile.Kind + " with no risk features"
		}
		return false, ""
	}},
}

// Evaluate applies a maximum over the rules; standard is only the fallback
// when no special rule produces a candidate.
func Evaluate(profile change.ChangeProfile, features []change.Feature) Result {
	var candidates []Result
	for _, r := range riskRules {
		if applies, explanation := r.evaluate(profile, features); applies {
			candidates = append(candidates, Result{r.level, explanation})
		}
	}

	if len(candidates) == 0 {
		return Result{LevelStandard, "standard by default: no none/low/elevated/high rule applies"}
	}
	maximum := candidates[0]
	for _, candidate := range candidates[1:] {
		if rank(candidate.Level) > rank(maximum.Level) {
			maximum = candidate
		}
	}
	return maximum
}

func firstPresent(features []change.Feature, names ...string) string {
	for _, name := range names {
		if statusOf(features, name) == change.FeaturePresent {
			return name
		}
	}
	return ""
}

// statusOf prioritizes Present wherever it appears in the slice, instead
// of returning only the first match by name: if a caller were to insert
// duplicate entries for the same name, a duplicate Absent before one
// Present would no longer silently underestimate the risk (T3.4 review).
func statusOf(features []change.Feature, name string) change.FeatureStatus {
	found := false
	status := change.FeatureIndeterminate
	for _, feature := range features {
		if feature.Name != name {
			continue
		}
		if feature.State == change.FeaturePresent {
			return change.FeaturePresent
		}
		if !found {
			status = feature.State
			found = true
		}
	}
	return status
}

func rank(level Level) int {
	switch level {
	case LevelNone:
		return 0
	case LevelLow:
		return 1
	case LevelStandard:
		return 2
	case LevelElevated:
		return 3
	case LevelHigh:
		return 4
	default:
		return -1
	}
}
