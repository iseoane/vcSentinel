package change

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCohesionByConnectedComponents(t *testing.T) {
	cases := []struct {
		name        string
		paths       []string
		history     string
		wantCluster int
		wantScore   float64
		wantSplit   bool
	}{
		{
			name: "three independent groups",
			paths: []string{
				"internal/review/finding.go",
				"internal/review/ledger.go",
				"internal/setup/github.go",
				".github/workflows/ci.yml",
			},
			wantCluster: 3,
			wantScore:   1.0 / 3.0,
			wantSplit:   true,
		},
		{
			name: "large cohesive change by module",
			paths: []string{
				"internal/change/clases.go",
				"internal/change/perfil.go",
				"internal/change/caracteristicas.go",
				"internal/change/cohesion.go",
				"internal/change/cohesion_test.go",
			},
			wantCluster: 1,
			wantScore:   1,
			wantSplit:   false,
		},
		{
			name:        "only historical co-change connects",
			paths:       []string{"internal/auth/token.go", "pkg/session/store.go"},
			history:     "commit:abc123\n\ninternal/auth/token.go\npkg/session/store.go\n",
			wantCluster: 1,
			wantScore:   1,
			wantSplit:   false,
		},
		{
			name:  "historical co-change is transitive",
			paths: []string{"internal/auth/token.go", "pkg/session/store.go", "cmd/api/main.go"},
			history: "commit:abc123\n\ninternal/auth/token.go\npkg/session/store.go\n" +
				"commit:def456\n\npkg/session/store.go\ncmd/api/main.go\n",
			wantCluster: 1,
			wantScore:   1,
			wantSplit:   false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls := 0
			fake := func(args ...string) (string, error) {
				calls++
				if len(args) == 0 || args[0] != "log" || !contains(args, "--name-only") {
					t.Fatalf("unexpected git read: %v", args)
				}
				for _, path := range c.paths {
					if !contains(args, path) {
						t.Errorf("git log did not receive the path %q: %v", path, args)
					}
				}
				return c.history, nil
			}

			result, err := Cohesion(c.paths, fake)
			if err != nil {
				t.Fatalf("Cohesion returned an error: %v", err)
			}
			if result.Clusters != c.wantCluster || result.Score != c.wantScore || result.SuggestSplit != c.wantSplit {
				t.Errorf("Cohesion = %#v, want clusters=%d score=%v split=%v", result, c.wantCluster, c.wantScore, c.wantSplit)
			}
			if calls != 1 {
				t.Errorf("git calls = %d, want 1", calls)
			}
		})
	}
}

func TestCohesionReturnsMembersOfEachCluster(t *testing.T) {
	paths := []string{"internal/auth/config.yaml", "internal/auth/login.go", "cmd/tool/main.go"}
	result, err := Cohesion(paths, func(args ...string) (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("Cohesion returned an error: %v", err)
	}
	expected := [][]string{{"internal/auth/config.yaml", "internal/auth/login.go"}, {"cmd/tool/main.go"}}
	if !reflect.DeepEqual(result.Groups, expected) {
		t.Fatalf("groups = %v, expected %v", result.Groups, expected)
	}
}

// TestCohesionSplitsPathsIntoGitLogBatches covers the T3.5 review: passing
// more than maxHistoryBatchPaths paths at once to "git log -- ..." risked
// the process argument limit. With 250 paths there must be 2 calls
// (200 + 50), none with more than maxHistoryBatchPaths paths.
func TestCohesionSplitsPathsIntoGitLogBatches(t *testing.T) {
	paths := make([]string, 250)
	for i := range paths {
		paths[i] = fmt.Sprintf("internal/same/file%03d.go", i)
	}

	calls := 0
	var batchSizes []int
	fake := func(args ...string) (string, error) {
		calls++
		pathsInBatch := 0
		for _, arg := range args {
			if strings.HasPrefix(arg, "internal/same/") {
				pathsInBatch++
			}
		}
		batchSizes = append(batchSizes, pathsInBatch)
		if pathsInBatch > maxHistoryBatchPaths {
			t.Fatalf("batch of %d paths exceeds maxHistoryBatchPaths=%d", pathsInBatch, maxHistoryBatchPaths)
		}
		return "", nil
	}

	result, err := Cohesion(paths, fake)
	if err != nil {
		t.Fatalf("Cohesion returned an error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("git calls = %d, want 2 (250 paths in batches of %d)", calls, maxHistoryBatchPaths)
	}
	if batchSizes[0] != maxHistoryBatchPaths || batchSizes[1] != 50 {
		t.Errorf("batch sizes = %v, want [%d, 50]", batchSizes, maxHistoryBatchPaths)
	}
	// Same directory ("internal/same"): structural proximity connects them
	// all in a single cluster, regardless of the empty history.
	if result.Clusters != 1 {
		t.Errorf("Clusters = %d, want 1 (same directory)", result.Clusters)
	}
}

// TestCohesionMergesCoChangeAcrossBatches covers the CRITICAL of the T3.5
// review: two files co-changed in the SAME historical commit, but split
// across different "git log" batches, must still be joined. Before the fix,
// each batch only saw its own files per commit and the union was silently
// lost in the successful case (not only when a batch failed).
//
// 250 paths fully isolated from each other by directory/module (no
// structural proximity at all), except for one pair —one in batch 1, the
// other in batch 2— that shares a simulated historical commit. Without
// merging across batches: 250 clusters (the pair never joins). With
// merging: 249 (the pair fuses).
func TestCohesionMergesCoChangeAcrossBatches(t *testing.T) {
	const (
		fileBatch1 = "group_a/unique/file0.go"
		fileBatch2 = "group_b/unique/file200.go"
	)
	paths := make([]string, 250)
	for i := range paths {
		paths[i] = fmt.Sprintf("filler/idx%03d/f.go", i)
	}
	paths[0] = fileBatch1
	paths[200] = fileBatch2

	calls := 0
	fake := func(args ...string) (string, error) {
		calls++
		var output strings.Builder
		hasBatch1 := contains(args, fileBatch1)
		hasBatch2 := contains(args, fileBatch2)
		if hasBatch1 || hasBatch2 {
			output.WriteString("commit:sharedSha\n\n")
			if hasBatch1 {
				output.WriteString(fileBatch1 + "\n")
			}
			if hasBatch2 {
				output.WriteString(fileBatch2 + "\n")
			}
		}
		return output.String(), nil
	}

	result, err := Cohesion(paths, fake)
	if err != nil {
		t.Fatalf("Cohesion returned an error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("git calls = %d, want 2", calls)
	}
	if result.Clusters != 249 {
		t.Fatalf("Clusters = %d, want 249 (250 isolated with one pair fused by co-change across batches)", result.Clusters)
	}
}
