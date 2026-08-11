package change

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
)

type simboloAST struct {
	completo, api  string
	apiDeterminada bool
}

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
			case clave == "package" && !existe:
				continue
			case !existe:
				resultado.Deleted++
				resultado.Complete = resultado.Complete && previo.apiDeterminada
				if previo.api != "" {
					resultado.ExportedTouched++
				}
			case previo.completo != actual.completo:
				resultado.Modified++
				resultado.Complete = resultado.Complete && previo.apiDeterminada && actual.apiDeterminada
				if previo.api != actual.api {
					resultado.ExportedTouched++
				}
			}
		}
		for clave, actual := range despues {
			if _, existe := antes[clave]; !existe {
				if clave == "package" {
					continue
				}
				resultado.Added++
				resultado.Complete = resultado.Complete && actual.apiDeterminada
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
	if ruta == "" || ClasificarPorRuta(ruta, ReglasPorDefecto()) != ClaseSource {
		return resultado, true
	}
	if filepath.Ext(ruta) != ".go" {
		return nil, false
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
	resultado["package"] = simboloAST{archivo.Name.Name, archivo.Name.Name, true}
	tiposAlcanzables := tiposPrivadosAlcanzables(archivo)
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
			resultado[clave] = simboloAST{completo, api, true}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				for _, nombre := range nombresDeSpec(spec) {
					completo := imprimirAST(fset, spec)
					api := ""
					determinada := true
					if ast.IsExported(nombre) || tiposAlcanzables[nombre] {
						api = imprimirAPI(fset, spec)
					} else if _, esTipo := spec.(*ast.TypeSpec); esTipo {
						determinada = false
					}
					resultado[d.Tok.String()+":"+nombre] = simboloAST{completo, api, determinada}
				}
			}
		}
	}
	return resultado, true
}

func imprimirAPI(fset *token.FileSet, nodo ast.Node) string {
	texto := imprimirAST(fset, nodo)
	copia, copiaSet := copiarNodoAPI(texto, nodo)
	if copia == nil {
		return texto
	}
	ast.Inspect(copia, func(n ast.Node) bool {
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
	return imprimirAST(copiaSet, copia)
}

func copiarNodoAPI(texto string, original ast.Node) (ast.Node, *token.FileSet) {
	fset := token.NewFileSet()
	if _, ok := original.(*ast.FuncType); ok {
		nodo, err := parser.ParseExprFrom(fset, "api.go", texto, 0)
		if err == nil {
			return nodo, fset
		}
	}
	prefijo := "var "
	if _, ok := original.(*ast.TypeSpec); ok {
		prefijo = "type "
	}
	archivo, err := parser.ParseFile(fset, "api.go", "package api\n"+prefijo+texto, 0)
	if err == nil {
		return archivo.Decls[0].(*ast.GenDecl).Specs[0], fset
	}
	return nil, fset
}

func tiposPrivadosAlcanzables(archivo *ast.File) map[string]bool {
	tipos := map[string]*ast.TypeSpec{}
	for _, decl := range archivo.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok {
			for _, spec := range gen.Specs {
				if tipo, ok := spec.(*ast.TypeSpec); ok {
					tipos[tipo.Name.Name] = tipo
				}
			}
		}
	}
	alcanzables := map[string]bool{}
	marcar := func(n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && tipos[id.Name] != nil {
				alcanzables[id.Name] = true
			}
			return true
		})
	}
	for _, decl := range archivo.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if ast.IsExported(d.Name.Name) {
				marcar(d.Type)
				if d.Recv != nil {
					marcar(d.Recv)
				}
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				for _, nombre := range nombresDeSpec(spec) {
					if ast.IsExported(nombre) {
						marcar(spec)
					}
				}
			}
		}
	}
	for cambio := true; cambio; {
		cambio = false
		for nombre := range alcanzables {
			antes := len(alcanzables)
			marcar(tipos[nombre].Type)
			cambio = cambio || len(alcanzables) != antes
		}
	}
	return alcanzables
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
