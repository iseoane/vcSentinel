package change

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ChangeProfile keeps the vocabulary of the stable contract so consumers can
// persist it without translating its fields.
type ChangeProfile struct {
	Base        string         `json:"base"`
	Head        string         `json:"head"`
	Kind        string         `json:"kind"`
	Size        ChangeSize     `json:"size"`
	Symbols     ChangeSymbols  `json:"symbols"`
	Modules     []string       `json:"modules"`
	FileClasses map[string]int `json:"file_classes"`
}

// ChangeSize keeps the metrics of the diff separate because none of them
// substitutes the others when deciding the risk of a change.
type ChangeSize struct {
	Files   int `json:"files"`
	Added   int `json:"added"`
	Deleted int `json:"deleted"`
	Hunks   int `json:"hunks"`
}

// ChangeSymbols summarizes the AST comparison; Complete distinguishes zero
// changes from a comparison that could not be exact.
type ChangeSymbols struct {
	Added           int  `json:"added"`
	Modified        int  `json:"modified"`
	Deleted         int  `json:"deleted"`
	ExportedTouched int  `json:"exported_touched"`
	Complete        bool `json:"complete"`
}

type pathChange struct{ before, after string }

// GitReader abstracts the git reads this package needs: a function instead of
// an interface with methods because they all share the same shape (arguments
// -> raw output, error) and the only real variation is the arguments. It
// decouples changeKind/isRefactor from invoking real git and allows testing
// them with test doubles (T3.2 review).
type GitReader func(args ...string) (string, error)

// ComputeChangeProfile computes deterministic signals over the immutable trees
// of the range.
func ComputeChangeProfile(base, head string) (ChangeProfile, error) {
	return computeChangeProfileWith(base, head, gitOutput)
}

// ComputeCommitProfile computes a full profile for a commit. Root commits are
// compared against Git's empty tree because they have no parent.
func ComputeCommitProfile(commit string) (ChangeProfile, error) {
	parents, err := gitOutput("rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return ChangeProfile{}, err
	}
	fields := strings.Fields(parents)
	if len(fields) == 0 {
		return ChangeProfile{}, fmt.Errorf("could not resolve commit %s", commit)
	}
	base := ""
	if len(fields) > 1 {
		base = fields[1]
	} else {
		base, err = gitEmptyTree()
		if err != nil {
			return ChangeProfile{}, err
		}
	}
	return ComputeChangeProfile(base, commit)
}

// computeChangeProfileWith is ComputeChangeProfile with the git reader
// injected: a testable variant that does not invoke real git.
func computeChangeProfileWith(base, head string, git GitReader) (ChangeProfile, error) {
	baseTree, err := revisionTree(git, base)
	if err != nil {
		return ChangeProfile{}, err
	}
	headTree, err := revisionTree(git, head)
	if err != nil {
		return ChangeProfile{}, err
	}
	diffRange := baseTree + ".." + headTree
	paths, err := diffPaths(git, diffRange)
	if err != nil {
		return ChangeProfile{}, err
	}
	changes, err := pathChanges(git, diffRange)
	if err != nil {
		return ChangeProfile{}, err
	}
	rules := DefaultRules()
	classify := func(path string) string { return ClassifyByPath(path, rules) }

	profile := ChangeProfile{
		Base:        base,
		Head:        head,
		Size:        ChangeSize{Files: len(paths)},
		Symbols:     changedSymbols(git, changes, baseTree, headTree, classify),
		Modules:     modulesOfPaths(paths),
		FileClasses: classCounts(paths),
	}
	if profile.Size.Added, profile.Size.Deleted, err = diffLines(git, diffRange); err != nil {
		return ChangeProfile{}, err
	}
	if profile.Size.Hunks, err = diffHunks(git, diffRange); err != nil {
		return ChangeProfile{}, err
	}
	messages, err := gitDiff(git, base+".."+head, "read the messages", "log", "--format=%s", base+".."+head)
	if err != nil {
		return ChangeProfile{}, err
	}
	profile.Kind = changeKind(git, classificationInput{paths: paths, changes: changes, base: baseTree, head: headTree, messages: messages})
	return profile, nil
}

func revisionTree(git GitReader, revision string) (string, error) {
	output, err := gitDiff(git, revision, "resolve the tree", "rev-parse", "--verify", "--end-of-options", revision+"^{tree}")
	return strings.TrimSpace(output), err
}

// gitOutput is the real implementation of GitReader, on the git CLI.
func gitOutput(args ...string) (string, error) {
	output, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func gitEmptyTree() (string, error) {
	cmd := exec.Command("git", "hash-object", "-t", "tree", "--stdin")
	cmd.Stdin = strings.NewReader("")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

// gitDiff runs one git read and wraps the error with the description of the
// action in a single place, instead of repeating the same fmt.Errorf in every
// read function (T3.2 review).
func gitDiff(git GitReader, diffRange, action string, args ...string) (string, error) {
	output, err := git(args...)
	if err != nil {
		return "", fmt.Errorf("could not %s of %s: %w", action, diffRange, err)
	}
	return output, nil
}

func diffPaths(git GitReader, diffRange string) ([]string, error) {
	output, err := gitDiff(git, diffRange, "list the files", "diff", "--name-only", "-z", "-M", diffRange)
	if err != nil {
		return nil, err
	}
	return nullSeparatedPaths(output), nil
}
func pathChanges(git GitReader, diffRange string) ([]pathChange, error) {
	output, err := gitDiff(git, diffRange, "list the changes", "diff", "--name-status", "-z", "-M", diffRange)
	if err != nil {
		return nil, err
	}
	parts := nullSeparatedPaths(output)
	var changes []pathChange
	for i := 0; i < len(parts); {
		status := parts[i]
		i++
		if status == "" || i >= len(parts) {
			return nil, fmt.Errorf("invalid file status in %s", diffRange)
		}
		switch status[0] {
		case 'R', 'C':
			if i+1 >= len(parts) {
				return nil, fmt.Errorf("invalid rename status in %s", diffRange)
			}
			changes = append(changes, pathChange{before: parts[i], after: parts[i+1]})
			i += 2
		case 'M', 'T':
			changes = append(changes, pathChange{before: parts[i], after: parts[i]})
			i++
		case 'D':
			changes = append(changes, pathChange{before: parts[i]})
			i++
		default:
			changes = append(changes, pathChange{after: parts[i]})
			i++
		}
	}
	return changes, nil
}
func nullSeparatedPaths(output string) []string {
	parts := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	if len(parts) == 1 && parts[0] == "" {
		return nil
	}
	return parts
}
func diffLines(git GitReader, diffRange string) (int, int, error) {
	output, err := gitDiff(git, diffRange, "measure the lines", "diff", "--numstat", diffRange)
	if err != nil {
		return 0, 0, err
	}
	var added, deleted int
	for _, line := range strings.Split(output, "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 2 || fields[0] == "-" || fields[1] == "-" {
			continue
		}
		a, errA := strconv.Atoi(fields[0])
		b, errB := strconv.Atoi(fields[1])
		if errA == nil && errB == nil {
			added += a
			deleted += b
		}
	}
	return added, deleted, nil
}
func diffHunks(git GitReader, diffRange string) (int, error) {
	output, err := gitDiff(git, diffRange, "measure the hunks", "diff", "--no-color", "--unified=0", diffRange)
	if err != nil {
		return 0, err
	}
	hunks := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "@@ ") {
			hunks++
		}
	}
	return hunks, nil
}
func classCounts(paths []string) map[string]int {
	counts := map[string]int{ClassSource: 0, ClassTest: 0, ClassConfig: 0, ClassGenerated: 0, ClassDocs: 0, ClassInfra: 0, ClassCI: 0}
	rules := DefaultRules()
	for _, path := range paths {
		counts[ClassifyByPath(path, rules)]++
	}
	return counts
}
func modulesOfPaths(paths []string) []string {
	set := make(map[string]bool)
	for _, path := range paths {
		if module := moduleOfPath(path); module != "" {
			set[module] = true
		}
	}
	modules := make([]string, 0, len(set))
	for module := range set {
		modules = append(modules, module)
	}
	sort.Strings(modules)
	return modules
}

// moduleDepth is the minimum number of path segments for a "module" to exist
// (the first two directories, e.g. "internal/review"): less than that is the
// repo root or a single directory, not an identifiable module.
const moduleDepth = 3

// moduleOfPath is the single source of truth of the package's "module"
// criterion: modulesOfPaths (cross_module) and Cohesion (structural
// proximity, T3.5) share it instead of each reimplementing its own segment
// cut (T3.5 review).
func moduleOfPath(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	if len(parts) < moduleDepth {
		return ""
	}
	return filepath.ToSlash(filepath.Join(parts[0], parts[1]))
}

// classificationInput groups the already derived signals (paths, changes) and
// the raw identifiers/text (base, head, messages) that changeKind needs: a
// struct instead of 5 positional parameters, so that adding a new signal in
// F4 does not keep widening the signature (T3.2 review).
type classificationInput struct {
	paths    []string
	changes  []pathChange
	base     string
	head     string
	messages string
}

// changeKind applies the contract order from highest to lowest priority: an
// unambiguous signal must hide more general labels of the same diff.
func changeKind(git GitReader, input classificationInput) string {
	classes := classCounts(input.paths)
	switch {
	case classes[ClassGenerated] > 0:
		return "generated"
	case touchesDependencies(input.paths):
		return "dependency"
	case classes[ClassInfra] > 0:
		return "infra"
	case classes[ClassCI] > 0:
		return "ci_cd"
	case classes[ClassConfig] > 0:
		return "configuration"
	case classes[ClassDocs] > 0:
		return "documentation"
	case classes[ClassTest] == len(input.paths) && len(input.paths) > 0:
		return "test_only"
	case isRefactor(git, input.changes, input.base, input.head):
		return "refactor"
	case touchesExistingCode(input.changes) && containsBugfix(input.messages):
		return "bugfix"
	default:
		return "feature"
	}
}
func touchesDependencies(paths []string) bool {
	for _, path := range paths {
		switch filepath.ToSlash(path) {
		case "go.mod", "go.work", "go.work.sum", "vendor/modules.txt", "package.json", "Cargo.toml", "requirements.txt", "pyproject.toml":
			return true
		}
	}
	return false
}
func touchesExistingCode(changes []pathChange) bool {
	rules := DefaultRules()
	for _, change := range changes {
		if change.before != "" && ClassifyByPath(change.before, rules) == ClassSource {
			return true
		}
	}
	return false
}
func containsBugfix(messages string) bool {
	for _, message := range strings.Split(messages, "\n") {
		message = strings.ToLower(strings.TrimSpace(message))
		if strings.HasPrefix(message, "fix:") || strings.HasPrefix(message, "fix(") {
			return true
		}
	}
	return false
}

type changedFunction struct{ name, body, path string }

func isRefactor(git GitReader, changes []pathChange, base, head string) bool {
	var before, after []changedFunction
	for _, change := range changes {
		before = append(before, functionsInRevision(git, base, change.before)...)
		after = append(after, functionsInRevision(git, head, change.after)...)
	}
	for _, old := range before {
		for _, new := range after {
			if old.body == new.body && (old.name != new.name || old.path != new.path) {
				return true
			}
		}
	}
	return false
}
func functionsInRevision(git GitReader, revision, path string) []changedFunction {
	if path == "" || !strings.HasSuffix(path, ".go") {
		return nil
	}
	content, err := git("show", revision+":"+filepath.ToSlash(path))
	if err != nil {
		return nil
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, content, 0)
	if err != nil {
		return nil
	}
	var functions []changedFunction
	for _, decl := range file.Decls {
		function, ok := decl.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		var body bytes.Buffer
		if format.Node(&body, token.NewFileSet(), function.Body) == nil {
			functions = append(functions, changedFunction{function.Name.Name, body.String(), filepath.ToSlash(path)})
		}
	}
	return functions
}
