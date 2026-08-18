package remediation

import (
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
		t.Fatalf("Edit: file content = %q, want it reverted to the original %q", editor.files[tenLineFile], before)
	}
	if len(editor.editCalls) != 2 {
		t.Fatalf("Edit: expected the underlying edit followed by a revert (2 calls), got %v", editor.editCalls)
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
					t.Fatalf("Edit(line %d): file content = %q, want it reverted to %q", tc.line, editor.files[tenLineFile], before)
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
