package graph

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const limiteContextoCodeGraph = 32 << 10

type ejecutorCodeGraph func(context.Context, string, []string, string, []string, int) ([]byte, error)

// ProveedorCodeGraph selecciona contexto heurístico; no implementa GraphProvider.
type ProveedorCodeGraph struct {
	raiz, ejecutable string
	limite           int
	ejecutar         ejecutorCodeGraph
}

func DetectarProveedorCodeGraph(raiz string) ContextProvider {
	return detectarProveedorCodeGraph(raiz, exec.LookPath, ejecutarCodeGraph)
}

func detectarProveedorCodeGraph(raiz string, lookup func(string) (string, error), ejecutar ejecutorCodeGraph) ContextProvider {
	info, err := os.Stat(filepath.Join(raiz, ".codegraph"))
	if err != nil || !info.IsDir() {
		return nil
	}
	binario, err := lookup("codegraph")
	if err != nil {
		return nil
	}
	return &ProveedorCodeGraph{raiz: raiz, ejecutable: binario, limite: limiteContextoCodeGraph, ejecutar: ejecutar}
}

func (p *ProveedorCodeGraph) Nombre() string { return "codegraph" }

func (p *ProveedorCodeGraph) Contexto(rutas []string) ([]ReferenciaContexto, error) {
	var validas []string
	for _, ruta := range rutas {
		limpia := filepath.Clean(ruta)
		if ruta != "" && !filepath.IsAbs(limpia) && limpia != ".." && !strings.HasPrefix(limpia, ".."+string(filepath.Separator)) && !strings.HasPrefix(ruta, "-") && !strings.ContainsAny(ruta, "\x00\r\n") {
			validas = append(validas, filepath.ToSlash(limpia))
		}
	}
	if len(validas) == 0 || p.ejecutar == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	salida, err := p.ejecutar(ctx, p.ejecutable, []string{"explore", "-p", p.raiz, "--max-files", "4", "Reviewer context for: " + strings.Join(validas, ", ")}, p.raiz, entornoCodeGraph(p.ejecutable), p.limite)
	texto := string(salida)
	if err != nil || len(salida) == 0 || len(salida) >= p.limite || strings.HasPrefix(texto, "⚠️") || !strings.Contains(texto, "**Exploration:") || !strings.Contains(texto, "**Source Code**") {
		return nil, nil
	}
	return []ReferenciaContexto{{Explicacion: rutaAbsoluta.ReplaceAllString(strings.ReplaceAll(texto, p.raiz, "."), "$1[path]")}}, nil
}

func entornoCodeGraph(ejecutable string) []string {
	env := []string{"PATH=" + filepath.Dir(ejecutable)}
	for _, nombre := range []string{"SystemRoot", "TEMP", "TMP", "TMPDIR"} {
		if valor := os.Getenv(nombre); valor != "" {
			env = append(env, nombre+"="+valor)
		}
	}
	return env
}

var rutaAbsoluta = regexp.MustCompile(`(?m)(^|[\s(\x60"'])(?:[A-Za-z]:[\\/]|/)[^\s\x60"']+`)

type escritorLimitado struct {
	bytes.Buffer
	restante int
}

func (w *escritorLimitado) Write(p []byte) (int, error) {
	if len(p) > w.restante {
		return 0, io.ErrShortBuffer
	}
	w.restante -= len(p)
	return w.Buffer.Write(p)
}

func ejecutarCodeGraph(ctx context.Context, ejecutable string, args []string, dir string, env []string, limite int) ([]byte, error) {
	w := &escritorLimitado{restante: limite}
	cmd := exec.CommandContext(ctx, ejecutable, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, w, io.Discard
	err := cmd.Run()
	return w.Bytes(), err
}
