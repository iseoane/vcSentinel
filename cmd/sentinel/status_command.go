package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// auditFlags holds the options common to review and status.
type auditFlags struct {
	targets []string // SHAs or expressions to audit (default [HEAD])
	dims    []string
	all     bool
	chain   bool
	gate    bool
	prune   bool // removes records of commits that no longer exist in the repo
	profile string
	answer  string
	jsonOut bool
	// timeout is the per-invocation override of the per-call agent limit
	// (review.timeout from the config). Zero means "no override".
	timeout time.Duration
}

// parseAuditFlags walks the subcommand's arguments and extracts the options
// with their values. Value flags consume the following argument.
// Every positional argument is a target: several are accepted so multiple
// commits can be audited in a single invocation (the i/N counter numbers them).
func parseAuditFlags(args []string) (auditFlags, error) {
	flags := auditFlags{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--all":
			flags.all = true
		case "--chain":
			flags.chain = true
		case "--gate":
			flags.gate = true
		case "--prune":
			flags.prune = true
		case "--json":
			flags.jsonOut = true
		case "--dims", "--profile", "--answer", "--timeout":
			if i+1 >= len(args) {
				return flags, fmt.Errorf("the %s flag needs a value", arg)
			}
			i++
			value := args[i]
			switch arg {
			case "--dims":
				for _, dim := range strings.Split(value, ",") {
					if d := strings.TrimSpace(dim); d != "" {
						flags.dims = append(flags.dims, d)
					}
				}
			case "--profile":
				flags.profile = value
			case "--answer":
				flags.answer = value
			case "--timeout":
				seconds, err := strconv.Atoi(strings.TrimSpace(value))
				if err != nil || seconds <= 0 {
					return flags, fmt.Errorf("--timeout needs a positive number of seconds, received %q", value)
				}
				flags.timeout = time.Duration(seconds) * time.Second
			}
		default:
			if strings.HasPrefix(arg, "-") {
				return flags, fmt.Errorf("unknown option: %s", arg)
			}
			flags.targets = append(flags.targets, arg)
		}
	}
	// No default to "HEAD" here (B17): status and review share this function,
	// and only review has a concept of "target" (defaulted in resolveAuditSHAs,
	// cmd/sentinel/comandos_review.go). Filling targets here unconditionally
	// made flagsNotApplicableToStatus unable to distinguish "the user passed
	// nothing" from "the user passed a target": sentinel status with no
	// arguments was ALWAYS rejected.
	return flags, nil
}

// runLint runs the lint_commands from the configuration.
// Each command runs in the system shell; if any fails, exit code 1.
func runLint(worktree string) {
	// STRICT config (orchestrator finding, outside the original text of the
	// record): an unknown key in the yml must cut here with an explicit error,
	// not silently continue with the default config.
	cfg, err := config.LoadStrictLocalConfig(worktree)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	if len(cfg.LintCommands) == 0 {
		fmt.Println("✅ No lint commands configured (lint_commands in vassentinel.yml).")
		return
	}

	failed := false
	for _, command := range cfg.LintCommands {
		fmt.Printf("🔧 %s\n", command)
		out, err := runInShell(command)
		if out != "" {
			fmt.Print(out)
			if !strings.HasSuffix(out, "\n") {
				fmt.Println()
			}
		}
		if err != nil {
			fmt.Printf("❌ Failed with exit code %d.\n", exitCodeFromError(err))
			failed = true
		} else {
			fmt.Println("✅ OK")
		}
	}
	if failed {
		os.Exit(1)
	}
}

// runInShell runs a command through the system shell.
func runInShell(command string) (string, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func exitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// runRebase updates the branch with fetch + rebase against its upstream.
// It never acts silently: if the worktree is dirty or there is no upstream,
// it aborts.
func runRebase() {
	clean, err := git.WorktreeClean()
	if err != nil {
		fmt.Printf("❌ Could not check the worktree state: %v\n", err)
		os.Exit(1)
	}
	if !clean {
		fmt.Println("❌ The worktree has pending changes. Commit or slice before rebasing.")
		os.Exit(1)
	}

	upstream, err := git.UpstreamOrMain()
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	branch, err := git.CurrentBranch()
	if err != nil {
		fmt.Printf("❌ Could not read the current branch: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("🔎 Rebase of %s onto %s (fetch + rebase).\n", branch, upstream)
	fmt.Print("Proceed? (y/N): ")
	line, err := readLine()
	if err != nil {
		return
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer != "s" && answer != "si" && answer != "y" && answer != "yes" {
		fmt.Println("🚫 Canceled, no changes.")
		return
	}

	remote := ""
	if upstream == "@{u}" {
		// The real remote comes from the branch config, not from the symbolic ref.
		remote = git.BranchRemote(branch)
	} else if parts := strings.Split(upstream, "/"); len(parts) > 1 {
		remote = parts[0]
	}
	if remote == "" {
		fmt.Println("❌ Could not determine the remote to fetch from. Check branch." + branch + ".remote.")
		os.Exit(1)
	}
	if out, err := runInShell("git fetch " + remote); err != nil {
		fmt.Printf("❌ Fetch failed (%s):\n%s", remote, out)
		os.Exit(1)
	}

	out, err := runInShell("git rebase " + upstream)
	if err != nil {
		fmt.Printf("❌ Rebase failed:\n%s", out)
		fmt.Println("Resolve the conflicts or run 'git rebase --abort'.")
		os.Exit(1)
	}
	fmt.Println(out)
	fmt.Printf("✅ %s rebased onto %s.\n", branch, upstream)
	// T9.5 event-driven retention: the fetch + rebase just moved the remote
	// boundary, so published execution detail is collectible now.
	// Best-effort by contract: it never fails the rebase that succeeded.
	// "." is the same repository every git call in this function already
	// queries: main dispatches all commands (including gate's worktree
	// parameter) from the process working directory.
	tryRetentionAfterPublish(os.Stdout, ".")
}

// flagsNotApplicableToStatus reports whether the parsed flags do not apply to
// status (which only understands --json and --prune). They are rejected
// instead of silently accepted.
func flagsNotApplicableToStatus(flags auditFlags) bool {
	return len(flags.targets) > 0 || len(flags.dims) > 0 || flags.all || flags.chain || flags.gate ||
		flags.profile != "" || flags.answer != "" || flags.timeout > 0
}

// applyTimeoutFlag returns the configuration with the audit timeout
// overridden by --timeout when it was passed. Without the flag the config
// wins: the flag is a per-invocation override, not a persistent change. It
// returns a copy; it never mutates the received config.
func applyTimeoutFlag(cfg config.Config, flags auditFlags) config.Config {
	if flags.timeout > 0 {
		cfg.Review.Timeout = flags.timeout
	}
	return cfg
}

// applyTimeoutSeconds is the same override expressed in seconds, which is
// how the gate receives it. It lives next to applyTimeoutFlag on purpose: a
// single site decides which field is replaced, so review and gate cannot
// diverge in unit or in destination.
func applyTimeoutSeconds(cfg config.Config, seconds int) config.Config {
	if seconds <= 0 {
		return cfg
	}
	cfg.Review.Timeout = time.Duration(seconds) * time.Second
	return cfg
}

// purgeOrphans deletes the records of commits that no longer exist in the
// repo and returns the removed SHAs. Useful after rebase/amend/squash.
//
// It purges ALL the ledgers of the repository, not just the current
// checkout's. The v1 ledger is anchored on the gitDir, so a review made from
// a linked worktree writes to <gitCommonDir>/worktrees/<name>/vas-sentinel,
// and delegating to a writer with a dedicated worktree is the usual flow
// here. Purging only our own left all those records orphaned.
//
// This matters beyond hygiene: T9.5 builds its retention cascade on this
// primitive and on collectProvenanceReferences, which already enumerates
// every ledger. Deciding what to keep by looking at one and deleting in one
// leaves the cascade accounting wrong (FU-12).
func purgeOrphans(worktree, gitDir string) ([]string, error) {
	byDirectory, enumerationErr := purgeOrphansPerLedger(worktree, gitDir)
	// Same partial-result contract as purgeOrphansWithEvents, and stated
	// here rather than left implicit: two facades over one primitive that
	// disagree about what a failure returns are a trap, because the safe one
	// reads as proof that the other is safe too.
	removed := []string{}
	for _, dir := range slices.Sorted(maps.Keys(byDirectory)) {
		removed = append(removed, byDirectory[dir]...)
	}
	return removed, enumerationErr
}

// purgeOrphansPerLedger purges every ledger of the repository and returns the
// removed SHAs GROUPED BY DIRECTORY. The grouping is not a detail: the events
// also live per gitDir, so deleting a checkout's record and its events from
// another leaves orphaned records exactly where the command claims to have
// cleaned them.
//
// Existence is resolved against worktree, not against the process working
// directory. With a single ledger the difference was invisible because both
// coincided; when walking every ledger of the repository, classifying with
// the CWD would delete live records as soon as the process ran from elsewhere.
func purgeOrphansPerLedger(worktree, gitDir string) (map[string][]string, error) {
	// The repository is validated ONCE, before classifying anything. From
	// then on a per-SHA failure can only mean an unknown object, which is
	// the orphan case; without this check, a broken invocation would read as
	// "none of these commits exist" and empty the ledgers.
	if err := git.RequireUsableRepository(worktree); err != nil {
		return nil, err
	}
	exists := func(sha string) (bool, error) { return git.ContentInSomeRefFrom(worktree, sha) }

	gitCommonDir, err := git.GetGitCommonDir(worktree)
	if err != nil {
		purged, perr := review.NewLedger(gitDir).PurgeOrphans(exists)
		if perr != nil {
			return nil, perr
		}
		return map[string][]string{gitDir: purged}, fmt.Errorf("linked worktree ledgers were not purged: %w", err)
	}
	directories, err := ledgerV1Directories(gitCommonDir)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(directories, gitDir) {
		directories = append(directories, gitDir)
	}

	byDirectory := map[string][]string{}
	for _, dir := range directories {
		purged, err := review.NewLedger(dir).PurgeOrphans(exists)
		// Recorded BEFORE the error is examined, and the accumulated map is
		// returned WITH it. PurgarHuerfanas hands back what it had already
		// deleted alongside its failure, and by the time one ledger fails the
		// earlier ones are already purged. Discarding that left those records
		// gone with their events intact, because events are cleaned from this
		// very result.
		byDirectory[dir] = purged
		if err != nil {
			return byDirectory, fmt.Errorf("purging the ledger at %s: %w", dir, err)
		}
	}
	return byDirectory, nil
}

// purgeOrphansWithEvents purges the orphan records and, for each removed
// SHA, also deletes its lines from events.jsonl: the events of commits that
// are still alive are always preserved. An event-cleanup failure returns an
// error but the already-purged records are not restored.
func purgeOrphansWithEvents(worktree, gitDir string) ([]string, error) {
	byDirectory, enumerationErr := purgeOrphansPerLedger(worktree, gitDir)
	// A partial result travels WITH its error. The common-directory fallback
	// deletes this checkout's records and only then reports that the linked
	// ledgers could not be enumerated, so returning nil here dropped the SHAs
	// it had already removed: their events survived pointing at records the
	// command had silently deleted, and the operator was told only "it failed".
	removed := []string{}
	// The events are cleaned in the SAME directory the record lived in.
	// events.jsonl lives per gitDir just like the ledger, so deleting the
	// records of every checkout and the events of only one would leave
	// events pointing at removed SHAs exactly where the command says it
	// cleaned them.
	for _, dir := range slices.Sorted(maps.Keys(byDirectory)) {
		purged := byDirectory[dir]
		removed = append(removed, purged...)
		if len(purged) == 0 {
			continue
		}
		if err := cleanEvents(dir, purged); err != nil {
			// Joined, not replaced. enumerationErr says some ledgers were never
			// enumerated at all; overwriting it with the event failure left the
			// operator believing the purge had reached every ledger and only
			// stumbled on cleanup.
			return removed, errors.Join(enumerationErr, err)
		}
	}
	return removed, enumerationErr
}

// cleanEvents deletes from a gitDir's events.jsonl the lines of the purged
// SHAs. Extracted so the failure point has a name in the combined error
// returned by purgeOrphansWithEvents.
func cleanEvents(dir string, purged []string) error {
	if _, err := ops.PurgeEventsOf(dir, purged); err != nil {
		return fmt.Errorf("records purged but cleaning their events failed at %s: %w", dir, err)
	}
	return nil
}

// runPruneAndReport runs the --prune purge and presents its result. The two
// handlers that offer it, status and review, share this function instead of
// repeating the policy: the distinction between an empty and a partial
// result decides whether a verified conclusion is printed, and two copies of
// that rule desynchronize as soon as one is touched.
//
// It returns the exit code instead of calling os.Exit so the decision stays
// with the caller.
func runPruneAndReport(worktree, gitDir string, jsonOut bool) int {
	removed, err := purgeOrphansWithEvents(worktree, gitDir)
	if err != nil {
		// Only what the purge actually deleted is reported, and only if it
		// deleted anything. reportPrune's empty case prints a VERIFIED
		// conclusion — "no orphans exist, every SHA is reachable" — and a purge
		// that failed never established that. Its JSON form says the same with
		// an empty list. The failure goes to stderr so --json still emits at
		// most one parseable object on stdout.
		if len(removed) > 0 {
			reportPrune(gitDir, removed, jsonOut, worktree)
		}
		fmt.Fprintf(os.Stderr, "? Could not purge every orphan record: %v\n", err)
		return 1
	}
	reportPrune(gitDir, removed, jsonOut, worktree)
	return 0
}

// reportPrune shows the result of purgeOrphansWithEvents as text or JSON
// depending on jsonOut. It shares the presentation between status, review
// and pr so the format is not duplicated.
func reportPrune(gitDir string, removed []string, jsonOut bool, worktree string) {
	if jsonOut {
		out := map[string]any{
			"worktree": worktree,
			"purged":   removed,
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Printf("? Could not serialize the state: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(data))
		return
	}
	if len(removed) == 0 {
		fmt.Println("? --prune: no orphan records (every SHA exists).")
		return
	}
	fmt.Printf("? --prune: removed %d records of commits that no longer exist (and their events).\n", len(removed))
	for _, sha := range removed {
		fmt.Printf("  - %s\n", sha)
	}
}

// statusVerdictLabel returns a record's current verdict label for `status`:
// the Result of its last AUTHORITATIVE revision (RULE 1 in coverage.go), or
// "not reviewed" when there is none. A supplementary (operator-narrowed) run
// can never set, clear or downgrade the verdict, and a supplementary-only
// record was never enough to review the commit.
func statusVerdictLabel(record *review.Record) string {
	if record == nil {
		return "not reviewed"
	}
	current, _, ok := review.LastAuthoritativeRevision(*record)
	if !ok {
		return "not reviewed"
	}
	return current.Result
}

// runStatus summarizes the guardian's state: pending volume, audit records
// and latest events. With --json it emits the same information as JSON.
func runStatus(worktree string, args []string) {
	flags, err := parseAuditFlags(args)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}
	// status only understands --json and --prune; the rest of the audit flags
	// do not apply here and are rejected instead of silently accepted.
	if flagsNotApplicableToStatus(flags) {
		fmt.Println("? status only accepts the --json and --prune flags.")
		os.Exit(1)
	}

	gitDir, err := git.GetGitDirFrom(worktree)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}

	lines, state, err := git.CheckDiffLimits()
	if err != nil {
		fmt.Printf("❌ Could not compute the volume: %v\n", err)
		os.Exit(1)
	}

	// --prune runs BEFORE the shared ledger is built, and that order is load
	// bearing. purgeOrphansWithEvents enumerates every per-checkout ledger
	// and keeps a documented fallback for when the common directory cannot be
	// resolved; building the shared ledger first turned that same resolution
	// failure into an exit, so the fallback became unreachable.
	//
	// No test pins this order, and that gap is recorded rather than glossed:
	// reaching it needs GetGitCommonDir to fail while the checkout's own
	// gitDir still resolves, and neither this command nor purgeOrphansPerLedger
	// takes that resolver as a seam. Adding one is the fix; until then, moving
	// the shared ledger above this block silently restores the regression.
	if flags.prune {
		os.Exit(runPruneAndReport(worktree, gitDir, flags.jsonOut))
	}

	// Anchored on the common directory, not on gitDir: see sharedReviewLedger.
	// gitDir stays for the purge above and the event log, which enumerate every
	// per-checkout ledger on purpose.
	ledger, err := sharedReviewLedger(worktree)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		os.Exit(1)
	}

	shas, err := ledger.ListRecords()
	if err != nil {
		fmt.Printf("⚠️ Could not list the records: %v\n", err)
	}

	records := []map[string]any{}
	orphanCount := 0
	for _, sha := range shas {
		fixedIn := ""
		record, err := ledger.ReadRecord(sha)
		if err == nil && record != nil {
			fixedIn = record.FixedIn
		}
		isOrphan := !git.ContentInSomeRef(sha)
		if isOrphan {
			orphanCount++
		}
		records = append(records, map[string]any{
			"sha":     sha,
			"verdict": statusVerdictLabel(record),
			"fixedIn": fixedIn,
			"orphan":  isOrphan,
		})
	}

	events, err := ops.RecentEvents(gitDir, 5)
	if err != nil {
		fmt.Printf("⚠️ Could not read the events: %v\n", err)
	}

	if flags.jsonOut {
		out := map[string]any{
			"worktree":     worktree,
			"lines":        lines,
			"state":        state,
			"records":      records,
			"orphans":      orphanCount,
			"latestEvents": events,
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Printf("❌ Could not serialize the state: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(data))
		return
	}

	fmt.Printf("📊 Modified lines in this Worktree: %d [%s]\n", lines, state)
	fmt.Printf("🔎 Audit records: %d (orphans: %d)\n", len(records), orphanCount)
	for _, record := range records {
		fixedNote := ""
		if record["fixedIn"] != "" {
			fixedNote = fmt.Sprintf("  🔧 fixed in %s", record["fixedIn"])
		}
		fmt.Printf("  %s  %s%s\n", record["sha"], record["verdict"], fixedNote)
	}
	if len(events) > 0 {
		fmt.Printf("🕒 Latest events:\n")
		for _, event := range events {
			fmt.Printf("  %s %s (%d)\n", event.At.Format("15:04:05"), event.Cmd, event.Exit)
		}
	}
}
