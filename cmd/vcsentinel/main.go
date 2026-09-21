package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vcSentinel/internal/git"
	"github.com/ISeoane-Quental/vcSentinel/internal/registry"
	"github.com/ISeoane-Quental/vcSentinel/internal/setup"
)

var version = "dev"

var resolveRepositoryRegistryPath = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".vas_sentinel", "repositories.json"), nil
}

func updateRepositoryRegistry(repositoryPath string, update func(*registry.Registry, string) (bool, error)) {
	path, err := resolveRepositoryRegistryPath()
	if err == nil {
		stored, openErr := registry.Open(path)
		if openErr != nil {
			err = openErr
		} else {
			_, err = update(stored, repositoryPath)
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "vas-sentinel: repository registry update failed: %v\n", err)
	}
}

// markerBegin and markerEnd delimit the volume rules block in a way that is
// stable across versions: init/uninit detect and remove it by these markers,
// not by the inner text, so a future version can reword the text while
// staying idempotent and leaving no orphans behind.
const markerBegin = "<!-- vas-sentinel:begin -->"
const markerEnd = "<!-- vas-sentinel:end -->"

// volumeRulesBody is the visible text of the rule, free to change its wording
// between versions: detection does not depend on it.
const volumeRulesBody = "## CRITICAL VOLUME RULE (THE GUARDIAN)\n- Before making changes or proposing a plan, run `vcsentinel check`. It measures the whole worktree and is advisory, including when the state is `CRITICAL`.\n- The repository's `pre-commit` hook runs `vcsentinel check --staged`. This is the enforcement boundary: it rejects staged authored code over the 400-line review budget.\n- When the worktree check is `CRITICAL`, run `vcsentinel slice plan --json` to produce reviewable selections without committing.\n- After the user answers every pending decision, apply the approved selections with `vcsentinel slice apply --plan plan.json --answers answers.json`. Never answer those decisions on the user's behalf.\n"

// volumeRules is the block that 'init' injects today into AGENTS.md,
// CLAUDE.md and .claudecode.md, wrapped in markerBegin/markerEnd.
const volumeRules = "\n" + markerBegin + "\n" + volumeRulesBody + markerEnd + "\n"

// legacyVolumeRules is the EXACT block (without markers) that every version
// before this one injected. It is frozen as-is forever: it is never written
// again in this format, only recognized and removed, so files that already
// carry it from previous versions (including this repo's own) can be cleaned
// up and migrated. The Spanish text is intentional legacy payload used for
// byte-exact legacy detection; never reword it.
const legacyVolumeRules = "\n## REGLA CRÍTICA DE VOLUMEN (EL GUARDIÁN)\n- Antes de realizar cualquier cambio o proponer un plan, DEBES ejecutar: \"sentinel check\".\n- Si el estado es \"CRÍTICO\" (>400 líneas), tienes ESTRICTAMENTE PROHIBIDO seguir escribiendo código.\n- Debes detenerte de inmediato e invocar: \"sentinel slice\" para fragmentar el código acumulado antes de continuar.\n"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	subcommand := os.Args[1]

	switch subcommand {
	case "--version", "-v", "version":
		if containsHelpFlag(os.Args[2:]) {
			writeCommandHelp(os.Stdout, "version")
			return
		}
		fmt.Printf("📦 vcSentinel version: %s\n", version)
		return
	case "--help", "-h":
		printHelp()
		return
	case "help":
		handleHelpCommand(os.Args[2:])
		return
	}

	// Central -h/--help interception (ticket 15): before any validation,
	// initialization, or side effect, every command and subcommand answers
	// with its dedicated help on stdout and exit 0. This also removes the
	// 'vcsentinel pr --help' bug, which fell through to the gh passthrough and
	// ran the record cleanup as a side effect.
	if handleHelp(os.Stdout, os.Stderr, subcommand, os.Args[2:]) {
		return
	}

	currentWorktree, err := os.Getwd()
	if err != nil {
		fmt.Printf("❌ Error identifying the current directory: %v\n", err)
		os.Exit(1)
	}

	// An argument the subcommand does not accept cuts before executing
	// anything: a clear error beats an operation that seems to have obeyed a
	// flag it actually ignored (H1/B6).
	if message := validateArguments(subcommand, os.Args[2:]); message != "" {
		fmt.Println(message)
		os.Exit(1)
	}

	switch subcommand {
	case "init":
		runInit(currentWorktree)
	case "uninit":
		runUninit(currentWorktree)
	case "check":
		requireInitialized(currentWorktree)
		os.Exit(runCheck(currentWorktree, os.Args[2:]))
	case "slice":
		requireInitialized(currentWorktree)
		if len(os.Args) > 2 {
			switch os.Args[2] {
			case "plan":
				os.Exit(runSlicePlan(os.Stdout, os.Args[3:]))
			case "apply":
				os.Exit(runSliceApply(os.Stdout, os.Args[3:]))
			}
		}
		runSlice(currentWorktree)
	case "review":
		requireInitialized(currentWorktree)
		runReview(currentWorktree, os.Args[2:])
	case "refute":
		requireInitialized(currentWorktree)
		os.Exit(runRefute(os.Stdout, currentWorktree, os.Args[2:]))
	case "accept":
		requireInitialized(currentWorktree)
		os.Exit(runAccept(os.Stdout, currentWorktree, os.Args[2:]))
	case "reopen":
		requireInitialized(currentWorktree)
		os.Exit(runReopen(os.Stdout, currentWorktree, os.Args[2:]))
	case "gate":
		requireInitialized(currentWorktree)
		os.Exit(runGate(os.Stdout, currentWorktree, os.Args[2:]))
	case "lint":
		requireInitialized(currentWorktree)
		runLint(currentWorktree)
	case "rebase":
		requireInitialized(currentWorktree)
		runRebase()
	case "status":
		requireInitialized(currentWorktree)
		runStatus(currentWorktree, os.Args[2:])
	case "metrics":
		requireInitialized(currentWorktree)
		os.Exit(executeMetrics(os.Stdout, currentWorktree, os.Args[2:]))
	case "doctor":
		requireInitialized(currentWorktree)
		os.Exit(runDoctor(os.Stdout, currentWorktree, version, os.Args[2:]))
	case "consent-diff":
		requireInitialized(currentWorktree)
		os.Exit(runConsentDiff(os.Stdout, currentWorktree, os.Args[2:]))
	case "explain":
		requireInitialized(currentWorktree)
		if err := runExplain(os.Stdout, os.Args[2:]); err != nil {
			fmt.Printf("❌ %v\n", err)
			os.Exit(1)
		}
	case "pr":
		requireInitialized(currentWorktree)
		runPr(currentWorktree, os.Args[2:])
	case "runs":
		requireInitialized(currentWorktree)
		os.Exit(executeRuns(os.Stdout, currentWorktree, os.Args[2:]))
	case "tui":
		requireInitialized(currentWorktree)
		os.Exit(executeTui(os.Stdout, currentWorktree))
	case "install":
		if err := setup.RunFullInstall(); err != nil {
			fmt.Printf("❌ Install failed: %v\n", err)
			os.Exit(1)
		}
	case "upgrade":
		if err := setup.RunUpgradeFromGitHub(); err != nil {
			fmt.Printf("❌ Upgrade failed: %v\n", err)
			os.Exit(1)
		}
	case "uninstall":
		if err := setup.RunFullUninstall(); err != nil {
			fmt.Printf("❌ Uninstall failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("❌ Unknown subcommand: '%s'. Use 'version', 'help', 'init', 'uninit', 'check', 'slice', 'review', 'refute', 'accept', 'reopen', 'gate', 'lint', 'rebase', 'status', 'metrics', 'doctor', 'explain', 'pr', 'runs', 'tui', 'consent-diff', 'install', 'upgrade' or 'uninstall'.\n", subcommand)
		os.Exit(1)
	}
}

// requireInitialized demands that the worktree has the per-project
// configuration (vcsentinel init) before running any subcommand that depends on
// it; without it, it cuts with exit 1 instead of failing later with a less
// clear error.
func requireInitialized(currentWorktree string) {
	if !setup.IsInitialized(currentWorktree) {
		fmt.Println("❌ This repository has not been initialized with vcSentinel.")
		fmt.Println("Run 'vcsentinel init' to configure the guardian in this project.")
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("🤖 vcSentinel: Local Code Guardian")
	fmt.Println("Usage: vcsentinel [version | help | init | uninit | check | slice | review | refute | accept | reopen | gate | lint | rebase | status | metrics | explain | pr | runs | tui | consent-diff | install | upgrade | uninstall]")
}

// printHelp prints the subcommand help built by buildHelp.
func printHelp() {
	fmt.Print(buildHelp())
}

// buildHelp returns the help text. Subcommand descriptions are wrapped at a
// fixed width with continuations aligned to the description column (14
// spaces), so nothing invades the items' argument zone.
func buildHelp() string {
	var b strings.Builder
	b.WriteString("🤖 vcSentinel: Local Code Guardian\n")
	b.WriteString("Usage: vcsentinel [version | help | init | uninit | check | slice | review |\n")
	b.WriteString("             refute | accept | reopen | gate | lint | rebase | status |\n")
	b.WriteString("             metrics | explain | pr | runs | tui | consent-diff |\n")
	b.WriteString("             install | upgrade | uninstall]\n\n")
	b.WriteString("Subcommands:\n")
	printHelpItem(&b, "version", "Shows the installed version.")
	printHelpItem(&b, "help", "Shows this help.")
	printHelpItem(&b, "init", "Injects the volume rules into your agents, creates the per-project config, and installs the repository pre-commit hook. Always runs at the repository root (redirects automatically from a subdirectory).")
	printHelpItem(&b, "uninit", "Reverts 'init' in this repository: removes the volume rules, deletes the per-project config, and removes the pre-commit hook (only if it is still the one vcSentinel installed).")
	printHelpItem(&b, "check", "Audits the active worktree volume. Use --staged for the pending commit candidate; --json emits a machine-readable report.")
	printHelpItem(&b, "slice", "Splits the pending modifications into commits of at most 400 lines.")
	printHelpItem(&b, "", "slice plan [--json] proposes without committing (exit 3 when decisions are pending).")
	printHelpItem(&b, "", "slice apply --plan X --answers Y executes an already approved plan.")
	printHelpItem(&b, "review", "Audits a commit (default HEAD) against its dimension set and stores the record.")
	printHelpItem(&b, "", "Flags: <sha|HEAD~n> --dims a,b --all --chain --gate --profile X --answer \"...\" --timeout N.")
	printHelpItem(&b, "refute", "Record an evidence-bound human refutation of one reviewed finding (clears only its block).")
	printHelpItem(&b, "", "Usage: refute --sha SHA --fingerprint FP --reason TEXT --line-start N --line-end M.")
	printHelpItem(&b, "accept", "Record a human acceptance of one reviewed finding (documents judgement, never clears the block).")
	printHelpItem(&b, "", "Usage: accept --sha SHA --fingerprint FP --reason TEXT.")
	printHelpItem(&b, "reopen", "Record an evidence-bound human reopen of one cleared finding (blocks again).")
	printHelpItem(&b, "", "Usage: reopen --sha SHA --fingerprint FP --reason TEXT --line-start N --line-end M.")
	printHelpItem(&b, "lint", "Runs the lint_commands from the configuration.")
	printHelpItem(&b, "rebase", "Updates the branch with fetch + rebase against its upstream (asks for confirmation).")
	printHelpItem(&b, "status", "Guardian summary: volume, review records, and recent events.")
	printHelpItem(&b, "", "With --json it emits JSON; with --prune it deletes orphan records.")
	printHelpItem(&b, "metrics", "Print deterministic local aggregates from the durable store; --json emits machine-readable output with null for unknown measurements.")
	printHelpItem(&b, "doctor", "Preflight the review environment: agents, search binary, codegraph gates, hook. Advisory, exits 0. Flag: --check-updates.")
	printHelpItem(&b, "explain", "Explains the profile, detectors, risk, and cohesion of a range. Usage: explain [base..head] [--json].")
	printHelpItem(&b, "pr", "Pull-request operations: pr review authors and persists a local branch judgement; pr create publishes through gh. The legacy passthrough was removed.")
	printHelpItem(&b, "", "pr review analyzes the branch, then authors and saves its local judgement and evidence without publishing.")
	printHelpItem(&b, "", "pr review flags: --base X --parent X --overview --json.")
	printHelpItem(&b, "", "        create --base X --parent X --chain-pr --audit-pending --force --reason \"...\".")
	printHelpItem(&b, "runs", "Operator commands over durable runs: start, status, logs, respond, abort, retry, recover, verify. See docs/design/runs-cli.md for flags, JSON shapes, and exit codes.")
	printHelpItem(&b, "tui", "Open the full-screen control center (starts/stops this repository's daemon for the session).")
	printHelpItem(&b, "consent-diff", "Manages the local per-user and per-repository grant: grant, revoke, or status.")
	printHelpItem(&b, "install", "Downloads and installs the latest published release from GitHub.")
	printHelpItem(&b, "upgrade", "Replaces the current binary with the latest published release.")
	printHelpItem(&b, "uninstall", "Removes the installed binary and the global configuration.")
	b.WriteString("\nFlags:\n")
	b.WriteString("  --version, -v   Shows the installed version (equivalent to 'version').\n")
	b.WriteString("  --help, -h      Shows this help (equivalent to 'help').\n")
	return b.String()
}

// printHelpItem adds a subcommand entry: if name is non-empty, it goes in the
// command column (2 spaces + up to 12 of name); the following lines align to
// the description column. The description wraps at a matching width so the
// maximum line width is not exceeded.
func printHelpItem(b *strings.Builder, name, description string) {
	const descriptionWidth = 96
	if name == "" {
		for _, line := range wrap(description, descriptionWidth) {
			b.WriteString("              " + line + "\n")
		}
		return
	}
	for i, line := range wrap(description, descriptionWidth) {
		if i == 0 {
			separator := ""
			if len(name) >= 12 {
				separator = " "
			}
			fmt.Fprintf(b, "  %-12s%s%s\n", name, separator, line)
		} else {
			b.WriteString("              " + line + "\n")
		}
	}
}

// wrap splits text into lines of at most width characters, cutting at spaces
// and never cutting words. It returns at least one line (empty if the text
// is).
func wrap(text string, width int) []string {
	var lines []string
	current := ""
	for _, word := range strings.Fields(text) {
		if current == "" {
			current = word
			continue
		}
		if len(current)+1+len(word) <= width {
			current += " " + word
			continue
		}
		lines = append(lines, current)
		current = word
	}
	if current != "" || len(lines) == 0 {
		lines = append(lines, current)
	}
	return lines
}

func runInit(path string) {
	root, err := git.GetWorktreeRoot()
	if err != nil {
		fmt.Println("❌ init must run inside a Git repository (worktree root not found).")
		os.Exit(1)
	}
	if !git.IsSamePath(path, root) {
		fmt.Printf("📂 Repository root detected: %s\n", root)
		fmt.Println("⚙️ Redirecting init to the repository root...")
		path = root
	}

	fmt.Println("⚙️ Initializing vcSentinel in this environment...")

	targetFiles := []string{"AGENTS.md", "CLAUDE.md", ".claudecode.md"}
	rulesSucceeded := true

	for _, name := range targetFiles {
		written, err := injectRulesIntoFile(filepath.Join(path, name))
		switch {
		case err != nil:
			rulesSucceeded = false
			fmt.Printf("⚠️ Could not inject into %s: %v\n", name, err)
		case written:
			fmt.Printf("📝 Volume rules injected into: %s\n", name)
		}
	}

	if _, err := exec.LookPath("git"); err != nil {
		fmt.Println("❌ git is not on the PATH. Install it before running 'vcsentinel init'.")
		os.Exit(1)
	}

	// The hook is written directly into the repository's common dir (the same
	// one for all its linked worktrees): no global folder and no
	// core.hooksPath, Git already detects it there by default and no other
	// repository of the user is affected.
	commonDir, err := git.GetGitCommonDir(path)
	if err != nil {
		fmt.Printf("❌ Could not determine the repository's Git directory: %v\n", err)
		os.Exit(1)
	}
	hooksDir := filepath.Join(commonDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		fmt.Printf("❌ Could not create %s: %v\n", hooksDir, err)
		os.Exit(1)
	}

	scriptContent := generateHookScript()
	hookPath := filepath.Join(hooksDir, "pre-commit")
	if err := os.WriteFile(hookPath, []byte(scriptContent), 0755); err != nil {
		fmt.Printf("❌ Could not write the hook to %s: %v\n", hookPath, err)
		os.Exit(1)
	}

	configurationSucceeded := true
	if err := setup.CreatePerProjectConfig(path); err != nil {
		configurationSucceeded = false
		fmt.Printf("⚠️ Could not create the per-project configuration: %v\n", err)
	} else {
		fmt.Println("📄 Per-project configuration created at: .vas_sentinel/vassentinel.yml")
	}

	fmt.Println("⚓ Git Hook 'pre-commit' installed in this repository. Environment successfully secured.")
	if rulesSucceeded && configurationSucceeded {
		updateRepositoryRegistry(path, (*registry.Registry).Register)
	}
}

// runUninit reverts in this repository exactly what 'init' did: it removes
// the rules block from AGENTS.md/CLAUDE.md/.claudecode.md, deletes the
// per-project config, and removes the 'pre-commit' hook — but only if its
// content matches byte for byte what generateHookScript would produce today;
// if it differs (another tool replaced it, or it comes from elsewhere), it
// leaves it intact and warns instead of deleting something vcSentinel did
// not install.
func runUninit(path string) {
	root, err := git.GetWorktreeRoot()
	if err != nil {
		fmt.Println("❌ uninit must run inside a Git repository (worktree root not found).")
		os.Exit(1)
	}
	if !git.IsSamePath(path, root) {
		fmt.Printf("📂 Repository root detected: %s\n", root)
		fmt.Println("⚙️ Redirecting uninit to the repository root...")
		path = root
	}

	fmt.Println("🗑️ Reverting vcSentinel in this repository...")

	cleanupSucceeded := true
	for _, name := range []string{"AGENTS.md", "CLAUDE.md", ".claudecode.md"} {
		removed, err := removeRulesFromFile(filepath.Join(path, name))
		switch {
		case err != nil:
			cleanupSucceeded = false
			fmt.Printf("⚠️ Could not clean %s: %v\n", name, err)
		case removed:
			fmt.Printf("📝 Volume rules removed from: %s\n", name)
		}
	}

	configPath := filepath.Join(path, ".vas_sentinel", "vassentinel.yml")
	if err := os.Remove(configPath); err != nil {
		if !os.IsNotExist(err) {
			cleanupSucceeded = false
			fmt.Printf("⚠️ Could not remove %s: %v\n", configPath, err)
		}
	} else {
		fmt.Println("📄 Per-project configuration removed: .vas_sentinel/vassentinel.yml")
	}

	commonDir, err := git.GetGitCommonDir(path)
	if err != nil {
		cleanupSucceeded = false
		fmt.Printf("⚠️ Could not determine the repository's Git directory: %v\n", err)
	} else {
		if err := removeHookIfVCSentinelOwned(filepath.Join(commonDir, "hooks", "pre-commit")); err != nil {
			cleanupSucceeded = false
			fmt.Printf("⚠️ Could not clean the hook: %v\n", err)
		}
	}

	fmt.Println("✅ vcSentinel reverted in this repository.")
	if cleanupSucceeded {
		updateRepositoryRegistry(path, (*registry.Registry).Remove)
	}
}

// injectRulesIntoFile appends the managed rule when it is absent and
// replaces it when an earlier managed version is present. Re-running init with
// the current rule is byte-for-byte idempotent, while old marked and legacy
// blocks are migrated to one current marked block.
func injectRulesIntoFile(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	content := string(data)

	if markedVolumeRulesPattern.MatchString(content) {
		withoutRules := removeVolumeRules(content)
		updated := withoutRules + volumeRulesFor(content)
		if updated == content {
			return false, nil
		}
		return true, os.WriteFile(path, []byte(updated), 0644)
	}

	if legacyVolumeRulesPattern.MatchString(content) {
		// removeVolumeRules (not a direct ReplaceAllString) because it removes
		// up to the fixed point: necessary when two legacy copies are glued
		// together with a single separating newline, see
		// removeAllMatches in reglasvolumen.go.
		withoutLegacy := removeVolumeRules(content)
		updated := withoutLegacy + volumeRulesFor(content)
		return true, os.WriteFile(path, []byte(updated), 0644)
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if _, err := f.WriteString(volumeRulesFor(content)); err != nil {
		return false, err
	}
	return true, nil
}

// removeRulesFromFile removes ALL occurrences of the volumeRules block from
// the file when it is present and returns whether it made any change. A
// missing file or one without the block is not an error: there was simply
// nothing to remove. Removing all occurrences also repairs the duplicates
// older versions of init left behind.
func removeRulesFromFile(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if !containsVolumeRules(string(data)) {
		return false, nil
	}
	updated := removeVolumeRules(string(data))
	if strings.TrimSpace(updated) == "" {
		// The file had no content of its own: init created it only for the
		// rules block, so uninit deletes it instead of leaving it empty.
		return true, os.Remove(path)
	}
	return true, os.WriteFile(path, []byte(updated), 0644)
}

// removeHookIfVCSentinelOwned deletes the pre-commit hook at hookPath only if
// its content exactly matches what generateHookScript produces now;
// if it differs (another origin) or does not exist, it is left alone.
func removeHookIfVCSentinelOwned(hookPath string) error {
	current, err := os.ReadFile(hookPath)
	switch {
	case os.IsNotExist(err):
		return nil
	case err != nil:
		return err
	case string(current) != generateHookScript():
		fmt.Println("⚠️ The current 'pre-commit' hook does not match the one installed by vcSentinel: leaving it untouched.")
		return nil
	}
	if err := os.Remove(hookPath); err != nil {
		return err
	}
	fmt.Println("⚓ Hook 'pre-commit' removed.")
	return nil
}

// generateHookScript returns the pre-commit hook content adapted to the
// operating system where the initializer runs. It uses the absolute path of
// the running binary so it does not depend on the hook shell's PATH.
func generateHookScript() string {
	exe, err := os.Executable()
	if err != nil {
		exe = "vcsentinel"
	}
	return generateHookScriptFor(exe)
}

func generateHookScriptFor(exe string) string {
	exeAbs := filepath.ToSlash(exe)
	switch runtime.GOOS {
	case "windows":
		// Git for Windows runs hooks with its sh.exe: it uses double quotes to
		// tolerate spaces in the path (e.g. "C:/Program Files").
		return "#!/bin/sh\n\"" + exeAbs + "\" check --staged\n"
	default:
		// Linux/macOS: standard sh with the binary's absolute path.
		return "#!/bin/sh\n\"" + exeAbs + "\" check --staged\n"
	}
}

type checkReport struct {
	Worktree           string `json:"worktree"`
	AuthoredLines      int    `json:"authored_lines"`
	InformationalLines int    `json:"informational_lines"`
	State              string `json:"state"`
	Advisory           bool   `json:"advisory"`
	Recommendation     string `json:"recommendation,omitempty"`
	Error              string `json:"error,omitempty"`
}

func runCheck(path string, args []string) int {
	flags, err := parseCheckFlags(args)
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return 1
	}
	if flags.staged {
		return runStagedCheck(path, flags.jsonOut)
	}
	return runCheckWith(os.Stdout, path, flags.jsonOut, git.MeasureVolume)
}

func runCheckWith(w io.Writer, path string, jsonOut bool, measure func() (git.PendingVolume, error)) int {
	volume, err := measure()
	if err != nil {
		report := checkReport{Worktree: path, State: "ERROR", Error: err.Error()}
		if jsonOut {
			_ = writeCheckJSON(w, report)
			return 1
		}
		fmt.Fprintf(w, "❌ Git error: %v\n", err)
		return 1
	}

	report := checkReport{
		Worktree:           path,
		AuthoredLines:      volume.Blocking,
		InformationalLines: volume.Informational,
		State:              volume.State,
		Advisory:           true,
	}
	if volume.State == "CRITICAL" {
		report.Recommendation = "vcsentinel slice plan --json"
	}
	if jsonOut {
		return writeCheckJSON(w, report)
	}

	fmt.Fprintf(w, "📊 Added code lines in this worktree: %d [%s]\n", volume.Blocking, volume.State)
	if volume.Informational > 0 {
		// Documentation and generated content is reported but does not block:
		// the guardian measures code reviewability, not bytes.
		fmt.Fprintf(w, "📄 Additionally, %d documentation and generated-file lines (they do not count toward the limit).\n", volume.Informational)
	}
	if volume.State == "CRITICAL" {
		fmt.Fprintln(w, "⚠️ Advisory: the worktree exceeds 400 authored lines. Run `vcsentinel slice plan --json` to plan a reviewable split.")
		fmt.Fprintln(w, "✅ Measurement succeeded. You can continue implementation.")
		return 0
	}
	fmt.Fprintln(w, "✅ Volume under control. You can continue.")
	return 0
}

func writeCheckJSON(w io.Writer, report checkReport) int {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(w, "❌ Could not serialize check report: %v\n", err)
		return 1
	}
	if _, err := fmt.Fprintln(w, string(data)); err != nil {
		return 1
	}
	return 0
}

// errRefactorApplied signals that an agent already applied the refactoring
// plan and that runSlice must recompute the plan with the updated working
// tree.
var errRefactorApplied = errors.New("refactor applied by the agent")

func runSlice(path string) {
	for {
		fmt.Println("✂️ Starting the deterministic partitioning algorithm...")
		files, err := git.GetModifiedFiles()
		if err != nil || len(files) == 0 {
			fmt.Println("📭 No pending modifications to process.")
			return
		}

		plan, err := git.BuildFragmentationPlanWithReader(files, buildOversizedDecision(path), runGitForChange)
		if errors.Is(err, errRefactorApplied) {
			fmt.Println("\n♻️ Refactor applied. Recomputing the fragmentation plan...")
			continue
		}
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			os.Exit(1)
		}

		_, canceled := chooseAdapterAndGenerateMessages(path, plan)
		if canceled {
			fmt.Println("\n🚫 Operation canceled. Nothing was committed.")
			return
		}

		if !approveAndExecute(plan, path) {
			fmt.Println("\n🚫 Operation canceled. Nothing was committed.")
			return
		}
		return
	}
}

// buildOversizedDecision returns the callback that decides what to do with a
// massive code file (>500 lines) while the plan is being built:
// 1) refactor with AI, 2) AI bypass, or 3) abort.
func buildOversizedDecision(path string) func(git.ModifiedFile) (bool, error) {
	return func(f git.ModifiedFile) (bool, error) {
		for {
			fmt.Printf("\n⚠️ The file %s has %d lines and exceeds the suggested maximum of %d.\n", f.Path, f.Lines, git.GiantCodeLimit)
			fmt.Println("How do you want to proceed?")
			fmt.Println("  1) Refactor with AI: proposes a file-splitting plan (SRP)")
			fmt.Println("  2) AI bypass: fragment the file as-is (no review)")
			fmt.Println("  3) Abort the operation")
			fmt.Print("Choice (1-3): ")

			// An EOF here (stdin closed) neither bypasses nor defaults to
			// anything: it propagates as an error and BuildFragmentationPlan
			// aborts the fragmentation. approveAndExecute applies the same
			// criterion on EOF (cancel, see B9): keep both dialogs consistent
			// on a closed stdin if either one is modified.
			answer, err := readLine()
			if err != nil {
				return false, fmt.Errorf("error reading the option: %w", err)
			}
			answer = strings.TrimSpace(answer)
			switch answer {
			case "1":
				refactored, err := refactorOversized(path, f)
				if err != nil {
					return false, err
				}
				if !refactored {
					continue
				}
				return false, fmt.Errorf("apply the proposed refactoring plan and run 'vcsentinel slice' again so the already-split file re-enters the plan")
			case "2":
				return true, nil
			case "3":
				return false, nil
			default:
				fmt.Println("Invalid option. Choose 1, 2, or 3.")
			}
		}
	}
}

// refactorOversized asks the automatic agent for a splitting plan for the
// massive file, shows it, and asks how to apply it: manually by the user, by
// delegating it to an agent (with a fallback to another agent on failure), or
// cancel. It returns (false, nil) to go back to the previous menu; it returns
// an error to abort the slice (errRefactorApplied when the agent already
// applied the plan and the plan must be recomputed).
func refactorOversized(path string, f git.ModifiedFile) (bool, error) {
	adapter, err := agentadapter.NewAgentAdapter(path)
	if err != nil {
		fmt.Printf("⚠️ Could not create the refactoring agent: %v\n", err)
		return false, nil
	}

	refactorer, ok := adapter.(agentadapter.AdapterRefactor)
	if !ok {
		fmt.Println("⚠️ The active agent does not support refactoring proposals.")
		return false, nil
	}

	if !git.VerifyAdapter(adapter) {
		fmt.Println("⚠️ The active agent did not respond correctly.")
		return false, nil
	}

	fmt.Printf("🔍 Asking the configured agent for a splitting plan for %s...\n", f.Path)
	refactorPlan, err := refactorer.ProposeRefactorPlan(f.Path)
	if err != nil {
		fmt.Printf("⚠️ The agent failed to generate the plan: %v\n", err)
		return false, nil
	}
	if strings.TrimSpace(refactorPlan) == "" {
		fmt.Println("⚠️ The agent returned an empty plan.")
		return false, nil
	}

	fmt.Println("\n📋 Proposed splitting plan:")
	for _, line := range strings.Split(refactorPlan, "\n") {
		fmt.Printf("   %s\n", line)
	}

	for {
		fmt.Println("\nHow do you want to apply the plan?")
		fmt.Println("  1) Apply it yourself: you apply it manually and then run 'vcsentinel slice' again")
		fmt.Println("  2) Delegate it to an agent: the agent applies it and slice re-runs automatically")
		fmt.Println("  c) Cancel the refactoring")
		fmt.Print("Choice (1, 2, or c): ")

		answer, err := readLine()
		if err != nil {
			return false, fmt.Errorf("error reading the option: %w", err)
		}
		answer = strings.ToLower(strings.TrimSpace(answer))
		switch answer {
		case "1":
			return false, fmt.Errorf("apply the proposed refactoring plan and run 'vcsentinel slice' again so the already-split file re-enters the plan")
		case "2":
			applied, err := delegateRefactor(path, f, refactorPlan)
			if err != nil {
				return false, err
			}
			if applied {
				return false, errRefactorApplied
			}
			return false, nil
		case "c", "cancel":
			return false, nil
		default:
			fmt.Println("Invalid option. Choose 1, 2, or c.")
		}
	}
}

// delegateRefactor asks the automatic agent to apply the splitting plan over
// the working tree. If the automatic agent does not exist or fails, it offers
// choosing another available agent or canceling. It returns true when an
// agent applied the refactoring.
func delegateRefactor(path string, f git.ModifiedFile, refactorPlan string) (bool, error) {
	adapter, err := agentadapter.NewAgentAdapter(path)
	if err != nil || !git.VerifyAdapter(adapter) {
		return chooseRefactorAgentLoop(path, f, refactorPlan)
	}

	refactorer, ok := adapter.(agentadapter.AdapterRefactor)
	if !ok {
		return chooseRefactorAgentLoop(path, f, refactorPlan)
	}

	applied, err := applyRefactor(refactorer, f.Path, refactorPlan)
	if err != nil || !applied {
		return chooseRefactorAgentLoop(path, f, refactorPlan)
	}
	return true, nil
}

// chooseRefactorAgentLoop offers choosing another agent to apply the
// refactoring plan when the automatic agent could not, or canceling the
// refactoring. It returns true when some agent applied the plan.
func chooseRefactorAgentLoop(path string, f git.ModifiedFile, refactorPlan string) (bool, error) {
	for {
		names := agentadapter.AvailableAdapterNames(path)
		if len(names) == 0 {
			fmt.Println("⚠️ No agents are available to apply the refactoring.")
			return false, nil
		}

		fmt.Println("\n⚠️ The automatic agent could not apply the plan. Choose another agent:")
		for i, name := range names {
			fmt.Printf("  %d) %s\n", i+1, name)
		}
		fmt.Println("  c) Cancel the refactoring")
		fmt.Print("Choice: ")

		answer, err := readLine()
		if err != nil {
			return false, nil
		}
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer == "c" || answer == "cancel" {
			return false, nil
		}

		number, err := strconv.Atoi(answer)
		if err != nil || number < 1 || number > len(names) {
			fmt.Println("⚠️ Invalid option. Try again.")
			continue
		}

		adapter, err := agentadapter.NewAgentAdapterNamed(path, names[number-1])
		if err != nil {
			fmt.Printf("⚠️ Could not create the adapter for %s: %v\n", names[number-1], err)
			continue
		}
		refactorer, ok := adapter.(agentadapter.AdapterRefactor)
		if !ok {
			fmt.Printf("⚠️ The agent %s does not support refactoring.\n", names[number-1])
			continue
		}
		if !git.VerifyAdapter(adapter) {
			fmt.Printf("⚠️ The agent %s did not respond correctly. Choose another option.\n", names[number-1])
			continue
		}

		applied, err := applyRefactor(refactorer, f.Path, refactorPlan)
		if err != nil || !applied {
			fmt.Printf("⚠️ The agent %s failed to apply the plan. Choose another option.\n", names[number-1])
			continue
		}
		return true, nil
	}
}

// applyRefactor runs the refactoring plan with the given agent and confirms
// it returned a non-empty response.
func applyRefactor(refactorer agentadapter.AdapterRefactor, path string, refactorPlan string) (bool, error) {
	fmt.Printf("🔧 Asking the selected agent to apply the plan over %s...\n", path)
	summary, err := refactorer.ApplyRefactorPlan(path, refactorPlan)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(summary) == "" {
		return false, fmt.Errorf("the agent returned an empty summary")
	}
	fmt.Printf("✅ %s\n", strings.TrimSpace(summary))
	return true, nil
}

// stdinReader reads standard input line by line for the interactive flows.
var stdinReader = bufio.NewReader(os.Stdin)

func readLine() (string, error) {
	line, err := stdinReader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// chooseAdapterAndGenerateMessages selects the adapter (auto by default),
// probes it once, and generates the plan's messages. If the automatic adapter
// does not exist, the probe fails, or it fails to generate some message, it
// offers the user deterministic automatic messages, another available
// adapter, or cancel.
func chooseAdapterAndGenerateMessages(path string, plan *git.FragmentationPlan) (agentadapter.AgentAdapter, bool) {
	if !allowsExternalAgentDiff(path) {
		fmt.Println("ℹ️ No code is sent to the agent: the repository request or the local consent is missing; deterministic local messages are used.")
		git.ApplyAutomaticMessages(plan)
		return nil, false
	}
	adapter, err := agentadapter.NewAgentAdapterForMessage(path)
	if err != nil {
		fmt.Printf("⚠️ %v\n", err)
		return chooseAdapterLoop(path, plan)
	}

	if git.VerifyAdapter(adapter) {
		if fallbacks := git.GenerateBatchMessages(plan, adapter); fallbacks == 0 {
			return adapter, false
		}
		fmt.Println("⚠️ The automatic agent failed to generate some messages.")
		return chooseAdapterLoop(path, plan)
	}

	fmt.Println("⚠️ The automatic agent did not respond correctly.")
	return chooseAdapterLoop(path, plan)
}

// chooseAdapterLoop offers the choice of message source when the automatic
// adapter will not do: deterministic automatic messages, one of the available
// agents, or cancel. It returns the chosen adapter (nil when automatic
// messages are chosen) and whether the operation was canceled.
func chooseAdapterLoop(path string, plan *git.FragmentationPlan) (agentadapter.AgentAdapter, bool) {
	for {
		names := agentadapter.AvailableAdapterNames(path)
		fmt.Println("\nChoose how to obtain the commit messages:")
		fmt.Println("  1) Use deterministic automatic messages for all batches")
		for i, name := range names {
			fmt.Printf("  %d) Use the agent %s\n", i+2, name)
		}
		fmt.Println("  c) Cancel the operation")
		fmt.Print("Choice: ")

		answer, err := readLine()
		if err != nil {
			return nil, true
		}
		answer = strings.ToLower(strings.TrimSpace(answer))

		switch {
		case answer == "1" || answer == "a" || answer == "auto":
			git.ApplyAutomaticMessages(plan)
			return nil, false
		case answer == "c" || answer == "cancel":
			return nil, true
		default:
			number, err := strconv.Atoi(answer)
			if err != nil || number < 2 || number > len(names)+1 {
				fmt.Println("⚠️ Invalid option. Try again.")
				continue
			}
			name := names[number-2]
			adapter, err := agentadapter.NewAgentAdapterNamedForMessage(path, name)
			if err != nil {
				fmt.Printf("⚠️ Could not create the adapter for %s: %v\n", name, err)
				continue
			}
			if !git.VerifyAdapter(adapter) {
				fmt.Printf("⚠️ The agent %s did not respond correctly. Choose another option.\n", name)
				continue
			}
			if fallbacks := git.GenerateBatchMessages(plan, adapter); fallbacks > 0 {
				fmt.Printf("⚠️ The agent %s failed to generate some messages. Choose another option.\n", name)
				continue
			}
			return adapter, false
		}
	}
}

// planAction represents the decision taken in the fragmentation plan's
// approval menu, separated from stdin reading and from its execution so
// decideAction can be tested without simulating input or touching the plan
// or git.
type planAction int

const (
	actionInvalid planAction = iota
	actionApprove
	actionRegenerate
	actionEdit
	actionCancel
)

// decideAction translates the line read from the approval menu into the
// corresponding action. It is a pure function: it never reads stdin and has
// no effects.
func decideAction(line string) planAction {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "a", "approve":
		return actionApprove
	case "r", "regenerate":
		return actionRegenerate
	case "e", "edit":
		return actionEdit
	case "c", "cancel":
		return actionCancel
	default:
		return actionInvalid
	}
}

// approveAndExecute shows the proposed plan and drives the approval flow:
// approve everything, regenerate a message with another agent, edit a
// message, or cancel. It returns false when the user canceled without
// committing anything.
func approveAndExecute(plan *git.FragmentationPlan, path string) bool {
	for {
		printPlan(plan)
		fmt.Println("\nOptions:")
		fmt.Println("  (A)pprove all and execute (Enter)")
		fmt.Println("  (R)egenerate a batch's message with another agent")
		fmt.Println("  (E)dit a batch's message manually")
		fmt.Println("  (C)ancel without committing anything")
		fmt.Print("Choice [A]: ")

		// B9 (CRITICAL): a readLine error (EOF with closed stdin — CI,
		// nohup, a pipeline that ends, an agent without a console) is NOT a
		// user answer and must not be treated as an empty line: a real empty
		// line (Enter, with no error) must approve, because it is the
		// default the menu itself announces ("Choice [A]: "). That is why
		// the EOF decision is taken here, before decideAction — decideAction
		// stays a pure function that only classifies text; it must never
		// know the read's error state. This same criterion (EOF cancels, it
		// neither approves nor bypasses) is what buildOversizedDecision
		// already applied; no future change may separate them again.
		line, err := readLine()
		if err != nil {
			fmt.Println("\n⚠️ Standard input closed (EOF). Operation canceled, nothing was committed.")
			return false
		}

		switch decideAction(line) {
		case actionApprove:
			return runApprovedPlan(plan)
		case actionRegenerate:
			regenerateBatchMessageInteractive(plan, path)
		case actionEdit:
			editBatchMessageInteractive(plan)
		case actionCancel:
			return false
		default:
			fmt.Println("⚠️ Invalid option. Use A, R, E, or C.")
		}
	}
}

func printPlan(plan *git.FragmentationPlan) {
	fmt.Println("\n📋 Proposed fragmentation plan (nothing has been committed yet):")
	totalLines := 0
	for _, batch := range plan.Batches {
		suffix := ""
		if batch.DeterministicMessage {
			suffix = " (automatic)"
		}
		totalLines += batch.TotalLines
		fmt.Printf("  [%s] batch #%d — %d files (%d lines) — message: %s%s\n",
			batch.Layer, batch.Number, len(batch.Paths), batch.TotalLines, batch.Message, suffix)
	}
	fmt.Printf("Total: %d batches, %d lines.\n", len(plan.Batches), totalLines)
}

func regenerateBatchMessageInteractive(plan *git.FragmentationPlan, path string) {
	if !allowsExternalAgentDiff(path) {
		fmt.Println("⚠️ Cannot regenerate: the repository request or the local consent to expose the micro-diff is missing.")
		return
	}
	fmt.Print("Which batch do you want to regenerate? (number): ")
	line, err := readLine()
	if err != nil {
		return
	}
	number, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		fmt.Println("⚠️ Invalid batch number.")
		return
	}

	names := agentadapter.AvailableAdapterNames(path)
	for {
		fmt.Printf("Which agent regenerates batch #%d?\n", number)
		fmt.Println("  1) Deterministic automatic message")
		for i, name := range names {
			fmt.Printf("  %d) %s\n", i+2, name)
		}
		fmt.Print("Choice: ")

		answer, err := readLine()
		if err != nil {
			return
		}
		answer = strings.ToLower(strings.TrimSpace(answer))

		switch {
		case answer == "1" || answer == "a" || answer == "auto":
			if err := git.ApplyAutomaticMessageBatch(plan, number); err != nil {
				fmt.Printf("⚠️ %v\n", err)
			}
			return
		case answer == "c" || answer == "cancel":
			return
		default:
			index, err := strconv.Atoi(answer)
			if err != nil || index < 2 || index > len(names)+1 {
				fmt.Println("⚠️ Invalid option. Try again.")
				continue
			}
			name := names[index-2]
			adapter, err := agentadapter.NewAgentAdapterNamedForMessage(path, name)
			if err != nil {
				fmt.Printf("⚠️ Could not create the adapter for %s: %v\n", name, err)
				continue
			}
			if !git.VerifyAdapter(adapter) {
				fmt.Printf("⚠️ The agent %s did not respond correctly. Choose another option.\n", name)
				continue
			}
			if err := git.RegenerateBatchMessage(plan, number, adapter); err != nil {
				fmt.Printf("⚠️ %v\n", err)
			}
			return
		}
	}
}

func editBatchMessageInteractive(plan *git.FragmentationPlan) {
	fmt.Print("Which batch do you want to edit? (number): ")
	line, err := readLine()
	if err != nil {
		return
	}
	number, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		fmt.Println("⚠️ Invalid batch number.")
		return
	}
	fmt.Print("New commit message: ")
	message, err := readLine()
	if err != nil {
		return
	}
	if strings.TrimSpace(message) == "" {
		fmt.Println("⚠️ The message cannot be empty.")
		return
	}
	if err := git.EditBatchMessage(plan, number, message); err != nil {
		fmt.Printf("⚠️ %v\n", err)
	}
}

func runApprovedPlan(plan *git.FragmentationPlan) bool {
	results, err := git.RunFragmentationPlan(plan)
	if err != nil {
		fmt.Printf("❌ Critical error while creating commits: %v\n", err)
		os.Exit(1)
	}
	printSummary(results)
	return true
}

func printSummary(results []git.CommitResult) {
	fmt.Printf("\n🎉 History fragmented successfully! Created %d commits.\n", len(results))
	for _, r := range results {
		fmt.Printf("  %s  [%s]  %d files  %s\n", r.Hash, r.Layer, r.Files, r.Message)
	}
	clean, err := git.WorktreeClean()
	switch {
	case err != nil:
		fmt.Printf("⚠️ Could not verify the worktree state: %v\n", err)
	case clean:
		fmt.Println("✅ The worktree is clean. Volume under control.")
	default:
		fmt.Println("⚠️ Pending changes remain in the worktree. Review with 'vcsentinel check'.")
	}
}
