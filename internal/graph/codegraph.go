package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

const limiteContextoCodeGraph = 32 << 10
const maxReferenciasCodeGraph = 32

type ejecutorCodeGraph func(context.Context, string, []string, string, []string, string, int) ([]byte, error)

type ProveedorCodeGraph struct {
	raiz, ejecutable, git string
	limite                int
	ejecutar              ejecutorCodeGraph
}

func DetectarProveedorCodeGraph(raiz string) review.ContextProvider {
	return detectarProveedorCodeGraph(raiz, exec.LookPath, ejecutarCodeGraph)
}

func detectarProveedorCodeGraph(raiz string, lookup func(string) (string, error), ejecutar ejecutorCodeGraph) review.ContextProvider {
	canonica, err := filepath.EvalSymlinks(raiz)
	if err != nil {
		return nil
	}
	info, err := os.Stat(filepath.Join(canonica, ".codegraph"))
	if err != nil || !info.IsDir() {
		return nil
	}
	binario, err := lookup("codegraph")
	if err != nil {
		return nil
	}
	git, err := lookup("git")
	if err != nil {
		return nil
	}
	return &ProveedorCodeGraph{raiz: canonica, ejecutable: binario, git: git, limite: limiteContextoCodeGraph, ejecutar: ejecutar}
}

func (p *ProveedorCodeGraph) Nombre() string { return "codegraph" }

func (p *ProveedorCodeGraph) Contexto(sha string, rutas []string) ([]review.Reference, error) {
	validas := rutasSeguras(rutas, 0)
	if sha == "" || len(validas) == 0 || p.ejecutar == nil {
		return nil, nil
	}
	env := entornoCodeGraph(p.ejecutable)
	head, err := p.ejecutarConTimeout(p.git, []string{"rev-parse", "--verify", "HEAD^{commit}"}, env, "")
	if err != nil {
		return nil, fmt.Errorf("codegraph context skipped: head_mismatch: %w", err)
	}
	if strings.TrimSpace(string(head)) != sha {
		return nil, fmt.Errorf("codegraph context skipped: head_mismatch")
	}
	sucio, err := p.ejecutarConTimeout(p.git, []string{"status", "--porcelain"}, env, "")
	if err != nil {
		return nil, fmt.Errorf("codegraph context skipped: dirty_worktree: %w", err)
	}
	if len(bytes.TrimSpace(sucio)) != 0 {
		return nil, fmt.Errorf("codegraph context skipped: dirty_worktree")
	}
	estadoRaw, err := p.ejecutarConTimeout(p.ejecutable, []string{"status", "--json", p.raiz}, env, "")
	var estado struct {
		Initialized      bool                                    `json:"initialized"`
		ProjectPath      string                                  `json:"projectPath"`
		Pending          *struct{ Added, Modified, Removed int } `json:"pendingChanges"`
		WorktreeMismatch json.RawMessage                         `json:"worktreeMismatch"`
	}
	if err != nil {
		return nil, fmt.Errorf("codegraph context skipped: status_unavailable: %w", err)
	}
	if len(estadoRaw) == 0 || len(estadoRaw) >= p.limite {
		return nil, fmt.Errorf("codegraph context skipped: status_unavailable")
	}
	if err := json.Unmarshal(estadoRaw, &estado); err != nil {
		return nil, fmt.Errorf("codegraph context skipped: status_unavailable: %w", err)
	}
	if !estado.Initialized {
		return nil, fmt.Errorf("codegraph context skipped: uninitialized_index")
	}
	if filepath.Clean(estado.ProjectPath) != p.raiz {
		return nil, fmt.Errorf("codegraph context skipped: project_path_mismatch")
	}
	if estado.Pending == nil || estado.Pending.Added != 0 || estado.Pending.Modified != 0 || estado.Pending.Removed != 0 {
		return nil, fmt.Errorf("codegraph context skipped: pending_changes")
	}
	if string(estado.WorktreeMismatch) != "null" {
		return nil, fmt.Errorf("codegraph context skipped: worktree_mismatch")
	}
	salida, err := p.ejecutarConTimeout(p.ejecutable, []string{"affected", "-p", p.raiz, "--stdin", "--json"}, env, strings.Join(validas, "\n")+"\n")
	var afectado struct {
		AffectedTests []string `json:"affectedTests"`
	}
	if err != nil {
		return nil, fmt.Errorf("codegraph context skipped: affected_unavailable: %w", err)
	}
	if len(salida) == 0 || len(salida) >= p.limite {
		return nil, fmt.Errorf("codegraph context skipped: affected_unavailable")
	}
	if err := json.Unmarshal(salida, &afectado); err != nil {
		return nil, fmt.Errorf("codegraph context skipped: affected_unavailable: %w", err)
	}
	tests := rutasSeguras(afectado.AffectedTests, maxReferenciasCodeGraph)
	refs := make([]review.Reference, 0, len(tests))
	for _, ruta := range tests {
		resuelta, err := filepath.EvalSymlinks(filepath.Join(p.raiz, filepath.FromSlash(ruta)))
		relativa, relErr := filepath.Rel(p.raiz, resuelta)
		if info, statErr := os.Stat(resuelta); err == nil && relErr == nil && statErr == nil && info.Mode().IsRegular() && relativa != ".." && !strings.HasPrefix(relativa, ".."+string(filepath.Separator)) {
			refs = append(refs, review.Reference{Path: ruta, Relation: review.RelationAffectedTest, Reason: review.ReasonCodeGraph})
		}
	}
	return refs, nil
}

// ejecutarConTimeout da a cada subproceso su propio presupuesto de 3s, para
// que las verificaciones previas (rev-parse, status) no le resten tiempo a
// 'affected', la única llamada que recorre el grafo de dependientes.
func (p *ProveedorCodeGraph) ejecutarConTimeout(ejecutable string, args []string, env []string, stdin string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return p.ejecutar(ctx, ejecutable, args, p.raiz, env, stdin, p.limite)
}

func rutasSeguras(rutas []string, max int) []string {
	unicas := map[string]bool{}
	for _, ruta := range rutas {
		normalizada := strings.ReplaceAll(ruta, "\\", "/")
		limpia := path.Clean(normalizada)
		drive := len(limpia) >= 2 && limpia[1] == ':'
		if ruta != "" && !path.IsAbs(limpia) && !drive && limpia != ".." && !strings.HasPrefix(limpia, "../") && !strings.HasPrefix(ruta, "-") && !strings.ContainsAny(ruta, "\x00\r\n") {
			unicas[limpia] = true
		}
	}
	salida := make([]string, 0, len(unicas))
	for ruta := range unicas {
		salida = append(salida, ruta)
	}
	sort.Strings(salida)
	if max > 0 && len(salida) > max {
		salida = salida[:max]
	}
	return salida
}

func entornoCodeGraph(ejecutable string) []string {
	dirs := []string{filepath.Dir(ejecutable)}
	// An npm-style launcher is a shebang script (#!/usr/bin/env node), so the
	// tool directory alone leaves the kernel unable to resolve its
	// interpreter and every invocation dies with exit 127. Appending the
	// interpreter's directory is the minimum that makes such a launcher
	// runnable. Containment is preserved: both entries are constructed here,
	// the interpreter lookup runs in the parent, and no parent variable
	// reaches the child — from its side exactly two explicit directories are
	// reachable, never the caller's environment.
	if dir := directorioInterprete(ejecutable); dir != "" && dir != dirs[0] {
		dirs = append(dirs, dir)
	}
	env := []string{"PATH=" + strings.Join(dirs, string(os.PathListSeparator))}
	for _, nombre := range []string{"SystemRoot", "TEMP", "TMP", "TMPDIR"} {
		if valor := os.Getenv(nombre); valor != "" {
			env = append(env, nombre+"="+valor)
		}
	}
	return env
}

// directorioInterprete returns the directory of the interpreter named by the
// executable's shebang line, or "" when there is none or it cannot be
// resolved. Only the first line is read: a shebang longer than the kernel
// limit could never execute anyway.
func directorioInterprete(ejecutable string) string {
	raw, err := os.ReadFile(ejecutable)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(raw), "\n")
	fields := strings.Fields(strings.TrimPrefix(line, "#!"))
	if !strings.HasPrefix(line, "#!") || len(fields) == 0 {
		return ""
	}
	program := fields[0]
	if filepath.Base(program) == "env" {
		program = ""
		for _, field := range fields[1:] {
			if !strings.HasPrefix(field, "-") {
				program = field
				break
			}
		}
		if program == "" {
			return ""
		}
	}
	if resolved, err := exec.LookPath(program); err == nil {
		return filepath.Dir(resolved)
	}
	if strings.ContainsRune(program, os.PathSeparator) {
		return filepath.Dir(program)
	}
	return ""
}

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

func ejecutarCodeGraph(ctx context.Context, ejecutable string, args []string, dir string, env []string, stdin string, limite int) ([]byte, error) {
	w := &escritorLimitado{restante: limite}
	cmd := exec.CommandContext(ctx, ejecutable, args...)
	cmd.Dir, cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = dir, env, strings.NewReader(stdin), w, io.Discard
	err := cmd.Run()
	return w.Bytes(), err
}
