package remediation

import (
	"errors"
	"fmt"
	"math"
	"os"
	"sort"

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

// allowedWindows computes, for one file, the line ranges a fix may touch.
// Findings matching file are first partitioned into located (LineaInicio >
// 0) and unlocated (LineaInicio <= 0). An unlocated finding has nothing real
// to bound against, but it must never widen or swallow a located finding's
// narrow window: as long as at least one located finding exists, every
// unlocated finding for that file is ignored entirely, and the result is
// built only from the located findings' (margin-expanded, merged) windows.
// Only when every matching finding is unlocated does this fall back to
// authorizing the whole file, since there is then nothing real to bound
// against at all.
//
// LineaInicio and LineaFin come from finding data this package does not
// otherwise validate, so both start and end are clamped before the
// arithmetic that could push them past the int range's edges: start is
// clamped before subtracting margin (an extreme negative LineaInicio could
// otherwise underflow past math.MinInt and wrap around to a large positive
// number), and end is clamped before adding margin (an extreme LineaFin,
// e.g. math.MaxInt, could otherwise overflow past math.MaxInt and wrap
// around to a large negative number). The resulting windows this function
// returns never have an End below Start from wraparound; End may still
// legitimately equal math.MaxInt exactly when the clamp saturates it, which
// mergeWindows handles explicitly.
func allowedWindows(file string, findings []review.Hallazgo, margin int) []git.LineRange {
	var located []git.LineRange
	matched := false
	for _, f := range findings {
		if normalizePath(f.Location.Archivo) != normalizePath(file) {
			continue
		}
		matched = true
		if f.Location.LineaInicio <= 0 {
			continue
		}
		start := f.Location.LineaInicio
		if start < math.MinInt+margin {
			start = math.MinInt + margin
		}
		start -= margin
		if start < 1 {
			start = 1
		}
		end := f.Location.LineaFin
		if end < f.Location.LineaInicio {
			end = f.Location.LineaInicio
		}
		if end > math.MaxInt-margin {
			end = math.MaxInt - margin
		}
		located = append(located, git.LineRange{Start: start, End: end + margin})
	}
	if len(located) == 0 {
		if matched {
			return []git.LineRange{{Start: 1, End: math.MaxInt}}
		}
		return nil
	}
	return mergeWindows(located)
}

// mergeWindows sorts windows by Start and merges any that overlap or are
// adjacent (the next window starts at or before one past the current end)
// into a single window, so withinAny can check a touched range against the
// union of authorized lines rather than requiring it to fit inside a single
// finding's window. It does not reorder the caller's slice in place:
// sort.Slice runs on a private copy. The len<2 shortcut below returns the
// input slice itself rather than a copy, but that is harmless because that
// path returns before this function writes to any element; the len>=2 path
// below does mutate merged[i].End, but only within the private copy.
//
// allowedWindows clamps every window it builds so End never exceeds
// math.MaxInt and never wraps around from overflow. A clamped window can
// still legitimately equal math.MaxInt exactly (an extreme LineaFin
// saturated by that clamp, or the whole-file fallback window), and the
// last.End == math.MaxInt check below exists precisely to merge that window
// safely without ever computing last.End+1 itself.
func mergeWindows(windows []git.LineRange) []git.LineRange {
	if len(windows) < 2 {
		return windows
	}
	sorted := append([]git.LineRange(nil), windows...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })

	merged := []git.LineRange{sorted[0]}
	for _, w := range sorted[1:] {
		last := &merged[len(merged)-1]
		// last.End == math.MaxInt is checked separately to avoid overflowing
		// last.End+1: allowedWindows clamps its windows so End never exceeds
		// math.MaxInt and never wraps around, but a clamped window can still
		// legitimately equal math.MaxInt exactly, which is precisely what
		// this check exists to handle safely during the merge.
		if last.End == math.MaxInt || w.Start <= last.End+1 {
			if w.End > last.End {
				last.End = w.End
			}
			continue
		}
		merged = append(merged, w)
	}
	return merged
}

// withinAny reports whether r fits entirely inside at least one window;
// windows must already be merged via mergeWindows — an unmerged, overlapping
// set of windows would make this check unreliable.
func withinAny(r git.LineRange, windows []git.LineRange) bool {
	for _, w := range windows {
		if r.Start >= w.Start && r.End <= w.End {
			return true
		}
	}
	return false
}

// GuardedEditor decorates an Editor so every Edit call is checked by a
// DiffGuard against the fix's own findings before it is ever applied. When
// the check fails, the underlying editor is never called at all: the caller
// gets the guard's "remediation out of scope" error, never a partially
// applied fix.
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

// Edit checks content against g.guard before ever applying it: it reads the
// file's current content (treating a missing file as empty), runs the guard
// check against the would-be before/after pair, and only calls the
// underlying editor's Edit when that check passes. A fix that goes out of
// scope is rejected before it ever reaches the underlying editor, so it
// never stands even partially — there is nothing to revert because nothing
// was ever written.
func (g *GuardedEditor) Edit(path, content string) error {
	before, err := g.editor.Read(path)
	switch {
	case err == nil:
	case errors.Is(err, os.ErrNotExist):
		before = ""
	default:
		return fmt.Errorf("remediation: could not read %q before editing: %w", path, err)
	}

	if err := g.guard.Check(path, before, content, g.findings); err != nil {
		return err
	}

	return g.editor.Edit(path, content)
}
