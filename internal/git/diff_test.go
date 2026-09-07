package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCountAddedLines(t *testing.T) {
	tests := []struct {
		name string
		diff string
		want int
	}{
		{name: "empty diff", diff: "", want: 0},
		{name: "only file headers", diff: "+++ b/main.go\n--- a/main.go\n", want: 0},
		{name: "normal added lines", diff: "+func main() {\n+\treturn\n+}\n", want: 3},
		{
			name: "mix with context and deletions",
			diff: " func main() {\n-\tfmt.Println(\"hola\")\n+\treturn\n \t_ = 0\n+++ b/main.go\n",
			want: 1,
		},
		{name: "line with more than three pluses is a header", diff: "+++++ not a header\n", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countAddedLines(tt.diff); got != tt.want {
				t.Errorf("countAddedLines(%q) = %d, expected %d", tt.diff, got, tt.want)
			}
		})
	}
}

func TestClassifyState(t *testing.T) {
	tests := []struct {
		name  string
		lines int
		want  string
	}{
		{name: "zero", lines: 0, want: "SMALL"},
		{name: "just below the optimum", lines: 199, want: "SMALL"},
		{name: "lower bound of the optimum", lines: 200, want: "OPTIMAL_POINT"},
		{name: "inside the optimum", lines: 300, want: "OPTIMAL_POINT"},
		{name: "upper bound of the optimum", lines: 400, want: "OPTIMAL_POINT"},
		{name: "above the critical limit", lines: 401, want: "CRITICAL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyState(tt.lines); got != tt.want {
				t.Errorf("classifyState(%d) = %q, expected %q", tt.lines, got, tt.want)
			}
		})
	}
}

func TestCheckDiffLimitsInRealRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	t.Run("clean tree reports zero and SMALL", func(t *testing.T) {
		lines, state, err := CheckDiffLimits()
		if err != nil {
			t.Fatalf("CheckDiffLimits returned error: %v", err)
		}
		if lines != 0 {
			t.Errorf("lines = %d, expected 0", lines)
		}
		if state != "SMALL" {
			t.Errorf("state = %q, expected SMALL", state)
		}
	})

	t.Run("250 added lines report OPTIMAL_POINT", func(t *testing.T) {
		appendLines(t, "a.go", 250)
		lines, state, err := CheckDiffLimits()
		if err != nil {
			t.Fatalf("CheckDiffLimits returned error: %v", err)
		}
		if lines != 250 {
			t.Errorf("lines = %d, expected 250", lines)
		}
		if state != "OPTIMAL_POINT" {
			t.Errorf("state = %q, expected OPTIMAL_POINT", state)
		}
	})

	t.Run("more than 400 lines report CRITICAL", func(t *testing.T) {
		appendLines(t, "a.go", 250)
		lines, state, err := CheckDiffLimits()
		if err != nil {
			t.Fatalf("CheckDiffLimits returned error: %v", err)
		}
		if lines != 500 {
			t.Errorf("lines = %d, expected 500", lines)
		}
		if state != "CRITICAL" {
			t.Errorf("state = %q, expected CRITICAL", state)
		}
	})
}

func TestCheckDiffLimitsIncludesUntrackedFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	// New unstaged (untracked) file with more than 400 lines: it must be seen
	// as CRITICAL, not as SMALL. Reproduces B1: "git diff HEAD" ignores the
	// untracked files.
	content := strings.Repeat("// generated line\n", 450)
	if err := os.WriteFile(filepath.Join(dir, "new.go"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	lines, state, err := CheckDiffLimits()
	if err != nil {
		t.Fatalf("CheckDiffLimits returned error: %v", err)
	}
	if lines != 450 {
		t.Errorf("lines = %d, expected 450", lines)
	}
	if state != "CRITICAL" {
		t.Errorf("state = %q, expected CRITICAL", state)
	}
}

func TestCheckDiffLimitsMatchesGetModifiedFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{
		"a.go": "package a\n",
	})
	t.Chdir(dir)

	// Mix of a modified tracked file and a new untracked file: check and
	// slice must measure exactly the same volume (B2).
	appendLines(t, "a.go", 100)
	newContent := strings.Repeat("// line\n", 50)
	if err := os.WriteFile(filepath.Join(dir, "new.go"), []byte(newContent), 0644); err != nil {
		t.Fatal(err)
	}

	lines, _, err := CheckDiffLimits()
	if err != nil {
		t.Fatalf("CheckDiffLimits returned error: %v", err)
	}

	files, err := GetModifiedFiles()
	if err != nil {
		t.Fatalf("GetModifiedFiles returned error: %v", err)
	}
	expectedSum := 0
	for _, f := range files {
		expectedSum += f.Lines
	}

	if lines != expectedSum {
		t.Errorf("CheckDiffLimits = %d, GetModifiedFiles sums %d; they must match", lines, expectedSum)
	}
}

func TestMeasureVolumeFailsOutsideRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real Git measurement in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	t.Chdir(t.TempDir())
	volume, err := MeasureVolume()
	if err == nil {
		t.Fatal("MeasureVolume should fail outside a Git repository")
	}
	if volume.State != "ERROR" {
		t.Errorf("failure state = %q, want ERROR", volume.State)
	}
}

// prepareTestRepo, runGitInDir and appendLines are package-wide test fixtures
// shared with other test files in this package.

func prepareTestRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GIT_AUTHOR_NAME", "VAS Sentinel Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@vas-sentinel")
	t.Setenv("GIT_COMMITTER_NAME", "VAS Sentinel Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@vas-sentinel")

	runGitInDir(t, dir, "init", "-q")
	runGitInDir(t, dir, "config", "commit.gpgsign", "false")
	runGitInDir(t, dir, "config", "core.autocrlf", "false")
	runGitInDir(t, dir, "config", "core.hooksPath", "no-hooks")

	for path, content := range files {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0644); err != nil {
			t.Fatalf("could not create %s: %v", path, err)
		}
	}

	runGitInDir(t, dir, "add", "-A")
	runGitInDir(t, dir, "commit", "-q", "-m", "initial state")
	return dir
}

func runGitInDir(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := append([]string{"-C", dir}, args...)
	out, err := exec.Command("git", command...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func appendLines(t *testing.T, path string, count int) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatalf("could not open %s: %v", path, err)
	}
	defer f.Close()
	for i := 0; i < count; i++ {
		if _, err := f.WriteString("// generated line\n"); err != nil {
			t.Fatalf("could not write to %s: %v", path, err)
		}
	}
}
