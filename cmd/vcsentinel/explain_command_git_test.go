package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestExplainDiffArgsForcePrefixesAgainstRealGit answers the review
// finding against b7ac0cf and 0269350 with Git itself rather than with a
// fabricated diff. The parser recognises a path by its b/ prefix, and a header
// it does not recognise loses its added lines silently, which starves every
// content-reading detector.
//
// Each case runs the diff twice, once without the forced flags and once with
// them, and asserts both headers. Asserting only the forced one would prove the
// header is right without proving the flags are what makes it right, and would
// leave every comparative claim resting on prose.
//
// What the two runs establish: diff.noprefix and the diff.srcPrefix and
// diff.dstPrefix pair each change the unforced header, so the flags are load
// bearing. diff.mnemonicPrefix does not, because this command diffs one commit
// against another and Git keeps a/ and b/ for a commit-to-commit range whether
// the setting is on or not. That case is a guard for a future caller diffing
// the index or the worktree, where mnemonic prefixes do substitute c/, i/ and
// w/ — ticket 05 reaches those callers — and the test now proves it is a guard
// rather than claiming it.
//
// The forced run uses explainDiffArgs, so the test cannot drift from the
// invocation the command actually issues.
func TestExplainDiffArgsForcePrefixesAgainstRealGit(t *testing.T) {
	dir := t.TempDir()
	environ := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + dir,
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = dir, environ
		var errOut strings.Builder
		cmd.Stderr = &errOut
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, errOut.String())
		}
		return string(output)
	}

	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-qm", "first")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-qm", "second")

	const forced = "+++ b/a.txt"
	scenarios := []struct {
		name      string
		config    [][2]string
		unforced  string // the header this setting produces when the flags are absent
		guardOnly bool   // true when the setting cannot change a commit-to-commit header
	}{
		{name: "no configuration", unforced: forced, guardOnly: true},
		{name: "diff.noprefix", config: [][2]string{{"diff.noprefix", "true"}}, unforced: "+++ a.txt"},
		{name: "diff.mnemonicPrefix", config: [][2]string{{"diff.mnemonicPrefix", "true"}}, unforced: forced, guardOnly: true},
		{name: "diff.srcPrefix and diff.dstPrefix", config: [][2]string{{"diff.srcPrefix", "X/"}, {"diff.dstPrefix", "Y/"}}, unforced: "+++ Y/a.txt"},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			for _, setting := range scenario.config {
				run("config", setting[0], setting[1])
			}
			defer func() {
				for _, setting := range scenario.config {
					run("config", "--unset", setting[0])
				}
			}()

			if got := postImageHeader(run("diff", "--no-color", "--unified=0", "-M", "HEAD^..HEAD")); got != scenario.unforced {
				t.Errorf("unforced header under %s = %q, want %q", scenario.name, got, scenario.unforced)
			}
			if got := postImageHeader(run(explainDiffArgs("HEAD^..HEAD")...)); got != forced {
				t.Errorf("forced header under %s = %q, want %q; the flags did not override the setting",
					scenario.name, got, forced)
			}
			// Stated rather than left implicit: a case whose unforced header is
			// already correct proves nothing about the flags, and is kept only
			// so a future caller that can break it fails here.
			if scenario.guardOnly != (scenario.unforced == forced) {
				t.Errorf("%s is marked guard-only=%t but its unforced header is %q",
					scenario.name, scenario.guardOnly, scenario.unforced)
			}
		})
	}
}

// postImageHeader returns the first +++ header of a diff, or the empty string
// when there is none.
func postImageHeader(diff string) string {
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++ ") {
			return line
		}
	}
	return ""
}
