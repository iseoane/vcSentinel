package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestBuildFragmentationPlanGroupsByLayersInOrder(t *testing.T) {
	files := []ModifiedFile{
		{Path: "web/app.tsx", Lines: 100, Layer: "frontend"},
		{Path: "cmd/main.go", Lines: 100, Layer: "backend"},
		{Path: "config.yaml", Lines: 100, Layer: "config"},
		{Path: "internal/git/slice_test.go", Lines: 100, Layer: "test"},
	}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	if len(plan.Batches) != 4 {
		t.Fatalf("expected 4 batches, got %d", len(plan.Batches))
	}
	var layers []string
	for _, batch := range plan.Batches {
		layers = append(layers, batch.Layer)
	}
	expected := []string{"config", "backend", "frontend", "test"}
	if !reflect.DeepEqual(layers, expected) {
		t.Errorf("layer order = %v, expected %v", layers, expected)
	}
	for i, batch := range plan.Batches {
		if batch.Number != i+1 {
			t.Errorf("batch %d: number %d, expected %d", i, batch.Number, i+1)
		}
	}
}

func TestBuildFragmentationPlanGroupsByCohesionAndOrdersClasses(t *testing.T) {
	files := []ModifiedFile{
		{Path: "internal/auth/login_test.go", Lines: 40, Layer: "test"},
		{Path: "cmd/tool/main.go", Lines: 30, Layer: "backend"},
		{Path: "internal/auth/login.go", Lines: 40, Layer: "backend"},
		{Path: "internal/auth/config.yaml", Lines: 20, Layer: "config"},
	}
	plan, err := BuildFragmentationPlanWithReader(files,
		func(ModifiedFile) (bool, error) { return true, nil },
		func(args ...string) (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlanWithReader returned error: %v", err)
	}
	if len(plan.Batches) != 2 {
		t.Fatalf("batches = %+v, expected 2 clusters", plan.Batches)
	}
	expected := []string{"internal/auth/config.yaml", "internal/auth/login.go", "internal/auth/login_test.go"}
	if !reflect.DeepEqual(plan.Batches[0].Paths, expected) {
		t.Fatalf("auth cluster = %v, expected %v", plan.Batches[0].Paths, expected)
	}
}

func TestBuildFragmentationPlanSplitsClusterOnlyByLimit(t *testing.T) {
	files := []ModifiedFile{
		{Path: "internal/auth/a.go", Lines: 250, Layer: "backend"},
		{Path: "internal/auth/b.go", Lines: 250, Layer: "backend"},
	}
	plan, err := BuildFragmentationPlanWithReader(files,
		func(ModifiedFile) (bool, error) { return true, nil },
		func(args ...string) (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Batches) != 2 || plan.Batches[0].TotalLines > 400 || plan.Batches[1].TotalLines > 400 {
		t.Fatalf("limit not respected: %+v", plan.Batches)
	}
}

// The path pivots on "./" instead of on a backslash: git always reports
// paths separated by "/" (even when running on Windows), so a backslash in a
// git path is a literal character of the file name, not a separator to
// convert. filepath.Clean does need to normalize "./" so the path ends up in
// the same cluster as its neighbor.
func TestBuildFragmentationPlanNormalizesPathsOnce(t *testing.T) {
	files := []ModifiedFile{
		{Path: `internal/auth/./login.go`, Lines: 40, Layer: "backend"},
		{Path: `internal/auth/login_test.go`, Lines: 20, Layer: "test"},
	}
	plan, err := BuildFragmentationPlanWithReader(files,
		func(ModifiedFile) (bool, error) { return true, nil },
		func(args ...string) (string, error) { return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Batches) != 1 {
		t.Fatalf("batches = %+v, expected one normalized cluster", plan.Batches)
	}
	expected := []string{"internal/auth/login.go", "internal/auth/login_test.go"}
	if !reflect.DeepEqual(plan.Batches[0].Paths, expected) || plan.Batches[0].TotalLines != 60 {
		t.Fatalf("batch = %+v, expected paths %v and 60 lines", plan.Batches[0], expected)
	}
}

// TestBuildFragmentationPlanDoesNotMixClasses covers T0.12: a .go and a .md
// that fall into the same layer ("backend", the default case of ClassifyLayer
// for a .md) must not end up in the same batch. Before this task,
// groupByLayers only looked at the layer and mixed them, as really happened
// in commit 3160133 (code and design doc in a single commit).
func TestBuildFragmentationPlanDoesNotMixClasses(t *testing.T) {
	files := []ModifiedFile{
		{Path: "cmd/main.go", Lines: 50, Layer: "backend"},
		{Path: "docs/guide.md", Lines: 50, Layer: "backend"},
	}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	if len(plan.Batches) != 2 {
		t.Fatalf("expected 2 batches (one per class), got %d", len(plan.Batches))
	}
	for _, batch := range plan.Batches {
		classes := make(map[string]bool)
		for _, path := range batch.Paths {
			classes[FileClass(path)] = true
		}
		if len(classes) > 1 {
			t.Errorf("batch #%d mixes classes: paths %v", batch.Number, batch.Paths)
		}
	}
}

func TestBuildFragmentationPlanKeepsGroupingByLimits(t *testing.T) {
	files := []ModifiedFile{
		{Path: "a.go", Lines: 200, Layer: "backend"},
		{Path: "b.go", Lines: 200, Layer: "backend"},
		{Path: "c.go", Lines: 200, Layer: "backend"},
	}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	if len(plan.Batches) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(plan.Batches))
	}
	if !reflect.DeepEqual(plan.Batches[0].Paths, []string{"a.go", "b.go"}) {
		t.Errorf("batch 1 paths = %v", plan.Batches[0].Paths)
	}
	if plan.Batches[0].TotalLines != 400 {
		t.Errorf("batch 1 lines = %d, expected 400", plan.Batches[0].TotalLines)
	}
	if plan.Batches[0].Number != 1 || plan.Batches[1].Number != 2 {
		t.Errorf("batch numbers = %d, %d; expected 1, 2", plan.Batches[0].Number, plan.Batches[1].Number)
	}
	if !reflect.DeepEqual(plan.Batches[1].Paths, []string{"c.go"}) {
		t.Errorf("batch 2 paths = %v", plan.Batches[1].Paths)
	}
}

func TestBuildFragmentationPlanIsolatesGiantConfig(t *testing.T) {
	files := []ModifiedFile{
		{Path: "package-lock.json", Lines: 450, Layer: "config"},
		{Path: "cmd/main.go", Lines: 100, Layer: "backend"},
	}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	if len(plan.Batches) != 2 {
		t.Fatalf("expected 2 batches (giant + normal), got %d", len(plan.Batches))
	}
	// Since T0.12 the batches are grouped first by file class (FileClass) in
	// the order config → source → test → docs → generated. "package-lock.json"
	// is class "generated" (isGenerated by the "-lock.json" suffix), even
	// though its business layer is "config"; that is why the normal code batch
	// (class "source") comes out before the isolated giant.
	normal := plan.Batches[0]
	if normal.IsOversized || normal.Number != 1 {
		t.Errorf("normal batch = %+v", normal)
	}
	if normal.Message != "" {
		t.Errorf("the normal batch message must be born empty and generated later, got %q", normal.Message)
	}
	if normal.DeterministicMessage {
		t.Errorf("the normal batch should not be born deterministic")
	}
	giant := plan.Batches[1]
	if !giant.IsOversized {
		t.Errorf("the second batch should be giant, got %+v", giant)
	}
	if giant.Message != isolatedDepsMessage {
		t.Errorf("giant message = %q, expected %q", giant.Message, isolatedDepsMessage)
	}
	if !giant.DeterministicMessage {
		t.Errorf("the giant message should be deterministic")
	}
	if !reflect.DeepEqual(giant.Paths, []string{"package-lock.json"}) {
		t.Errorf("giant paths = %v", giant.Paths)
	}
	if giant.TotalLines != 450 {
		t.Errorf("giant lines = %d, expected 450", giant.TotalLines)
	}
}

func TestBuildFragmentationPlanIsolatesConfirmedGiantCode(t *testing.T) {
	files := []ModifiedFile{
		{Path: "big.go", Lines: 600, Layer: "backend"},
		{Path: "config.yaml", Lines: 50, Layer: "config"},
	}
	confirmed := false
	plan, err := BuildFragmentationPlan(files, func(f ModifiedFile) (bool, error) {
		confirmed = true
		if f.Path != "big.go" {
			t.Errorf("bypass confirmation received %q, expected big.go", f.Path)
		}
		return true, nil
	})
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	if !confirmed {
		t.Error("bypass confirmation should have been invoked for the giant code")
	}
	if len(plan.Batches) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(plan.Batches))
	}
	giant := plan.Batches[1]
	if !giant.IsOversized || giant.Layer != "backend" {
		t.Errorf("giant = %+v", giant)
	}
	expected := "chore(slice): bypass AI for massive file big.go"
	if giant.Message != expected {
		t.Errorf("message = %q, expected %q", giant.Message, expected)
	}
	if !giant.DeterministicMessage {
		t.Errorf("the confirmed giant message should be deterministic")
	}
}

func TestBuildFragmentationPlanAbortsWhenGiantRejected(t *testing.T) {
	files := []ModifiedFile{
		{Path: "big.go", Lines: 600, Layer: "backend"},
	}
	_, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return false, nil })
	if err == nil {
		t.Fatal("expected an error when rejecting giant code")
	}
	if !strings.Contains(err.Error(), "aborted") {
		t.Errorf("error = %q, expected an abort mention", err)
	}
}

func TestBuildFragmentationPlanPropagatesConfirmationError(t *testing.T) {
	files := []ModifiedFile{
		{Path: "big.go", Lines: 600, Layer: "backend"},
	}
	_, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return false, os.ErrPermission })
	if err != os.ErrPermission {
		t.Errorf("error = %v, expected os.ErrPermission", err)
	}
}

func TestBuildFragmentationPlanAbortsWithoutCommittingAnything(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	if err := os.WriteFile("big.go", []byte("package big\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, dir, "add", "big.go")

	files := []ModifiedFile{
		{Path: "big.go", Lines: 600, Layer: "backend"},
	}
	_, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return false, nil })
	if err == nil {
		t.Fatal("expected an error when rejecting giant code")
	}

	totalCommits := runGitInDir(t, dir, "rev-list", "--count", "HEAD")
	if totalCommits != "1" {
		t.Errorf("no commit should be created when aborting, got %s commits", totalCommits)
	}
	status := runGitInDir(t, dir, "status", "--porcelain")
	if !strings.Contains(status, "big.go") {
		t.Errorf("big.go should remain pending after aborting, got: %q", status)
	}
}

func TestGenerateBatchMessagesWithAdapter(t *testing.T) {
	files := []ModifiedFile{
		{Path: "config.yaml", Lines: 50, Layer: "config"},
		{Path: "cmd/main.go", Lines: 100, Layer: "backend"},
		{Path: "package-lock.json", Lines: 450, Layer: "config"},
	}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}

	fallbacks := GenerateBatchMessages(plan, testAdapter{})
	if fallbacks != 0 {
		t.Errorf("expected 0 fallbacks with a working adapter, got %d", fallbacks)
	}
	for _, batch := range plan.Batches {
		if batch.Message == "" {
			t.Errorf("batch %d (layer %s) was left without a message", batch.Number, batch.Layer)
		}
		if batch.IsOversized {
			if batch.Message != isolatedDepsMessage {
				t.Errorf("the giant message was overwritten: %q", batch.Message)
			}
			continue
		}
		if batch.Message != "chore(slice): test" {
			t.Errorf("batch %d message = %q, expected the adapter's", batch.Number, batch.Message)
		}
		if batch.DeterministicMessage {
			t.Errorf("batch %d should be marked as generated by the adapter", batch.Number)
		}
	}
}

func TestGenerateBatchMessagesUsesFallbackWhenAdapterFails(t *testing.T) {
	files := []ModifiedFile{
		{Path: "a.go", Lines: 200, Layer: "backend"},
		{Path: "b.go", Lines: 200, Layer: "backend"},
		{Path: "c.go", Lines: 200, Layer: "backend"},
	}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	if len(plan.Batches) != 2 {
		t.Fatalf("precondition: expected 2 batches, got %d", len(plan.Batches))
	}

	failingAdapter := agentadapterFunc(func(paths []string, layer string, num int) (string, error) {
		return "", os.ErrPermission
	})
	fallbacks := GenerateBatchMessages(plan, failingAdapter)
	if fallbacks != 2 {
		t.Errorf("expected 2 fallbacks, got %d", fallbacks)
	}
	for i, batch := range plan.Batches {
		expected := "chore(slice): auto-fragmented backend batch #" + strconv.Itoa(i+1)
		if batch.Message != expected {
			t.Errorf("batch %d message = %q, expected %q", i, batch.Message, expected)
		}
		if !batch.DeterministicMessage {
			t.Errorf("batch %d should be marked deterministic after the fallback", i)
		}
	}
}

func TestGenerateBatchMessagesRegeneratesEvenAlreadyFallenBatches(t *testing.T) {
	files := []ModifiedFile{
		{Path: "a.go", Lines: 50, Layer: "backend"},
	}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}

	failingAdapter := agentadapterFunc(func(paths []string, layer string, num int) (string, error) {
		return "", os.ErrPermission
	})
	GenerateBatchMessages(plan, failingAdapter)
	if !plan.Batches[0].DeterministicMessage {
		t.Fatal("precondition: the batch should have fallen back")
	}

	// A new adapter must regenerate the message even when the batch was
	// already marked as deterministic by the previous fallback.
	fallbacks := GenerateBatchMessages(plan, testAdapter{})
	if fallbacks != 0 {
		t.Errorf("expected 0 fallbacks with the new adapter, got %d", fallbacks)
	}
	if plan.Batches[0].Message != "chore(slice): test" || plan.Batches[0].DeterministicMessage {
		t.Errorf("the batch should be regenerated with the new adapter, got %+v", plan.Batches[0])
	}
}

func TestApplyAutomaticMessages(t *testing.T) {
	files := []ModifiedFile{
		{Path: "a.go", Lines: 50, Layer: "backend"},
		{Path: "package-lock.json", Lines: 450, Layer: "config"},
	}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	if len(plan.Batches) != 2 {
		t.Fatalf("expected 2 batches (giant + normal), got %d", len(plan.Batches))
	}
	// Same as in TestBuildFragmentationPlanIsolatesGiantConfig: by class,
	// "a.go" (source) comes out before "package-lock.json" (generated).
	normal := plan.Batches[0]
	giant := plan.Batches[1]
	GenerateBatchMessages(plan, testAdapter{})
	if normal.DeterministicMessage {
		t.Fatal("precondition: the normal batch should be non-deterministic before applying automatics")
	}

	ApplyAutomaticMessages(plan)
	for _, batch := range plan.Batches {
		if !batch.DeterministicMessage {
			t.Errorf("batch %d should end up deterministic", batch.Number)
		}
		if batch.Message != batch.AutoMessage {
			t.Errorf("batch %d message = %q, expected %q", batch.Number, batch.Message, batch.AutoMessage)
		}
	}
	if giant.Message != isolatedDepsMessage {
		t.Errorf("the giant should keep its deterministic message, got %q", giant.Message)
	}
}

func TestRegenerateBatchMessage(t *testing.T) {
	files := []ModifiedFile{
		{Path: "a.go", Lines: 200, Layer: "backend"},
		{Path: "b.go", Lines: 200, Layer: "backend"},
		{Path: "c.go", Lines: 200, Layer: "backend"},
	}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	if len(plan.Batches) != 2 {
		t.Fatalf("precondition: expected 2 batches, got %d", len(plan.Batches))
	}

	adapter := agentadapterFunc(func(paths []string, layer string, num int) (string, error) {
		return "fix: regenerated", nil
	})
	if err := RegenerateBatchMessage(plan, 2, adapter); err != nil {
		t.Fatalf("RegenerateBatchMessage returned error: %v", err)
	}
	if plan.Batches[1].Message != "fix: regenerated" {
		t.Errorf("message = %q, expected 'fix: regenerated'", plan.Batches[1].Message)
	}
	if plan.Batches[1].DeterministicMessage {
		t.Errorf("a regenerated batch should not be marked deterministic")
	}
	if plan.Batches[0].Message != "" {
		t.Errorf("batch 1 should not be touched, got %q", plan.Batches[0].Message)
	}
}

func TestRegenerateBatchMessageFallsBackOnFailure(t *testing.T) {
	files := []ModifiedFile{{Path: "a.go", Lines: 50, Layer: "backend"}}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	failingAdapter := agentadapterFunc(func(paths []string, layer string, num int) (string, error) {
		return "", os.ErrPermission
	})
	if err := RegenerateBatchMessage(plan, 1, failingAdapter); err != nil {
		t.Fatalf("RegenerateBatchMessage returned error: %v", err)
	}
	if plan.Batches[0].Message != "chore(slice): auto-fragmented backend batch #1" {
		t.Errorf("message = %q, expected the fallback", plan.Batches[0].Message)
	}
	if !plan.Batches[0].DeterministicMessage {
		t.Errorf("batch should be marked deterministic after the failure")
	}
}

func TestRegenerateBatchMessageWithUnknownNumber(t *testing.T) {
	files := []ModifiedFile{{Path: "a.go", Lines: 50, Layer: "backend"}}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	if err := RegenerateBatchMessage(plan, 99, testAdapter{}); err == nil {
		t.Fatal("expected an error with an unknown batch number")
	}
}

func TestApplyAutomaticMessageBatch(t *testing.T) {
	files := []ModifiedFile{{Path: "a.go", Lines: 50, Layer: "backend"}}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	GenerateBatchMessages(plan, testAdapter{})

	if err := ApplyAutomaticMessageBatch(plan, 1); err != nil {
		t.Fatalf("ApplyAutomaticMessageBatch returned error: %v", err)
	}
	if plan.Batches[0].Message != "chore(slice): auto-fragmented backend batch #1" {
		t.Errorf("message = %q, expected the deterministic one", plan.Batches[0].Message)
	}
	if !plan.Batches[0].DeterministicMessage {
		t.Errorf("batch should be marked deterministic")
	}
}

func TestEditBatchMessage(t *testing.T) {
	files := []ModifiedFile{{Path: "a.go", Lines: 50, Layer: "backend"}}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	if err := EditBatchMessage(plan, 1, "feat: manual"); err != nil {
		t.Fatalf("EditBatchMessage returned error: %v", err)
	}
	if plan.Batches[0].Message != "feat: manual" {
		t.Errorf("message = %q, expected 'feat: manual'", plan.Batches[0].Message)
	}
	if plan.Batches[0].DeterministicMessage {
		t.Errorf("an edited message should not be marked deterministic")
	}
}

func TestEditBatchMessageWithUnknownNumber(t *testing.T) {
	files := []ModifiedFile{{Path: "a.go", Lines: 50, Layer: "backend"}}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	if err := EditBatchMessage(plan, 42, "feat: x"); err == nil {
		t.Fatal("expected an error with an unknown batch number")
	}
}

func TestVerifyAdapter(t *testing.T) {
	if !VerifyAdapter(testAdapter{}) {
		t.Error("testAdapter should verify correctly")
	}
	failingAdapter := agentadapterFunc(func(paths []string, layer string, num int) (string, error) {
		return "", os.ErrPermission
	})
	if VerifyAdapter(failingAdapter) {
		t.Error("a failing adapter should not verify")
	}
}

func TestRunFragmentationPlanInRealRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{
		"a.go": "package a\n",
		"b.go": "package b\n",
		"c.go": "package c\n",
		"d.go": "package d\n",
	})
	t.Chdir(dir)

	appendLines(t, "b.go", 200)
	appendLines(t, "c.go", 200)
	appendLines(t, "d.go", 200)

	files, err := GetModifiedFiles()
	if err != nil {
		t.Fatalf("GetModifiedFiles returned error: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 modified files, got %d", len(files))
	}

	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	GenerateBatchMessages(plan, testAdapter{})

	results, err := RunFragmentationPlan(plan)
	if err != nil {
		t.Fatalf("RunFragmentationPlan returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 commits, got %d", len(results))
	}
	totalCommits := runGitInDir(t, dir, "rev-list", "--count", "HEAD")
	if totalCommits != "3" {
		t.Errorf("expected 3 commits (initial + 2 batches), got %s", totalCommits)
	}
	for i, r := range results {
		if r.Hash == "" {
			t.Errorf("result %d should include a short hash", i)
		}
		if r.Message != "chore(slice): test" {
			t.Errorf("result %d message = %q, expected the approved one", i, r.Message)
		}
		if r.Layer != "backend" {
			t.Errorf("result %d layer = %q, expected backend", i, r.Layer)
		}
	}
	if results[0].Files != 2 || results[1].Files != 1 {
		t.Errorf("files per commit = %d, %d; expected 2, 1", results[0].Files, results[1].Files)
	}
	status := runGitInDir(t, dir, "status", "--porcelain")
	if status != "" {
		t.Errorf("the worktree should be clean after fragmenting, got: %s", status)
	}
}

func TestRunFragmentationPlanSkipsHookVerification(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	// Hook that always fails: the slice flow (the guardian's fragmentation
	// mechanism) skips verification with --no-verify for all of its commits.
	dirHooks := t.TempDir()
	hook := filepath.Join(dirHooks, "pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, dir, "config", "core.hooksPath", filepath.ToSlash(dirHooks))

	// Confirmed giant batch: the commit must skip the hook.
	if err := os.WriteFile("big.go", []byte("package big\n"), 0644); err != nil {
		t.Fatal(err)
	}
	files := []ModifiedFile{
		{Path: "big.go", Lines: 600, Layer: "backend"},
	}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	if len(plan.Batches) != 1 || !plan.Batches[0].IsOversized {
		t.Fatalf("precondition: expected 1 giant batch, got %+v", plan.Batches)
	}
	if _, err := RunFragmentationPlan(plan); err != nil {
		t.Fatalf("the giant batch should skip the hook, got error: %v", err)
	}

	// A normal batch with the same hook must skip it too: the total pending
	// volume of other batches cannot reject legitimate slice commits.
	appendLines(t, "a.go", 10)
	normalPlan, err := BuildFragmentationPlan([]ModifiedFile{
		{Path: "a.go", Lines: 10, Layer: "backend"},
	}, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	if _, err := RunFragmentationPlan(normalPlan); err != nil {
		t.Fatalf("the normal batch should skip the hook, got error: %v", err)
	}
}

func TestRunFragmentationPlanUsesFallbackMessageWhenAdapterFails(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	appendLines(t, "a.go", 10)
	files, err := GetModifiedFiles()
	if err != nil {
		t.Fatalf("GetModifiedFiles returned error: %v", err)
	}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}

	failingAdapter := agentadapterFunc(func(paths []string, layer string, num int) (string, error) {
		return "", os.ErrPermission
	})
	GenerateBatchMessages(plan, failingAdapter)

	if _, err := RunFragmentationPlan(plan); err != nil {
		t.Fatalf("RunFragmentationPlan returned error: %v", err)
	}
	message := runGitInDir(t, dir, "log", "-1", "--pretty=%s")
	if !strings.Contains(message, "auto-fragmented") {
		t.Errorf("the commit should use the fallback message, got: %q", message)
	}
}

func TestRunFragmentationPlanUsesApprovedMessages(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	appendLines(t, "a.go", 10)
	files, err := GetModifiedFiles()
	if err != nil {
		t.Fatalf("GetModifiedFiles returned error: %v", err)
	}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	plan.Batches[0].Message = "feat: approved by the user"

	if _, err := RunFragmentationPlan(plan); err != nil {
		t.Fatalf("RunFragmentationPlan returned error: %v", err)
	}
	message := runGitInDir(t, dir, "log", "-1", "--pretty=%s")
	if message != "feat: approved by the user" {
		t.Errorf("message = %q, expected the user-approved one", message)
	}
}

func TestRunFragmentationPlanIsolatesGiantConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	var builder strings.Builder
	builder.WriteString("{\n")
	for i := 0; i < 450; i++ {
		builder.WriteString("  \"dep\": true,\n")
	}
	builder.WriteString("}\n")
	if err := os.WriteFile("package-lock.json", []byte(builder.String()), 0644); err != nil {
		t.Fatal(err)
	}

	files, err := GetModifiedFiles()
	if err != nil {
		t.Fatalf("GetModifiedFiles returned error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d: %+v", len(files), files)
	}
	if files[0].Layer != "config" {
		t.Errorf("layer = %q, expected config", files[0].Layer)
	}

	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	if _, err := RunFragmentationPlan(plan); err != nil {
		t.Fatalf("RunFragmentationPlan returned error: %v", err)
	}

	message := runGitInDir(t, dir, "log", "-1", "--pretty=%s")
	if message != isolatedDepsMessage {
		t.Errorf("message = %q, expected %q", message, isolatedDepsMessage)
	}
	status := runGitInDir(t, dir, "status", "--porcelain")
	if status != "" {
		t.Errorf("the worktree should be clean after isolating the lock, got: %s", status)
	}
}

func TestRunFragmentationPlanIsolatesConfirmedGiantCode(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	if err := os.WriteFile("big.go", []byte("package big\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, dir, "add", "big.go")

	files := []ModifiedFile{
		{Path: "big.go", Lines: 600, Layer: "backend"},
	}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}
	if _, err := RunFragmentationPlan(plan); err != nil {
		t.Fatalf("RunFragmentationPlan returned error: %v", err)
	}

	message := runGitInDir(t, dir, "log", "-1", "--pretty=%s")
	if message != "chore(slice): bypass AI for massive file big.go" {
		t.Errorf("message = %q, expected the bypass", message)
	}
	status := runGitInDir(t, dir, "status", "--porcelain")
	if status != "" {
		t.Errorf("the worktree should be clean after the bypass, got: %s", status)
	}
}

func TestRunFragmentationPlanDeliversDiffToAdapter(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	appendLines(t, "a.go", 5)
	files, err := GetModifiedFiles()
	if err != nil {
		t.Fatalf("GetModifiedFiles returned error: %v", err)
	}
	plan, err := BuildFragmentationPlan(files, func(ModifiedFile) (bool, error) { return true, nil })
	if err != nil {
		t.Fatalf("BuildFragmentationPlan returned error: %v", err)
	}

	adapter := &testAdapterWithDiff{}
	GenerateBatchMessages(plan, adapter)
	if !strings.Contains(adapter.receivedDiff, "+// generated line") {
		t.Errorf("the batch diff should contain the added line, got: %q", adapter.receivedDiff)
	}

	if _, err := RunFragmentationPlan(plan); err != nil {
		t.Fatalf("RunFragmentationPlan returned error: %v", err)
	}
	message := runGitInDir(t, dir, "log", "-1", "--pretty=%s")
	if message != "chore(slice): with diff" {
		t.Errorf("message = %q, expected the adapter's message with diff", message)
	}
}

func TestWorktreeClean(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	clean, err := WorktreeClean()
	if err != nil {
		t.Fatalf("WorktreeClean returned error: %v", err)
	}
	if !clean {
		t.Error("a freshly prepared worktree should be clean")
	}

	appendLines(t, "a.go", 5)
	clean, err = WorktreeClean()
	if err != nil {
		t.Fatalf("WorktreeClean returned error: %v", err)
	}
	if clean {
		t.Error("a worktree with changes should report not clean")
	}
}

// TestRunFragmentationPlanCommitsTrackedFileGitignoreLaterIgnores reproduces
// B13: an already tracked file (like .atl/ in this repo) that .gitignore
// starts to affect afterwards. Without -f, 'git add' on that path warns and
// exits with code 1 even though it leaves the file staged anyway, and that
// error used to abort the whole batch.
func TestRunFragmentationPlanCommitsTrackedFileGitignoreLaterIgnores(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{
		"ledger.txt": "initial state\n",
	})
	t.Chdir(dir)

	if err := os.WriteFile(".gitignore", []byte("ledger.txt\n"), 0644); err != nil {
		t.Fatalf("could not write .gitignore: %v", err)
	}
	runGitInDir(t, dir, "add", "-f", ".gitignore")
	runGitInDir(t, dir, "commit", "-q", "-m", "ignores ledger.txt")

	appendLines(t, "ledger.txt", 5)

	plan := &FragmentationPlan{Batches: []PlannedBatch{
		{Layer: "backend", Number: 1, Paths: []string{"ledger.txt"}, Message: "chore(slice): test"},
	}}

	results, err := RunFragmentationPlan(plan)
	if err != nil {
		t.Fatalf("RunFragmentationPlan returned error with a tracked and ignored file: %v", err)
	}
	if len(results) != 1 || results[0].Hash == "" {
		t.Fatalf("expected 1 commit with a hash, got %+v", results)
	}
	status := runGitInDir(t, dir, "status", "--porcelain")
	if status != "" {
		t.Errorf("the worktree should be clean, got: %s", status)
	}
}

// TestRunFragmentationPlanErrorIncludesGitOutput checks that a real git
// failure is no longer reduced to "exit status 1": it must include git's
// output (here, the pathspec warning) so it can be diagnosed without
// guessing.
func TestRunFragmentationPlanErrorIncludesGitOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	plan := &FragmentationPlan{Batches: []PlannedBatch{
		{Layer: "backend", Number: 1, Paths: []string{"missing.go"}, Message: "chore(slice): test"},
	}}

	_, err := RunFragmentationPlan(plan)
	if err == nil {
		t.Fatal("expected an error: the batch path does not exist")
	}
	if !strings.Contains(err.Error(), "pathspec") {
		t.Errorf("the error should include git's real output (pathspec), got: %v", err)
	}
}
