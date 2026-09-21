package main

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/change"
	"github.com/ISeoane-Quental/vcSentinel/internal/review"
	"github.com/ISeoane-Quental/vcSentinel/internal/risk"
)

// TestCountKeepsDuplicateDimensions is the reason the cost unit is agent
// invocations. BundlesForRisk schedules DimSpec in both the correctness and the
// contracts bundle at high risk, and AuditCommit dedupes by bundle name, not
// by dimension, so that dimension is audited twice. Collapsing the duplicate
// here would understate the cost of ticket 05 exactly where it lands.
func TestCountKeepsDuplicateDimensions(t *testing.T) {
	features := []change.Feature{{Name: "public_api", State: change.FeaturePresent}}
	bundles := review.BundlesForRisk(risk.Result{Level: risk.LevelHigh}, features)

	invocations, dimensions := count(bundles)
	if invocations != len(dimensions) {
		t.Fatalf("invocations %d and dimensions %d must agree", invocations, len(dimensions))
	}
	repeated := map[string]int{}
	for _, d := range dimensions {
		repeated[d]++
	}
	if repeated[review.DimSpec] < 2 {
		t.Errorf("spec scheduled %d times, want at least 2 (correctness plus contracts)", repeated[review.DimSpec])
	}
	if invocations <= len(repeated) {
		t.Errorf("invocations %d must exceed distinct dimensions %d at high risk", invocations, len(repeated))
	}
}

// TestCountOnNoBundles pins the none-risk arm: no bundle means no invocation,
// which is the state every documentation commit is in today. The empty plan
// must serialize as [] and not null, because the artifact is read by script and
// a null there forces every consumer to special-case it.
func TestCountOnNoBundles(t *testing.T) {
	invocations, dimensions := count(nil)
	if invocations != 0 || len(dimensions) != 0 {
		t.Errorf("empty plan = (%d, %v), want (0, [])", invocations, dimensions)
	}
	encoded, err := json.Marshal(dimensions)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(encoded) != "[]" {
		t.Errorf("empty plan serializes as %s, want []", encoded)
	}
}

func TestUnlockedFeaturesReportsOnlyNewlyPresent(t *testing.T) {
	planner := []change.Feature{
		{Name: "security_sensitive", State: change.FeatureAbsent},
		{Name: "public_api", State: change.FeaturePresent},
		{Name: "concurrency", State: change.FeatureAbsent},
	}
	shared := []change.Feature{
		{Name: "security_sensitive", State: change.FeaturePresent},
		{Name: "public_api", State: change.FeaturePresent},
		{Name: "concurrency", State: change.FeaturePresent},
	}
	got := unlockedFeatures(planner, shared)
	want := []string{"concurrency", "security_sensitive"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unlocked = %v, want %v", got, want)
	}
	if got := unlockedFeatures(shared, shared); len(got) != 0 {
		t.Errorf("identical inputs unlocked %v, want none", got)
	}
}

// TestContainsSourceSeparatesTheStrata guards the headline split. profile.Kind
// is not usable here: a commit changing two Go files alongside several
// documents classifies as kind=documentation.
func TestContainsSourceSeparatesTheStrata(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		want  bool
	}{
		{"go source among documents", []string{"docs/a.md", "internal/change/x.go", "README.md"}, true},
		{"documents only", []string{"docs/a.md", "docs/b.md"}, false},
		{"tests only", []string{"internal/change/x_test.go"}, false},
		{"config only", []string{"config/app.json", "go.mod"}, false},
		{"nothing changed", nil, false},
	}
	for _, tc := range cases {
		if got := containsSource(tc.paths); got != tc.want {
			t.Errorf("%s = %t, want %t", tc.name, got, tc.want)
		}
	}
}

// TestAddToAccumulatesBothArms keeps the stratum totals honest: a stratum must
// carry both arms so the delta is readable per stratum, not only overall.
func TestAddToAccumulatesBothArms(t *testing.T) {
	var e stratum
	addTo(&e, measurement{RiskChanged: true, PlannerInvocations: 3, SharedInvocations: 6})
	addTo(&e, measurement{RiskChanged: false, PlannerInvocations: 0, SharedInvocations: 4})
	if e.Commits != 2 || e.RiskChanged != 1 || e.PlannerInvocations != 3 || e.SharedInvocations != 10 {
		t.Errorf("stratum = %+v, want commits 2, changed 1, 3 -> 10", e)
	}
}
