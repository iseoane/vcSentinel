package remediation

import (
	"errors"
	"fmt"
	"os"
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
// convention.
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
		allowed[f.Location.Archivo] = true
	}
	return Scope{allowed: allowed}
}

func (s Scope) hasFinding(path string) bool {
	return s.allowed[path]
}

// isTestFile reports whether path matches the Go test-file naming
// convention this repository uses for new-file exceptions.
func isTestFile(path string) bool {
	return strings.HasSuffix(path, "_test.go")
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

// Edit applies content to path if and only if path is in scope: an existing
// finding file, or a genuinely new file that looks like a Go test.
//
// Existence is determined by attempting a Read first rather than adding a
// dedicated Exists method to Editor: the interface stays minimal to exactly
// Read+Edit, which is itself part of the "no Bash, no network" guarantee.
// If the underlying editor reports the path does not exist (via
// errors.Is(err, os.ErrNotExist)), the path is new.
func (a *ScopedEditor) Edit(path, content string) error {
	if a.scope.hasFinding(path) {
		return a.editor.Edit(path, content)
	}
	_, err := a.editor.Read(path)
	isNew := errors.Is(err, os.ErrNotExist)
	if isNew && isTestFile(path) {
		return a.editor.Edit(path, content)
	}
	return fmt.Errorf("remediation: write to %q is out of scope", path)
}
