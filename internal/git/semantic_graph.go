package git

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"

	internalchange "github.com/ISeoane-Quental/vas.sentinel/internal/change"
)

func readSemanticFiles(changes []PlannedChange) ([]semanticFile, bool) {
	files := make([]semanticFile, len(changes))
	opaque := false
	for i, change := range changes {
		file := semanticFile{change: change, path: change.Path, pkg: packageKey(change.Path, ""), class: ClaseArchivo(change.Path), layer: ClasificarCapa(change.Path), test: strings.HasSuffix(change.Path, "_test.go"), defs: map[string]bool{}}
		if !strings.HasSuffix(strings.ToLower(change.Path), ".go") {
			files[i] = file
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.FromSlash(change.Path), nil, 0)
		if err != nil {
			file.opaque = true
			opaque = true
			files[i] = file
			continue
		}
		file.pkg = packageKey(change.Path, parsed.Name.Name)
		for _, declaration := range parsed.Decls {
			switch declaration := declaration.(type) {
			case *ast.FuncDecl:
				file.defs[declaration.Name.Name] = true
			case *ast.GenDecl:
				for _, spec := range declaration.Specs {
					switch spec := spec.(type) {
					case *ast.TypeSpec:
						file.defs[spec.Name.Name] = true
					case *ast.ValueSpec:
						for _, name := range spec.Names {
							file.defs[name.Name] = true
						}
					}
				}
			}
		}
		aliases := map[string]bool{}
		for _, spec := range parsed.Imports {
			alias := pathpkg.Base(strings.Trim(spec.Path.Value, "\""))
			if spec.Name != nil {
				alias = spec.Name.Name
			}
			if alias != "_" && alias != "." {
				aliases[alias] = true
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			if selector, ok := node.(*ast.SelectorExpr); ok {
				if identifier, ok := selector.X.(*ast.Ident); ok && aliases[identifier.Name] {
					file.refs = append(file.refs, semanticRef{alias: identifier.Name, name: selector.Sel.Name})
				}
			}
			if identifier, ok := node.(*ast.Ident); ok {
				file.refs = append(file.refs, semanticRef{name: identifier.Name})
			}
			return true
		})
		files[i] = file
	}
	return files, opaque
}

func packageKey(path, name string) string {
	dir := pathpkg.Dir(filepath.ToSlash(path))
	if dir == "." {
		dir = ""
	}
	return dir + "#" + name
}

func semanticDependencies(files []semanticFile) semanticGraph {
	graph := semanticGraph{files: files, deps: map[int]map[int]bool{}, sets: newSemanticSets(len(files))}
	definitions := map[string]map[string][]int{}
	for i, file := range files {
		if definitions[file.pkg] == nil {
			definitions[file.pkg] = map[string][]int{}
		}
		for name := range file.defs {
			definitions[file.pkg][name] = append(definitions[file.pkg][name], i)
		}
	}
	for i, file := range files {
		if !file.opaque {
			continue
		}
		for j, candidate := range files {
			if strings.HasSuffix(strings.ToLower(candidate.path), ".go") {
				graph.sets.union(i, j)
			}
		}
	}
	for i, file := range files {
		for _, ref := range file.refs {
			pkg := file.pkg
			if ref.alias != "" {
				pkg = importedPackage(ref.alias, files)
			}
			for _, dependency := range definitions[pkg][ref.name] {
				if dependency != i {
					if graph.deps[i] == nil {
						graph.deps[i] = map[int]bool{}
					}
					graph.deps[i][dependency] = true
					graph.sets.union(i, dependency)
				}
			}
		}
	}
	for i, file := range files {
		if !file.test {
			continue
		}
		for j, candidate := range files {
			if candidate.test || i == j || candidate.pkg != file.pkg {
				continue
			}
			if strings.TrimSuffix(pathpkg.Base(file.path), "_test.go") == strings.TrimSuffix(pathpkg.Base(candidate.path), pathpkg.Ext(candidate.path)) {
				graph.sets.union(i, j)
			}
		}
	}
	return graph
}

func importedPackage(alias string, files []semanticFile) string {
	for _, file := range files {
		if pathpkg.Base(file.pkg[:strings.LastIndex(file.pkg, "#")]) == alias || strings.HasSuffix(file.pkg, "#"+alias) {
			return file.pkg
		}
	}
	return ""
}

func semanticCohesion(files []semanticFile, reader internalchange.LectorGit) (map[string]int, string) {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.path)
	}
	result, err := internalchange.Cohesion(paths, reader)
	if err != nil {
		return map[string]int{}, fmt.Sprintf("Structural cohesion was unavailable; deterministic fallback used: %v.", err)
	}
	sort.Slice(result.Grupos, func(i, j int) bool {
		return smallestPath(result.Grupos[i]) < smallestPath(result.Grupos[j])
	})
	ranks := map[string]int{}
	for rank, group := range result.Grupos {
		for _, path := range group {
			ranks[path] = rank
		}
	}
	return ranks, "Structural cohesion informed deterministic grouping before file-class and line-count fallback."
}

func smallestPath(paths []string) string {
	if len(paths) == 0 {
		return "~"
	}
	result := paths[0]
	for _, path := range paths[1:] {
		if path < result {
			result = path
		}
	}
	return result
}

func validateSemanticBoundaries(boundaries []SemanticSliceBoundary, files []semanticFile, graph semanticGraph) (map[int]int, error) {
	assigned := map[int]int{}
	ids := map[string]bool{}
	paths := map[string]int{}
	for i, file := range files {
		paths[changeKey(file.change.Path, "")] = i
		paths[changeKey(file.change.Path, file.change.OldPath)] = i
	}
	for b, boundary := range boundaries {
		if boundary.ID == "" || ids[boundary.ID] {
			return nil, fmt.Errorf("semantic boundaries require unique non-empty IDs")
		}
		ids[boundary.ID] = true
		indices := map[int]bool{}
		for _, path := range boundary.Paths {
			i, ok := paths[changeKey(filepath.ToSlash(filepath.Clean(filepath.FromSlash(path))), "")]
			if !ok {
				return nil, fmt.Errorf("semantic boundary %q references missing path %q", boundary.ID, path)
			}
			indices[i] = true
		}
		for _, selector := range boundary.Selectors {
			i, ok := paths[changeKey(selector.Path, selector.OldPath)]
			if !ok {
				return nil, fmt.Errorf("semantic boundary %q references missing selector %q", boundary.ID, selector.Path)
			}
			indices[i] = true
		}
		if len(indices) == 0 {
			return nil, fmt.Errorf("semantic boundary %q has no selections", boundary.ID)
		}
		for i := range indices {
			if previous, ok := assigned[i]; ok && previous != b {
				return nil, fmt.Errorf("semantic boundaries overlap at %q", files[i].path)
			}
			assigned[i] = b
		}
	}
	for dependent, dependencies := range graph.deps {
		for dependency := range dependencies {
			left, leftOK := assigned[dependency]
			right, rightOK := assigned[dependent]
			if leftOK && rightOK && left != right {
				return nil, fmt.Errorf("semantic boundaries split compile dependency %q -> %q", files[dependency].path, files[dependent].path)
			}
		}
	}
	return assigned, nil
}

func semanticComponents(files []semanticFile, graph semanticGraph, boundaries map[int]int, ranks map[string]int) []semanticComponent {
	for i, boundary := range boundaries {
		for j, other := range boundaries {
			if boundary == other {
				graph.sets.union(i, j)
			}
		}
	}
	groups := map[int][]int{}
	for i := range files {
		groups[graph.sets.find(i)] = append(groups[graph.sets.find(i)], i)
	}
	components := make([]semanticComponent, 0, len(groups))
	for _, ids := range groups {
		component := semanticComponent{ids: ids, boundary: len(files), rank: len(files), class: "~", first: "~"}
		for _, i := range ids {
			if boundary, ok := boundaries[i]; ok && boundary < component.boundary {
				component.boundary = boundary
			}
			if rank := ranks[files[i].path]; rank < component.rank {
				component.rank = rank
			}
			if rangoClase(files[i].class) < rangoClase(component.class) {
				component.class = files[i].class
			}
			if files[i].path < component.first {
				component.first = files[i].path
			}
			component.lines += files[i].change.AddedLines
		}
		component.ordered = semanticOrder(ids, files, graph.deps, ranks)
		components = append(components, component)
	}
	sort.SliceStable(components, func(i, j int) bool {
		left, right := components[i], components[j]
		if left.boundary != right.boundary {
			return left.boundary < right.boundary
		}
		if left.rank != right.rank {
			return left.rank < right.rank
		}
		if rangoClase(left.class) != rangoClase(right.class) {
			return rangoClase(left.class) < rangoClase(right.class)
		}
		return left.first < right.first
	})
	return components
}

func semanticOrder(ids []int, files []semanticFile, deps map[int]map[int]bool, ranks map[string]int) []int {
	allowed := map[int]bool{}
	for _, i := range ids {
		allowed[i] = true
	}
	var result []int
	for len(result) < len(ids) {
		var ready []int
		for _, i := range ids {
			if containsInt(result, i) {
				continue
			}
			readyHere := true
			for dependency := range deps[i] {
				if allowed[dependency] && !containsInt(result, dependency) {
					readyHere = false
				}
			}
			if readyHere {
				ready = append(ready, i)
			}
		}
		if len(ready) == 0 {
			for _, i := range ids {
				if !containsInt(result, i) {
					ready = append(ready, i)
				}
			}
		}
		sort.SliceStable(ready, func(i, j int) bool { return semanticLess(ready[i], ready[j], files, ranks) })
		result = append(result, ready[0])
	}
	return result
}

func semanticLess(left, right int, files []semanticFile, ranks map[string]int) bool {
	if ranks[files[left].path] != ranks[files[right].path] {
		return ranks[files[left].path] < ranks[files[right].path]
	}
	if rangoClase(files[left].class) != rangoClase(files[right].class) {
		return rangoClase(files[left].class) < rangoClase(files[right].class)
	}
	if files[left].layer != files[right].layer {
		return files[left].layer < files[right].layer
	}
	return files[left].path < files[right].path
}

func newSemanticSets(size int) semanticSets {
	parent := make([]int, size)
	for i := range parent {
		parent[i] = i
	}
	return semanticSets{parent: parent}
}

func (sets *semanticSets) find(index int) int {
	if sets.parent[index] != index {
		sets.parent[index] = sets.find(sets.parent[index])
	}
	return sets.parent[index]
}

func (sets *semanticSets) union(left, right int) {
	left, right = sets.find(left), sets.find(right)
	if left != right {
		sets.parent[right] = left
	}
}
