package git

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMeasureStagedVolumeUsesTheIndexScope(t *testing.T) {
	if testing.Short() {
		t.Skip("skips real Git scope integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	tests := []struct {
		name           string
		stagedLines    int
		unstagedLines  int
		wantStaged     int
		wantWorktree   int
		wantStagedPath bool
	}{
		{name: "staged-only", stagedLines: 120, wantStaged: 120, wantWorktree: 120, wantStagedPath: true},
		{name: "unstaged-only", unstagedLines: 120, wantStaged: 0, wantWorktree: 120},
		{name: "mixed", stagedLines: 120, unstagedLines: 450, wantStaged: 120, wantWorktree: 570, wantStagedPath: true},
		{name: "empty-index", wantStaged: 0, wantWorktree: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := prepareTestRepo(t, map[string]string{"base.go": "package base\n"})
			t.Chdir(dir)

			if tt.stagedLines > 0 {
				writeLines(t, filepath.Join(dir, "staged.go"), tt.stagedLines)
				runGitInDir(t, dir, "add", "staged.go")
			}
			if tt.unstagedLines > 0 {
				writeLines(t, filepath.Join(dir, "unstaged.go"), tt.unstagedLines)
			}

			staged, err := MeasureStagedVolume()
			if err != nil {
				t.Fatalf("MeasureStagedVolume returned error: %v", err)
			}
			if staged.Blocking != tt.wantStaged {
				t.Errorf("staged authored lines = %d, want %d", staged.Blocking, tt.wantStaged)
			}
			if tt.wantStagedPath && len(staged.Paths) != 1 {
				t.Errorf("staged paths = %v, want the staged file only", staged.Paths)
			}
			if !tt.wantStagedPath && len(staged.Paths) != 0 {
				t.Errorf("staged paths = %v, want none", staged.Paths)
			}

			worktree, err := MeasureVolume()
			if err != nil {
				t.Fatalf("MeasureVolume returned error: %v", err)
			}
			if worktree.Blocking != tt.wantWorktree {
				t.Errorf("worktree authored lines = %d, want %d", worktree.Blocking, tt.wantWorktree)
			}
		})
	}
}

func TestMeasureStagedVolumeFailureIsBlocking(t *testing.T) {
	if testing.Short() {
		t.Skip("skips real Git failure integration in short mode")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not available in PATH")
	}

	t.Chdir(t.TempDir())
	volume, err := MeasureStagedVolume()
	if err == nil {
		t.Fatal("MeasureStagedVolume should fail outside a Git repository")
	}
	if volume.State != "ERROR" {
		t.Errorf("failure state = %q, want ERROR", volume.State)
	}
}
