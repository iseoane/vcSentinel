package git

import (
	"os"
	"strings"
	"testing"
)

// TestDiffCommitForcesStablePrefixes covers what the review-plan tests cannot:
// they feed the plan derivation a hand-written diff, so an incorrect flag here
// would leave them green while the production planner is starved. The planner
// recognises a path by its "b/" prefix, and diff.noprefix or a custom prefix
// changes that header, losing every added line with no error.
//
// It uses prepareTempRepo, the package fixture, which changes the process
// working directory because the helpers under test operate on it. That is the
// documented convention here and the reason this package must not use
// t.Parallel(); no test in it does.
func TestDiffCommitForcesStablePrefixes(t *testing.T) {
	repo := prepareTempRepo(t)
	_ = repo

	writeAndCommit := func(content, message string) {
		t.Helper()
		if err := os.WriteFile("a.txt", []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := runGitOutput("add", "a.txt"); err != nil {
			t.Fatalf("add: %v (%s)", err, out)
		}
		if out, err := runGitOutput("commit", "-qm", message); err != nil {
			t.Fatalf("commit: %v (%s)", err, out)
		}
	}
	writeAndCommit("one\n", "first")
	writeAndCommit("one\ntwo\n", "second")

	for _, tc := range []struct{ name, key, value string }{
		{"no configuration", "", ""},
		{"diff.noprefix", "diff.noprefix", "true"},
		{"diff.dstPrefix", "diff.dstPrefix", "Y/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.key != "" {
				if out, err := runGitOutput("config", tc.key, tc.value); err != nil {
					t.Fatalf("config: %v (%s)", err, out)
				}
				defer runGitOutput("config", "--unset", tc.key)
			}
			commit, err := DiffCommit("HEAD")
			if err != nil {
				t.Fatalf("DiffCommit: %v", err)
			}
			if !strings.Contains(commit, "+++ b/a.txt") {
				t.Errorf("DiffCommit under %s produced no \"+++ b/a.txt\" header:\n%s", tc.name, commit)
			}
			diffOut, err := RangeDiff("HEAD^", "HEAD")
			if err != nil {
				t.Fatalf("RangeDiff: %v", err)
			}
			if !strings.Contains(diffOut, "+++ b/a.txt") {
				t.Errorf("RangeDiff under %s produced no \"+++ b/a.txt\" header:\n%s", tc.name, diffOut)
			}
		})
	}
}
