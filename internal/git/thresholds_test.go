package git

import "testing"

// TestThresholdsDeriveFromReviewableLimit covers B4: the 400 lived in three
// unrelated places. There is now a single source and the others derive from
// it, so recalibrating the guardian cannot leave behind the fragmentation or
// the config isolation.
func TestThresholdsDeriveFromReviewableLimit(t *testing.T) {
	if batchLinesLimit != ReviewableLinesLimit {
		t.Errorf("batchLinesLimit = %d, must derive from ReviewableLinesLimit (%d)",
			batchLinesLimit, ReviewableLinesLimit)
	}
	if giantConfigLimit != ReviewableLinesLimit {
		t.Errorf("giantConfigLimit = %d, must derive from ReviewableLinesLimit (%d)",
			giantConfigLimit, ReviewableLinesLimit)
	}
}

// TestThresholdValuesDoNotChange: T0.4 unifies, it does not recalibrate. If
// these values change, it is a product decision that must be made separately.
func TestThresholdValuesDoNotChange(t *testing.T) {
	cases := []struct {
		name   string
		actual int
		value  int
	}{
		{"ReviewableLinesLimit", ReviewableLinesLimit, 400},
		{"OptimalPointThreshold", OptimalPointThreshold, 200},
		{"GiantCodeLimit", GiantCodeLimit, 500},
	}
	for _, c := range cases {
		if c.actual != c.value {
			t.Errorf("%s = %d, expected %d: this task unifies, it does not recalibrate",
				c.name, c.actual, c.value)
		}
	}
}

// TestClassifyStateRespectsThresholds pins the exact boundaries so that
// deriving the constants does not displace any of them unintentionally.
func TestClassifyStateRespectsThresholds(t *testing.T) {
	cases := []struct {
		lines int
		want  string
	}{
		{0, "SMALL"},
		{OptimalPointThreshold - 1, "SMALL"},
		{OptimalPointThreshold, "OPTIMAL_POINT"},
		{ReviewableLinesLimit, "OPTIMAL_POINT"},
		{ReviewableLinesLimit + 1, "CRITICAL"},
	}
	for _, c := range cases {
		if state := classifyState(c.lines); state != c.want {
			t.Errorf("classifyState(%d) = %q, expected %q", c.lines, state, c.want)
		}
	}
}
