package change

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
)

type simboloAST struct{ completo, api string }

func simbolosCambiados(git LectorGit, cambios []cambioRuta, base, head string) ChangeSymbols {
	resultado := ChangeSymbols{Complete: true}
	for _, cambio := range cambios {
		antes, okAntes := simbolosEnRevision(git, base, cambio.antes)
		despues, okDespues := simbolosEnRevision(git, head, cambio.despues)
		if !okAntes || !okDespues {
			resultado.Complete = false
			continue
		}
		for clave, previo := range antes {
			actual, existe := despues[clave]
			switch {
			case !existe:
				resultado.Deleted++
				if previo.api != "" {
					resultado.ExportedTouched++
				}
			case previo.completo != actual.completo:
				resultado.Modified++
				if previo.api != actual.api {
					resultado.ExportedTouched++
				}
			}
		}
		for clave, actual := range despues {
			if _, existe := antes[clave]; !existe {
				resultado.Added++
				if actual.api != "" {
					resultado.ExportedTouched++
				}
			}
		}
	}
	return resultado
}

func simbolosEnRevision(git LectorGit, revision, ruta string) (map[string]simboloAST, bool) {
	resultado := map[string]simboloAST{}
	if ruta == "" || filepath.Ext(ruta) != ".go" {
		return resultado, true
	}
	contenido, err := git("show", revision+":"+filepath.ToSlash(ruta))
	if err != nil {
		return nil, false
	}
	fset := token.NewFileSet()
	archivo, err := parser.ParseFile(fset, ruta, contenido, 0)
	if err != nil {
		return nil, false
	}
	for _, declaracion := range archivo.Decls {
		switch d := declaracion.(type) {
		case *ast.FuncDecl:
			clave := "func:" + d.Name.Name
			if d.Recv != nil {
				clave += ":" + imprimirAST(fset, d.Recv.List[0].Type)
			}
			completo := imprimirAST(fset, d)
			api := ""
			if ast.IsExported(d.Name.Name) {
				api = imprimirAPI(fset, d.Type)
			}
			resultado[clave] = simboloAST{completo, api}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				for _, nombre := range nombresDeSpec(spec) {
					completo := imprimirAST(fset, spec)
					api := ""
					if ast.IsExported(nombre) {
						api = imprimirAPI(fset, spec)
					}
					resultado[d.Tok.String()+":"+nombre] = simboloAST{completo, api}
				}
			}
		}
	}
	return resultado, true
}

func imprimirAPI(fset *token.FileSet, nodo ast.Node) string {
	ast.Inspect(nodo, func(n ast.Node) bool {
		if funcion, ok := n.(*ast.FuncType); ok {
			for _, lista := range []*ast.FieldList{funcion.TypeParams, funcion.Params, funcion.Results} {
				if lista != nil {
					for _, campo := range lista.List {
						campo.Names = nil
					}
				}
			}
		}
		return true
	})
	return imprimirAST(fset, nodo)
}

func nombresDeSpec(spec ast.Spec) []string {
	if tipo, ok := spec.(*ast.TypeSpec); ok {
		return []string{tipo.Name.Name}
	}
	valores, ok := spec.(*ast.ValueSpec)
	if !ok {
		return nil
	}
	var nombres []string
	for _, nombre := range valores.Names {
		nombres = append(nombres, nombre.Name)
	}
	return nombres
}

func imprimirAST(fset *token.FileSet, nodo any) string {
	var salida bytes.Buffer
	format.Node(&salida, fset, nodo)
	return salida.String()
}
