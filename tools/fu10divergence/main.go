// Command fu10divergence measures what feeding the review planner the same
// change evidence as `vcsentinel explain` would cost, so ticket 03 of the FU-10
// sequence decides from a number rather than a preference.
//
// Both arms share one change profile per commit. Only the detector input
// varies: the planner arm reproduces the starved derivation as it stood before
// ticket 05, and the shared arm is the input `explain` assembles. Since 51cc5f7
// production behaves like the shared arm, so this tool measures the historical
// gap rather than a live one.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/ISeoane-Quental/vcSentinel/internal/change"
	"github.com/ISeoane-Quental/vcSentinel/internal/review"
	"github.com/ISeoane-Quental/vcSentinel/internal/risk"
)

// sensitivePatternsExplain replicates the literal that comandos_explain.go
// passes today. Moving it out of the CLI command is ticket 04's job.
var sensitivePatternsExplain = []string{"**/auth/**", "**/*auth*.go", "**/security/**"}

type measurement struct {
	SHA                string   `json:"sha"`
	Subject            string   `json:"subject"`
	Kind               string   `json:"kind"`
	SourceBearing      bool     `json:"source_bearing"`
	PlannerRisk        string   `json:"planner_risk"`
	SharedRisk         string   `json:"shared_risk"`
	PlannerInvocations int      `json:"planner_invocations"`
	SharedInvocations  int      `json:"shared_invocations"`
	PlannerDimensions  []string `json:"planner_dimensions"`
	SharedDimensions   []string `json:"shared_dimensions"`
	UnlockedFeatures   []string `json:"unlocked_features,omitempty"`
	RiskChanged        bool     `json:"risk_changed"`
}

type stratum struct {
	Commits            int `json:"commits"`
	RiskChanged        int `json:"risk_changed"`
	PlannerInvocations int `json:"planner_invocations"`
	SharedInvocations  int `json:"shared_invocations"`
}

type report struct {
	Ref                string             `json:"ref"`
	RefSHA             string             `json:"ref_sha"`
	Window             int                `json:"window"`
	Requested          int                `json:"requested"`
	Failed             int                `json:"failed"`
	MergesExcluded     int                `json:"merges_excluded"`
	CountingConvention string             `json:"counting_convention"`
	Strata             map[string]stratum `json:"strata"`
	SourceBearing      stratum            `json:"source_bearing_headline"`
	ProseOnly          stratum            `json:"prose_only"`
	Commits            []measurement      `json:"commits"`
}

func main() {
	window := flag.Int("n", 120, "how many non-merge commits to walk back from the tip")
	ref := flag.String("ref", "HEAD", "tip to walk back from")
	flag.Parse()

	// Resolve the tip to a SHA before walking, and walk from that SHA: reading
	// the identity afterwards would let a ref that moves in between label the
	// report with a tip it never measured.
	tip, err := git("rev-parse", *ref)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tip = strings.TrimSpace(tip)

	shas, merges, err := nonMergeCommits(tip, *window)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	out := report{
		Ref:            *ref,
		RefSHA:         tip,
		Requested:      len(shas),
		MergesExcluded: merges,
		// Bundle scheduling dedupes by bundle name, not by dimension, so at
		// high risk `spec` is scheduled by both the correctness and the
		// contracts bundle and `logic` by both correctness and
		// concurrency-data. Each of those is a separate agent invocation.
		CountingConvention: "agent invocations: the sum of bundle dimensions, without deduplicating a dimension scheduled by two bundles",
		Strata:             map[string]stratum{},
	}

	for _, sha := range shas {
		m, err := measure(sha)
		if err != nil {
			// An evidence harness must not present a short measurement as a
			// complete one: record the failure, keep going so the operator sees
			// every broken commit at once, and refuse to exit successfully.
			fmt.Fprintf(os.Stderr, "%s: %v\n", sha[:8], err)
			out.Failed++
			continue
		}
		out.Commits = append(out.Commits, m)
		accumulate(out.Strata, m.Kind, m)
		if m.SourceBearing {
			addTo(&out.SourceBearing, m)
		} else {
			addTo(&out.ProseOnly, m)
		}
	}

	out.Window = len(out.Commits)
	if err := json.NewEncoder(os.Stdout).Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	summarize(os.Stderr, out)
	if out.Failed > 0 {
		fmt.Fprintf(os.Stderr, "\nINCOMPLETE: %d of %d commits could not be measured; the aggregates above cover %d\n",
			out.Failed, out.Requested, out.Window)
		os.Exit(1)
	}
}

func measure(sha string) (measurement, error) {
	// One profile per commit, shared by both arms: deriving it separately on
	// each side would let a kind or symbol disagreement land inside the delta.
	profile, err := change.ComputeCommitProfile(sha)
	if err != nil {
		return measurement{}, err
	}
	paths, err := pathsOf(sha)
	if err != nil {
		return measurement{}, err
	}
	lines, err := addedLines(sha, paths)
	if err != nil {
		return measurement{}, err
	}
	gitattributes, err := readGitattributes(sha)
	if err != nil {
		return measurement{}, err
	}

	// Empty evidence reproduces the planner as it behaved BEFORE ticket 05, not
	// as it behaves now: production callers supply the diff and the attributes
	// since 51cc5f7. The arm is kept starved on purpose so the recorded
	// artifact stays reproducible; read it as the historical baseline the
	// measurement compared against, never as current behaviour.
	plan := review.PlanForProfile(profile, paths, "", "")

	shared := change.DetectFeatures(change.FeaturesInput{
		Symbols: profile.Symbols, Paths: paths, AddedLines: lines,
		Gitattributes: gitattributes, SensitivePatterns: sensitivePatternsExplain,
	})
	sharedRisk := risk.Evaluate(profile, shared)
	sharedBundles := review.BundlesForRisk(sharedRisk, shared)

	subject, err := git("show", "-s", "--format=%s", sha)
	if err != nil {
		return measurement{}, fmt.Errorf("reading the subject of %s: %w", sha, err)
	}
	plannerInvocations, plannerDimensions := count(plan.Bundles)
	sharedInvocations, sharedDimensions := count(sharedBundles)

	return measurement{
		SHA: sha, Subject: strings.TrimSpace(subject), Kind: profile.Kind,
		SourceBearing:      containsSource(paths),
		PlannerRisk:        string(plan.Risk.Level),
		SharedRisk:         string(sharedRisk.Level),
		PlannerInvocations: plannerInvocations, SharedInvocations: sharedInvocations,
		PlannerDimensions: plannerDimensions, SharedDimensions: sharedDimensions,
		UnlockedFeatures: unlockedFeatures(plan.Characteristics, shared),
		RiskChanged:      plan.Risk.Level != sharedRisk.Level,
	}, nil
}

// count returns the agent invocations and the list of dimensions exactly as
// they are scheduled, with repetitions. The repetitions are the point:
// AuditCommit dedupes by bundle name, not by dimension, so a dimension
// scheduled by two bundles is audited twice.
func count(bundles []review.ReviewBundle) (invocations int, dimensions []string) {
	dimensions = []string{} // an empty plan must serialize as [], not null
	for _, bundle := range bundles {
		invocations += len(bundle.Dimensions)
		dimensions = append(dimensions, bundle.Dimensions...)
	}
	sort.Strings(dimensions)
	return invocations, dimensions
}

func unlockedFeatures(planner, shared []change.Feature) []string {
	state := map[string]change.FeatureStatus{}
	for _, f := range planner {
		state[f.Name] = f.State
	}
	var names []string
	for _, f := range shared {
		if f.State == change.FeaturePresent && state[f.Name] != change.FeaturePresent {
			names = append(names, f.Name)
		}
	}
	sort.Strings(names)
	return names
}

func containsSource(paths []string) bool {
	rules := change.DefaultRules()
	for _, path := range paths {
		if change.ClassifyByPath(path, rules) == change.ClassSource {
			return true
		}
	}
	return false
}

func accumulate(strata map[string]stratum, key string, m measurement) {
	e := strata[key]
	addTo(&e, m)
	strata[key] = e
}

func addTo(e *stratum, m measurement) {
	e.Commits++
	if m.RiskChanged {
		e.RiskChanged++
	}
	e.PlannerInvocations += m.PlannerInvocations
	e.SharedInvocations += m.SharedInvocations
}

// nonMergeCommits excludes merges: under --no-ff the reviewed commits keep
// their SHA, so the non-merge ones are the population review audits.
func nonMergeCommits(ref string, n int) ([]string, int, error) {
	noMerge, err := git("rev-list", "--no-merges", fmt.Sprintf("-n%d", n), ref)
	if err != nil {
		return nil, 0, err
	}
	list := strings.Fields(noMerge)
	if len(list) == 0 {
		return nil, 0, fmt.Errorf("no non-merge commits reachable from %s", ref)
	}
	// Merges inside the same window: reachable from the tip and not from the
	// oldest commit walked. Counting a fixed multiple of n instead would report
	// the padding, not the merges.
	mergeCount, err := git("rev-list", "--count", "--min-parents=2", list[len(list)-1]+".."+ref)
	if err != nil {
		return nil, 0, fmt.Errorf("counting merges in the window: %w", err)
	}
	merges := 0
	if _, err := fmt.Sscanf(strings.TrimSpace(mergeCount), "%d", &merges); err != nil {
		return nil, 0, fmt.Errorf("unreadable merge count %q: %w", strings.TrimSpace(mergeCount), err)
	}
	return list, merges, nil
}

// pathsOf reads the changed paths NUL-delimited: Git quotes and escapes paths
// holding spaces or non-ASCII bytes in its default line output, and a quoted
// path fed back to a pathspec silently matches nothing. --root makes a root
// commit report its files instead of none.
func pathsOf(sha string) ([]string, error) {
	output, err := git("diff-tree", "--no-commit-id", "--name-only", "-r", "-z", "--root", sha)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, path := range strings.Split(output, "\x00") {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// addedLines replicates the per-path read that explain does today. The
// in-memory parser that replaces it is ticket 04's work.
//
// It reads through `git show` rather than a two-dot diff so a root commit,
// which has no `sha^`, reports its files as wholly added instead of failing. A
// path that cannot be read is an error: silently contributing no lines would
// understate the characteristics and bias the measurement toward the planner.
func addedLines(sha string, paths []string) (map[string][]string, error) {
	result := make(map[string][]string)
	for _, path := range paths {
		output, err := git("show", "--no-color", "--unified=0", "--format=", sha, "--", ":(literal)"+path)
		if err != nil {
			return nil, fmt.Errorf("reading added lines of %s in %s: %w", path, sha, err)
		}
		inHunk := false
		for _, line := range strings.Split(output, "\n") {
			if strings.HasPrefix(line, "@@") {
				inHunk = true
				continue
			}
			if inHunk && strings.HasPrefix(line, "+") {
				result[path] = append(result[path], strings.TrimPrefix(line, "+"))
			}
		}
	}
	return result, nil
}

// readGitattributes distinguishes absence from failure. Neither `git show
// <sha>:.gitattributes` nor `git cat-file -e` serve for that: both exit with a
// non-zero code both when the path is missing and when the repository or the
// object cannot be read, so either of them would turn a real failure into "no
// attributes" and degrade the evidence silently.
//
// `git ls-tree` does separate them: an absent path is empty output with a zero
// code, and only a real failure exits with a non-zero code.
func readGitattributes(sha string) (string, error) {
	listing, err := git("ls-tree", "--name-only", sha, "--", ".gitattributes")
	if err != nil {
		return "", fmt.Errorf("looking for .gitattributes at %s: %w", sha, err)
	}
	if strings.TrimSpace(listing) == "" {
		return "", nil // absent in this tree, which is the common case
	}
	content, err := git("show", sha+":.gitattributes")
	if err != nil {
		return "", fmt.Errorf("reading .gitattributes at %s: %w", sha, err)
	}
	return content, nil
}

func git(args ...string) (string, error) {
	output, err := exec.Command("git", args...).Output()
	return string(output), err
}

func summarize(w *os.File, out report) {
	fmt.Fprintf(w, "\n%s (%s)\n", out.Ref, out.RefSHA)
	fmt.Fprintf(w, "window=%d of %d non-merge commits measured, %d failed (%d merges excluded)\n",
		out.Window, out.Requested, out.Failed, out.MergesExcluded)
	fmt.Fprintf(w, "counting: %s\n\n", out.CountingConvention)
	fmt.Fprintf(w, "%-16s %8s %14s %12s %12s\n", "stratum", "commits", "risk changed", "inv today", "inv shared")
	keys := make([]string, 0, len(out.Strata))
	for k := range out.Strata {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e := out.Strata[k]
		fmt.Fprintf(w, "%-16s %8d %14d %12d %12d\n", k, e.Commits, e.RiskChanged, e.PlannerInvocations, e.SharedInvocations)
	}
	e := out.SourceBearing
	fmt.Fprintf(w, "\nHEADLINE source-bearing: %d commits, %d change risk, %d -> %d agent invocations\n",
		e.Commits, e.RiskChanged, e.PlannerInvocations, e.SharedInvocations)
	p := out.ProseOnly
	fmt.Fprintf(w, "prose-only:              %d commits, %d change risk, %d -> %d agent invocations\n",
		p.Commits, p.RiskChanged, p.PlannerInvocations, p.SharedInvocations)
}
