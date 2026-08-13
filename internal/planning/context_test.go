package planning

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestBuildContextPreservesRequiredLayersAndTruncatesLowerPriorities(t *testing.T) {
	reader := &recordedTreeReader{contents: map[string]string{
		"tree-1:internal/example/example.go": "package example\n\nfunc Current() {}\n",
	}}
	input := ContextInput{
		Tree:           "tree-1",
		Diff:           "diff --git a/internal/example/example.go b/internal/example/example.go\n",
		CommitMessages: []string{"feat(example): add current behavior"},
		Validation:     []ValidationResult{{Capability: "test", Exit: 1, Output: "--- FAIL: TestExample\n"}},
		TouchedPaths:   []TouchedPath{{Path: "internal/example/example.go"}},
		ChangedSymbols: []Symbol{{Name: "Current", Path: "internal/example/example.go"}},
		DirectCallers:  []Symbol{{Name: "Run", Path: "cmd/sentinel/main.go"}},
		PackageTests:   []string{"internal/example/example_test.go"},
		Contracts:      []Symbol{{Name: "Runner", Path: "internal/example/example.go"}},
		DiscoverablePaths: []string{
			"cmd/sentinel/main.go",
			"internal/example/example.go",
		},
	}

	required, err := BuildContext(input, reader, 0)
	if err != nil {
		t.Fatal(err)
	}
	budget := required.Layers[0].Bytes + required.Layers[1].Bytes

	context, err := BuildContext(input, reader, budget)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := layerNumbers(context.Layers), []int{1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("included layers = %v, want %v", got, want)
	}
	if context.BytesUsed != budget {
		t.Fatalf("bytes used = %d, want %d", context.BytesUsed, budget)
	}
	for _, layer := range context.Layers {
		encoded, err := json.Marshal(layer)
		if err != nil {
			t.Fatal(err)
		}
		if layer.Bytes != len(encoded) {
			t.Fatalf("layer %d bytes = %d, serialized bytes = %d", layer.Number, layer.Bytes, len(encoded))
		}
	}
	if !reflect.DeepEqual(reader.calls, []string{"tree-1:internal/example/example.go"}) {
		t.Fatalf("tree reads = %v", reader.calls)
	}
}

func TestBuildContextReadsFinalContentsFromTree(t *testing.T) {
	reader := &recordedTreeReader{contents: map[string]string{
		"tree-1:internal/example/example.go": "package example\n\nfunc Current() {}\n",
	}}

	context, err := BuildContext(ContextInput{
		Tree:         "tree-1",
		Diff:         "diff\n",
		TouchedPaths: []TouchedPath{{Path: "internal/example/example.go"}},
	}, reader, 0)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := context.Layers[2].Files[0].Content, "package example\n\nfunc Current() {}\n"; got != want {
		t.Fatalf("final content = %q, want %q", got, want)
	}
}

func TestBuildContextSkipsUnreadableFinalFilesWhenBudgetEndsAtRequiredLayers(t *testing.T) {
	input := ContextInput{
		Tree:           "tree-1",
		Diff:           "diff --git a/internal/example/example.go b/internal/example/example.go\n",
		CommitMessages: []string{"feat(example): add current behavior"},
		Validation:     []ValidationResult{{Capability: "test", Exit: 1, Output: "--- FAIL: TestExample\n"}},
		TouchedPaths:   []TouchedPath{{Path: "internal/example/unavailable.go"}},
	}
	requiredInput := input
	requiredInput.TouchedPaths = nil
	required, err := BuildContext(requiredInput, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	budget := required.Layers[0].Bytes + required.Layers[1].Bytes
	reader := &recordedTreeReader{err: errors.New("final file is unavailable")}

	context, err := BuildContext(input, reader, budget)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := layerNumbers(context.Layers), []int{1, 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("included layers = %v, want %v", got, want)
	}
	if got, want := context.OmittedLayers, []int{3, 4, 5, 6, 7}; !reflect.DeepEqual(got, want) {
		t.Fatalf("omitted layers = %v, want %v", got, want)
	}
	if len(reader.calls) != 0 {
		t.Fatalf("tree reads = %v, want none", reader.calls)
	}
}

func TestBuildContextSkipsDeletedTouchedFiles(t *testing.T) {
	reader := &recordedTreeReader{contents: map[string]string{
		"tree-1:internal/example/existing.go": "package example\n",
	}}

	context, err := BuildContext(ContextInput{
		Tree: "tree-1",
		Diff: "diff --git a/internal/example/deleted.go b/internal/example/deleted.go\ndeleted file mode 100644\n",
		TouchedPaths: []TouchedPath{
			{Path: "internal/example/deleted.go", Deleted: true},
			{Path: "internal/example/existing.go"},
		},
	}, reader, 0)
	if err != nil {
		t.Fatal(err)
	}

	if got, want := context.Layers[2].Files, []FileContent{{Path: "internal/example/existing.go", Content: "package example\n"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("final touched files = %v, want %v", got, want)
	}
	if got, want := reader.calls, []string{"tree-1:internal/example/existing.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tree reads = %v, want %v", got, want)
	}
}

func TestBuildContextPropagatesReaderErrorForFinalTouchedFile(t *testing.T) {
	reader := &recordedTreeReader{err: errors.New("final file is unavailable")}

	_, err := BuildContext(ContextInput{
		Tree:         "tree-1",
		TouchedPaths: []TouchedPath{{Path: "internal/example/unavailable.go"}},
	}, reader, 0)
	if !errors.Is(err, reader.err) {
		t.Fatalf("BuildContext error = %v, want %v", err, reader.err)
	}
}

func TestBuildContextSerializesValidationExitDeterministically(t *testing.T) {
	results := []ValidationResult{
		{Capability: "test", Command: "go test ./...", Exit: 1, Output: "failed"},
		{Capability: "test", Command: "go test ./...", Exit: 0, Output: "failed"},
	}
	first, err := BuildContext(ContextInput{Tree: "tree-1", Validation: results}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildContext(ContextInput{Tree: "tree-1", Validation: []ValidationResult{results[1], results[0]}}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("serialized contexts differ:\n%s\n%s", firstJSON, secondJSON)
	}
}

type recordedTreeReader struct {
	contents map[string]string
	calls    []string
	err      error
}

func (r *recordedTreeReader) Show(tree, path string) (string, error) {
	key := tree + ":" + path
	r.calls = append(r.calls, key)
	if r.err != nil {
		return "", r.err
	}
	return r.contents[key], nil
}

func layerNumbers(layers []ContextLayer) []int {
	numbers := make([]int, len(layers))
	for i, layer := range layers {
		numbers[i] = layer.Number
	}
	return numbers
}
