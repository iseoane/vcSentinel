package main

import (
	"fmt"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
)

// TestHelperProcess is not a real test: it is the child process started by
// this package's exit-code tests to exercise functions that call os.Exit
// without killing the parent `go test` process (the standard Go pattern, the
// same one the os/exec library uses to test itself). Without the environment
// variable guard, a normal `go test` run treats it as an empty passing test.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("VAS_SENTINEL_HELPER_PROCESS") != "1" {
		return
	}
	worktree := os.Getenv("VAS_SENTINEL_HELPER_WORKTREE")
	switch os.Getenv("VAS_SENTINEL_HELPER_FN") {
	case "runLint":
		runLint(worktree)
	case "runReview":
		runReview(worktree, []string{"HEAD"})
	case "runPrReview":
		runPrReview(worktree, nil)
	}
	os.Exit(0)
}

// runAsSubprocess relaunches this same test binary to invoke fn (one of the
// branches of TestHelperProcess) over worktree, and captures its combined
// output and exit code. home pins the subprocess's HOME/USERPROFILE: the real
// machine user's global yml must never leak into the test. cmd.Dir is set to
// worktree (never to the real cwd of the vas.sentinel repo): if the fix under
// test regressed and the function kept going past the config error, any
// git/agent command it tried to launch must operate on the isolated tmpdir,
// not on this real repository.
func runAsSubprocess(t *testing.T, fn, worktree, home string) (out string, exitCode int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Dir = worktree
	env := []string{
		"VAS_SENTINEL_HELPER_PROCESS=1",
		"VAS_SENTINEL_HELPER_FN=" + fn,
		"VAS_SENTINEL_HELPER_WORKTREE=" + worktree,
		"HOME=" + home,
		"USERPROFILE=" + home,
	}
	for _, kv := range os.Environ() {
		key := strings.SplitN(kv, "=", 2)[0]
		if key == "HOME" || key == "USERPROFILE" || strings.HasPrefix(kv, "VAS_SENTINEL_HELPER_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = env

	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	if err == nil {
		return buf.String(), 0
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("could not run the %s subprocess: %v", fn, err)
	}
	return buf.String(), exitErr.ExitCode()
}

func worktreeWithCorruptHumanDisposition(t *testing.T) string {
	t.Helper()
	worktree := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = worktree
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, output)
		}
	}
	run("init", "-q")
	run("config", "user.email", "sentinel@example.test")
	run("config", "user.name", "Sentinel Test")
	if err := os.MkdirAll(filepath.Join(worktree, ".vas_sentinel"), 0755); err != nil {
		t.Fatal(err)
	}
	const configYAML = "validation:\n  capabilities:\n    format:\n      command: \"true\"\n  profiles:\n    standard: [format]\n"
	if err := os.WriteFile(filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml"), []byte(configYAML), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "a.go"), []byte("package fixture\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "fixture")
	commonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(commonDir, "vas-sentinel", "dispositions.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	const corrupt = `{"sha":"deadbeef","fingerprint":"fp","status":"unknown","actor":"human","source":"human"}` + "\n"
	if err := os.WriteFile(path, []byte(corrupt), 0600); err != nil {
		t.Fatal(err)
	}
	return worktree
}

// writeYmlWithUnknownKey writes a per-project vassentinel.yml with a key
// outside the schema, for the F1 error-propagation tests.
func writeYmlWithUnknownKey(t *testing.T, worktree string) {
	t.Helper()
	path := filepath.Join(worktree, ".vas_sentinel", "vassentinel.yml")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("could not create %s: %v", filepath.Dir(path), err)
	}
	content := "active_agent: \"claude\"\nnonexistent_key: true\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("could not write %s: %v", path, err)
	}
}

// TestRunLint_UnknownKeyInYml_Exit1WithLine covers Fix 1 (F1, orchestrator
// finding): before this fix, runLint used LoadLocalConfig (no
// error), so an unknown key in the yml was silently ignored and, with no
// lint_commands left configured by the broken yml, the command ended with
// "no lint commands" and exit 0 — exactly the opposite of what the record
// demands. With LoadStrictLocalConfig it must cut with exit 1 and
// the visible error.
func TestRunLint_UnknownKeyInYml_Exit1WithLine(t *testing.T) {
	home := t.TempDir()
	worktree := t.TempDir()
	writeYmlWithUnknownKey(t, worktree)

	out, exit := runAsSubprocess(t, "runLint", worktree, home)

	if exit != 1 {
		t.Errorf("expected exit 1, got %d (output: %q)", exit, out)
	}
	if !strings.Contains(out, "line") {
		t.Errorf("the output must include the yml error line, got: %q", out)
	}
}

func TestParseAuditFlagsDefault(t *testing.T) {
	// With no arguments, targets stays empty (B17): the default to "HEAD" no
	// longer lives here, because status (no concept of target) and review
	// (which applies it in resolveAuditSHAs) share this function.
	flags, err := parseAuditFlags(nil)
	if err != nil {
		t.Fatalf("parseAuditFlags(nil) returned error: %v", err)
	}
	if len(flags.targets) != 0 {
		t.Errorf("targets = %+v, expected empty", flags.targets)
	}
	if flags.all || flags.chain || flags.gate || flags.jsonOut || flags.prune {
		t.Error("boolean flags should be off by default")
	}
}

func TestParseAuditFlagsComplete(t *testing.T) {
	flags, err := parseAuditFlags([]string{
		"abc123", "--dims", "logic, security", "--profile", "deep",
		"--chain", "--answer", "does not apply here", "--gate",
	})
	if err != nil {
		t.Fatalf("parseAuditFlags returned error: %v", err)
	}
	if len(flags.targets) != 1 || flags.targets[0] != "abc123" {
		t.Errorf("targets = %+v, expected [abc123]", flags.targets)
	}
	if len(flags.dims) != 2 || flags.dims[0] != "logic" || flags.dims[1] != "security" {
		t.Errorf("dims = %+v, expected [logic security]", flags.dims)
	}
	if flags.profile != "deep" || flags.answer != "does not apply here" {
		t.Errorf("profile/answer = %q/%q", flags.profile, flags.answer)
	}
	if !flags.chain || !flags.gate {
		t.Error("chain/gate should be on")
	}
}

func TestParseAuditFlagsMultipleTargets(t *testing.T) {
	flags, err := parseAuditFlags([]string{"abc123", "def456", "HEAD~2"})
	if err != nil {
		t.Fatalf("parseAuditFlags returned error: %v", err)
	}
	if !reflect.DeepEqual(flags.targets, []string{"abc123", "def456", "HEAD~2"}) {
		t.Errorf("targets = %+v, expected [abc123 def456 HEAD~2]", flags.targets)
	}
}

func TestParseAuditFlagsErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"flag without value", []string{"--profile"}},
		{"unknown option", []string{"--nothing"}},
	}
	for _, c := range cases {
		if _, err := parseAuditFlags(c.args); err == nil {
			t.Errorf("%s: should return error", c.name)
		}
	}
}

func TestParseAuditFlagsExplicitHead(t *testing.T) {
	flags, err := parseAuditFlags([]string{"HEAD"})
	if err != nil {
		t.Fatalf("parseAuditFlags(HEAD) returned error: %v", err)
	}
	if !reflect.DeepEqual(flags.targets, []string{"HEAD"}) {
		t.Errorf("targets = %+v, expected [HEAD]", flags.targets)
	}
}

func TestStatusRejectsFlagsNotApplicable(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"--dims", []string{"--dims", "logic"}},
		{"--profile", []string{"--profile", "deep"}},
		{"--chain", []string{"--chain"}},
		{"--gate", []string{"--gate"}},
		{"--all", []string{"--all"}},
		{"target", []string{"abc123"}},
	}
	for _, c := range cases {
		flags, err := parseAuditFlags(c.args)
		if err != nil {
			t.Fatalf("%s: parseAuditFlags returned error: %v", c.name, err)
		}
		if !flagsNotApplicableToStatus(flags) {
			t.Errorf("%s: should be detected as not applicable to status", c.name)
		}
	}
}

// TestStatusAcceptsBareInvocation reproduces the real bug: "sentinel status"
// with no arguments was always rejected, because parseAuditFlags filled
// targets with ["HEAD"] unconditionally (so review/lint would not have to
// repeat that default), and flagsNotApplicableToStatus could not distinguish
// "the user passed nothing" from "the user passed a target". status has no
// target to accept, so the bare invocation must pass clean.
func TestStatusAcceptsBareInvocation(t *testing.T) {
	flags, err := parseAuditFlags(nil)
	if err != nil {
		t.Fatalf("parseAuditFlags(nil) returned error: %v", err)
	}
	if flagsNotApplicableToStatus(flags) {
		t.Error("sentinel status with no arguments should be accepted, not rejected")
	}
}

func TestParseAuditFlagsTimeout(t *testing.T) {
	flags, err := parseAuditFlags([]string{"--timeout", "900"})
	if err != nil {
		t.Fatalf("parseAuditFlags(--timeout 900) returned error: %v", err)
	}
	if flags.timeout != 900*time.Second {
		t.Errorf("timeout = %v, expected 900s", flags.timeout)
	}
}

func TestParseAuditFlagsTimeoutAbsentIsZero(t *testing.T) {
	flags, err := parseAuditFlags([]string{"HEAD"})
	if err != nil {
		t.Fatalf("parseAuditFlags returned error: %v", err)
	}
	if flags.timeout != 0 {
		t.Errorf("timeout = %v, expected 0 (no override)", flags.timeout)
	}
}

func TestParseAuditFlagsTimeoutInvalid(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing value", []string{"--timeout"}},
		{"not numeric", []string{"--timeout", "many"}},
		{"zero", []string{"--timeout", "0"}},
		{"negative", []string{"--timeout", "-30"}},
	}
	for _, c := range cases {
		if _, err := parseAuditFlags(c.args); err == nil {
			t.Errorf("--timeout %s: should return error", c.name)
		}
	}
}

func TestStatusRejectsTimeout(t *testing.T) {
	flags, err := parseAuditFlags([]string{"--timeout", "900"})
	if err != nil {
		t.Fatalf("parseAuditFlags returned error: %v", err)
	}
	if !flagsNotApplicableToStatus(flags) {
		t.Error("--timeout should be detected as not applicable to status")
	}
}

func TestApplyTimeoutFlag(t *testing.T) {
	base := config.Config{Review: config.ReviewConfig{Timeout: 600 * time.Second}}

	withoutFlag := applyTimeoutFlag(base, auditFlags{})
	if withoutFlag.Review.Timeout != 600*time.Second {
		t.Errorf("without --timeout: Timeout = %v, expected the config's (600s)", withoutFlag.Review.Timeout)
	}

	withFlag := applyTimeoutFlag(base, auditFlags{timeout: 900 * time.Second})
	if withFlag.Review.Timeout != 900*time.Second {
		t.Errorf("with --timeout: Timeout = %v, expected 900s", withFlag.Review.Timeout)
	}
	if base.Review.Timeout != 600*time.Second {
		t.Errorf("applyTimeoutFlag mutated the original config: %v", base.Review.Timeout)
	}
}

// TestPurgeOrphansReachesWorktreeLedgers is the second half of FU-12,
// and the reason it must land before T9.5 rather than after.
//
// PurgeOrphans is a deletion primitive, and T9.5 builds its retention
// cascade on it together with collectProvenanceReferences. That collector was
// already taught to enumerate every ledger in the repository; this one still
// purged the current checkout's ledger alone. A cascade assembled from the two
// would decide what to keep by consulting thirteen ledgers and then delete from
// one, which leaks every record a delegated writer produced and makes the
// cascade's own accounting wrong.
// repoWithLinkedWorktree builds a repository with one commit and one linked
// worktree named "linked", as a sibling of the main checkout, and returns the
// main checkout.
// uniqueRepoContent prevents two test repositories created in the same
// second, with the same tree, message and identity, from producing
// byte-identical commits and therefore the SAME SHA. Without it a test that
// says "this commit does not exist in the other repository" could be lying,
// and pass for that reason instead of the one it declares.
var uniqueRepoContent atomic.Int64

func repoWithLinkedWorktree(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	worktree := filepath.Join(base, "main")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + base,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	content := fmt.Sprintf("repo %d\n", uniqueRepoContent.Add(1))
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-qm", "first")
	run("worktree", "add", "-q", "--detach", filepath.Join(base, "linked"))
	return worktree
}

func TestPurgeOrphansReachesWorktreeLedgers(t *testing.T) {
	worktree := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + worktree,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-qm", "first")

	linked := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", "-q", "--detach", linked)

	mainGitDir, err := git.GetGitDirFrom(worktree)
	if err != nil {
		t.Fatal(err)
	}
	linkedGitDir, err := git.GetGitDirFrom(linked)
	if err != nil {
		t.Fatal(err)
	}

	// Two records in the linked worktree's ledger: one for a SHA the repository
	// does not contain, one for its real HEAD. Both are needed. Without the
	// live one the test would pass against a purge that simply deleted
	// everything it found, which is the opposite failure.
	orphan := "0123456789abcdef0123456789abcdef01234567"
	live := worktreeRevision(t, worktree, "HEAD")
	linkedLedger := review.NewLedger(linkedGitDir)
	for _, sha := range []string{orphan, live} {
		if err := linkedLedger.SaveRevision(sha, "fixture", "bucket", "model", review.Revision{
			At: time.Now(), Result: "ok",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// No working-directory change: orphanhood is now resolved against worktree
	// itself. That the live SHA survives while the test binary runs from an
	// unrelated repository is precisely the property under test.
	removed, err := purgeOrphans(worktree, mainGitDir)
	if err != nil {
		t.Fatalf("purgeOrphans() error = %v", err)
	}
	if !slices.Contains(removed, orphan) {
		t.Fatalf("purgeOrphans() = %v, missing the orphan %q held in the linked worktree ledger %q; T9.5 would decide from every ledger and delete from one",
			removed, orphan, linkedGitDir)
	}
	if record, err := linkedLedger.ReadRecord(orphan); err != nil || record != nil {
		t.Errorf("the orphan record survives in the linked worktree ledger (record=%v, err=%v)", record, err)
	}
	if slices.Contains(removed, live) {
		t.Errorf("purgeOrphans() deleted %q, whose commit exists; it is purging by reach and not by orphanhood", live)
	}
	if record, err := linkedLedger.ReadRecord(live); err != nil || record == nil {
		t.Errorf("the record of a live commit was deleted from the linked worktree ledger (record=%v, err=%v)", record, err)
	}
}

// worktreeRevision resolves a revision inside worktree without depending on
// the process working directory.
func worktreeRevision(t *testing.T, worktree, revision string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", worktree, "rev-parse", revision).Output()
	if err != nil {
		t.Fatalf("rev-parse %s: %v", revision, err)
	}
	return strings.TrimSpace(string(out))
}

// TestPurgeOrphansCleansEventsWhereTheirRecordsLived pins the pairing the
// review found broken. events.jsonl lives per gitDir exactly as the ledger
// does, so purging records across every checkout while cleaning events in one
// leaves entries pointing at deleted SHAs precisely where the command reports
// having cleaned them.
func TestPurgeOrphansCleansEventsWhereTheirRecordsLived(t *testing.T) {
	worktree := repoWithLinkedWorktree(t)
	mainGitDir, err := git.GetGitDirFrom(worktree)
	if err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(worktree, "..", "linked")
	linkedGitDir, err := git.GetGitDirFrom(linked)
	if err != nil {
		t.Fatal(err)
	}

	orphan := "0123456789abcdef0123456789abcdef01234567"
	live := worktreeRevision(t, worktree, "HEAD")
	if err := review.NewLedger(linkedGitDir).SaveRevision(orphan, "gone", "b", "m", review.Revision{At: time.Now(), Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	// One event per SHA in the linked worktree's own stream.
	for _, sha := range []string{orphan, live} {
		if err := ops.RecordEvent(linkedGitDir, "review", 0, []string{sha}, ops.EventDetail{}, ""); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := purgeOrphansWithEvents(worktree, mainGitDir); err != nil {
		t.Fatalf("purgeOrphansWithEvents() error = %v", err)
	}

	events, err := ops.RecentEvents(linkedGitDir, 100)
	if err != nil {
		t.Fatalf("UltimosEventos: %v", err)
	}
	var orphanRemains, liveRemains bool
	for _, event := range events {
		for _, sha := range event.Shas {
			if sha == orphan {
				orphanRemains = true
			}
			if sha == live {
				liveRemains = true
			}
		}
	}
	if orphanRemains {
		t.Errorf("the event of the purged record survives in the linked worktree stream; the command reports having cleaned it")
	}
	if !liveRemains {
		t.Errorf("the event of a live commit was deleted; the purge is removing by reach and not by orphanhood")
	}
}

// TestPurgeOrphansIgnoresGitDirFromEnvironment pins the CRITICAL that blocked the
// previous commit. GIT_DIR takes priority over "-C": with it set, a
// worktree-scoped query answers for the repository GIT_DIR names instead.
// Sentinel runs inside its own pre-commit hook, which is exactly a context
// where Git exports these variables, so a query that decides deletions cannot
// trust "-C" without clearing them. Redirected, every live commit of the target
// repository looks orphaned and the purge empties the ledgers.
func TestPurgeOrphansIgnoresGitDirFromEnvironment(t *testing.T) {
	worktree := repoWithLinkedWorktree(t)
	mainGitDir, err := git.GetGitDirFrom(worktree)
	if err != nil {
		t.Fatal(err)
	}
	live := worktreeRevision(t, worktree, "HEAD")
	if err := review.NewLedger(mainGitDir).SaveRevision(live, "live", "b", "m", review.Revision{At: time.Now(), Result: "ok"}); err != nil {
		t.Fatal(err)
	}

	// An unrelated repository, which knows nothing about the SHA above.
	unrelated := repoWithLinkedWorktree(t)
	unrelatedGitDir, err := git.GetGitDirFrom(unrelated)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_DIR", unrelatedGitDir)

	removed, err := purgeOrphans(worktree, mainGitDir)
	if err != nil {
		t.Fatalf("purgeOrphans() error = %v", err)
	}
	if slices.Contains(removed, live) {
		t.Errorf("purgeOrphans() deleted %q while GIT_DIR pointed at an unrelated repository; the containment query was redirected away from the worktree it was told to purge", live)
	}
	if record, err := review.NewLedger(mainGitDir).ReadRecord(live); err != nil || record == nil {
		t.Errorf("the record of a live commit was deleted (record=%v, err=%v)", record, err)
	}
}

// TestPurgeOrphansDoesNotMixRepositories pins the defect that sanitizing half
// the purge introduced. Containment was queried against the requested worktree
// while ledger discovery still inherited an ambient GIT_DIR, so with GIT_DIR
// naming repository B its ledgers were enumerated and then classified against
// repository A's refs. Every live record in B reads as orphaned there. Half a
// fix was worse than none, because inconsistency deletes across repositories
// while consistency merely looks at the wrong one.
func TestPurgeOrphansDoesNotMixRepositories(t *testing.T) {
	target := repoWithLinkedWorktree(t)
	targetGitDir, err := git.GetGitDirFrom(target)
	if err != nil {
		t.Fatal(err)
	}

	unrelated := repoWithLinkedWorktree(t)
	unrelatedGitDir, err := git.GetGitDirFrom(unrelated)
	if err != nil {
		t.Fatal(err)
	}
	// A live record in the OTHER repository, which the purge must never reach.
	liveElsewhere := worktreeRevision(t, unrelated, "HEAD")
	if err := review.NewLedger(unrelatedGitDir).SaveRevision(liveElsewhere, "live elsewhere", "b", "m", review.Revision{At: time.Now(), Result: "ok"}); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GIT_DIR", unrelatedGitDir)

	if _, err := purgeOrphans(target, targetGitDir); err != nil {
		t.Fatalf("purgeOrphans() error = %v", err)
	}

	if record, err := review.NewLedger(unrelatedGitDir).ReadRecord(liveElsewhere); err != nil || record == nil {
		t.Errorf("purging %q deleted a live record belonging to the unrelated repository %q (record=%v, err=%v); ledger discovery and containment were resolving different repositories",
			target, unrelated, record, err)
	}
}

// TestPurgeOrphansDoesNotDeleteWhenItCannotAsk is the property the previous
// four rounds kept missing one hole at a time. While any command failure meant
// "absent", every environment variable that could break the query became a
// deletion of live records, and closing them one by one only changed which
// failure reached the wrong rule.
//
// An unrelated object store is the case the review named: rev-parse and the
// common-dir lookup both still succeed, so the repository looks perfectly
// usable, and only the object read fails. The purge must refuse to decide
// rather than decide wrongly.
func TestPurgeOrphansDoesNotDeleteWhenItCannotAsk(t *testing.T) {
	worktree := repoWithLinkedWorktree(t)
	gitDir, err := git.GetGitDirFrom(worktree)
	if err != nil {
		t.Fatal(err)
	}
	live := worktreeRevision(t, worktree, "HEAD")
	if err := review.NewLedger(gitDir).SaveRevision(live, "live", "b", "m", review.Revision{At: time.Now(), Result: "ok"}); err != nil {
		t.Fatal(err)
	}

	// An empty object store: the repository resolves, its objects do not.
	empty := filepath.Join(t.TempDir(), "no-objects")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_OBJECT_DIRECTORY", empty)

	_, err = purgeOrphans(worktree, gitDir)
	if err == nil {
		t.Errorf("purgeOrphans() returned no error while it could not read the repository's objects; a question it cannot answer must never authorise a deletion")
	}
	if record, lerr := review.NewLedger(gitDir).ReadRecord(live); lerr != nil || record == nil {
		t.Errorf("the record of a live commit was deleted because the object store was unreadable (record=%v, err=%v)", record, lerr)
	}
}

// repoWithUnbreakableLedger builds the fixture both partial-result tests need: a
// repository with a linked worktree, one orphan record in the common directory
// that the purge can delete, and one in the worktree whose path is a non-empty
// directory, which is listed like any record and refuses to be removed.
//
// The failure is a file shape and not a permission bit, which is a no-op under
// root. It returns the worktree, the main gitDir and the SHA that must survive
// in the result.
func repoWithUnbreakableLedger(t *testing.T) (worktree, mainGitDir, deletable string) {
	t.Helper()
	worktree = t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + worktree,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-qm", "first")
	linked := filepath.Join(t.TempDir(), "linked")
	run("worktree", "add", "-q", "--detach", linked)

	var err error
	mainGitDir, err = git.GetGitDirFrom(worktree)
	if err != nil {
		t.Fatal(err)
	}
	linkedGitDir, err := git.GetGitDirFrom(linked)
	if err != nil {
		t.Fatal(err)
	}

	// The common directory is visited first, so this one is deleted before the
	// loop reaches the ledger it cannot purge.
	deletable = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := review.NewLedger(mainGitDir).SaveRevision(deletable, "fixture", "b", "m",
		review.Revision{At: time.Now(), Result: review.VerdictOK}); err != nil {
		t.Fatal(err)
	}
	path := review.NewLedger(linkedGitDir).RecordPath("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err := os.MkdirAll(filepath.Join(path, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	return worktree, mainGitDir, deletable
}

// TestPurgeOrphansPerLedgerReturnsAlreadyDeletedOnFailure pins the half of the
// partial-result contract the outer function could not fix on its own. The
// per-ledger loop deletes records directory by directory, so when one of them
// fails the earlier ones are already gone. Returning nil there dropped them:
// purgeOrphansWithEvents cleans events from that very list, so the events of
// the deleted records survived pointing at records the command had removed, and
// the operator was told only that the purge failed.
func TestPurgeOrphansPerLedgerReturnsAlreadyDeletedOnFailure(t *testing.T) {
	worktree, mainGitDir, deletable := repoWithUnbreakableLedger(t)

	byDirectory, err := purgeOrphansPerLedger(worktree, mainGitDir)
	if err == nil {
		t.Fatalf("purgeOrphansPerLedger() error = nil; want the ledger it could not purge reported")
	}
	if !slices.Contains(byDirectory[mainGitDir], deletable) {
		t.Errorf("purgeOrphansPerLedger() = %v, want %q under %q: it was deleted before the failure, and its events are cleaned from this very result",
			byDirectory, deletable, mainGitDir)
	}
	if record, lerr := review.NewLedger(mainGitDir).ReadRecord(deletable); lerr != nil || record != nil {
		t.Errorf("the record reported as deleted is still readable (record=%v, err=%v); the fixture no longer exercises the case", record, lerr)
	}
}

// TestPurgeOrphansReturnsAlreadyDeletedOnFailure holds the sibling facade to the
// same contract. Two facades over one primitive that disagree about what a
// failure returns are a trap: the safe one reads as proof that the other is
// safe too, and this one flattens the very map the other returns.
func TestPurgeOrphansReturnsAlreadyDeletedOnFailure(t *testing.T) {
	worktree, mainGitDir, deletable := repoWithUnbreakableLedger(t)

	removed, err := purgeOrphans(worktree, mainGitDir)
	if err == nil {
		t.Fatalf("purgeOrphans() error = nil; want the ledger it could not purge reported")
	}
	if !slices.Contains(removed, deletable) {
		t.Errorf("purgeOrphans() = %v, want it to contain %q, which it deleted before the failure", removed, deletable)
	}
}

// TestPurgeOrphansWithEventsReturnsAlreadyDeletedOnFailure closes the last
// facade over the partial-result contract. The two below it were pinned first,
// but this is the one both commands actually call, and it is where the deleted
// SHAs turn into the list whose events get cleaned.
func TestPurgeOrphansWithEventsReturnsAlreadyDeletedOnFailure(t *testing.T) {
	worktree, mainGitDir, deletable := repoWithUnbreakableLedger(t)

	removed, err := purgeOrphansWithEvents(worktree, mainGitDir)
	if err == nil {
		t.Fatalf("purgeOrphansWithEvents() error = nil; want the ledger it could not purge reported")
	}
	if !slices.Contains(removed, deletable) {
		t.Errorf("purgeOrphansWithEvents() = %v, want it to contain %q, whose events are cleaned from this very list", removed, deletable)
	}
}

// TestRunPruneAndReportDoesNotClaimWhatItDidNotVerify covers the policy both
// command facades share: after a failure the purge must not print a VERIFIED
// conclusion. reportPrune's empty case says "no orphans exist, every SHA is
// reachable", which a failed purge never established, and its JSON form says
// the same with an empty list.
//
// stdout is captured through a pipe because that claim is the whole point: an
// exit code alone would not distinguish a silent failure from one that printed
// a false all-clear.
func TestRunPruneAndReportDoesNotClaimWhatItDidNotVerify(t *testing.T) {
	worktree, mainGitDir, deletable := repoWithUnbreakableLedger(t)

	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := runPruneAndReport(worktree, mainGitDir, false)
	os.Stdout = original
	if cerr := writer.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	var out strings.Builder
	if _, cerr := io.Copy(&out, reader); cerr != nil {
		t.Fatal(cerr)
	}

	if code != 1 {
		t.Errorf("runPruneAndReport() = %d, want 1: the purge could not finish", code)
	}
	if strings.Contains(out.String(), "no orphan records") {
		t.Errorf("stdout claimed there are no orphans after a failed purge: %q", out.String())
	}
	// What it DID delete is still reported, because that list is the operator's
	// only record of what is already gone.
	if !strings.Contains(out.String(), deletable) {
		t.Errorf("stdout = %q, want it to name %q, which the purge deleted before failing", out.String(), deletable)
	}
}

// TestRunPruneAndReportStaysSilentWhenNothingDeleted is the other half, and the
// one that actually distinguishes the policy from "always report". When the
// purge fails before deleting anything, the result is empty, and reportPrune
// turns an empty result into the claim that no orphans exist. Printing that is
// the original defect: a conclusion asserted from a check that never ran.
func TestRunPruneAndReportStaysSilentWhenNothingDeleted(t *testing.T) {
	worktree, mainGitDir := repoWithFirstLedgerUnbreakable(t)

	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	code := runPruneAndReport(worktree, mainGitDir, false)
	os.Stdout = original
	if cerr := writer.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	var out strings.Builder
	if _, cerr := io.Copy(&out, reader); cerr != nil {
		t.Fatal(cerr)
	}

	if code != 1 {
		t.Errorf("runPruneAndReport() = %d, want 1: the purge could not finish", code)
	}
	if out.String() != "" {
		t.Errorf("stdout = %q, want nothing: the purge deleted no record and verified no SHA, so it has nothing to report",
			out.String())
	}
}

// repoWithFirstLedgerUnbreakable stages the failure in the FIRST ledger the
// purge visits, the common directory, so it aborts before deleting anything.
func repoWithFirstLedgerUnbreakable(t *testing.T) (worktree, mainGitDir string) {
	t.Helper()
	worktree = t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", worktree}, args...)...)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "HOME=" + worktree,
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-qm", "first")

	var err error
	mainGitDir, err = git.GetGitDirFrom(worktree)
	if err != nil {
		t.Fatal(err)
	}
	path := review.NewLedger(mainGitDir).RecordPath("cccccccccccccccccccccccccccccccccccccccc")
	if err := os.MkdirAll(filepath.Join(path, "occupied"), 0o755); err != nil {
		t.Fatal(err)
	}
	return worktree, mainGitDir
}
