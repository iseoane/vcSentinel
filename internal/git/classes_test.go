package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestFileClass pins the file-class taxonomy, independent of the layer
// (which only orders the slice batches). It includes the real false
// positives of ClassifyLayer, which treats "latest" or "contest" as tests
// for containing the "test" substring.
func TestFileClass(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		// Documentation: does not stop the guardian.
		{path: "README.md", want: ClassDocs},
		{path: "docs/arquitectura/replanteamiento-objetivo.md", want: ClassDocs},
		{path: filepath.Join("docs", "reengineering", "f1-gate.md"), want: ClassDocs},
		{path: "CHANGELOG.rst", want: ClassDocs},

		// Generated: nobody reviews it line by line, does not block.
		{path: "go.sum", want: ClassGenerated},
		{path: "package-lock.json", want: ClassGenerated},
		{path: "yarn.lock", want: ClassGenerated},
		{path: "api/v1/user.pb.go", want: ClassGenerated},
		{path: "internal/store/model_gen.go", want: ClassGenerated},

		// Hand-written configuration: it is code that runs, it does block.
		{path: "go.mod", want: ClassConfig},
		{path: "docker-compose.yml", want: ClassConfig},
		{path: ".github/workflows/ci.yml", want: ClassConfig},
		{path: "requirements.txt", want: ClassConfig},

		// Tests: by suffix and by directory, never by loose substring.
		{path: "internal/git/diff_test.go", want: ClassTest},
		{path: filepath.Join("internal", "agentadapter", "testdata", "sleeper", "main.go"), want: ClassTest},
		{path: "web/component.spec.ts", want: ClassTest},

		// Code: including the historical false positives of ClassifyLayer.
		{path: "internal/git/diff.go", want: ClassSource},
		{path: "latest/version.go", want: ClassSource},
		{path: "contest.go", want: ClassSource},
		{path: "internal/setup/install.go", want: ClassSource},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := FileClass(tt.path); got != tt.want {
				t.Errorf("FileClass(%q) = %q, expected %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestCountsTowardVolume pins which classes stop the guardian. The guardian
// measures code reviewability, not bytes: documentation and generated
// content are reported but do not block.
func TestCountsTowardVolume(t *testing.T) {
	tests := []struct {
		class string
		want  bool
	}{
		{ClassSource, true},
		{ClassTest, true},
		{ClassConfig, true},
		{ClassDocs, false},
		{ClassGenerated, false},
	}

	for _, tt := range tests {
		t.Run(tt.class, func(t *testing.T) {
			if got := CountsTowardVolume(tt.class); got != tt.want {
				t.Errorf("CountsTowardVolume(%q) = %v, expected %v", tt.class, got, tt.want)
			}
		})
	}
}

// TestMeasureVolumeExcludesDocumentationAndGenerated is the test of the
// rule: a huge document is reported as informational and does NOT put the
// guardian in CRITICAL. Without this, writing architecture documentation
// blocks development without adding any safety.
func TestMeasureVolumeExcludesDocumentationAndGenerated(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(dir)

	writeLines(t, filepath.Join(dir, "ARCHITECTURE.md"), 1000)
	writeLines(t, filepath.Join(dir, "go.sum"), 500)
	writeLines(t, filepath.Join(dir, "new.go"), 10)

	volume, err := MeasureVolume()
	if err != nil {
		t.Fatalf("MeasureVolume returned error: %v", err)
	}
	if volume.Blocking != 10 {
		t.Errorf("Blocking = %d, expected 10 (only new.go)", volume.Blocking)
	}
	if volume.Informational != 1500 {
		t.Errorf("Informational = %d, expected 1500 (document + generated)", volume.Informational)
	}
	if volume.State != "SMALL" {
		t.Errorf("State = %q, expected SMALL: 1500 lines of documentation cannot stop the guardian", volume.State)
	}
}

// TestGiantDocumentationFileDoesNotOfferRefactor checks that a long document
// does not go through the massive-code branch, which offers to split it with
// AI applying SRP. Proposing an SRP refactor over prose makes no sense.
func TestGiantDocumentationFileDoesNotOfferRefactor(t *testing.T) {
	document := ModifiedFile{Path: "docs/architecture/goal.md", Lines: 1792, Layer: "backend"}
	if isGiantCode(document) {
		t.Error("a 1792-line document must not be treated as massive code")
	}
	if !isExtensiveDocumentation(document) {
		t.Error("a long document must be isolated into its own batch, like config files")
	}

	generated := ModifiedFile{Path: "go.sum", Lines: 900, Layer: "config"}
	if isGiantCode(generated) {
		t.Error("a generated file must not be treated as massive code")
	}

	code := ModifiedFile{Path: "internal/git/slice.go", Lines: 501, Layer: "backend"}
	if !isGiantCode(code) {
		t.Error("code above the limit must keep offering the refactor")
	}
}

// writeLines creates a file with the given number of lines.
func writeLines(t *testing.T, path string, count int) {
	t.Helper()
	var b strings.Builder
	for i := 0; i < count; i++ {
		b.WriteString("line\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		t.Fatalf("could not create %s: %v", path, err)
	}
}
