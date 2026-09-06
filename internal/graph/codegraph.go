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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

const limiteContextoCodeGraph = 32 << 10
const maxReferenciasCodeGraph = 32

// perRelationBudget caps each additive relation (callers, callees, impact)
// at 8 references. It does not split maxReferenciasCodeGraph: affectedTests
// consumes the full maxReferenciasCodeGraph on its own, and each additive
// relation adds up to perRelationBudget on top, bounding the total at
// maxTotalRefs (32 + 3*8 = 56).
const perRelationBudget = maxReferenciasCodeGraph / 4

// maxTotalRefs bounds the total references a codegraph context returns:
// maxReferenciasCodeGraph affectedTests plus one perRelationBudget for each
// of the three additive relations.
const maxTotalRefs = maxReferenciasCodeGraph + 3*perRelationBudget

// codeGraphEntryLimit asks each callers/callees query for up to 16
// entries — twice the per-relation budget — so several entries naming the
// same file still leave room to fill the budget after per-path
// deduplication.
const codeGraphEntryLimit = perRelationBudget * 2

// maxCodeGraphSymbols caps the symbols derived from the audited diff, so a
// large commit costs a bounded number of additive subprocess calls.
const maxCodeGraphSymbols = 8

// impactQueryDepth bounds the graph traversal of each impact query at two
// hops: one hop reaches only direct dependents. It limits the cost of that
// single query, not the subprocess count — that is already bounded by
// maxCodeGraphSymbols x len(additiveRelations).
const impactQueryDepth = 2

// reAddedFuncDecl matches an added diff line declaring a top-level Go
// function or method; the symbol name is capture group 1. Indented nested
// declarations never match: a top-level line starts right after the '+'.
var reAddedFuncDecl = regexp.MustCompile(`^\+func\s+(?:\([^)]*\)\s+)?(\w+)`)

// reAddedTypeDecl matches an added diff line declaring a top-level Go type.
var reAddedTypeDecl = regexp.MustCompile(`^\+type\s+(\w+)`)

type ejecutorCodeGraph func(context.Context, string, []string, string, []string, string, int) ([]byte, error)

type ProveedorCodeGraph struct {
	raiz, ejecutable, git string
	limite                int
	ejecutar              ejecutorCodeGraph
	// excludes resolves the parent-side global excludes file to pass the
	// sanitized child explicitly. Nil means no file: the status call goes
	// out unchanged.
	excludes func(string) string
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
	return &ProveedorCodeGraph{raiz: canonica, ejecutable: binario, git: git, limite: limiteContextoCodeGraph, ejecutar: ejecutar, excludes: buscarExcludesGlobal}
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
	sucio, err := p.ejecutarConTimeout(p.git, argsEstadoPorcelain(p.excludes, p.git), env, "")
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
		if p.validatedPath(ruta) {
			refs = append(refs, review.Reference{Path: ruta, Relation: review.RelationAffectedTest, Reason: review.ReasonCodeGraph})
		}
	}
	return p.widenWithRelations(env, sha, validas, refs), nil
}

// validatedPath is the shared gate for every untrusted path that may become
// a reference: already sanitized by rutasSeguras, it must resolve (through
// EvalSymlinks) to a regular file contained under p.raiz.
func (p *ProveedorCodeGraph) validatedPath(ruta string) bool {
	resuelta, err := filepath.EvalSymlinks(filepath.Join(p.raiz, filepath.FromSlash(ruta)))
	if err != nil {
		return false
	}
	relativa, relErr := filepath.Rel(p.raiz, resuelta)
	if relErr != nil {
		return false
	}
	info, statErr := os.Stat(resuelta)
	if statErr != nil || !info.Mode().IsRegular() {
		return false
	}
	return relativa != ".." && !strings.HasPrefix(relativa, ".."+string(filepath.Separator))
}

// graphEntry is one entry of a callers/callees/impact --json response; the
// filePath is repo-relative and untrusted until validatedPath approves it.
type graphEntry struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	FilePath  string `json:"filePath"`
	StartLine int    `json:"startLine"`
}

// relationResponse mirrors the JSON shapes of `codegraph callers|callees|impact
// --json`: callers and callees report their entries under the matching key,
// impact reports them under "affected".
type relationResponse struct {
	Callers  []graphEntry `json:"callers"`
	Callees  []graphEntry `json:"callees"`
	Affected []graphEntry `json:"affected"`
}

// entriesFor selects the entry list a relation reads from the shared shape.
func entriesFor(respuesta relationResponse, relation review.Relation) []graphEntry {
	switch relation {
	case review.RelationCaller:
		return respuesta.Callers
	case review.RelationCallee:
		return respuesta.Callees
	case review.RelationImpact:
		return respuesta.Affected
	}
	return nil
}

// relationQueryArgs builds the codegraph CLI invocation for one additive
// relation, matching the probed shapes:
//
//	callers|callees -p <root> -l <n> --json <symbol>
//	impact -p <root> -d <depth> --json <symbol>
func relationQueryArgs(relation review.Relation, root, symbol string) []string {
	switch relation {
	case review.RelationCaller:
		return []string{"callers", "-p", root, "-l", strconv.Itoa(codeGraphEntryLimit), "--json", symbol}
	case review.RelationCallee:
		return []string{"callees", "-p", root, "-l", strconv.Itoa(codeGraphEntryLimit), "--json", symbol}
	case review.RelationImpact:
		return []string{"impact", "-p", root, "-d", strconv.Itoa(impactQueryDepth), "--json", symbol}
	}
	return nil
}

// additiveRelations enumerates the relations widened beyond affectedTests, in
// the deterministic order their references are appended.
var additiveRelations = []review.Relation{review.RelationCaller, review.RelationCallee, review.RelationImpact}

// widenWithRelations adds caller/callee/impact references for the symbols
// derived from the audited diff. Every failure — symbol derivation, one
// query, an empty, oversized, or malformed response — degrades to that piece
// contributing nothing: additive context never errors and never regresses the
// affectedTests result it extends.
func (p *ProveedorCodeGraph) widenWithRelations(env []string, sha string, paths []string, refs []review.Reference) []review.Reference {
	symbols := p.diffSymbols(env, sha, paths)
	if len(symbols) == 0 {
		return refs
	}
	candidates := map[review.Relation][]string{}
	for _, symbol := range symbols {
		for _, relation := range additiveRelations {
			output, err := p.ejecutarConTimeout(p.ejecutable, relationQueryArgs(relation, p.raiz, symbol), env, "")
			if err != nil || len(output) == 0 || len(output) >= p.limite {
				continue
			}
			var respuesta relationResponse
			if json.Unmarshal(output, &respuesta) != nil {
				continue
			}
			for _, entry := range entriesFor(respuesta, relation) {
				candidates[relation] = append(candidates[relation], entry.FilePath)
			}
		}
	}
	for _, relation := range additiveRelations {
		// Validated over the full over-fetch before capping, so invalid
		// graph paths cannot starve the budget with valid references.
		validated := make([]string, 0, len(candidates[relation]))
		for _, ruta := range rutasSeguras(candidates[relation], 0) {
			if p.validatedPath(ruta) {
				validated = append(validated, ruta)
			}
		}
		if len(validated) > perRelationBudget {
			validated = validated[:perRelationBudget]
		}
		// Same path under different relations is intentional: each relation is a distinct reviewer signal.
		for _, ruta := range validated {
			refs = append(refs, review.Reference{Path: ruta, Relation: relation, Reason: review.ReasonCodeGraph})
		}
	}
	return refs
}

// diffSymbols derives candidate symbols deterministically from the audited
// commit: `git show <sha> --format= -- <validated input paths>` restricted to
// those paths, harvesting the names of added top-level function, method, and
// type declarations, sorted and capped at maxCodeGraphSymbols. A failing
// call, an oversized output, or a diff without Go declarations (non-Go
// files, no matches) yields nil so the provider keeps its affected-only
// result without error.
func (p *ProveedorCodeGraph) diffSymbols(env []string, sha string, paths []string) []string {
	args := make([]string, 0, len(paths)+5)
	args = append(args, "show", sha, "--format=", "--")
	args = append(args, paths...)
	output, err := p.ejecutarConTimeout(p.git, args, env, "")
	if err != nil || len(output) == 0 || len(output) >= p.limite {
		return nil
	}
	names := make([]string, 0, maxCodeGraphSymbols)
	seen := make(map[string]bool, maxCodeGraphSymbols)
	for _, line := range strings.Split(string(output), "\n") {
		name := ""
		if m := reAddedFuncDecl.FindStringSubmatch(line); m != nil {
			name = m[1]
		} else if m := reAddedTypeDecl.FindStringSubmatch(line); m != nil {
			name = m[1]
		}
		if name != "" && !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > maxCodeGraphSymbols {
		names = names[:maxCodeGraphSymbols]
	}
	return names
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
// resolved. Only a 256-byte prefix is read: a shebang is capped at 127 bytes
// on Linux and a few hundred elsewhere, so a first line that does not fit
// could never execute anyway.
func directorioInterprete(ejecutable string) string {
	archivo, err := os.Open(ejecutable)
	if err != nil {
		return ""
	}
	defer archivo.Close()
	var prefijo [256]byte
	n, _ := io.ReadFull(archivo, prefijo[:])
	line, _, _ := strings.Cut(string(prefijo[:n]), "\n")
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
