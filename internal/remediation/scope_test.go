package remediation

import (
	"io/fs"
	"os"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// fakeEditor is a double that records every Edit call so a rejected write
// can be asserted as never having reached the underlying editor, not just
// inferred from the returned error.
type fakeEditor struct {
	files     map[string]string
	editCalls []string
}

func (f *fakeEditor) Read(path string) (string, error) {
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
		name       string
		presetFile string // pre-existing path in the fake, if any
		editPath   string
		wantReject bool
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
			name:       "new non-test file without a finding is rejected",
			editPath:   "internal/remediation/helper.go",
			wantReject: true,
		},
		{
			name:       "existing test file without a finding is rejected",
			presetFile: "internal/remediation/existing_test.go",
			editPath:   "internal/remediation/existing_test.go",
			wantReject: true,
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
