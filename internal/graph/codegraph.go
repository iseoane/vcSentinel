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

const codeGraphContextLimit = 32 << 10
const maxCodeGraphRefs = 32

// perRelationBudget caps each additive relation (callers, callees, impact)
// at 8 references. It does not split maxCodeGraphRefs: affectedTests
// consumes the full maxCodeGraphRefs on its own, and each additive
// relation adds up to perRelationBudget on top, bounding the total at
// maxTotalRefs (32 + 3*8 = 56).
const perRelationBudget = maxCodeGraphRefs / 4

// maxTotalRefs bounds the total references a codegraph context returns:
// maxCodeGraphRefs affectedTests plus one perRelationBudget for each
// of the three additive relations.
const maxTotalRefs = maxCodeGraphRefs + 3*perRelationBudget

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

type codeGraphRunner func(context.Context, string, []string, string, []string, string, int) ([]byte, error)

type CodeGraphProvider struct {
	root, binary, git string
	limit             int
	run               codeGraphRunner
	// excludes resolves the parent-side global excludes file to pass the
	// sanitized child explicitly. Nil means no file: the status call goes
	// out unchanged.
	excludes func(string) string
}

func DetectCodeGraphProvider(root string) review.ContextProvider {
	return detectCodeGraphProvider(root, exec.LookPath, runCodeGraph)
}

func detectCodeGraphProvider(root string, lookup func(string) (string, error), run codeGraphRunner) review.ContextProvider {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil
	}
	info, err := os.Stat(filepath.Join(canonical, ".codegraph"))
	if err != nil || !info.IsDir() {
		return nil
	}
	binary, err := lookup("codegraph")
	if err != nil {
		return nil
	}
	git, err := lookup("git")
	if err != nil {
		return nil
	}
	return &CodeGraphProvider{root: canonical, binary: binary, git: git, limit: codeGraphContextLimit, run: run, excludes: resolveGlobalExcludes}
}

func (p *CodeGraphProvider) Name() string { return "codegraph" }

func (p *CodeGraphProvider) Context(sha string, paths []string) ([]review.Reference, error) {
	validPaths := safePaths(paths, 0)
	if sha == "" || len(validPaths) == 0 || p.run == nil {
		return nil, nil
	}
	env := codeGraphEnv(p.binary)
	head, err := p.runWithTimeout(p.git, []string{"rev-parse", "--verify", "HEAD^{commit}"}, env, "")
	if err != nil {
		return nil, fmt.Errorf("codegraph context skipped: head_mismatch: %w", err)
	}
	if strings.TrimSpace(string(head)) != sha {
		return nil, fmt.Errorf("codegraph context skipped: head_mismatch")
	}
	dirty, err := p.runWithTimeout(p.git, argsStatePorcelain(p.excludes, p.git), env, "")
	if err != nil {
		return nil, fmt.Errorf("codegraph context skipped: dirty_worktree: %w", err)
	}
	if len(bytes.TrimSpace(dirty)) != 0 {
		return nil, fmt.Errorf("codegraph context skipped: dirty_worktree")
	}
	statusRaw, err := p.runWithTimeout(p.binary, []string{"status", "--json", p.root}, env, "")
	var status struct {
		Initialized      bool                                    `json:"initialized"`
		ProjectPath      string                                  `json:"projectPath"`
		Pending          *struct{ Added, Modified, Removed int } `json:"pendingChanges"`
		WorktreeMismatch json.RawMessage                         `json:"worktreeMismatch"`
	}
	if err != nil {
		return nil, fmt.Errorf("codegraph context skipped: status_unavailable: %w", err)
	}
	if len(statusRaw) == 0 || len(statusRaw) >= p.limit {
		return nil, fmt.Errorf("codegraph context skipped: status_unavailable")
	}
	if err := json.Unmarshal(statusRaw, &status); err != nil {
		return nil, fmt.Errorf("codegraph context skipped: status_unavailable: %w", err)
	}
	if !status.Initialized {
		return nil, fmt.Errorf("codegraph context skipped: uninitialized_index")
	}
	if filepath.Clean(status.ProjectPath) != p.root {
		return nil, fmt.Errorf("codegraph context skipped: project_path_mismatch")
	}
	if status.Pending == nil || status.Pending.Added != 0 || status.Pending.Modified != 0 || status.Pending.Removed != 0 {
		return nil, fmt.Errorf("codegraph context skipped: pending_changes")
	}
	if string(status.WorktreeMismatch) != "null" {
		return nil, fmt.Errorf("codegraph context skipped: worktree_mismatch")
	}
	output, err := p.runWithTimeout(p.binary, []string{"affected", "-p", p.root, "--stdin", "--json"}, env, strings.Join(validPaths, "\n")+"\n")
	var affected struct {
		AffectedTests []string `json:"affectedTests"`
	}
	if err != nil {
		return nil, fmt.Errorf("codegraph context skipped: affected_unavailable: %w", err)
	}
	if len(output) == 0 || len(output) >= p.limit {
		return nil, fmt.Errorf("codegraph context skipped: affected_unavailable")
	}
	if err := json.Unmarshal(output, &affected); err != nil {
		return nil, fmt.Errorf("codegraph context skipped: affected_unavailable: %w", err)
	}
	tests := safePaths(affected.AffectedTests, maxCodeGraphRefs)
	refs := make([]review.Reference, 0, len(tests))
	for _, path := range tests {
		if p.validatedPath(path) {
			refs = append(refs, review.Reference{Path: path, Relation: review.RelationAffectedTest, Reason: review.ReasonCodeGraph})
		}
	}
	return p.widenWithRelations(env, sha, validPaths, refs), nil
}

// validatedPath is the shared gate for every untrusted path that may become
// a reference: already sanitized by safePaths, it must resolve (through
// EvalSymlinks) to a regular file contained under p.root.
func (p *CodeGraphProvider) validatedPath(path string) bool {
	resolved, err := filepath.EvalSymlinks(filepath.Join(p.root, filepath.FromSlash(path)))
	if err != nil {
		return false
	}
	relative, relErr := filepath.Rel(p.root, resolved)
	if relErr != nil {
		return false
	}
	info, statErr := os.Stat(resolved)
	if statErr != nil || !info.Mode().IsRegular() {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
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
func entriesFor(response relationResponse, relation review.Relation) []graphEntry {
	switch relation {
	case review.RelationCaller:
		return response.Callers
	case review.RelationCallee:
		return response.Callees
	case review.RelationImpact:
		return response.Affected
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
func (p *CodeGraphProvider) widenWithRelations(env []string, sha string, paths []string, refs []review.Reference) []review.Reference {
	symbols := p.diffSymbols(env, sha, paths)
	if len(symbols) == 0 {
		return refs
	}
	candidates := map[review.Relation][]string{}
	for _, symbol := range symbols {
		for _, relation := range additiveRelations {
			output, err := p.runWithTimeout(p.binary, relationQueryArgs(relation, p.root, symbol), env, "")
			if err != nil || len(output) == 0 || len(output) >= p.limit {
				continue
			}
			var response relationResponse
			if json.Unmarshal(output, &response) != nil {
				continue
			}
			for _, entry := range entriesFor(response, relation) {
				candidates[relation] = append(candidates[relation], entry.FilePath)
			}
		}
	}
	for _, relation := range additiveRelations {
		// Validated over the full over-fetch before capping, so invalid
		// graph paths cannot starve the budget with valid references.
		validated := make([]string, 0, len(candidates[relation]))
		for _, path := range safePaths(candidates[relation], 0) {
			if p.validatedPath(path) {
				validated = append(validated, path)
			}
		}
		if len(validated) > perRelationBudget {
			validated = validated[:perRelationBudget]
		}
		// Same path under different relations is intentional: each relation is a distinct reviewer signal.
		for _, path := range validated {
			refs = append(refs, review.Reference{Path: path, Relation: relation, Reason: review.ReasonCodeGraph})
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
func (p *CodeGraphProvider) diffSymbols(env []string, sha string, paths []string) []string {
	args := make([]string, 0, len(paths)+5)
	args = append(args, "show", sha, "--format=", "--")
	args = append(args, paths...)
	output, err := p.runWithTimeout(p.git, args, env, "")
	if err != nil || len(output) == 0 || len(output) >= p.limit {
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

// runWithTimeout gives each subprocess its own 3s budget, so that the
// pre-checks (rev-parse, status) do not eat into 'affected' time — the only
// call that walks the dependents graph.
func (p *CodeGraphProvider) runWithTimeout(executable string, args []string, env []string, stdin string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return p.run(ctx, executable, args, p.root, env, stdin, p.limit)
}

func safePaths(paths []string, max int) []string {
	unique := map[string]bool{}
	for _, raw := range paths {
		normalized := strings.ReplaceAll(raw, "\\", "/")
		clean := path.Clean(normalized)
		drive := len(clean) >= 2 && clean[1] == ':'
		if raw != "" && !path.IsAbs(clean) && !drive && clean != ".." && !strings.HasPrefix(clean, "../") && !strings.HasPrefix(raw, "-") && !strings.ContainsAny(raw, "\x00\r\n") {
			unique[clean] = true
		}
	}
	output := make([]string, 0, len(unique))
	for clean := range unique {
		output = append(output, clean)
	}
	sort.Strings(output)
	if max > 0 && len(output) > max {
		output = output[:max]
	}
	return output
}

func codeGraphEnv(executable string) []string {
	dirs := []string{filepath.Dir(executable)}
	// An npm-style launcher is a shebang script (#!/usr/bin/env node), so the
	// tool directory alone leaves the kernel unable to resolve its
	// interpreter and every invocation dies with exit 127. Appending the
	// interpreter's directory is the minimum that makes such a launcher
	// runnable. Containment is preserved: both entries are constructed here,
	// the interpreter lookup runs in the parent, and no parent variable
	// reaches the child — from its side exactly two explicit directories are
	// reachable, never the caller's environment.
	if dir := interpreterDir(executable); dir != "" && dir != dirs[0] {
		dirs = append(dirs, dir)
	}
	env := []string{"PATH=" + strings.Join(dirs, string(os.PathListSeparator))}
	for _, name := range []string{"SystemRoot", "TEMP", "TMP", "TMPDIR"} {
		if value := os.Getenv(name); value != "" {
			env = append(env, name+"="+value)
		}
	}
	return env
}

// interpreterDir returns the directory of the interpreter named by the
// executable's shebang line, or "" when there is none or it cannot be
// resolved. Only a 256-byte prefix is read: a shebang is capped at 127 bytes
// on Linux and a few hundred elsewhere, so a first line that does not fit
// could never execute anyway.
func interpreterDir(executable string) string {
	file, err := os.Open(executable)
	if err != nil {
		return ""
	}
	defer file.Close()
	var prefix [256]byte
	n, _ := io.ReadFull(file, prefix[:])
	line, _, _ := strings.Cut(string(prefix[:n]), "\n")
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

type limitedWriter struct {
	bytes.Buffer
	remaining int
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, io.ErrShortBuffer
	}
	w.remaining -= len(p)
	return w.Buffer.Write(p)
}

func runCodeGraph(ctx context.Context, executable string, args []string, dir string, env []string, stdin string, limit int) ([]byte, error) {
	w := &limitedWriter{remaining: limit}
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir, cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = dir, env, strings.NewReader(stdin), w, io.Discard
	err := cmd.Run()
	return w.Bytes(), err
}
