package remediation

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

const tenLineFile = "internal/foo.go"

// tenLines returns a 10-line file, "L1\nL2\n...\nL10\n", used as the shared
// "before" fixture for the boundary and scope tests below.
func tenLines() string {
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = "L" + strconv.Itoa(i+1)
	}
	return strings.Join(lines, "\n") + "\n"
}

// changeLine returns content with line n (1-indexed) replaced by newText.
func changeLine(content string, n int, newText string) string {
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	lines[n-1] = newText
	return strings.Join(lines, "\n") + "\n"
}

func TestDiffGuardWithinWindowSucceeds(t *testing.T) {
	before := tenLines()
	after := changeLine(before, 6, "L6-fixed") // finding at line 5, margin 3 -> window [2,8]

	editor := &fakeEditor{files: map[string]string{tenLineFile: before}}
	findings := []review.Hallazgo{{ID: "f1", Location: review.Ubicacion{Archivo: tenLineFile, LineaInicio: 5, LineaFin: 5}}}
	ge := NewGuardedEditor(editor, NewDiffGuard(3), findings)

	if err := ge.Edit(tenLineFile, after); err != nil {
		t.Fatalf("Edit: unexpected error: %v", err)
	}
	if editor.files[tenLineFile] != after {
		t.Fatalf("Edit: file content = %q, want %q", editor.files[tenLineFile], after)
	}
	if len(editor.editCalls) != 1 {
		t.Fatalf("Edit: expected exactly one underlying edit call (no revert), got %v", editor.editCalls)
	}
}

func TestDiffGuardOutOfScopeDiscardsWholeFix(t *testing.T) {
	before := tenLines()
	// Line 10 sits outside the window [2,8] authorized by the finding at
	// line 5 with margin 3.
	after := changeLine(before, 10, "L10-renamed")

	editor := &fakeEditor{files: map[string]string{tenLineFile: before}}
	findings := []review.Hallazgo{{ID: "f1", Location: review.Ubicacion{Archivo: tenLineFile, LineaInicio: 5, LineaFin: 5}}}
	ge := NewGuardedEditor(editor, NewDiffGuard(3), findings)

	err := ge.Edit(tenLineFile, after)
	if err == nil {
		t.Fatalf("Edit: expected an out-of-scope error, got nil")
	}
	if !strings.Contains(err.Error(), "out of scope") {
		t.Fatalf("Edit: error = %q, want it to contain %q", err.Error(), "out of scope")
	}
	if editor.files[tenLineFile] != before {
		t.Fatalf("Edit: file content = %q, want it untouched at the original %q", editor.files[tenLineFile], before)
	}
	if len(editor.editCalls) != 0 {
		t.Fatalf("Edit: expected the check to reject the fix before ever calling the underlying editor (0 calls), got %v", editor.editCalls)
	}
}

func TestDiffGuardFileWithoutFindingsIsNeverChecked(t *testing.T) {
	const path = "internal/remediation/new_helper_test.go"
	newContent := "package remediation\n\nfunc TestSomethingBrandNew(t *testing.T) {}\n"

	// DiffGuard.Check directly: no finding points at path, so no diff is
	// even computed.
	guard := NewDiffGuard(3)
	if err := guard.Check(path, "", newContent, nil); err != nil {
		t.Fatalf("Check: unexpected error for a file with no findings: %v", err)
	}

	// Through GuardedEditor as well, simulating the new-test-file case Scope
	// already authorizes on its own: the file does not exist yet, and no
	// finding names it.
	editor := &fakeEditor{files: map[string]string{}}
	findings := []review.Hallazgo{{ID: "f1", Location: review.Ubicacion{Archivo: tenLineFile, LineaInicio: 5, LineaFin: 5}}}
	ge := NewGuardedEditor(editor, guard, findings)
	if err := ge.Edit(path, newContent); err != nil {
		t.Fatalf("Edit: unexpected error for a file with no findings: %v", err)
	}
	if editor.files[path] != newContent {
		t.Fatalf("Edit: file content = %q, want %q", editor.files[path], newContent)
	}
}

func TestDiffGuardMarginBoundaryIsInclusive(t *testing.T) {
	// Finding at line 5 with margin 3 authorizes window [2, 8]: line 2 and
	// line 8 are the inclusive edges; lines 1 and 9 are one step beyond them.
	cases := []struct {
		name       string
		line       int
		wantReject bool
	}{
		{name: "lower boundary line 2 is accepted", line: 2, wantReject: false},
		{name: "one line below the lower boundary is rejected", line: 1, wantReject: true},
		{name: "upper boundary line 8 is accepted", line: 8, wantReject: false},
		{name: "one line above the upper boundary is rejected", line: 9, wantReject: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := tenLines()
			after := changeLine(before, tc.line, "L"+strconv.Itoa(tc.line)+"-changed")

			editor := &fakeEditor{files: map[string]string{tenLineFile: before}}
			findings := []review.Hallazgo{{ID: "f1", Location: review.Ubicacion{Archivo: tenLineFile, LineaInicio: 5, LineaFin: 5}}}
			ge := NewGuardedEditor(editor, NewDiffGuard(3), findings)

			err := ge.Edit(tenLineFile, after)
			if tc.wantReject {
				if err == nil {
					t.Fatalf("Edit(line %d): expected an out-of-scope error, got nil", tc.line)
				}
				if !strings.Contains(err.Error(), "out of scope") {
					t.Fatalf("Edit(line %d): error = %q, want it to contain %q", tc.line, err.Error(), "out of scope")
				}
				if editor.files[tenLineFile] != before {
					t.Fatalf("Edit(line %d): file content = %q, want it untouched at %q", tc.line, editor.files[tenLineFile], before)
				}
				if len(editor.editCalls) != 0 {
					t.Fatalf("Edit(line %d): expected the check to reject the fix before ever calling the underlying editor (0 calls), got %v", tc.line, editor.editCalls)
				}
				return
			}
			if err != nil {
				t.Fatalf("Edit(line %d): unexpected error: %v", tc.line, err)
			}
			if editor.files[tenLineFile] != after {
				t.Fatalf("Edit(line %d): file content = %q, want %q", tc.line, editor.files[tenLineFile], after)
			}
		})
	}
}

// twentyFiveLines returns a 25-line file, "L1\nL2\n...\nL25\n", long enough to
// hold a line clearly outside the merged window built from two findings at
// lines 5 and 12 with margin 3 ([2,15]).
func twentyFiveLines() string {
	lines := make([]string, 25)
	for i := range lines {
		lines[i] = "L" + strconv.Itoa(i+1)
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestDiffGuardMergedAdjacentWindowsCoverGapBetweenFindings(t *testing.T) {
	// Findings at lines 5 and 12, margin 3, produce windows [2,8] and [9,15]:
	// contiguous but not merged by the old code. Merged, they form [2,15].
	findings := []review.Hallazgo{
		{ID: "f1", Location: review.Ubicacion{Archivo: tenLineFile, LineaInicio: 5, LineaFin: 5}},
		{ID: "f2", Location: review.Ubicacion{Archivo: tenLineFile, LineaInicio: 12, LineaFin: 12}},
	}

	t.Run("a fix spanning both original windows is accepted", func(t *testing.T) {
		before := twentyFiveLines()
		// A single contiguous hunk over lines 7-10 straddles both original
		// windows ([2,8] and [9,15]) without fitting entirely inside either
		// one alone; it only fits inside their merged union [2,15].
		after := before
		for _, n := range []int{7, 8, 9, 10} {
			after = changeLine(after, n, "L"+strconv.Itoa(n)+"-fixed")
		}

		editor := &fakeEditor{files: map[string]string{tenLineFile: before}}
		ge := NewGuardedEditor(editor, NewDiffGuard(3), findings)

		if err := ge.Edit(tenLineFile, after); err != nil {
			t.Fatalf("Edit: unexpected error: %v", err)
		}
		if editor.files[tenLineFile] != after {
			t.Fatalf("Edit: file content = %q, want %q", editor.files[tenLineFile], after)
		}
	})

	t.Run("a fix outside the merged window is still rejected", func(t *testing.T) {
		before := twentyFiveLines()
		after := changeLine(before, 20, "L20-renamed") // well outside [2,15]

		editor := &fakeEditor{files: map[string]string{tenLineFile: before}}
		ge := NewGuardedEditor(editor, NewDiffGuard(3), findings)

		err := ge.Edit(tenLineFile, after)
		if err == nil {
			t.Fatalf("Edit: expected an out-of-scope error, got nil")
		}
		if !strings.Contains(err.Error(), "out of scope") {
			t.Fatalf("Edit: error = %q, want it to contain %q", err.Error(), "out of scope")
		}
		if len(editor.editCalls) != 0 {
			t.Fatalf("Edit: expected 0 underlying edit calls on rejection, got %v", editor.editCalls)
		}
	})
}

func TestDiffGuardUnlocatedFindingAuthorizesWholeFile(t *testing.T) {
	before := tenLines()
	// Line 10 sits far outside any margin window a located finding at line 5
	// would produce, but this finding has no location at all.
	after := changeLine(before, 10, "L10-changed")

	editor := &fakeEditor{files: map[string]string{tenLineFile: before}}
	findings := []review.Hallazgo{{ID: "f1", Location: review.Ubicacion{Archivo: tenLineFile, LineaInicio: 0, LineaFin: 0}}}
	ge := NewGuardedEditor(editor, NewDiffGuard(3), findings)

	if err := ge.Edit(tenLineFile, after); err != nil {
		t.Fatalf("Edit: unexpected error for an unlocated finding: %v", err)
	}
	if editor.files[tenLineFile] != after {
		t.Fatalf("Edit: file content = %q, want %q", editor.files[tenLineFile], after)
	}
}

func TestDiffGuardLocatedFindingIsNotSwallowedByUnlocatedFinding(t *testing.T) {
	// An unlocated finding (f1) and a located finding at line 5, margin 3
	// (f2, window [2,8]) both point at the same file. Regression: the
	// unlocated finding used to authorize the whole file, silently disabling
	// the located finding's own narrow window too.
	findings := []review.Hallazgo{
		{ID: "f1", Location: review.Ubicacion{Archivo: tenLineFile, LineaInicio: 0, LineaFin: 0}},
		{ID: "f2", Location: review.Ubicacion{Archivo: tenLineFile, LineaInicio: 5, LineaFin: 5}},
	}

	t.Run("a fix outside the located finding's window is still rejected", func(t *testing.T) {
		before := twentyFiveLines()
		after := changeLine(before, 20, "L20-renamed") // well outside [2,8]

		editor := &fakeEditor{files: map[string]string{tenLineFile: before}}
		ge := NewGuardedEditor(editor, NewDiffGuard(3), findings)

		err := ge.Edit(tenLineFile, after)
		if err == nil {
			t.Fatalf("Edit: expected an out-of-scope error, got nil")
		}
		if !strings.Contains(err.Error(), "out of scope") {
			t.Fatalf("Edit: error = %q, want it to contain %q", err.Error(), "out of scope")
		}
		if editor.files[tenLineFile] != before {
			t.Fatalf("Edit: file content = %q, want it untouched at %q", editor.files[tenLineFile], before)
		}
		if len(editor.editCalls) != 0 {
			t.Fatalf("Edit: expected 0 underlying edit calls on rejection, got %v", editor.editCalls)
		}
	})

	t.Run("a fix within the located finding's window is still accepted", func(t *testing.T) {
		before := twentyFiveLines()
		after := changeLine(before, 6, "L6-fixed") // inside [2,8]

		editor := &fakeEditor{files: map[string]string{tenLineFile: before}}
		ge := NewGuardedEditor(editor, NewDiffGuard(3), findings)

		if err := ge.Edit(tenLineFile, after); err != nil {
			t.Fatalf("Edit: unexpected error: %v", err)
		}
		if editor.files[tenLineFile] != after {
			t.Fatalf("Edit: file content = %q, want %q", editor.files[tenLineFile], after)
		}
	})
}

func TestDiffGuardEditPropagatesNonNotExistReadError(t *testing.T) {
	readErr := errors.New("permission denied reading the file")

	editor := &fakeEditor{
		files:      map[string]string{},
		readErrors: map[string]error{tenLineFile: readErr},
	}
	findings := []review.Hallazgo{{ID: "f1", Location: review.Ubicacion{Archivo: tenLineFile, LineaInicio: 5, LineaFin: 5}}}
	ge := NewGuardedEditor(editor, NewDiffGuard(3), findings)

	err := ge.Edit(tenLineFile, "new content")
	if err == nil {
		t.Fatalf("Edit: expected an error, got nil")
	}
	if !strings.Contains(err.Error(), readErr.Error()) {
		t.Fatalf("Edit: error = %q, want it to contain %q", err.Error(), readErr.Error())
	}
	if len(editor.editCalls) != 0 {
		t.Fatalf("Edit: expected 0 underlying edit calls after a read error, got %v", editor.editCalls)
	}
}
