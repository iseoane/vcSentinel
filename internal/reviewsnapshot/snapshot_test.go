package reviewsnapshot

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSafePathsFiltersHostileVocabulary(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool // kept?
	}{
		{"simple relative", "cmd/sentinel/main.go", true},
		{"dot-relative", "./internal/store", true},
		{"backslash normalized", `internal\git\slice.go`, true},
		{"absolute rejected", "/etc/passwd", false},
		{"drive letter rejected", `C:\Windows`, false},
		{"parent escape rejected", "../secrets", false},
		{"bare dot rejected", ".", false},
		{"bare dotdot rejected", "..", false},
		{"option-looking rejected", "-rf", false},
		{"glob metachars rejected", "a*b.go", false},
		{"control char rejected", "a\x00b", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SafePaths([]string{tc.in})
			if tc.want && len(got) != 1 {
				t.Fatalf("SafePaths(%q) = %v, want it kept", tc.in, got)
			}
			if !tc.want && len(got) != 0 {
				t.Fatalf("SafePaths(%q) = %v, want it dropped", tc.in, got)
			}
		})
	}
}

func TestSafePathsCleansNavigation(t *testing.T) {
	got := SafePaths([]string{"internal/./store/../review/engine.go"})
	if len(got) != 1 || got[0] != "internal/review/engine.go" {
		t.Fatalf("SafePaths cleaned = %v, want [internal/review/engine.go]", got)
	}
}

// gitInit commits one file in a fresh repository and returns its root plus
// the short-safe full SHA of the commit.
func gitInit(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "audited.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "seed")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, string(out[:len(out)-1])
}

func TestCreateMaterializesCommittedContentAndCleansUp(t *testing.T) {
	root, sha := gitInit(t)
	snapshot, survived, cleanup, err := Create(root, sha, []string{"audited.go", "missing.go"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(snapshot, "audited.go"))
	if err != nil || string(data) != "package p\n" {
		t.Fatalf("snapshot content = %q, err=%v, want committed bytes", data, err)
	}
	if len(survived) != 1 || survived[0] != "audited.go" {
		t.Fatalf("survived = %v, want only audited.go (committed regular files)", survived)
	}
	cleanup()
	if _, err := os.Stat(snapshot); !os.IsNotExist(err) {
		t.Fatalf("snapshot still exists after cleanup: %v", err)
	}
}

func TestCreateRequiresSHA(t *testing.T) {
	if _, _, _, err := Create("", "", nil); err == nil {
		t.Fatal("Create without SHA must fail before any git call")
	}
}
