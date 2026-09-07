package change

import (
	"fmt"
	"path/filepath"
	"strings"
)

// CohesionResult summarizes the connected components of the changed paths.
type CohesionResult struct {
	Clusters     int
	Score        float64
	SuggestSplit bool
	// Groups keeps the paths of each component in input order. It is
	// operational information for slice; the public output of explain projects
	// only the stable summary of cohesion.
	Groups [][]string
}

// Cohesion connects paths by historical co-change and structural proximity.
// The score is 0 when there are no paths and 1/Clusters otherwise: it is 1 for
// a single component and decreases monotonically as its components grow.
//
// Inviolable rule of T3.5: this is a SUGGESTION, it is never applied
// automatically. Cohesion only returns data and neither rewrites nor groups
// anything.
func Cohesion(paths []string, git GitReader) (CohesionResult, error) {
	paths = normalizePaths(paths)
	if len(paths) == 0 {
		return CohesionResult{}, nil
	}
	if git == nil {
		return CohesionResult{}, fmt.Errorf("cannot compute cohesion without a git reader")
	}

	components := newDisjointSet(len(paths))
	connectByProximity(paths, components)

	perCommit, err := coChangeHistory(git, paths)
	if err != nil {
		return CohesionResult{}, err
	}
	connectByHistory(perCommit, paths, components)

	clusters := components.count()
	return CohesionResult{
		Clusters:     clusters,
		Score:        1 / float64(clusters),
		SuggestSplit: clusters > 1,
		Groups:       groupsOfComponents(paths, components),
	}, nil
}

func groupsOfComponents(paths []string, components *disjointSet) [][]string {
	indexByRoot := make(map[int]int)
	var groups [][]string
	for i, path := range paths {
		root := components.root(i)
		index, exists := indexByRoot[root]
		if !exists {
			index = len(groups)
			indexByRoot[root] = index
			groups = append(groups, nil)
		}
		groups[index] = append(groups[index], path)
	}
	return groups
}

// maxHistoryBatchPaths bounds how many paths go into a single "git log --
// <paths...>": passing hundreds of paths unsplit risks the process argument
// limit (ARG_MAX), exactly in the case this tool exists to detect. It does
// not degrade to proximity-only if a batch fails: it follows the same
// fail-fast criterion as ComputeChangeProfile before a git error, instead of
// returning a partial score without warning.
const maxHistoryBatchPaths = 200

// coChangeHistory aggregates, per commit SHA, the files (of the path set)
// that appeared in that commit, MERGING across batches.
//
// Real bug fixed (T3.5 review, CRITICAL): with independent batches, "git log
// -- <batch>" only reports the files of THAT batch per commit. Two paths
// co-changed in the same historical commit but split across different batches
// never matched under the same block after concatenating the text: each
// appeared in a separate "commit:<same-sha>" block, and connectByHistory
// treated them as independent groups, silently losing the signal exactly in
// the case maxHistoryBatchPaths exists to cover (many paths). Merging by SHA
// before connecting avoids this: a single commit accumulates the files any
// batch contributes to it.
func coChangeHistory(git GitReader, paths []string) (map[string][]string, error) {
	perCommit := map[string][]string{}
	for start := 0; start < len(paths); start += maxHistoryBatchPaths {
		end := start + maxHistoryBatchPaths
		if end > len(paths) {
			end = len(paths)
		}
		args := []string{"log", "--format=commit:%H", "--name-only", "--"}
		args = append(args, paths[start:end]...)
		output, err := gitDiff(git, "these paths", "read the historical co-change", args...)
		if err != nil {
			return nil, err
		}
		mergeHistory(output, perCommit)
	}
	return perCommit, nil
}

// mergeHistory parses a "commit:<sha>" block + paths and ACCUMULATES its
// files into perCommit[sha], instead of overwriting: so a later call
// (another batch) reporting the same commit adds to the previous one.
func mergeHistory(output string, perCommit map[string][]string) {
	var currentCommit string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if sha, isCommit := strings.CutPrefix(line, "commit:"); isCommit {
			currentCommit = sha
			continue
		}
		if line == "" || currentCommit == "" {
			continue
		}
		perCommit[currentCommit] = append(perCommit[currentCommit], line)
	}
}

func normalizePaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	result := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		path = filepath.ToSlash(filepath.Clean(path))
		if !seen[path] {
			seen[path] = true
			result = append(result, path)
		}
	}
	return result
}

func connectByProximity(paths []string, components *disjointSet) {
	directories := make(map[string]int)
	modules := make(map[string]int)
	for i, path := range paths {
		directory := filepath.ToSlash(filepath.Dir(path))
		connectToFirst(directories, directory, i, components)
		if module := moduleOfPath(path); module != "" {
			connectToFirst(modules, module, i, components)
		}
	}
}

func connectToFirst(group map[string]int, key string, index int, components *disjointSet) {
	if first, exists := group[key]; exists {
		components.union(first, index)
		return
	}
	group[key] = index
}

func connectByHistory(perCommit map[string][]string, paths []string, components *disjointSet) {
	indices := make(map[string]int, len(paths))
	for i, path := range paths {
		indices[path] = i
	}

	for _, files := range perCommit {
		var commitIndices []int
		for _, file := range files {
			if index, exists := indices[filepath.ToSlash(file)]; exists {
				commitIndices = append(commitIndices, index)
			}
		}
		for i := 1; i < len(commitIndices); i++ {
			components.union(commitIndices[0], commitIndices[i])
		}
	}
}

type disjointSet struct {
	parent []int
}

func newDisjointSet(size int) *disjointSet {
	set := &disjointSet{parent: make([]int, size)}
	for i := range set.parent {
		set.parent[i] = i
	}
	return set
}

func (set *disjointSet) root(index int) int {
	if set.parent[index] != index {
		set.parent[index] = set.root(set.parent[index])
	}
	return set.parent[index]
}

func (set *disjointSet) union(a, b int) {
	set.parent[set.root(b)] = set.root(a)
}

func (set *disjointSet) count() int {
	roots := make(map[int]bool, len(set.parent))
	for i := range set.parent {
		roots[set.root(i)] = true
	}
	return len(roots)
}
