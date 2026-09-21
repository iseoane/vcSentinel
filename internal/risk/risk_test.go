package risk

import (
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/change"
)

func TestEvaluate(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		complete bool
		statuses map[string]change.FeatureStatus
		want     Level
		rule     string
	}{
		{
			name:     "generated with no risk features",
			kind:     "generated",
			complete: true,
			want:     LevelNone,
			rule:     "kind=generated",
		},
		{
			name:     "security sensitive elevates a small change",
			kind:     "feature",
			complete: true,
			statuses: map[string]change.FeatureStatus{"security_sensitive": change.FeaturePresent},
			want:     LevelHigh,
			rule:     "security_sensitive",
		},
		{
			name:     "database is high",
			kind:     "feature",
			complete: true,
			statuses: map[string]change.FeatureStatus{"database": change.FeaturePresent},
			want:     LevelHigh,
			rule:     "database",
		},
		{
			name:     "test only is low",
			kind:     "test_only",
			complete: true,
			want:     LevelLow,
			rule:     "kind=test_only",
		},
		{name: "complete refactor without API is low", kind: "refactor", complete: true, want: LevelLow, rule: "refactor"},
		{
			name:     "public api is elevated",
			kind:     "feature",
			complete: true,
			statuses: map[string]change.FeatureStatus{"public_api": change.FeaturePresent},
			want:     LevelElevated,
			rule:     "public_api",
		},
		{
			name:     "cross module is elevated",
			kind:     "feature",
			complete: true,
			statuses: map[string]change.FeatureStatus{"cross_module": change.FeaturePresent},
			want:     LevelElevated,
			rule:     "cross_module",
		},
		{
			name:     "concurrency is elevated",
			kind:     "feature",
			complete: true,
			statuses: map[string]change.FeatureStatus{"concurrency": change.FeaturePresent},
			want:     LevelElevated,
			rule:     "concurrency",
		},
		{
			name:     "behavior without tests is elevated",
			kind:     "bugfix",
			complete: true,
			statuses: map[string]change.FeatureStatus{"behavior_change": change.FeaturePresent},
			want:     LevelElevated,
			rule:     "behavior_change without confirmed test_covered",
		},
		{
			name:     "behavior without confirmed coverage is elevated",
			kind:     "bugfix",
			complete: true,
			statuses: map[string]change.FeatureStatus{
				"behavior_change": change.FeaturePresent,
				"test_covered":    change.FeatureIndeterminate,
			},
			want: LevelElevated,
			rule: "behavior_change without confirmed test_covered",
		},
		{
			name:     "no special rule uses standard",
			kind:     "feature",
			complete: true,
			want:     LevelStandard,
			rule:     "by default",
		},
		{name: "behavior with incomplete graph is high", kind: "feature", complete: false, statuses: map[string]change.FeatureStatus{"behavior_change": change.FeaturePresent}, want: LevelHigh, rule: "incomplete graph"},
		{
			name:     "high wins when competing with elevated",
			kind:     "feature",
			complete: true,
			statuses: map[string]change.FeatureStatus{
				"public_api":         change.FeaturePresent,
				"security_sensitive": change.FeaturePresent,
			},
			want: LevelHigh,
			rule: "security_sensitive",
		},
		{
			name:     "dependency is simplified to high",
			kind:     "dependency",
			complete: true,
			want:     LevelHigh,
			rule:     "kind=dependency",
		},
	}

	seen := make(map[Level]bool)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profile := change.ChangeProfile{Kind: tc.kind, Symbols: change.ChangeSymbols{Complete: tc.complete}}
			result := Evaluate(profile, featuresWith(tc.statuses))
			if result.Level != tc.want {
				t.Fatalf("level = %q; want %q", result.Level, tc.want)
			}
			if result.Explanation == "" || !strings.Contains(result.Explanation, tc.rule) {
				t.Fatalf("explanation = %q; must identify %q", result.Explanation, tc.rule)
			}
			seen[result.Level] = true
		})
	}

	for _, level := range []Level{LevelNone, LevelLow, LevelStandard, LevelElevated, LevelHigh} {
		if !seen[level] {
			t.Errorf("level %q was not covered with an explanation", level)
		}
	}
}

// TestStatusOfDoesNotUnderestimateWithDuplicates covers the T3.4 review: an
// absent "security_sensitive" BEFORE a present one in the slice must not
// hide the present one and make the real risk (high) be underestimated.
func TestStatusOfDoesNotUnderestimateWithDuplicates(t *testing.T) {
	duplicates := []change.Feature{
		{Name: "security_sensitive", State: change.FeatureAbsent},
		{Name: "security_sensitive", State: change.FeaturePresent},
	}
	result := Evaluate(change.ChangeProfile{Kind: "feature"}, duplicates)
	if result.Level != LevelHigh {
		t.Fatalf("level with duplicates = %q; want %q (security_sensitive present at any position)", result.Level, LevelHigh)
	}
}

// TestFeatureConstantsMatchChange detects drift between the local feature
// name constants and the actual Name values returned by
// change.DetectFeatures: without this test, a literal change in
// internal/change would break this package silently, without a compile
// failure (T3.4 review).
func TestFeatureConstantsMatchChange(t *testing.T) {
	actualNames := map[string]bool{}
	for _, f := range change.DetectFeatures(change.FeaturesInput{}) {
		actualNames[f.Name] = true
	}
	locals := []string{
		featureSecuritySensitive, featureDatabase, featurePublicAPI,
		featureCrossModule, featureConcurrency, featureBehaviorChange,
		featureTestCoverage,
	}
	for _, name := range locals {
		if !actualNames[name] {
			t.Errorf("constant %q no longer matches any actual name from change.DetectFeatures: check drift against internal/change", name)
		}
	}
}

func featuresWith(statuses map[string]change.FeatureStatus) []change.Feature {
	names := []string{
		"public_api", "database", "security_sensitive", "concurrency", "behavior_change",
		"test_covered", "cross_module", "generated_code", "ci_cd", "infrastructure",
	}
	result := make([]change.Feature, 0, len(names))
	for _, name := range names {
		state := change.FeatureAbsent
		if indicated, ok := statuses[name]; ok {
			state = indicated
		}
		result = append(result, change.Feature{Name: name, State: state})
	}
	return result
}
