package reviewsnapshot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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

func TestSafePathsFiltersSensitiveEnvironmentFilesCaseInsensitively(t *testing.T) {
	paths := []string{
		`config\service.yaml`,
		`.env`,
		`config/.ENV`,
		`config/service.EnV.Local`,
		`config/service.env.example.extra`,
		`.env.example`,
		`config/service.env.example`,
		`config/SERVICE.ENV.EXAMPLE`,
	}
	want := []string{
		"config/service.yaml",
		".env.example",
		"config/service.env.example",
		"config/SERVICE.ENV.EXAMPLE",
	}

	if got := SafePaths(paths); !reflect.DeepEqual(got, want) {
		t.Fatalf("SafePaths() = %v, want %v", got, want)
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
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("SENSITIVE=value\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env.example"), []byte("SENSITIVE=example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// context.go is committed but never audited: it exists only so tests can
	// assert the whole-tree snapshot carries context beyond the audited paths.
	if err := os.WriteFile(filepath.Join(dir, "context.go"), []byte("package p\n// context\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	// A symlink tree entry (mode 120000) is added through git plumbing rather
	// than os.Symlink so this fixture behaves identically on Windows, which
	// has no unprivileged symlink support: a real filesystem symlink would
	// make this test flaky or fail there. This still exercises the mode
	// filter that must reject symlink blobs everywhere.
	blob := writeBlob(t, dir, "audited.go")
	run("update-index", "--add", "--cacheinfo", "120000,"+blob+",link.go")
	run("commit", "-qm", "seed")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, string(out[:len(out)-1])
}

// writeBlob hashes the committed content of an existing file at dir into the
// object database and returns its blob SHA, so a symlink tree entry can be
// added by cacheinfo without ever creating a real filesystem symlink.
func writeBlob(t *testing.T, dir, relativePath string) string {
	t.Helper()
	cmd := exec.Command("git", "hash-object", "-w", relativePath)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git hash-object: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func TestCreateMaterializesCommittedContentAndCleansUp(t *testing.T) {
	root, sha := gitInit(t)
	snapshot, survived, cleanup, err := Create(context.Background(), root, sha, []string{"audited.go", "missing.go"})
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

func TestCreateNeverMaterializesSensitiveEnvironmentFiles(t *testing.T) {
	root, sha := gitInit(t)
	snapshot, survived, cleanup, err := Create(context.Background(), root, sha, []string{".env", ".env.example"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer cleanup()

	if !reflect.DeepEqual(survived, []string{".env.example"}) {
		t.Fatalf("survived = %v, want only .env.example", survived)
	}
	if _, err := os.Stat(filepath.Join(snapshot, ".env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sensitive environment file exists in snapshot: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(snapshot, ".env.example"))
	if err != nil || string(data) != "SENSITIVE=example\n" {
		t.Fatalf("example content = %q, err=%v", data, err)
	}
}

// TestCreateMaterializesWholeCommittedTreeAsReadOnlyContext covers the
// confirmed defect: the reviewer needs to read files that are not under
// audit to understand the change, and a denied read for one of those files
// silently kills the whole turn. The snapshot must therefore carry every
// committed regular file, while allowed keeps naming only the audited paths.
func TestCreateMaterializesWholeCommittedTreeAsReadOnlyContext(t *testing.T) {
	root, sha := gitInit(t)
	snapshot, allowed, cleanup, err := Create(context.Background(), root, sha, []string{"audited.go"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer cleanup()

	if !reflect.DeepEqual(allowed, []string{"audited.go"}) {
		t.Fatalf("allowed = %v, want exactly the audited paths [audited.go]", allowed)
	}

	data, err := os.ReadFile(filepath.Join(snapshot, "context.go"))
	if err != nil || string(data) != "package p\n// context\n" {
		t.Fatalf("context.go missing from the whole-tree snapshot: content=%q err=%v", data, err)
	}

	if _, err := os.Stat(filepath.Join(snapshot, ".env")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf(".env leaked into the whole-tree snapshot: %v", err)
	}

	if _, err := os.Stat(filepath.Join(snapshot, "link.go")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the committed symlink (mode 120000) was materialized as a file: %v", err)
	}
}

func TestCreateRequiresSHA(t *testing.T) {
	if _, _, _, err := Create(context.Background(), "", "", nil); err == nil {
		t.Fatal("Create without SHA must fail before any git call")
	}
}

// TestCreateReapsAbandonedSnapshots covers a real leak: Create's cleanup is a
// defer in the caller, and a defer does not run when the process dies hard
// (Ctrl+C, aborted gate, daemon shutdown). There was NO reaper at all — the
// only place mentioning the prefix was the line that creates them — so every
// interrupted review left its snapshot behind forever.
//
// Measured on this machine: 472 MB accumulated on a 3.8 GB tmpfs. When /tmp
// fills up, not only do reviews fail; even building fails.
func TestCreateReapsAbandonedSnapshots(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)

	abandoned := filepath.Join(root, "vas-sentinel-review-abandonado")
	if err := os.MkdirAll(filepath.Join(abandoned, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	staleTime := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(abandoned, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}
	inProgress := filepath.Join(root, "vas-sentinel-review-en-curso")
	if err := os.MkdirAll(inProgress, 0o700); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(root, "something-else-entirely")
	if err := os.MkdirAll(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(foreign, staleTime, staleTime); err != nil {
		t.Fatal(err)
	}

	reaped := reapAbandonedSnapshots(time.Now(), staleSnapshotAge)

	if reaped != 1 {
		t.Errorf("reaped = %d, want 1", reaped)
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Error("the abandoned snapshot is still there: the leak continues")
	}
	if _, err := os.Stat(inProgress); err != nil {
		t.Error("a recent snapshot was deleted: it could be in use by a live review")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Error("a directory that is not ours was deleted")
	}
}

// gitInitManyFiles commits n regular files in a fresh repository, returning
// its root and the commit SHA. It exists so a context-cancellation test can
// force Create's whole-tree materialization loop to run more than one
// iteration deterministically, without depending on real elapsed time.
func gitInitManyFiles(t *testing.T, n int) (string, string) {
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
	for i := 0; i < n; i++ {
		name := "file" + strconv.Itoa(i) + ".go"
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package p\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("add", ".")
	run("commit", "-qm", "seed")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, string(out[:len(out)-1])
}

// countingCancelContext cancels itself the Nth time its own Err method is
// called, rather than after a fixed real-time delay. This lets a test drive
// Create's context check into the MIDDLE of a long loop deterministically:
// the exact call at which cancellation lands is a counted event, not a race
// against wall-clock timing the way the acpadapter integration test is.
type countingCancelContext struct {
	context.Context
	count  int32
	limit  int32
	cancel context.CancelFunc
}

func (c *countingCancelContext) Err() error {
	if atomic.AddInt32(&c.count, 1) == c.limit {
		c.cancel()
	}
	return c.Context.Err()
}

func newCountingCancelContext(limit int32) *countingCancelContext {
	base, cancel := context.WithCancel(context.Background())
	return &countingCancelContext{Context: base, limit: limit, cancel: cancel}
}

// TestCreateAbortsWhenContextAlreadyCanceled covers the cancel-before-call
// case: Create must never start any git work against an already-dead
// context, and must wrap the exact stdlib sentinel so
// errors.Is(err, context.Canceled) holds for every classifier that depends
// on it (reviewexec.DefaultClassifier, internal/execution's classify).
func TestCreateAbortsWhenContextAlreadyCanceled(t *testing.T) {
	root, sha := gitInit(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	snapshot, _, cleanup, err := Create(ctx, root, sha, []string{"audited.go"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want errors.Is(err, context.Canceled)", err)
	}
	if cleanup != nil {
		t.Fatalf("cleanup = %p, want nil: no snapshot should have been created", cleanup)
	}
	if snapshot != "" {
		t.Fatalf("snapshot = %q, want empty", snapshot)
	}
}

// TestCreateAbortsWhenDeadlineAlreadyExceeded is the deadline counterpart:
// the same abort path must instead wrap context.DeadlineExceeded, because
// several classifiers in this repository treat an exhausted budget and a
// deliberate cancellation as different outcome classes.
func TestCreateAbortsWhenDeadlineAlreadyExceeded(t *testing.T) {
	root, sha := gitInit(t)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, _, cleanup, err := Create(ctx, root, sha, []string{"audited.go"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want errors.Is(err, context.DeadlineExceeded)", err)
	}
	if cleanup != nil {
		t.Fatalf("cleanup = %p, want nil", cleanup)
	}
}

// TestCreateAbortsDuringMaterializationAndLeavesNoSnapshot is the
// deterministic replacement for the racy 150ms acpadapter integration test:
// it forces cancellation to land mid-loop, inside the whole-tree
// materialization Create performs before it ever returns, and asserts both
// that the wrapped sentinel survives and that cleanup left no directory
// behind — an aborted review must never leak a partial snapshot.
func TestCreateAbortsDuringMaterializationAndLeavesNoSnapshot(t *testing.T) {
	root, sha := gitInitManyFiles(t, 30)
	// limit=4 lets the entry check (1) and a couple of loop iterations (2, 3)
	// pass uncancelled, then cancels on the 4th Err() call, which lands
	// inside the materialization loop long before all 30 files are written.
	cancelCtx := newCountingCancelContext(4)

	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	_, _, cleanup, err := Create(cancelCtx, root, sha, []string{"file0.go"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want errors.Is(err, context.Canceled)", err)
	}
	if cleanup != nil {
		t.Fatalf("cleanup = %p, want nil: Create must clean up before returning on abort", cleanup)
	}
	entries, readErr := os.ReadDir(tmp)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), snapshotPrefix) {
			t.Fatalf("snapshot directory %q survived an aborted materialization", entry.Name())
		}
	}
}
