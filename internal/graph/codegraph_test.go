package graph

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
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
	if detectarProveedorCodeGraph(raiz, func(nombre string) (string, error) { return filepath.Join(raiz, nombre), nil }, nil) == nil {
		t.Fatal("proveedor ausente con índice y ejecutables")
	}
}

func TestProveedorCodeGraphDegradaAnteEstadoNoVinculado(t *testing.T) {
	if _, autoriza := any(&ProveedorCodeGraph{}).(GraphProvider); autoriza {
		t.Fatal("ProveedorCodeGraph satisface GraphProvider")
	}
	casos := []string{
		`{"initialized":false,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`,
		`{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":1,"modified":0,"removed":0},"worktreeMismatch":null}`,
		`{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":{}}`,
		`{"initialized":true,"projectPath":"/otro/proyecto","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`,
	}
	for _, estado := range casos {
		p, _ := proveedorConRespuestas(t, estado)
		refs, err := p.Contexto("head", []string{"a.go"})
		if err == nil || len(refs) != 0 {
			t.Fatalf("estado inseguro produjo contexto: (%v, %v)", refs, err)
		}
	}
}

func TestProveedorCodeGraphOmiteSHAAnterior(t *testing.T) {
	p, fake := proveedorConRespuestas(t, `{}`)
	refs, err := p.Contexto("older", []string{"a.go"})
	if err == nil || len(refs) != 0 || len(fake.llamadas) != 1 {
		t.Fatalf("SHA anterior no se omitió: refs=%v err=%v llamadas=%d", refs, err, len(fake.llamadas))
	}
}

func TestCodeGraphProviderExposesSkipReasons(t *testing.T) {
	const cleanState = `{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`
	cases := []struct {
		name   string
		want   string
		mutate func(*fakeCG)
	}{
		{
			name: "audited HEAD does not match current HEAD",
			want: "head_mismatch",
			mutate: func(fake *fakeCG) {
				fake.respuestas[0] = []byte("other\n")
			},
		},
		{
			name: "worktree is dirty",
			want: "dirty_worktree",
			mutate: func(fake *fakeCG) {
				fake.respuestas[1] = []byte(" M internal/file.go\n")
			},
		},
		{
			name: "index is uninitialized",
			want: "uninitialized_index",
			mutate: func(fake *fakeCG) {
				fake.respuestas[2] = []byte(`{"initialized":false,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`)
			},
		},
		{
			name: "CodeGraph project path does not match",
			want: "project_path_mismatch",
			mutate: func(fake *fakeCG) {
				fake.respuestas[2] = []byte(`{"initialized":true,"projectPath":"/other/project","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`)
			},
		},
		{
			name: "CodeGraph has pending changes",
			want: "pending_changes",
			mutate: func(fake *fakeCG) {
				fake.respuestas[2] = []byte(replaceRoot(`{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":1,"modified":0,"removed":0},"worktreeMismatch":null}`, fake.root))
			},
		},
		{
			name: "CodeGraph worktree mismatches",
			want: "worktree_mismatch",
			mutate: func(fake *fakeCG) {
				fake.respuestas[2] = []byte(replaceRoot(`{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":{}}`, fake.root))
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, fake := proveedorConRespuestas(t, cleanState)
			tc.mutate(fake)
			refs, err := provider.Contexto("head", []string{"a.go"})
			if len(refs) != 0 {
				t.Fatalf("refs = %#v, want no context on a skipped provider", refs)
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want observable reason %q", err, tc.want)
			}
		})
	}
}

func TestProveedorCodeGraphAffectedEstructuradoYAcotado(t *testing.T) {
	p, fake := proveedorConRespuestas(t, `{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`)
	for _, ruta := range []string{"a_test.go", "z_test.go"} {
		if err := os.WriteFile(filepath.Join(p.raiz, ruta), []byte("package x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	fake.respuestas = append(fake.respuestas, []byte(`{"changedFiles":["a.go"],"affectedTests":["z_test.go","../escape_test.go","..\\escape_test.go","C:\\escape_test.go","a_test.go","a_test.go","-x_test.go"],"totalDependentsTraversed":2}`))
	refs, err := p.Contexto("head", []string{"a.go"})
	esperado := []review.Reference{{Path: "a_test.go", Relation: review.RelationAffectedTest, Reason: review.ReasonCodeGraph}, {Path: "z_test.go", Relation: review.RelationAffectedTest, Reason: review.ReasonCodeGraph}}
	if err != nil || !reflect.DeepEqual(refs, esperado) {
		t.Fatalf("referencias = (%v, %v), esperado %v", refs, err, esperado)
	}
	if !reflect.DeepEqual(fake.llamadas[3].args, []string{"affected", "-p", p.raiz, "--stdin", "--json"}) || fake.llamadas[3].stdin != "a.go\n" || fake.llamadas[3].dir != p.raiz || len(fake.llamadas[3].env) == 0 {
		t.Fatalf("affected inválido: %+v", fake.llamadas[3])
	}
	esperadas := [][]string{{"rev-parse", "--verify", "HEAD^{commit}"}, {"status", "--porcelain"}, {"status", "--json", p.raiz}}
	for i, args := range esperadas {
		if !reflect.DeepEqual(fake.llamadas[i].args, args) || fake.llamadas[i].dir != p.raiz || fake.llamadas[i].stdin != "" || len(fake.llamadas[i].env) == 0 {
			t.Fatalf("llamada %d inválida: %+v", i, fake.llamadas[i])
		}
	}
}

type llamadaCG struct {
	binario    string
	args       []string
	dir, stdin string
	env        []string
}
type fakeCG struct {
	root       string
	respuestas [][]byte
	llamadas   []llamadaCG
}

func proveedorConRespuestas(t *testing.T, estado string) (*ProveedorCodeGraph, *fakeCG) {
	t.Helper()
	raiz := t.TempDir()
	estado = string([]byte(estado))
	estado = replaceRoot(estado, raiz)
	p := &ProveedorCodeGraph{raiz: raiz, ejecutable: "codegraph", git: "git", limite: 4096}
	fake := &fakeCG{root: raiz, respuestas: [][]byte{[]byte("head\n"), nil, []byte(estado)}}
	p.ejecutar = func(_ context.Context, binario string, args []string, dir string, env []string, stdin string, _ int) ([]byte, error) {
		fake.llamadas = append(fake.llamadas, llamadaCG{binario, append([]string(nil), args...), dir, stdin, append([]string(nil), env...)})
		if len(fake.respuestas) == 0 {
			return nil, errors.New("unexpected command")
		}
		salida := fake.respuestas[0]
		fake.respuestas = fake.respuestas[1:]
		return salida, nil
	}
	return p, fake
}

func replaceRoot(s, root string) string {
	b := []byte(s)
	return string(bytes.ReplaceAll(b, []byte("ROOT"), []byte(filepath.ToSlash(root))))
}
