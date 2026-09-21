package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentadapter"
	internalchange "github.com/ISeoane-Quental/vcSentinel/internal/change"
)

// PlannedBatch represents a batch proposed within the fragmentation plan.
// The message may start empty (normal batches) and be filled in later by
// GenerateBatchMessages, or start fixed (giants).
type PlannedBatch struct {
	Layer                string
	Number               int
	Paths                []string
	Selectors            []ChangeSelector
	TotalLines           int
	Message              string
	AutoMessage          string
	DeterministicMessage bool
	IsOversized          bool
}

// FragmentationPlan is the complete fragmentation proposal, built without
// creating any commit: the batches and their messages are approved before
// running.
type FragmentationPlan struct {
	Batches     []PlannedBatch
	Changes     []PlannedChange
	Explanation string
}

// CommitResult summarizes a commit created while running the plan.
type CommitResult struct {
	Hash    string
	Message string
	Layer   string
	Files   int
}

// BuildFragmentationPlan uses Cohesion's structural proximity. The
// production path calls BuildFragmentationPlanWithReader to also add
// historical co-change; this I/O-free wrapper keeps the tests and
// consumers that build plans from an already materialized list.
func BuildFragmentationPlan(files []ModifiedFile, confirmBypass func(ModifiedFile) (bool, error)) (*FragmentationPlan, error) {
	return BuildFragmentationPlanWithReader(files, confirmBypass, func(...string) (string, error) { return "", nil })
}

// BuildFragmentationPlanWithReader groups first by cohesion cluster and
// orders each cluster by class (config → source → test → docs →
// generated). A cluster is only split when buildBatches reaches the limit
// of 400.
func BuildFragmentationPlanWithReader(files []ModifiedFile, confirmBypass func(ModifiedFile) (bool, error), reader internalchange.GitReader) (*FragmentationPlan, error) {
	byPath := make(map[string]ModifiedFile, len(files))
	paths := make([]string, 0, len(files))
	linesPerPath := make(map[string]int, len(files))
	for _, f := range files {
		// f.Path always comes from "git status"/"git diff" (trackedFiles,
		// untrackedFiles in slice.go): git reports paths separated by "/"
		// on every OS, so a backslash here is a literal character of the
		// name, not a separator to convert. filepath.ToSlash/Clean only
		// need to normalize "./", "//" and the like.
		f.Path = filepath.ToSlash(filepath.Clean(f.Path))
		byPath[f.Path] = f
		paths = append(paths, f.Path)
		linesPerPath[f.Path] = f.Lines
	}
	cohesion, err := internalchange.Cohesion(paths, reader)
	if err != nil {
		return nil, err
	}
	groups := make([][]ModifiedFile, 0, len(cohesion.Groups))
	for _, groupPaths := range cohesion.Groups {
		group := make([]ModifiedFile, 0, len(groupPaths))
		for _, path := range groupPaths {
			group = append(group, byPath[path])
		}
		sortByClass(group)
		groups = append(groups, group)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		classI, classJ := classGroupRange(groups[i]), classGroupRange(groups[j])
		if classI != classJ {
			return classI < classJ
		}
		return layerGroupRange(groups[i]) < layerGroupRange(groups[j])
	})

	var plan FragmentationPlan
	number := 1
	for _, group := range groups {
		remaining := make([]ModifiedFile, 0, len(group))
		flushRemaining := func() {
			for _, batchFiles := range buildBatches(remaining) {
				batchPaths := make([]string, 0, len(batchFiles))
				for _, file := range batchFiles {
					batchPaths = append(batchPaths, file.Path)
				}
				plan.Batches = append(plan.Batches, normalBatch(batchWithLayer{Layer: batchLayer(batchFiles), Paths: batchPaths}, number, linesPerPath))
				number++
			}
			remaining = remaining[:0]
		}
		for _, f := range group {
			switch {
			case isOversizedConfig(f):
				flushRemaining()
				plan.Batches = append(plan.Batches, giantBatch(f, isolatedDepsMessage, number))
				number++
			case isExtensiveDocumentation(f):
				flushRemaining()
				// A long document is isolated like the configuration: it never
				// goes through the massive-code branch, which would offer to
				// split it with AI applying SRP.
				plan.Batches = append(plan.Batches, giantBatch(f, fmt.Sprintf(isolatedDocsMessage, filepath.Base(f.Path)), number))
				number++
			case isGiantCode(f):
				flushRemaining()
				ok, err := confirmBypass(f)
				if err != nil {
					return nil, err
				}
				if !ok {
					return nil, fmt.Errorf("fragmentation aborted: %s has %d lines and exceeds the limit of %d", f.Path, f.Lines, GiantCodeLimit)
				}
				plan.Batches = append(plan.Batches, giantBatch(f, fmt.Sprintf(giantBypassMessage, filepath.Base(f.Path)), number))
				number++
			default:
				remaining = append(remaining, f)
			}
		}
		flushRemaining()
	}
	return &plan, nil
}

func layerGroupRange(group []ModifiedFile) int {
	best := len(layerOrder)
	for _, file := range group {
		for i, layer := range layerOrder {
			if file.Layer == layer && i < best {
				best = i
			}
		}
	}
	return best
}

func sortByClass(files []ModifiedFile) {
	sort.SliceStable(files, func(i, j int) bool {
		return classRange(FileClass(files[i].Path)) < classRange(FileClass(files[j].Path))
	})
}

func classGroupRange(group []ModifiedFile) int {
	best := len(classOrder)
	for _, file := range group {
		if diffOut := classRange(FileClass(file.Path)); diffOut < best {
			best = diffOut
		}
	}
	return best
}

func classRange(class string) int {
	for i, candidate := range classOrder {
		if class == candidate {
			return i
		}
	}
	return len(classOrder)
}

func batchLayer(files []ModifiedFile) string {
	if len(files) == 0 {
		return "cohesion"
	}
	layer := files[0].Layer
	for _, file := range files[1:] {
		if file.Layer != layer {
			return "cohesion"
		}
	}
	return layer
}

// GenerateBatchMessages asks the adapter for the message of each non-giant
// batch and fills it into the plan. If the adapter fails for a batch, it
// uses the automatic fallback message and marks it as deterministic.
// Returns the number of batches that fell back so the UI can offer a
// fallback.
func GenerateBatchMessages(plan *FragmentationPlan, adapter agentadapter.AgentAdapter) int {
	fallbacks := 0
	for i := range plan.Batches {
		batch := &plan.Batches[i]
		if batch.IsOversized {
			continue
		}
		message, err := getMessageWithDiff(batch.Paths, batch.Layer, batch.Number, adapter)
		if err != nil {
			batch.Message = batch.AutoMessage
			batch.DeterministicMessage = true
			fallbacks++
			continue
		}
		batch.Message = message
		batch.DeterministicMessage = false
	}
	return fallbacks
}

// ApplyAutomaticMessages replaces every batch's message with its
// deterministic one: the fallback for normal batches and the giant's own
// for the isolated ones.
func ApplyAutomaticMessages(plan *FragmentationPlan) {
	for i := range plan.Batches {
		batch := &plan.Batches[i]
		batch.Message = batch.AutoMessage
		batch.DeterministicMessage = true
	}
}

// RegenerateBatchMessage asks the given adapter to produce a single
// batch's message again. If the adapter fails, it keeps the automatic
// fallback message.
func RegenerateBatchMessage(plan *FragmentationPlan, number int, adapter agentadapter.AgentAdapter) error {
	batch, err := batchByNumber(plan, number)
	if err != nil {
		return err
	}
	message, err := getMessageWithDiff(batch.Paths, batch.Layer, batch.Number, adapter)
	if err != nil {
		batch.Message = batch.AutoMessage
		batch.DeterministicMessage = true
		return nil
	}
	batch.Message = message
	batch.DeterministicMessage = false
	return nil
}

// ApplyAutomaticMessageBatch restores a batch's deterministic message.
func ApplyAutomaticMessageBatch(plan *FragmentationPlan, number int) error {
	batch, err := batchByNumber(plan, number)
	if err != nil {
		return err
	}
	batch.Message = batch.AutoMessage
	batch.DeterministicMessage = true
	return nil
}

// EditBatchMessage manually sets a batch's message.
func EditBatchMessage(plan *FragmentationPlan, number int, message string) error {
	batch, err := batchByNumber(plan, number)
	if err != nil {
		return err
	}
	batch.Message = strings.TrimSpace(message)
	batch.DeterministicMessage = false
	return nil
}

// VerifyAdapter probes an adapter with a minimal synthetic request and
// returns true if it responds without error. It helps detect unavailable
// or broken adapters before generating the messages for the whole plan.
func VerifyAdapter(adapter agentadapter.AgentAdapter) bool {
	_, err := getMessageWithDiff([]string{"probe.txt"}, "backend", 0, adapter)
	return err == nil
}

// RunFragmentationPlan commits each approved batch with its pre-approved
// message, in plan order, and returns a summary per commit created. Every
// commit skips hook verification (--no-verify): invoking vcsentinel slice IS
// the guardian's unlock, each batch is already validated (≤400 lines
// except giants with explicit bypass) and the volume hook would also
// measure the pending changes of the following batches, wrongly rejecting
// legitimate commits when the pending total exceeds 400 lines.
func RunFragmentationPlan(plan *FragmentationPlan) ([]CommitResult, error) {
	var results []CommitResult
	for _, batch := range plan.Batches {
		message := batch.Message
		if strings.TrimSpace(message) == "" {
			message = batch.AutoMessage
		}
		hash, err := commitBatchWithMessage(batch.Paths, message)
		if err != nil {
			return results, err
		}
		results = append(results, CommitResult{
			Hash:    hash,
			Message: message,
			Layer:   batch.Layer,
			Files:   len(batch.Paths),
		})
	}
	return results, nil
}

// WorktreeClean reports whether no pending changes remain in the worktree.
func WorktreeClean() (bool, error) {
	out, err := runGitOutput("status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

func groupByLayers(files []ModifiedFile) map[string][]ModifiedFile {
	byLayers := map[string][]ModifiedFile{"config": {}, "backend": {}, "frontend": {}, "test": {}}
	for _, f := range files {
		byLayers[f.Layer] = append(byLayers[f.Layer], f)
	}
	return byLayers
}

// classOrder pins the batches' output order by file class:
// config → source → test → docs → generated. It is the axis that groups
// before the layer, so that no batch mixes classes (T0.12).
var classOrder = []string{ClassConfig, ClassSource, ClassTest, ClassDocs, ClassGenerated}

// groupByClasses separates the files by FileClass, without touching the
// layer: they are two distinct axes that BuildFragmentationPlan combines
// in cascade.
func groupByClasses(files []ModifiedFile) map[string][]ModifiedFile {
	byClasses := make(map[string][]ModifiedFile, len(classOrder))
	for _, f := range files {
		class := FileClass(f.Path)
		byClasses[class] = append(byClasses[class], f)
	}
	return byClasses
}

func giantBatch(f ModifiedFile, message string, number int) PlannedBatch {
	return PlannedBatch{
		Layer:                f.Layer,
		Number:               number,
		Paths:                []string{f.Path},
		TotalLines:           f.Lines,
		Message:              message,
		AutoMessage:          message,
		DeterministicMessage: true,
		IsOversized:          true,
	}
}

func normalBatch(batch batchWithLayer, number int, linesPerPath map[string]int) PlannedBatch {
	total := 0
	for _, path := range batch.Paths {
		total += linesPerPath[path]
	}
	return PlannedBatch{
		Layer:       batch.Layer,
		Number:      number,
		Paths:       batch.Paths,
		TotalLines:  total,
		AutoMessage: fmt.Sprintf("chore(slice): auto-fragmented %s batch #%d", batch.Layer, number),
	}
}

func batchByNumber(plan *FragmentationPlan, number int) (*PlannedBatch, error) {
	for i := range plan.Batches {
		if plan.Batches[i].Number == number {
			return &plan.Batches[i], nil
		}
	}
	return nil, fmt.Errorf("batch #%d does not exist in the plan", number)
}

// commitBatchWithMessage adds the paths and creates the commit with the
// approved message. It skips the hooks (--no-verify) because the slice
// flow already validated each batch's size and it is the guardian's
// fragmentation mechanism.
//
// The add uses -f: a batch's paths always come from GetModifiedFiles,
// which only reports modified tracked files or untracked NOT-ignored
// files, so forcing the add never slips in a genuinely ignored file. What
// it does cover is the real case of a file that was already tracked when
// .gitignore started to affect it afterwards (e.g. .atl/): without -f,
// git add warns and exits with code 1 even though it leaves the file
// staged anyway, and that error aborted the whole batch without leaving a
// trace of the real cause (B13: before this change only "exit status 1"
// was visible).
func commitBatchWithMessage(paths []string, message string) (string, error) {
	argsAdd := append([]string{"add", "-f", "--"}, paths...)
	if out, err := exec.Command("git", argsAdd...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("git add failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("git", "commit", "-m", message, "--no-verify").CombinedOutput(); err != nil {
		return "", fmt.Errorf("git commit failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	hash, err := runGitOutput("rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(hash), nil
}
