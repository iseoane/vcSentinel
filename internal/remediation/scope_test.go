package remediation

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// fakeEditor is a double that records every Read and Edit call so a
// rejected write can be asserted as never having reached the underlying
// editor at all — not just inferred from the returned error, and not just
// for Edit: an early rejection (an unsafe path) must never even trigger the
// existence-check Read, which readCalls lets tests verify directly.
type fakeEditor struct {
	files      map[string]string
	readErrors map[string]error // arbitrary non-not-exist errors keyed by path
	readCalls  []string
	editCalls  []string
}

func (f *fakeEditor) Read(path string) (string, error) {
	f.readCalls = append(f.readCalls, path)
	if err, ok := f.readErrors[path]; ok {
		return "", err
	}
	if content, ok := f.files[path]; ok {
		return content, nil
	}
	return "", &fs.PathError{Op: "open", Path: path, Err: os.ErrNotExist}
}

func (f *fakeEditor) Edit(path, content string) error {
	f.editCalls = append(f.editCalls, path)
	f.files[path] = content
	return nil
}

func TestScopedEditorEdit(t *testing.T) {
	cases := []struct {
		name            string
		presetFile      string // pre-existing path in the fake, if any
		editPath        string
		wantReject      bool
		wantErrContains string // when set, err.Error() must contain this substring
	}{
		{
			name:       "file with a finding is writable",
			presetFile: "internal/remediation/scope.go",
			editPath:   "internal/remediation/scope.go",
			wantReject: false,
		},
		{
			name:       "new test file without a finding is writable",
			editPath:   "internal/remediation/scope_helper_test.go",
			wantReject: false,
		},
		{
			name:            "new non-test file without a finding is rejected",
			editPath:        "internal/remediation/helper.go",
			wantReject:      true,
			wantErrContains: "out of scope",
		},
		{
			name:            "existing test file without a finding is rejected",
			presetFile:      "internal/remediation/existing_test.go",
			editPath:        "internal/remediation/existing_test.go",
			wantReject:      true,
			wantErrContains: "out of scope",
		},
		{
			name:            "path traversal disguised as a new test file is rejected",
			editPath:        "../../../etc/cron.d/evil_test.go",
			wantReject:      true,
			wantErrContains: "unsafe",
		},
		{
			name:            "absolute path disguised as a new test file is rejected",
			editPath:        "/etc/x_test.go",
			wantReject:      true,
			wantErrContains: "unsafe",
		},
		{
			name:            "bare parent-directory reference is rejected",
			editPath:        "..",
			wantReject:      true,
			wantErrContains: "unsafe",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			editor := &fakeEditor{files: map[string]string{}}
			if tc.presetFile != "" {
				editor.files[tc.presetFile] = "original content"
			}

			findings := []review.Hallazgo{{ID: "f1", Location: review.Ubicacion{Archivo: "internal/remediation/scope.go"}}}
			scoped := NewScopedEditor(editor, NewScope(findings))

			err := scoped.Edit(tc.editPath, "new content")

			if tc.wantReject {
				if err == nil {
					t.Fatalf("Edit(%q) expected an out-of-scope error, got nil", tc.editPath)
				}
				if tc.wantErrContains != "" && !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Fatalf("Edit(%q) error = %q, want it to contain %q", tc.editPath, err.Error(), tc.wantErrContains)
				}
				if len(editor.editCalls) != 0 {
					t.Fatalf("Edit(%q) rejected but underlying editor was called: %v", tc.editPath, editor.editCalls)
				}
				return
			}
			if err != nil {
				t.Fatalf("Edit(%q) unexpected error: %v", tc.editPath, err)
			}
			if len(editor.editCalls) != 1 || editor.editCalls[0] != tc.editPath {
				t.Fatalf("Edit(%q) expected exactly one underlying call to %q, got %v", tc.editPath, tc.editPath, editor.editCalls)
			}
		})
	}
}

// TestScopedEditorReadIsUnrestricted verifies Read passes through to the
// underlying editor unconditionally: only Edit is scoped.
func TestScopedEditorReadIsUnrestricted(t *testing.T) {
	editor := &fakeEditor{files: map[string]string{"any/path.go": "content"}}
	scoped := NewScopedEditor(editor, NewScope(nil))

	content, err := scoped.Read("any/path.go")
	if err != nil || content != "content" {
		t.Fatalf("Read(existing) = (%q, %v), want (%q, nil)", content, err, "content")
	}

	if _, err := scoped.Read("missing/path.go"); !os.IsNotExist(err) {
		t.Fatalf("Read(missing) error = %v, want a not-exist error", err)
	}
}

// TestScopedEditorEditNormalizesPaths verifies that a finding's location and
// the path passed to Edit are compared after normalization, so different
// textual spellings of the same file ("internal/x.go" vs "./internal/x.go")
// resolve to the same scope entry, and that the underlying Editor receives
// that same normalized spelling rather than the caller's original one.
func TestScopedEditorEditNormalizesPaths(t *testing.T) {
	editor := &fakeEditor{files: map[string]string{
		"internal/remediation/scope.go": "original content",
	}}
	findings := []review.Hallazgo{{ID: "f1", Location: review.Ubicacion{Archivo: "internal/remediation/scope.go"}}}
	scoped := NewScopedEditor(editor, NewScope(findings))

	editPath := "./internal/remediation/scope.go"
	wantNormalized := "internal/remediation/scope.go"
	if err := scoped.Edit(editPath, "new content"); err != nil {
		t.Fatalf("Edit(%q) unexpected error: %v", editPath, err)
	}
	if len(editor.editCalls) != 1 || editor.editCalls[0] != wantNormalized {
		t.Fatalf("Edit(%q) expected exactly one underlying call to %q, got %v", editPath, wantNormalized, editor.editCalls)
	}
}

// TestScopedEditorEditSurfacesNonNotExistReadErrors verifies that a Read
// failure other than "does not exist" (permissions, I/O errors, and the
// like) is returned as-is rather than being folded into the generic
// out-of-scope error, so the real cause is not hidden.
func TestScopedEditorEditSurfacesNonNotExistReadErrors(t *testing.T) {
	const path = "internal/remediation/locked.go"
	readErr := errors.New("permission denied")
	editor := &fakeEditor{
		files:      map[string]string{},
		readErrors: map[string]error{path: readErr},
	}
	scoped := NewScopedEditor(editor, NewScope(nil))

	err := scoped.Edit(path, "new content")
	if err == nil {
		t.Fatalf("Edit(%q) expected an error, got nil", path)
	}
	if !strings.Contains(err.Error(), "permission denied") || !strings.Contains(err.Error(), "check") {
		t.Fatalf("Edit(%q) error = %q, want it to identify a check/read failure and contain the underlying cause", path, err.Error())
	}
	if len(editor.editCalls) != 0 {
		t.Fatalf("Edit(%q) errored but underlying editor was called: %v", path, editor.editCalls)
	}
}

// TestScopedEditorEditRejectsUnsafePathsBeforeAnyRead verifies the ordering
// guarantee ScopedEditor.Edit documents: an unsafe path (absolute,
// traversal, or empty) is rejected by the early isPathSafe check before the
// existence-check Read is ever attempted, not merely before Edit. Asserting
// only editCalls (as the other cases in this file do) cannot distinguish
// "rejected after calling Read" from "rejected before ever calling Read";
// asserting readCalls is what proves the underlying Editor was never
// reached at all.
func TestScopedEditorEditRejectsUnsafePathsBeforeAnyRead(t *testing.T) {
	cases := []struct {
		name     string
		editPath string
	}{
		{name: "absolute path", editPath: "/etc/x_test.go"},
		{name: "path traversal", editPath: "../../../etc/cron.d/evil_test.go"},
		{name: "empty path", editPath: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			editor := &fakeEditor{files: map[string]string{}}
			scoped := NewScopedEditor(editor, NewScope(nil))

			err := scoped.Edit(tc.editPath, "new content")
			if err == nil {
				t.Fatalf("Edit(%q) expected an unsafe-path error, got nil", tc.editPath)
			}
			if !strings.Contains(err.Error(), "unsafe") {
				t.Fatalf("Edit(%q) error = %q, want it to contain %q", tc.editPath, err.Error(), "unsafe")
			}
			if len(editor.readCalls) != 0 {
				t.Fatalf("Edit(%q) rejected as unsafe but underlying Read was called: %v", tc.editPath, editor.readCalls)
			}
			if len(editor.editCalls) != 0 {
				t.Fatalf("Edit(%q) rejected as unsafe but underlying Edit was called: %v", tc.editPath, editor.editCalls)
			}
		})
	}
}
