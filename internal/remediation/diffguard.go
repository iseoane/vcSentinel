package remediation

import (
	"errors"
	"fmt"
	"os"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// DefaultDiffGuardMargin is the default N (in lines) used when a caller
// does not configure one explicitly.
const DefaultDiffGuardMargin = 3

// DiffGuard rejects a fix whole when its diff touches lines outside the
// findings that authorized touching that file, expanded by Margin lines on
// each side. Without it, a fix that also renames a variable in an unrelated
// function would apply cleanly; with it, the whole fix is discarded instead
// of applying only its in-scope hunk.
type DiffGuard struct {
	Margin int
}

// NewDiffGuard builds a DiffGuard with the given line margin.
func NewDiffGuard(margin int) DiffGuard {
	return DiffGuard{Margin: margin}
}

// Check compares a file's content before and after a fix against the
// findings that authorized touching it. A file with no finding located in
// it (e.g. a brand-new test file Scope already authorized on its own) is
// not this guard's concern — Scope already decided the file itself may be
// touched; Check only bounds where inside an already-scoped file the fix
// may land — so it returns nil without computing any diff.
func (g DiffGuard) Check(file, before, after string, findings []review.Hallazgo) error {
	windows := allowedWindows(file, findings, g.Margin)
	if len(windows) == 0 {
		return nil
	}
	touched, err := git.TouchedRanges(before, after)
	if err != nil {
		return fmt.Errorf("remediation: could not compute the diff for %q: %w", file, err)
	}
	for _, r := range touched {
		if !withinAny(r, windows) {
			return fmt.Errorf("remediation out of scope: fix to %q touches lines %d-%d outside its findings", file, r.Start, r.End)
		}
	}
	return nil
}

// allowedWindows computes, for one file, the line ranges a fix may touch:
// each finding located in that file, expanded by margin lines on each side.
func allowedWindows(file string, findings []review.Hallazgo, margin int) []git.LineRange {
	var windows []git.LineRange
	for _, f := range findings {
		if normalizePath(f.Location.Archivo) != normalizePath(file) {
			continue
		}
		start := f.Location.LineaInicio - margin
		if start < 1 {
			start = 1
		}
		end := f.Location.LineaFin
		if end < f.Location.LineaInicio {
			end = f.Location.LineaInicio
		}
		windows = append(windows, git.LineRange{Start: start, End: end + margin})
	}
	return windows
}

// withinAny reports whether r fits entirely inside at least one window.
func withinAny(r git.LineRange, windows []git.LineRange) bool {
	for _, w := range windows {
		if r.Start >= w.Start && r.End <= w.End {
			return true
		}
	}
	return false
}

// GuardedEditor decorates an Editor so every Edit call is checked by a
// DiffGuard against the fix's own findings before it is allowed to stand.
// When the check fails, the whole fix is discarded: the file is reverted to
// its pre-edit content (when it existed) and the caller gets the guard's
// "remediation out of scope" error, never a partially applied fix.
type GuardedEditor struct {
	editor   Editor
	guard    DiffGuard
	findings []review.Hallazgo
}

// NewGuardedEditor wraps editor so its Edit calls are checked by guard
// against findings.
func NewGuardedEditor(editor Editor, guard DiffGuard, findings []review.Hallazgo) *GuardedEditor {
	return &GuardedEditor{editor: editor, guard: guard, findings: findings}
}

// Read passes through to the underlying editor unconditionally.
func (g *GuardedEditor) Read(path string) (string, error) {
	return g.editor.Read(path)
}

// Edit applies content to path through the underlying editor, then checks
// the resulting diff against g.guard. A failing check reverts the file (when
// it previously existed) and returns the guard's error, so a fix that goes
// out of scope never stands even partially.
func (g *GuardedEditor) Edit(path, content string) error {
	before, err := g.editor.Read(path)
	existed := true
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		before = ""
		existed = false
	default:
		return fmt.Errorf("remediation: could not read %q before editing: %w", path, err)
	}

	if err := g.editor.Edit(path, content); err != nil {
		return err
	}

	checkErr := g.guard.Check(path, before, content, g.findings)
	if checkErr == nil {
		return nil
	}
	if !existed {
		return checkErr
	}
	if revertErr := g.editor.Edit(path, before); revertErr != nil {
		return fmt.Errorf("%w (also failed to revert: %v)", checkErr, revertErr)
	}
	return checkErr
}
