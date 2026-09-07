package change

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
)

type astSymbol struct {
	full, api string
	apiKnown  bool
}

func changedSymbols(git GitReader, changes []pathChange, base, head string, classify func(string) string) ChangeSymbols {
	result := ChangeSymbols{Complete: true}
	for _, change := range changes {
		before, okBefore := symbolsInRevision(git, base, change.before, classify)
		after, okAfter := symbolsInRevision(git, head, change.after, classify)
		if !okBefore || !okAfter {
			result.Complete = false
			continue
		}
		for key, previous := range before {
			current, exists := after[key]
			switch {
			case key == "package" && !exists:
				continue
			case !exists:
				result.Deleted++
				result.Complete = result.Complete && previous.apiKnown
				if previous.api != "" {
					result.ExportedTouched++
				}
			case previous.full != current.full:
				result.Modified++
				result.Complete = result.Complete && previous.apiKnown && current.apiKnown
				if previous.api != current.api {
					result.ExportedTouched++
				}
			}
		}
		for key, current := range after {
			if _, exists := before[key]; !exists {
				if key == "package" {
					continue
				}
				result.Added++
				result.Complete = result.Complete && current.apiKnown
				if current.api != "" {
					result.ExportedTouched++
				}
			}
		}
	}
	return result
}

func symbolsInRevision(git GitReader, revision, path string, classify func(string) string) (map[string]astSymbol, bool) {
	result := map[string]astSymbol{}
	if path == "" || classify(path) != ClassSource {
		return result, true
	}
	if filepath.Ext(path) != ".go" {
		return nil, false
	}
	content, err := git("show", revision+":"+filepath.ToSlash(path))
	if err != nil {
		return nil, false
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, content, 0)
	if err != nil {
		return nil, false
	}
	result["package"] = astSymbol{file.Name.Name, file.Name.Name, true}
	reachableTypes := reachablePrivateTypes(file)
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			key := "func:" + d.Name.Name
			if d.Recv != nil {
				key += ":" + printAST(fset, d.Recv.List[0].Type)
			}
			full := printAST(fset, d)
			api := ""
			if ast.IsExported(d.Name.Name) {
				api = printAPI(fset, d.Type)
			}
			result[key] = astSymbol{full, api, true}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				for _, name := range specNames(spec) {
					full := printAST(fset, spec)
					api := ""
					known := true
					if ast.IsExported(name) || reachableTypes[name] {
						api = printAPI(fset, spec)
					} else if _, isType := spec.(*ast.TypeSpec); isType {
						known = false
					}
					result[d.Tok.String()+":"+name] = astSymbol{full, api, known}
				}
			}
		}
	}
	return result, true
}

func printAPI(fset *token.FileSet, node ast.Node) string {
	text := printAST(fset, node)
	copy, copySet := copyAPINode(text, node)
	if copy == nil {
		return text
	}
	ast.Inspect(copy, func(n ast.Node) bool {
		if function, ok := n.(*ast.FuncType); ok {
			for _, list := range []*ast.FieldList{function.TypeParams, function.Params, function.Results} {
				if list != nil {
					for _, field := range list.List {
						field.Names = nil
					}
				}
			}
		}
		return true
	})
	return printAST(copySet, copy)
}

func copyAPINode(text string, original ast.Node) (ast.Node, *token.FileSet) {
	fset := token.NewFileSet()
	if _, ok := original.(*ast.FuncType); ok {
		file, err := parser.ParseFile(fset, "api.go", "package api\nfunc placeholder"+strings.TrimPrefix(text, "func"), 0)
		if err == nil {
			return file.Decls[0].(*ast.FuncDecl).Type, fset
		}
	}
	prefix := "var "
	if _, ok := original.(*ast.TypeSpec); ok {
		prefix = "type "
	}
	file, err := parser.ParseFile(fset, "api.go", "package api\n"+prefix+text, 0)
	if err == nil {
		return file.Decls[0].(*ast.GenDecl).Specs[0], fset
	}
	return nil, fset
}

func reachablePrivateTypes(file *ast.File) map[string]bool {
	types := map[string]*ast.TypeSpec{}
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok {
			for _, spec := range gen.Specs {
				if typeSpec, ok := spec.(*ast.TypeSpec); ok {
					types[typeSpec.Name.Name] = typeSpec
				}
			}
		}
	}
	reachable := map[string]bool{}
	var queue []string
	mark := func(n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && types[id.Name] != nil && !reachable[id.Name] {
				reachable[id.Name] = true
				queue = append(queue, id.Name)
			}
			return true
		})
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if ast.IsExported(d.Name.Name) {
				mark(d.Type)
				if d.Recv != nil {
					mark(d.Recv)
				}
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				for _, name := range specNames(spec) {
					if ast.IsExported(name) {
						mark(spec)
					}
				}
			}
		}
	}
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		mark(types[name].Type)
	}
	return reachable
}

func specNames(spec ast.Spec) []string {
	if typeSpec, ok := spec.(*ast.TypeSpec); ok {
		return []string{typeSpec.Name.Name}
	}
	values, ok := spec.(*ast.ValueSpec)
	if !ok {
		return nil
	}
	var names []string
	for _, name := range values.Names {
		names = append(names, name.Name)
	}
	return names
}

func printAST(fset *token.FileSet, node any) string {
	var output bytes.Buffer
	format.Node(&output, fset, node)
	return output.String()
}
