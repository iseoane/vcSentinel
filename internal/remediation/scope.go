package remediation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// Editor is the minimal write capability the remediation agent may use to
// apply a fix: read a file's content, and write a new content for a path.
// It deliberately has no method for running a shell command or making a
// network request, so "no Bash, no network" is a property of the type, not
// just a runtime policy.
type Editor interface {
	Read(path string) (string, error)
	Edit(path, content string) error
}

// Scope is the set of file paths the remediation agent may write to: any
// file that already carries one of the routed findings, plus — for paths
// outside that set — a brand-new file whose name matches the Go test-file
// convention. Paths are stored and compared in normalized form so
// "./internal/x.go" and "internal/x.go" designate the same scope entry.
type Scope struct {
	allowed map[string]bool
}

// NewScope builds a Scope from the findings being remediated. Findings
// without a resolved file location contribute nothing to the scope.
func NewScope(findings []review.Hallazgo) Scope {
	allowed := make(map[string]bool, len(findings))
	for _, f := range findings {
		if f.Location.Archivo == "" {
			continue
		}
		allowed[normalizePath(f.Location.Archivo)] = true
	}
	return Scope{allowed: allowed}
}

// Allows is the whole write-authorization policy for one path: an existing
// finding file, or a brand-new file that looks like a Go test. exists
// reflects whether the underlying Editor already has content at path — the
// caller (ScopedEditor.Edit) is the one that talked to the Editor to learn
// this, since Scope itself has no I/O access.
func (s Scope) Allows(path string, exists bool) bool {
	if s.allowed[normalizePath(path)] {
		return true
	}
	return !exists && isTestFile(path)
}

// isTestFile reports whether path matches the Go test-file naming
// convention this repository uses for new-file exceptions.
func isTestFile(path string) bool {
	return strings.HasSuffix(path, "_test.go")
}

// normalizePath puts a path in one canonical, comparable form (cleaned,
// forward-slash separators) so scope membership does not depend on the
// exact textual spelling a caller happened to use.
func normalizePath(path string) string {
	return filepath.ToSlash(filepath.Clean(path))
}

// isSafeRelativePath rejects any path that could escape the repository the
// remediation agent is confined to: absolute paths, and relative paths that
// climb above their starting directory via "..". Scope membership alone
// does not guarantee this — a finding's Location (model-produced, untrusted)
// or the new-test-file exception could otherwise smuggle in a path like
// "../../../etc/cron.d/x_test.go".
func isSafeRelativePath(path string) bool {
	if path == "" || filepath.IsAbs(path) {
		return false
	}
	clean := normalizePath(path)
	return clean != ".." && !strings.HasPrefix(clean, "../")
}

// ScopedEditor restricts an Editor's writes to files inside its Scope,
// rejecting anything else before it ever reaches the underlying Editor.
type ScopedEditor struct {
	editor Editor
	scope  Scope
}

// NewScopedEditor wraps editor so its Edit calls are constrained to scope.
func NewScopedEditor(editor Editor, scope Scope) *ScopedEditor {
	return &ScopedEditor{editor: editor, scope: scope}
}

// Read is unrestricted: the agent needs to inspect any file to reason about
// a fix, and reading carries none of the accumulation risk the guardian
// exists to bound.
func (a *ScopedEditor) Read(path string) (string, error) {
	return a.editor.Read(path)
}

// Edit applies content to path if and only if Scope.Allows it. Existence is
// determined by attempting a Read first rather than adding a dedicated
// Exists method to Editor: the interface stays minimal to exactly Read+Edit,
// itself part of the "no Bash, no network" guarantee. A Read failure other
// than "does not exist" is surfaced as-is, not folded into "out of scope":
// a permission or I/O error is a different problem than a scope violation.
func (a *ScopedEditor) Edit(path, content string) error {
	if !isSafeRelativePath(path) {
		return fmt.Errorf("remediation: write to %q is out of scope", path)
	}
	_, err := a.editor.Read(path)
	var exists bool
	switch {
	case err == nil:
		exists = true
	case errors.Is(err, os.ErrNotExist):
		exists = false
	default:
		return fmt.Errorf("remediation: check %q before edit: %w", path, err)
	}
	if !a.scope.Allows(path, exists) {
		return fmt.Errorf("remediation: write to %q is out of scope", path)
	}
	return a.editor.Edit(path, content)
}
