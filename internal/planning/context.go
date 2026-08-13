package planning

import (
	"encoding/json"
	"fmt"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
)

// TreeReader reads a final-state file from an immutable Git tree.
type TreeReader interface {
	Show(tree, path string) (string, error)
}

// GitTreeReader reads Git objects without consulting the worktree contents.
type GitTreeReader struct {
	Repository string
}

// Show reads a blob with git show <tree>:<path>.
func (r GitTreeReader) Show(tree, filePath string) (string, error) {
	filePath, err := gitObjectPath(filePath)
	if err != nil {
		return "", err
	}
	args := []string{}
	if r.Repository != "" {
		args = append(args, "-C", r.Repository)
	}
	args = append(args, "show", tree+":"+filePath)
	output, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", fmt.Errorf("read %s from tree %s: %w", filePath, tree, err)
	}
	return string(output), nil
}

func gitObjectPath(filePath string) (string, error) {
	filePath = filepath.ToSlash(filePath)
	filePath = pathpkg.Clean(filePath)
	if filePath == "." || strings.HasPrefix(filePath, "../") || pathpkg.IsAbs(filePath) {
		return "", fmt.Errorf("invalid Git object path %q", filePath)
	}
	return filePath, nil
}

// ValidationResult is validation evidence supplied to a review context.
type ValidationResult struct {
	Capability string `json:"capability"`
	Command    string `json:"command,omitempty"`
	Exit       int    `json:"exit"`
	Output     string `json:"output,omitempty"`
}

// Symbol identifies a symbol in a final-state path.
type Symbol struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// ContextInput contains the already-discovered evidence for a frozen candidate.
type ContextInput struct {
	Tree              string             `json:"tree"`
	Diff              string             `json:"diff"`
	CommitMessages    []string           `json:"commit_messages,omitempty"`
	Validation        []ValidationResult `json:"validation,omitempty"`
	TouchedPaths      []string           `json:"touched_paths,omitempty"`
	ChangedSymbols    []Symbol           `json:"changed_symbols,omitempty"`
	DirectCallers     []Symbol           `json:"direct_callers,omitempty"`
	PackageTests      []string           `json:"package_tests,omitempty"`
	Contracts         []Symbol           `json:"contracts,omitempty"`
	DiscoverablePaths []string           `json:"discoverable_paths,omitempty"`
}

// FileContent is a final-state file read from the immutable tree.
type FileContent struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ContextLayer is one budgeted evidence layer in review priority order.
type ContextLayer struct {
	Number         int                `json:"number"`
	Name           string             `json:"name"`
	Bytes          int                `json:"bytes"`
	Diff           string             `json:"diff,omitempty"`
	CommitMessages []string           `json:"commit_messages,omitempty"`
	Validation     []ValidationResult `json:"validation,omitempty"`
	Files          []FileContent      `json:"files,omitempty"`
	Symbols        []Symbol           `json:"symbols,omitempty"`
	Callers        []Symbol           `json:"callers,omitempty"`
	Tests          []string           `json:"tests,omitempty"`
	Contracts      []Symbol           `json:"contracts,omitempty"`
	Paths          []string           `json:"paths,omitempty"`
}

// ReviewContext is the deterministic serialized context for a future reviewer.
type ReviewContext struct {
	Tree          string         `json:"tree"`
	Budget        int            `json:"budget,omitempty"`
	BytesUsed     int            `json:"bytes_used"`
	Layers        []ContextLayer `json:"layers"`
	OmittedLayers []int          `json:"omitted_layers,omitempty"`
}

// BuildContext adds evidence layers in priority order. A zero budget is unlimited;
// otherwise layers one and two are always retained and lower-priority layers stop
// at the first layer that does not fit.
func BuildContext(input ContextInput, reader TreeReader, budget int) (ReviewContext, error) {
	if input.Tree == "" {
		return ReviewContext{}, fmt.Errorf("context tree is required")
	}
	if budget < 0 {
		return ReviewContext{}, fmt.Errorf("context budget cannot be negative")
	}
	layers := contextLayerBuilders(input, reader)
	context := ReviewContext{Tree: input.Tree, Budget: budget}
	for index, builder := range layers {
		if index >= 2 && budget > 0 {
			minimum := ContextLayer{Number: builder.number, Name: builder.name}
			if err := measureLayer(&minimum); err != nil {
				return ReviewContext{}, err
			}
			if context.BytesUsed+minimum.Bytes > budget {
				for _, omitted := range layers[index:] {
					context.OmittedLayers = append(context.OmittedLayers, omitted.number)
				}
				break
			}
		}
		layer, err := builder.build()
		if err != nil {
			return ReviewContext{}, err
		}
		if err := measureLayer(&layer); err != nil {
			return ReviewContext{}, err
		}
		if index >= 2 && budget > 0 && context.BytesUsed+layer.Bytes > budget {
			for _, omitted := range layers[index:] {
				context.OmittedLayers = append(context.OmittedLayers, omitted.number)
			}
			break
		}
		context.Layers = append(context.Layers, layer)
		context.BytesUsed += layer.Bytes
	}
	return context, nil
}

type contextLayerBuilder struct {
	number int
	name   string
	build  func() (ContextLayer, error)
}

func contextLayerBuilders(input ContextInput, reader TreeReader) []contextLayerBuilder {
	return []contextLayerBuilder{
		{number: 1, name: "diff_and_commit_messages", build: func() (ContextLayer, error) {
			return ContextLayer{Number: 1, Name: "diff_and_commit_messages", Diff: input.Diff, CommitMessages: append([]string(nil), input.CommitMessages...)}, nil
		}},
		{number: 2, name: "validation_results", build: func() (ContextLayer, error) {
			return ContextLayer{Number: 2, Name: "validation_results", Validation: sortedValidation(input.Validation)}, nil
		}},
		{number: 3, name: "final_touched_files", build: func() (ContextLayer, error) {
			files, err := finalTouchedFiles(input.Tree, input.TouchedPaths, reader)
			if err != nil {
				return ContextLayer{}, err
			}
			return ContextLayer{Number: 3, Name: "final_touched_files", Files: files}, nil
		}},
		{number: 4, name: "changed_symbols_and_direct_callers", build: func() (ContextLayer, error) {
			return ContextLayer{Number: 4, Name: "changed_symbols_and_direct_callers", Symbols: sortedSymbols(input.ChangedSymbols), Callers: sortedSymbols(input.DirectCallers)}, nil
		}},
		{number: 5, name: "affected_package_tests", build: func() (ContextLayer, error) {
			return ContextLayer{Number: 5, Name: "affected_package_tests", Tests: sortedStrings(input.PackageTests)}, nil
		}},
		{number: 6, name: "implicated_contracts_and_interfaces", build: func() (ContextLayer, error) {
			return ContextLayer{Number: 6, Name: "implicated_contracts_and_interfaces", Contracts: sortedSymbols(input.Contracts)}, nil
		}},
		{number: 7, name: "discoverable_paths", build: func() (ContextLayer, error) {
			return ContextLayer{Number: 7, Name: "discoverable_paths", Paths: sortedStrings(input.DiscoverablePaths)}, nil
		}},
	}
}

func finalTouchedFiles(tree string, touchedPaths []string, reader TreeReader) ([]FileContent, error) {
	paths := sortedStrings(touchedPaths)
	if len(paths) > 0 && reader == nil {
		return nil, fmt.Errorf("context tree reader is required for touched paths")
	}
	files := make([]FileContent, 0, len(paths))
	for _, filePath := range paths {
		content, err := reader.Show(tree, filePath)
		if err != nil {
			return nil, err
		}
		files = append(files, FileContent{Path: filePath, Content: content})
	}
	return files, nil
}

func measureLayer(layer *ContextLayer) error {
	for {
		encoded, err := json.Marshal(layer)
		if err != nil {
			return fmt.Errorf("serialize context layer %d: %w", layer.Number, err)
		}
		if layer.Bytes == len(encoded) {
			return nil
		}
		layer.Bytes = len(encoded)
	}
}

func sortedStrings(values []string) []string {
	values = append([]string(nil), values...)
	sort.Strings(values)
	return values
}

func sortedSymbols(symbols []Symbol) []Symbol {
	symbols = append([]Symbol(nil), symbols...)
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].Path != symbols[j].Path {
			return symbols[i].Path < symbols[j].Path
		}
		return symbols[i].Name < symbols[j].Name
	})
	return symbols
}

func sortedValidation(results []ValidationResult) []ValidationResult {
	results = append([]ValidationResult(nil), results...)
	sort.Slice(results, func(i, j int) bool {
		if results[i].Capability != results[j].Capability {
			return results[i].Capability < results[j].Capability
		}
		if results[i].Command != results[j].Command {
			return results[i].Command < results[j].Command
		}
		return results[i].Output < results[j].Output
	})
	return results
}
