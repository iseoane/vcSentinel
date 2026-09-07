package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
)

func TestClassifyLayer(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "test file in subfolder", path: "internal/git/slice_test.go", want: "test"},
		{name: "spec document at base", path: "docs/spec.md", want: "test"},
		{name: "explicit frontend path", path: "web/frontend/app.tsx", want: "frontend"},
		{name: "tsx extension", path: "web/app.tsx", want: "frontend"},
		{name: "jsx extension", path: "app.jsx", want: "frontend"},
		{name: "css extension", path: "web/app.css", want: "frontend"},
		{name: "scss extension", path: "web/app.scss", want: "frontend"},
		{name: "lock file", path: "package-lock.json", want: "config"},
		{name: "sum file", path: "go.sum", want: "config"},
		{name: "yaml extension", path: "config.yaml", want: "config"},
		{name: "json extension", path: "config.json", want: "config"},
		{name: "toml extension", path: "config.toml", want: "config"},
		{name: "requirements.txt by name", path: "requirements.txt", want: "config"},
		{name: "yml extension recognized as config", path: "config.yml", want: "config"},
		{name: "go code in cmd", path: "cmd/main.go", want: "backend"},
		{name: "test in subfolder precedes backend", path: "internal/my_test/helper.go", want: "test"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyLayer(tt.path); got != tt.want {
				t.Errorf("ClassifyLayer(%q) = %q, expected %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestBuildBatches(t *testing.T) {
	file := func(path string, lines int) ModifiedFile {
		return ModifiedFile{Path: path, Lines: lines}
	}
	flatten := func(batches [][]ModifiedFile) [][]string {
		var got [][]string
		for _, batch := range batches {
			paths := make([]string, 0, len(batch))
			for _, f := range batch {
				paths = append(paths, f.Path)
			}
			got = append(got, paths)
		}
		return got
	}

	tests := []struct {
		name  string
		files []ModifiedFile
		want  [][]string
	}{
		{
			name:  "no files yields zero batches",
			files: nil,
			want:  nil,
		},
		{
			name:  "single file within the limit",
			files: []ModifiedFile{file("a.go", 300)},
			want:  [][]string{{"a.go"}},
		},
		{
			name:  "single file above the limit stays complete",
			files: []ModifiedFile{file("a.go", 450)},
			want:  [][]string{{"a.go"}},
		},
		{
			name:  "three files of 200 produce two batches",
			files: []ModifiedFile{file("a.go", 200), file("b.go", 200), file("c.go", 200)},
			want:  [][]string{{"a.go", "b.go"}, {"c.go"}},
		},
		{
			name:  "100 350 50 cut between the first and the second",
			files: []ModifiedFile{file("a.go", 100), file("b.go", 350), file("c.go", 50)},
			want:  [][]string{{"a.go"}, {"b.go", "c.go"}},
		},
		{
			name:  "200 450 cut and the big one stays alone",
			files: []ModifiedFile{file("a.go", 200), file("b.go", 450)},
			want:  [][]string{{"a.go"}, {"b.go"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := flatten(buildBatches(tt.files)); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("buildBatches() = %v, expected %v", got, tt.want)
			}
		})
	}
}

func TestBuildBatchSequenceRespectsLayerOrder(t *testing.T) {
	byLayers := map[string][]ModifiedFile{
		"test":     {{Path: "internal/git/slice_test.go", Lines: 10}},
		"backend":  {{Path: "cmd/main.go", Lines: 10}},
		"config":   {{Path: "config.yaml", Lines: 10}},
		"frontend": {{Path: "web/app.tsx", Lines: 10}},
	}

	sequence := buildBatchSequence(byLayers)
	gotLayers := make([]string, 0, len(sequence))
	for _, batch := range sequence {
		gotLayers = append(gotLayers, batch.Layer)
	}
	want := []string{"config", "backend", "frontend", "test"}
	if !reflect.DeepEqual(gotLayers, want) {
		t.Errorf("layer order = %v, expected %v", gotLayers, want)
	}
}

func TestBuildBatchSequenceRespectsGroupingAndLimits(t *testing.T) {
	byLayers := map[string][]ModifiedFile{
		"config": {
			{Path: "config.yaml", Lines: 300},
			{Path: "config2.yaml", Lines: 300},
		},
		"backend": {
			{Path: "cmd/main.go", Lines: 10},
		},
	}

	sequence := buildBatchSequence(byLayers)
	if len(sequence) != 3 {
		t.Fatalf("expected 3 batches, got %d", len(sequence))
	}
	want := []batchWithLayer{
		{Layer: "config", Paths: []string{"config.yaml"}},
		{Layer: "config", Paths: []string{"config2.yaml"}},
		{Layer: "backend", Paths: []string{"cmd/main.go"}},
	}
	if !reflect.DeepEqual(sequence, want) {
		t.Errorf("sequence = %+v, expected %+v", sequence, want)
	}
}

type testAdapter struct{}

func (testAdapter) GetCommitMessage(paths []string, layer string, batchNum int) (string, error) {
	return "chore(slice): test", nil
}

func TestGetModifiedFilesInRealRepository(t *testing.T) {
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

	appendLines(t, "a.go", 300)
	if err := os.WriteFile("b.go", []byte("package b\n"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitInDir(t, dir, "add", "b.go")

	files, err := GetModifiedFiles()
	if err != nil {
		t.Fatalf("GetModifiedFiles returned error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d: %+v", len(files), files)
	}

	var a, b *ModifiedFile
	for i := range files {
		if files[i].Path == "a.go" {
			a = &files[i]
		}
		if files[i].Path == "b.go" {
			b = &files[i]
		}
	}
	if a == nil || b == nil {
		t.Fatalf("a.go and b.go not found: %+v", files)
	}
	if a.Lines != 300 {
		t.Errorf("a.go lines = %d, expected 300", a.Lines)
	}
	if a.Layer != "backend" || b.Layer != "backend" {
		t.Errorf("expected backend layers, got a=%q b=%q", a.Layer, b.Layer)
	}
}

type agentadapterFunc func(paths []string, layer string, batchNum int) (string, error)

func (f agentadapterFunc) GetCommitMessage(paths []string, layer string, batchNum int) (string, error) {
	return f(paths, layer, batchNum)
}

var _ agentadapter.AgentAdapter = agentadapterFunc(nil)

func TestIsOversizedConfig(t *testing.T) {
	tests := []struct {
		name string
		file ModifiedFile
		want bool
	}{
		{name: "config within the limit", file: ModifiedFile{Path: "config.yaml", Lines: 400, Layer: "config"}, want: false},
		{name: "config above the limit", file: ModifiedFile{Path: "config.yaml", Lines: 401, Layer: "config"}, want: true},
		{name: "backend above 400 is not giant config", file: ModifiedFile{Path: "a.go", Lines: 450, Layer: "backend"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isOversizedConfig(tt.file); got != tt.want {
				t.Errorf("isOversizedConfig(%+v) = %v, expected %v", tt.file, got, tt.want)
			}
		})
	}
}

func TestIsGiantCode(t *testing.T) {
	tests := []struct {
		name string
		file ModifiedFile
		want bool
	}{
		{name: "code within the limit", file: ModifiedFile{Path: "a.go", Lines: 500, Layer: "backend"}, want: false},
		{name: "code above the limit", file: ModifiedFile{Path: "a.go", Lines: 501, Layer: "backend"}, want: true},
		{name: "config above 500 is not giant code", file: ModifiedFile{Path: "config.yaml", Lines: 600, Layer: "config"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isGiantCode(tt.file); got != tt.want {
				t.Errorf("isGiantCode(%+v) = %v, expected %v", tt.file, got, tt.want)
			}
		})
	}
}

func TestGetModifiedFilesIncludesUntracked(t *testing.T) {
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

	// Untracked file: no git add is run, it must be detected anyway.
	content := "package new\n\nfunc Hello() {}\n"
	if err := os.WriteFile("new.go", []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	files, err := GetModifiedFiles()
	if err != nil {
		t.Fatalf("GetModifiedFiles returned error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 untracked file, got %d: %+v", len(files), files)
	}
	if files[0].Path != "new.go" {
		t.Errorf("path = %q, expected new.go", files[0].Path)
	}
	if files[0].Lines != 3 {
		t.Errorf("lines = %d, expected 3", files[0].Lines)
	}
	if files[0].Layer != "backend" {
		t.Errorf("layer = %q, expected backend", files[0].Layer)
	}
}

func TestParseNumstat(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []ModifiedFile
	}{
		{
			name:   "empty output",
			output: "",
			want:   nil,
		},
		{
			name:   "simple path",
			output: "10\t2\tcmd/main.go\n",
			want: []ModifiedFile{
				{Path: "cmd/main.go", Lines: 10, Layer: "backend"},
			},
		},
		{
			name:   "path with spaces stays intact",
			output: "1\t0\tfile with spaces.go\n",
			want: []ModifiedFile{
				{Path: "file with spaces.go", Lines: 1, Layer: "backend"},
			},
		},
		{
			// Flat form of a rename. The whole string "old => new" is NOT a
			// valid path for "git add"; only the destination is.
			name:   "flat rename returns only the destination",
			output: "0\t0\told file.go => new file.go\n",
			want: []ModifiedFile{
				{Path: "new file.go", Lines: 0, Layer: "backend"},
			},
		},
		{
			// Abbreviated form with braces: git replaces only the stretch
			// that changes. The destination path must be rebuilt by
			// substituting the "{old => new}" block with its right half.
			name:   "braced rename rebuilds the destination path",
			output: "0\t0\tdir/{old => sub1/new}.go\n",
			want: []ModifiedFile{
				{Path: "dir/sub1/new.go", Lines: 0, Layer: "backend"},
			},
		},
		{
			name:   "binary marked with dash is ignored",
			output: "-\t-\timage.png\n",
			want:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseNumstat(tt.output); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("parseNumstat(%q) = %+v, expected %+v", tt.output, got, tt.want)
			}
		})
	}
}

func TestUntrackedPaths(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   []string
	}{
		{
			name:   "empty output",
			output: "",
			want:   nil,
		},
		{
			name:   "one simple file",
			output: "?? a.go\x00",
			want:   []string{"a.go"},
		},
		{
			// git status --porcelain -z emits the path raw, without quotes
			// or escapes: the path with spaces stays intact without needing
			// to unquote anything.
			name:   "path with spaces stays intact",
			output: "?? file with spaces.go\x00",
			want:   []string{"file with spaces.go"},
		},
		{
			// With --short (no -z), this same path would arrive quoted and
			// with octal escapes for the "ó" (B3 after the B1 fix).
			// With -z it arrives in pure UTF-8, without quotes or escapes.
			name:   "path with accented letter arrives without quotes or octal escapes",
			output: "?? configuración.go\x00",
			want:   []string{"configuración.go"},
		},
		{
			name:   "tracked entries are ignored",
			output: " M file.go\x00A  other.go\x00",
			want:   nil,
		},
		{
			name:   "mix of tracked and untracked",
			output: " M file.go\x00?? new.go\x00?? other with spaces.go\x00",
			want:   []string{"new.go", "other with spaces.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := untrackedPaths(tt.output); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("untrackedPaths(%q) = %+v, expected %+v", tt.output, got, tt.want)
			}
		})
	}
}

func TestCheckDiffLimitsWithUntrackedFileWithSpaces(t *testing.T) {
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

	// Reproduces the regression: with --short (no -z), "file with spaces.go"
	// arrives quoted; fields[1] of strings.Fields ends up as `"file`,
	// countPhysicalLines cannot open it and CheckDiffLimits returns ERROR
	// instead of measuring the volume. That would block the whole pre-commit
	// hook.
	content := strings.Repeat("// generated line\n", 450)
	if err := os.WriteFile(filepath.Join(dir, "file with spaces.go"), []byte(content), 0644); err != nil {
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

func TestGetModifiedFilesWithAccentedUntracked(t *testing.T) {
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

	// With --short (no -z), git emits this path with octal escapes for the
	// "ó" (e.g. \303\263), not just quoted. The verification is that the
	// returned path really exists on disk, not that it "looks" right.
	if err := os.WriteFile(filepath.Join(dir, "configuración.go"), []byte("package a\n\nfunc Hello() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	files, err := GetModifiedFiles()
	if err != nil {
		t.Fatalf("GetModifiedFiles returned error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d: %+v", len(files), files)
	}
	if _, err := os.Stat(filepath.Join(dir, files[0].Path)); err != nil {
		t.Errorf("the returned path %q does not exist on disk: %v", files[0].Path, err)
	}
}

func TestGetModifiedFilesWithPathWithSpaces(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{
		"file with spaces.go": "package a\n",
	})
	t.Chdir(dir)

	// Reproduces B3: strings.Fields split the path with spaces and only kept
	// the first fragment.
	appendLines(t, "file with spaces.go", 5)

	files, err := GetModifiedFiles()
	if err != nil {
		t.Fatalf("GetModifiedFiles returned error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d: %+v", len(files), files)
	}
	if files[0].Path != "file with spaces.go" {
		t.Errorf("path = %q, expected %q", files[0].Path, "file with spaces.go")
	}
}

// TestGetModifiedFilesWithRenameReturnsGitUsablePath reproduces a real
// rename (git mv) with spaces and checks that the returned path is not the
// raw numstat string ("old => new") but a path that really exists in the
// worktree and that "git add" accepts. Before the fix, slice passed the raw
// string to "git add" and failed with exit 128 right when trying to release
// the guardian.
func TestGetModifiedFilesWithRenameReturnsGitUsablePath(t *testing.T) {
	if testing.Short() {
		t.Skip("skips the real git repository integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	dir := prepareTestRepo(t, map[string]string{
		"file with spaces.go": "package a\n",
	})
	t.Chdir(dir)

	runGitInDir(t, dir, "mv", "file with spaces.go", "renamed with spaces.go")
	runGitInDir(t, dir, "add", "-A")

	files, err := GetModifiedFiles()
	if err != nil {
		t.Fatalf("GetModifiedFiles returned error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 renamed file, got %d: %+v", len(files), files)
	}
	path := files[0].Path

	// The path must really exist in the worktree...
	if _, err := os.Stat(filepath.Join(dir, path)); err != nil {
		t.Errorf("the returned path %q does not exist on disk: %v", path, err)
	}
	// ...and "git add" must accept it without failing (exactly what slice does).
	cmd := exec.Command("git", "-C", dir, "add", "--", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("git add %q failed: %v\n%s", path, err, out)
	}
}

type testAdapterWithDiff struct {
	receivedDiff string
}

func (a *testAdapterWithDiff) GetCommitMessage(paths []string, layer string, batchNum int) (string, error) {
	return "chore(slice): base", nil
}

func (a *testAdapterWithDiff) GetCommitMessageWithDiff(paths []string, layer string, batchNum int, diff string) (string, error) {
	a.receivedDiff = diff
	return "chore(slice): with diff", nil
}

var _ agentadapter.AdapterWithDiff = (*testAdapterWithDiff)(nil)
