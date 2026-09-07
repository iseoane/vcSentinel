package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectCIDetected(t *testing.T) {
	for _, name := range []string{"ci.yml", "ci.yaml"} {
		worktree := t.TempDir()
		if err := os.MkdirAll(filepath.Join(worktree, ".github", "workflows"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(worktree, ".github", "workflows", name), []byte("jobs: {}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if !DetectCI(worktree) {
			t.Errorf("DetectCI = false with .github/workflows/%s present", name)
		}
	}
}

func TestDetectCINotDetected(t *testing.T) {
	worktree := t.TempDir()
	if DetectCI(worktree) {
		t.Error("DetectCI = true in a worktree without CI configuration")
	}
}

func TestDetectCIOtherProviders(t *testing.T) {
	cases := map[string]string{
		"GitLab":   ".gitlab-ci.yml",
		"CircleCI": ".circleci/config.yml",
		"Azure":    ".azure-pipelines.yml",
		"Jenkins":  "Jenkinsfile",
	}
	for name, path := range cases {
		worktree := t.TempDir()
		parent := filepath.Dir(filepath.Join(worktree, path))
		if err := os.MkdirAll(parent, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(worktree, path), []byte("# ci\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if !DetectCI(worktree) {
			t.Errorf("DetectCI = false with %s present (%s)", path, name)
		}
	}
}
