package graph

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectarCodeGraphRequiereIndiceYCLI(t *testing.T) {
	raiz := t.TempDir()
	if detectarProveedorCodeGraph(raiz, func(string) (string, error) { return "codegraph", nil }, nil) != nil {
		t.Fatal("proveedor presente sin .codegraph")
	}
	_ = os.Mkdir(filepath.Join(raiz, ".codegraph"), 0o755)
	if detectarProveedorCodeGraph(raiz, func(string) (string, error) { return "", errors.New("no CLI") }, nil) != nil {
		t.Fatal("proveedor presente sin CLI")
	}
}

func TestProveedorCodeGraphDegradaYLimitaSalida(t *testing.T) {
	if _, autoriza := any(&ProveedorCodeGraph{}).(GraphProvider); autoriza {
		t.Fatal("ProveedorCodeGraph satisface GraphProvider")
	}
	fallo := &ProveedorCodeGraph{ejecutar: func(context.Context, string, []string, string, []string, int) ([]byte, error) {
		return nil, errors.New("timeout")
	}}
	if refs, err := fallo.Contexto([]string{"internal/review/engine.go"}); err != nil || len(refs) != 0 {
		t.Fatalf("fallo = (%v, %v)", refs, err)
	}
	proveedor := &ProveedorCodeGraph{raiz: t.TempDir(), ejecutable: "codegraph", limite: 128,
		ejecutar: func(_ context.Context, _ string, _ []string, _ string, _ []string, max int) ([]byte, error) {
			return []byte("**Exploration:** ok\n**Source Code**\n" + strings.Repeat("x", max)), nil
		}}
	if refs, err := proveedor.Contexto([]string{"internal/review/engine.go"}); err != nil || len(refs) != 0 {
		t.Fatalf("salida sobre límite no degradó: (%v, %v)", refs, err)
	}
}

func TestProveedorCodeGraphDevuelveContextoSanitizado(t *testing.T) {
	raiz := t.TempDir()
	proveedor := &ProveedorCodeGraph{raiz: raiz, ejecutable: "codegraph", limite: 1024,
		ejecutar: func(_ context.Context, _ string, args []string, _ string, _ []string, _ int) ([]byte, error) {
			if args[0] != "explore" {
				t.Fatalf("argumentos inesperados: %v", args)
			}
			return []byte("**Exploration:** reviewer context\n**Source Code**\n`internal/review/engine.go`"), nil
		}}
	refs, err := proveedor.Contexto([]string{"internal/review/engine.go"})
	if err != nil || len(refs) != 1 || !strings.Contains(refs[0].Explicacion, "engine.go") {
		t.Fatalf("contexto = (%v, %v)", refs, err)
	}
	if strings.Contains(refs[0].Explicacion, raiz) {
		t.Fatal("el contexto filtró la ruta absoluta")
	}
}
