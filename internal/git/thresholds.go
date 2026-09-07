package git

// Volume thresholds of the guardian. Before T0.4 the 400 lived in three
// unrelated places —classifyState, batchLinesLimit/giantConfigLimit and
// review.DecisionChainLimit— so recalibrating one silently left the others
// behind (B4). Here is the single source; the others derive from it.
//
// This declaration unifies, it does NOT recalibrate: the values are the
// long-standing ones.
const (
	// ReviewableLinesLimit is how much code can be reviewed in one sitting.
	// It is the guardian's threshold and, for the same reason, the maximum
	// size of a fragmentation batch and the point where a configuration file
	// is isolated into its own commit.
	ReviewableLinesLimit = 400

	// OptimalPointThreshold is from how many lines on the pending volume
	// already deserves a commit: below it is SMALL, between this value and
	// ReviewableLinesLimit it is OPTIMAL_POINT.
	OptimalPointThreshold = 200

	// GiantCodeLimit is the suggested maximum number of lines for a source
	// file before offering to refactor, bypass or abort. It is larger than
	// ReviewableLinesLimit on purpose: a 450-line file fits in its own
	// batch, but a 500-line one is no longer reviewed in one sitting nor
	// isolated.
	GiantCodeLimit = 500
)

// "Lines" means three different things in this project and they should not
// be confused when comparing against these thresholds (B4/O2):
//
//   - Added: what a diff sums up (git numstat). It is what the guardian
//     measures in MeasureVolume and what classifyState compares.
//   - Physical: what a file has on disk, for the untracked files that do
//     not appear in a diff (countPhysicalLines).
//   - Added+deleted: the churn of a commit range, which is what
//     review.DecisionChainLimit measures over a whole branch.
//
// All three are compared against the same number because they measure the
// same thing — how much change a person can review— not because they are
// interchangeable.
