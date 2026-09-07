package graph

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitWithEnv(t *testing.T, git, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command(git, append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return string(output)
}

func TestSelectGlobalExcludes(t *testing.T) {
	home := t.TempDir()
	xdg := filepath.Join(home, "xdg")
	existing := filepath.Join(home, "ignore-global")
	if err := os.WriteFile(existing, []byte("*.local.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	def := filepath.Join(xdg, "git", "ignore")
	if err := os.MkdirAll(filepath.Dir(def), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(def, []byte("*.local.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		configured string
		xdg        string
		want       string
	}{
		{"configured absolute wins over default", existing, xdg, existing},
		{"tilde expands against parent home", "~/ignore-global", xdg, existing},
		{"configured but missing falls back to nothing", filepath.Join(home, "missing"), xdg, ""},
		{"tilde of another user is unresolvable", "~other/ignore", xdg, ""},
		{"unset falls back to xdg default", "", xdg, def},
		{"unset without default file is empty", "", filepath.Join(home, "no-xdg"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := selectGlobalExcludes(tc.configured, home, tc.xdg); got != tc.want {
				t.Errorf("selectGlobalExcludes(%q) = %q, want %q", tc.configured, got, tc.want)
			}
		})
	}
}

func TestContextPassesExcludesToChild(t *testing.T) {
	p, fake := providerWithAnswers(t, `{"initialized":true,"projectPath":"ROOT","pendingChanges":{"added":0,"modified":0,"removed":0},"worktreeMismatch":null}`)
	excludesFile := filepath.Join(t.TempDir(), "ignore-global")
	if err := os.WriteFile(excludesFile, []byte("*.local.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.excludes = func(string) string { return excludesFile }
	fake.answers = append(fake.answers, []byte(`{"changedFiles":[],"affectedTests":[],"totalDependentsTraversed":0}`))
	if _, err := p.Context("head", []string{"a.go"}); err != nil {
		t.Fatalf("Context = %v", err)
	}
	if len(fake.calls) < 2 || len(fake.calls[1].args) != 4 ||
		fake.calls[1].args[0] != "-c" ||
		fake.calls[1].args[1] != "core.excludesFile="+excludesFile ||
		fake.calls[1].args[2] != "status" || fake.calls[1].args[3] != "--porcelain" {
		t.Fatalf("status without explicit excludes: %+v", fake.calls)
	}
	if len(fake.calls[0].args) != 3 || fake.calls[0].args[0] != "rev-parse" {
		t.Fatalf("rev-parse must not carry -c: %+v", fake.calls[0])
	}
}

func TestSanitizedChildHonorsExcludes(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	gitWithEnv(t, git, repo, os.Environ(), "init", "-q", ".")
	gitWithEnv(t, git, repo, os.Environ(), "config", "user.email", "t@t")
	gitWithEnv(t, git, repo, os.Environ(), "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitWithEnv(t, git, repo, os.Environ(), "add", "base.txt")
	gitWithEnv(t, git, repo, os.Environ(), "commit", "-qm", "init")
	if err := os.WriteFile(filepath.Join(repo, "settings.local.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "info-only.tmp"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	infoExclude, err := os.OpenFile(filepath.Join(repo, ".git", "info", "exclude"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := infoExclude.WriteString("info-only.tmp\n"); err != nil {
		t.Fatal(err)
	}
	infoExclude.Close()
	excludesFile := filepath.Join(t.TempDir(), "ignore-global")
	if err := os.WriteFile(excludesFile, []byte("*.local.json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sanitized := []string{"PATH=" + filepath.Dir(git), "SystemRoot=" + os.Getenv("SystemRoot")}
	withoutExcludes := strings.TrimSpace(gitWithEnv(t, git, repo, sanitized, "status", "--porcelain"))
	if !strings.Contains(withoutExcludes, "settings.local.json") {
		t.Fatalf("child without excludes does not see the globally ignored file: %q", withoutExcludes)
	}
	if strings.Contains(withoutExcludes, "info-only.tmp") {
		t.Fatalf("info/exclude should suffice on its own: %q", withoutExcludes)
	}
	withExcludes := strings.TrimSpace(gitWithEnv(t, git, repo, sanitized, "-c", "core.excludesFile="+excludesFile, "status", "--porcelain"))
	if withExcludes != "" {
		t.Fatalf("child with explicit excludes still dirty: %q", withExcludes)
	}
}
