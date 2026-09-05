package graph

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Condition is one codegraph preflight check: whether the review-time context
// gate it mirrors would let CodeGraph context through right now.
type Condition struct {
	Name   string
	OK     bool
	Detail string
}

// Preflight condition names, in evaluation order. The six review-time gates
// from ProveedorCodeGraph.Contexto keep their skip-reason vocabulary so a
// preflight row maps one-to-one to the silent skip it predicts.
const (
	CondBinary           = "binary"
	CondIndexDir         = "index_dir"
	CondHead             = "head"
	CondWorktreeClean    = "worktree_clean"
	CondIndexInitialized = "index_initialized"
	CondProjectPath      = "project_path"
	CondPendingChanges   = "pending_changes"
	CondWorktreeMatch    = "worktree_match"
)

// PreflightCodeGraph evaluates the codegraph preflight against the current
// worktree with real PATH resolution and subprocess execution.
func PreflightCodeGraph(root string) []Condition {
	return Preflight(root, exec.LookPath, ejecutarCodeGraph)
}

// Preflight evaluates, in order, the codegraph binary, the .codegraph index
// directory, and the six conditions that silently disable review context
// (HEAD resolution, clean worktree, initialized index, matching projectPath,
// zero pending changes, no worktree mismatch). Rows after a failed
// prerequisite report why they were skipped instead of running probes whose
// results would be meaningless. It never returns nil: callers always get the
// full eight-row shape.
func Preflight(root string, lookup func(string) (string, error), run ejecutorCodeGraph) []Condition {
	names := []string{CondBinary, CondIndexDir, CondHead, CondWorktreeClean, CondIndexInitialized, CondProjectPath, CondPendingChanges, CondWorktreeMatch}
	fail := func(detail string) []Condition {
		conds := make([]Condition, 0, len(names))
		for _, name := range names {
			conds = append(conds, Condition{Name: name, Detail: detail})
		}
		return conds
	}

	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fail(fmt.Sprintf("cannot resolve worktree path: %v", err))
	}
	conds := make([]Condition, 0, len(names))

	binary, err := lookup("codegraph")
	if err != nil {
		conds = append(conds, Condition{Name: CondBinary, Detail: "codegraph not found on PATH"})
		return append(conds, skipped(names[1:], "skipped: codegraph binary not found")...)
	}
	conds = append(conds, Condition{Name: CondBinary, Detail: binary})

	if info, err := os.Stat(filepath.Join(canonical, ".codegraph")); err != nil || !info.IsDir() {
		conds = append(conds, Condition{Name: CondIndexDir, Detail: "no .codegraph index directory under " + canonical})
		return append(conds, skipped(names[2:], "skipped: no .codegraph index directory")...)
	}
	conds = append(conds, Condition{Name: CondIndexDir, Detail: filepath.Join(canonical, ".codegraph")})

	gitBin, err := lookup("git")
	if err != nil {
		conds = append(conds,
			Condition{Name: CondHead, Detail: "git not found on PATH"},
			Condition{Name: CondWorktreeClean, Detail: "skipped: git not found on PATH"},
		)
		return append(conds, skipped(names[4:], "skipped: git not found on PATH")...)
	}
	p := &ProveedorCodeGraph{raiz: canonical, ejecutable: binary, git: gitBin, limite: limiteContextoCodeGraph, ejecutar: run}
	env := entornoCodeGraph(binary)

	head, err := p.ejecutarConTimeout(p.git, []string{"rev-parse", "--verify", "HEAD^{commit}"}, env, "")
	sha := strings.TrimSpace(string(head))
	if err != nil || sha == "" {
		conds = append(conds, Condition{Name: CondHead, Detail: fmt.Sprintf("HEAD does not resolve: %v", err)})
	} else {
		conds = append(conds, Condition{Name: CondHead, Detail: fmt.Sprintf("HEAD resolves to %s (each review compares it against the audited commit)", sha)})
	}

	dirty, err := p.ejecutarConTimeout(p.git, []string{"status", "--porcelain"}, env, "")
	if err != nil {
		conds = append(conds, Condition{Name: CondWorktreeClean, Detail: fmt.Sprintf("git status failed: %v", err)})
	} else if lines := len(strings.Split(strings.TrimSpace(string(dirty)), "\n")); len(bytes.TrimSpace(dirty)) != 0 {
		conds = append(conds, Condition{Name: CondWorktreeClean, Detail: fmt.Sprintf("worktree has %d dirty entries", lines)})
	} else {
		conds = append(conds, Condition{Name: CondWorktreeClean, Detail: "worktree clean"})
	}

	statusRaw, err := p.ejecutarConTimeout(p.ejecutable, []string{"status", "--json", p.raiz}, env, "")
	var status struct {
		Initialized      bool                                    `json:"initialized"`
		ProjectPath      string                                  `json:"projectPath"`
		Pending          *struct{ Added, Modified, Removed int } `json:"pendingChanges"`
		WorktreeMismatch json.RawMessage                         `json:"worktreeMismatch"`
	}
	if err != nil || len(statusRaw) == 0 || len(statusRaw) >= p.limite {
		detail := fmt.Sprintf("codegraph status unavailable: %v", err)
		return append(conds,
			Condition{Name: CondIndexInitialized, Detail: detail},
			Condition{Name: CondProjectPath, Detail: detail},
			Condition{Name: CondPendingChanges, Detail: detail},
			Condition{Name: CondWorktreeMatch, Detail: detail},
		)
	}
	if err := json.Unmarshal(statusRaw, &status); err != nil {
		detail := fmt.Sprintf("codegraph status unparsable: %v", err)
		return append(conds,
			Condition{Name: CondIndexInitialized, Detail: detail},
			Condition{Name: CondProjectPath, Detail: detail},
			Condition{Name: CondPendingChanges, Detail: detail},
			Condition{Name: CondWorktreeMatch, Detail: detail},
		)
	}

	if !status.Initialized {
		conds = append(conds, Condition{Name: CondIndexInitialized, Detail: "codegraph index not initialized"})
	} else {
		conds = append(conds, Condition{Name: CondIndexInitialized, Detail: "codegraph index initialized"})
	}
	if filepath.Clean(status.ProjectPath) != p.raiz {
		conds = append(conds, Condition{Name: CondProjectPath, Detail: fmt.Sprintf("projectPath %q does not match %s", status.ProjectPath, p.raiz)})
	} else {
		conds = append(conds, Condition{Name: CondProjectPath, Detail: "projectPath matches worktree"})
	}
	switch {
	case status.Pending == nil:
		conds = append(conds, Condition{Name: CondPendingChanges, Detail: "pendingChanges absent from codegraph status"})
	case status.Pending.Added != 0 || status.Pending.Modified != 0 || status.Pending.Removed != 0:
		conds = append(conds, Condition{Name: CondPendingChanges, Detail: fmt.Sprintf("codegraph reports %d added, %d modified, %d removed pending changes", status.Pending.Added, status.Pending.Modified, status.Pending.Removed)})
	default:
		conds = append(conds, Condition{Name: CondPendingChanges, OK: true, Detail: "zero pending changes"})
	}
	if string(status.WorktreeMismatch) != "null" {
		conds = append(conds, Condition{Name: CondWorktreeMatch, Detail: "codegraph reports a worktree mismatch"})
	} else {
		conds = append(conds, Condition{Name: CondWorktreeMatch, OK: true, Detail: "no worktree mismatch"})
	}
	markOK(conds)
	return conds
}

// skipped builds the tail rows sharing one skip reason.
func skipped(names []string, reason string) []Condition {
	conds := make([]Condition, 0, len(names))
	for _, name := range names {
		conds = append(conds, Condition{Name: name, Detail: reason})
	}
	return conds
}

// markOK flags the binary, index, head, worktree-clean, initialized and
// project rows OK unless their detail reports a failure. Pending and
// worktree-match set OK at construction because only their exact pass shape
// counts.
func markOK(conds []Condition) {
	for i := range conds {
		switch conds[i].Name {
		case CondBinary:
			conds[i].OK = !strings.Contains(conds[i].Detail, "not found")
		case CondIndexDir:
			conds[i].OK = !strings.HasPrefix(conds[i].Detail, "no .codegraph")
		case CondHead:
			conds[i].OK = strings.HasPrefix(conds[i].Detail, "HEAD resolves")
		case CondWorktreeClean:
			conds[i].OK = conds[i].Detail == "worktree clean"
		case CondIndexInitialized:
			conds[i].OK = conds[i].Detail == "codegraph index initialized"
		case CondProjectPath:
			conds[i].OK = conds[i].Detail == "projectPath matches worktree"
		}
	}
}
