package git

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectarCIVerdadero(t *testing.T) {
	worktree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(worktree, ".github", "workflows"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".github", "workflows", "ci.yml"), []byte("jobs: {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if !DetectarCI(worktree) {
		t.Error("DetectarCI = false con .github/workflows/ci.yml presente")
	}
}

func TestDetectarCIFalso(t *testing.T) {
	worktree := t.TempDir()
	if DetectarCI(worktree) {
		t.Error("DetectarCI = true en un worktree sin configuración de CI")
	}
}

func TestDetectarCIOtrosProveedores(t *testing.T) {
	casos := map[string]string{
		"GitLab":   ".gitlab-ci.yml",
		"CircleCI": ".circleci/config.yml",
		"Azure":    ".azure-pipelines.yml",
		"Jenkins":  "Jenkinsfile",
	}
	for nombre, ruta := range casos {
		worktree := t.TempDir()
		padre := filepath.Dir(filepath.Join(worktree, ruta))
		if err := os.MkdirAll(padre, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(worktree, ruta), []byte("# ci\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if !DetectarCI(worktree) {
			t.Errorf("DetectarCI = false con %s presente (%s)", ruta, nombre)
		}
	}
}
