package graph

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitConEnv(t *testing.T, git, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(git, append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	salida, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, salida)
	}
	return string(salida)
}

func TestSeleccionarExcludesGlobal(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg")
	existente := filepath.Join(home, "ignorar-global")
	if err := os.WriteFile(existente, []byte("*.local.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	defecto := filepath.Join(xdg, "git", "ignore")
	if err := os.MkdirAll(filepath.Dir(defecto), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(defecto, []byte("*.local.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	casos := []struct {
		nombre      string
		configurado string
		xdg         string
		want        string
	}{
		{"configured absolute wins over default", existente, xdg, existente},
		{"tilde expands against parent home", "~/ignorar-global", xdg, existente},
		{"configured but missing falls back to nothing", filepath.Join(home, "ausente"), xdg, ""},
		{"tilde of another user is unresolvable", "~otro/ignorar", xdg, ""},
		{"unset falls back to xdg default", "", xdg, defecto},
		{"unset without default file is empty", "", filepath.Join(home, "sin-xdg"), ""},
	}
	for _, tc := range casos {
		t.Run(tc.nombre, func(t *testing.T) {
			if got := seleccionarExcludes(tc.configurado, home, tc.xdg); got != tc.want {
				t.Errorf("seleccionarExcludes(%q) = %q, want %q", tc.configurado, got, tc.want)
			}
		})
	}
}

func TestContextoPasaExcludesAlHijo(t *testing.T) {
	p, fake := proveedorConRespuestas(t, `{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`)
	excluye := filepath.Join(t.TempDir(), "ignorar-global")
	if err := os.WriteFile(excluye, []byte("*.local.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.excludes = func(string) string { return excluye }
	fake.respuestas = append(fake.respuestas, []byte(`{"changedFiles":[],"affectedTests":[],"totalDependentsTraversed":0}`))
	if _, err := p.Contexto("head", []string{"a.go"}); err != nil {
		t.Fatalf("Contexto = %v", err)
	}
	if len(fake.llamadas) < 2 || len(fake.llamadas[1].args) != 4 ||
		fake.llamadas[1].args[0] != "-c" ||
		fake.llamadas[1].args[1] != "core.excludesFile="+excluye ||
		fake.llamadas[1].args[2] != "status" || fake.llamadas[1].args[3] != "--porcelain" {
		t.Fatalf("status sin excludes explícito: %+v", fake.llamadas)
	}
	if len(fake.llamadas[0].args) != 3 || fake.llamadas[0].args[0] != "rev-parse" {
		t.Fatalf("rev-parse no debe llevar -c: %+v", fake.llamadas[0])
	}
}

func TestHijoSaneadoRespetaExcludes(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	gitConEnv(t, git, repo, os.Environ(), "init", "-q", ".")
	gitConEnv(t, git, repo, os.Environ(), "config", "user.email", "t@t")
	gitConEnv(t, git, repo, os.Environ(), "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitConEnv(t, git, repo, os.Environ(), "add", "base.txt")
	gitConEnv(t, git, repo, os.Environ(), "commit", "-qm", "init")
	if err := os.WriteFile(filepath.Join(repo, "ajustes.local.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "solo-info.tmp"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	infoExclude, err := os.OpenFile(filepath.Join(repo, ".git", "info", "exclude"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := infoExclude.WriteString("solo-info.tmp\n"); err != nil {
		t.Fatal(err)
	}
	infoExclude.Close()
	excluye := filepath.Join(t.TempDir(), "ignorar-global")
	if err := os.WriteFile(excluye, []byte("*.local.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	saneado := []string{"PATH=" + filepath.Dir(git), "SystemRoot=" + os.Getenv("SystemRoot")}
	sinExcludes := strings.TrimSpace(gitConEnv(t, git, repo, saneado, "status", "--porcelain"))
	if !strings.Contains(sinExcludes, "ajustes.local.json") {
		t.Fatalf("hijo sin excludes no ve el ignorado global: %q", sinExcludes)
	}
	if strings.Contains(sinExcludes, "solo-info.tmp") {
		t.Fatalf("info/exclude debería bastar sin ayuda: %q", sinExcludes)
	}
	conExcludes := strings.TrimSpace(gitConEnv(t, git, repo, saneado, "-c", "core.excludesFile="+excluye, "status", "--porcelain"))
	if conExcludes != "" {
		t.Fatalf("hijo con excludes explícito sigue sucio: %q", conExcludes)
	}
}
