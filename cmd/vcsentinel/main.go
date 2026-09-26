package main

import (
	"bufio"
	"crypto/sha256"
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
	return filepath.Join(home, ".vcsentinel", "repositories.json"), nil
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
		fmt.Fprintf(os.Stderr, "vcsentinel: repository registry update failed: %v\n", err)
	}
}

// markerBegin and markerEnd delimit the volume rules block in a way that is
// stable across versions: init/uninit detect and remove it by these markers,
// not by the inner text, so a future version can reword the text while
// staying idempotent and leaving no orphans behind.
const markerBegin = "<!-- vcsentinel:begin -->"
const markerEnd = "<!-- vcsentinel:end -->"

// volumeRulesBody is the visible text of the rule, free to change its wording
// between versions: detection does not depend on it.
const volumeRulesBody = "## CRITICAL VOLUME RULE (THE GUARDIAN)\n- Before making changes or proposing a plan, run `vcsentinel check`. It measures the whole worktree and is advisory, including when the state is `CRITICAL`.\n- The repository's `pre-commit` hook runs `vcsentinel check --staged`. This is the enforcement boundary: it rejects staged authored code over the 400-line review budget.\n- When the worktree check is `CRITICAL`, run `vcsentinel slice plan --json` to produce reviewable selections without committing.\n- After the user answers every pending decision, apply the approved selections with `vcsentinel slice apply --plan plan.json --answers answers.json`. Never answer those decisions on the user's behalf.\n"

// volumeRules is the block that 'init' injects today into AGENTS.md,
// CLAUDE.md and .claudecode.md, wrapped in markerBegin/markerEnd.
const volumeRules = "\n" + markerBegin + "\n" + volumeRulesBody + markerEnd + "\n"

// vcsentinelSkillContent is the canonical repository-local skill installed by
// init. Ownership is determined by an exact byte-for-byte content match so
// uninit never removes a foreign or locally modified skill.
const vcsentinelSkillContent = `---
name: vcsentinel
description: "Trigger: vcSentinel, worktree volume, staged commits, review, gate, pull request, durable runs. Guide repository changes without bypassing human decisions."
license: Apache-2.0
metadata:
  author: "iseoane"
  version: "1.0"
---

<!-- vcsentinel:managed-skill -->

## Activation Contract

Load for repository changes governed by vcSentinel, large-change planning,
commit review, lifecycle validation, pull requests, or durable runs.

## Hard Rules

- Before making changes or proposing a plan, run vcsentinel check. It measures
  the whole worktree and is advisory, including when the state is CRITICAL.
- The pre-commit hook runs vcsentinel check --staged. More than 400 authored
  lines in the staged candidate are rejected.
- Do not commit, publish, answer decisions, bypass a gate, invent a verdict,
  or clear findings on the human's behalf.

## Decision Gates

| Situation | Action |
| --- | --- |
| slice plan reports pending_decisions or exits 3 | Show decisions verbatim; wait for the human's bypass or abort answer. |
| review | review audits commits and records findings and verdicts; never silently clear a finding. |
| gate | gate runs deterministic validation; it does not replace semantic review. |
| pr review / pr create | pr review records branch evidence; pr create publishes only after requirements and human approval. |
| runs | runs controls and observes durable execution; status, logs, verification, and delivery remain human-controlled. |

## Execution Steps

1. Run vcsentinel check before changing files or proposing a plan.
2. Create a plan without committing:

~~~sh
vcsentinel slice plan --json > plan.json
~~~

3. If decisions exist, show them verbatim and wait. Write only the human's
   literal bypass or abort answers to answers.json. Otherwise write
   {"plan_id":"<plan_id>","answers":{}} to answers.json.
4. Apply the answered plan:

~~~sh
vcsentinel slice apply --plan plan.json --answers answers.json
~~~

5. Follow help and exit codes; keep review, gate, pull-request, and run
   evidence visible. Never invent authority or bypass human control.

## Output Contract

Return commands and observed results, decisions and human answers,
review/gate/pull-request/run evidence, blockers, and what was not run. Never
claim approval, publication, or a semantic verdict without authoritative
evidence.

## References

- ../../../AGENTS.md — repository volume, slicing, and lifecycle rules.
- ../../../docs/design/runs-cli.md — durable-run commands and exit codes.
`

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
		return
	default:
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

const topLevelUsage = "Usage: vcsentinel [version | help | check | slice | review | refute | accept | reopen | gate | lint | explain | pr | runs | tui | consent-diff | rebase | status | metrics | doctor | init | uninit | install | upgrade | uninstall]"

func printUsage() {
	fmt.Println("🤖 vcSentinel: Local Code Guardian")
	fmt.Println(topLevelUsage)
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
	b.WriteString("Usage: vcsentinel [version | help | check | slice | review |\n")
	b.WriteString("             refute | accept | reopen | gate | lint | explain | pr |\n")
	b.WriteString("             runs | tui | consent-diff | rebase | status | metrics |\n")
	b.WriteString("             doctor | init | uninit | install | upgrade | uninstall]\n\n")
	b.WriteString("Subcommands:\n")
	printHelpItem(&b, "version", "Shows the installed vcSentinel version.")
	printHelpItem(&b, "help", "Shows this command list or detailed help for one command.")
	printHelpItem(&b, "check", "Measures added authored code; --staged checks the pending commit against the 400-line review limit.")
	printHelpItem(&b, "slice", "Groups pending changes into reviewable commits of at most 400 authored lines.")
	printHelpItem(&b, "", "slice plan [--json] [--intent TEXT] proposes selections without committing (exit 3 when a user decision is needed).")
	printHelpItem(&b, "", "slice apply --plan X --answers Y applies a plan only after every required decision is answered.")
	printHelpItem(&b, "review", "Reviews one or more commits, records the result, and can enforce a blocking result with --gate.")
	printHelpItem(&b, "refute", "Records evidence that one reviewed finding is not valid; it clears only that finding's block.")
	printHelpItem(&b, "accept", "Records that a person accepts one reviewed finding; acceptance does not clear its block.")
	printHelpItem(&b, "reopen", "Records that a previously cleared finding applies again and blocks the review.")
	printHelpItem(&b, "gate", "Runs the configured checks for a pre-commit, pre-push, or pull-request check.")
	printHelpItem(&b, "lint", "Runs the lint commands configured for this repository.")
	printHelpItem(&b, "explain", "Describes the size, changed areas, risks, and suggested split for a commit range.")
	printHelpItem(&b, "pr", "Saves a branch review with pr review, then publishes that saved result with pr create.")
	printHelpItem(&b, "", "pr review flags: --base X --parent X --overview --json.")
	printHelpItem(&b, "", "pr create flags: --base X --parent X --chain-pr --force --reason \"...\".")
	printHelpItem(&b, "runs", "Starts and manages tracked agent tasks: start, status, logs, respond, abort, retry, recover, verify, attach, daemon, and prune.")
	printHelpItem(&b, "tui", "Opens the interactive dashboard for this repository and its tracked tasks.")
	printHelpItem(&b, "consent-diff", "Grants, revokes, or shows permission to share small diffs with configured agents.")
	printHelpItem(&b, "rebase", "Fetches the upstream branch and rebases the current branch after confirmation.")
	printHelpItem(&b, "status", "Shows repository volume, saved review records, and recent command activity.")
	printHelpItem(&b, "metrics", "Shows local review, remediation, and execution measurements; unknown values stay unknown.")
	printHelpItem(&b, "doctor", "Checks whether the local tools and configured agents needed for review are ready.")
	printHelpItem(&b, "init", "Sets up vcSentinel in a Git repository: project settings, guidance, a pre-commit check, and the repository-local agent skill.")
	printHelpItem(&b, "uninit", "Removes vcSentinel's repository setup while preserving foreign or modified files it does not own.")
	printHelpItem(&b, "install", "Installs the latest release globally for your user account; initialize each repository separately with init.")
	printHelpItem(&b, "upgrade", "Updates the globally installed vcSentinel program without changing repository setup.")
	printHelpItem(&b, "uninstall", "Removes the global vcSentinel program and settings; per-repository setup remains until uninit is run there.")
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

	skillSucceeded := true
	skillPath := filepath.Join(path, ".agents", "skills", "vcsentinel", "SKILL.md")
	if written, err := installVCSentinelSkill(skillPath); err != nil {
		skillSucceeded = false
		fmt.Printf("⚠️ Could not install the vcSentinel agent skill: %v\n", err)
	} else if written {
		fmt.Println("📝 vcSentinel agent skill installed at: .agents/skills/vcsentinel/SKILL.md")
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

	hookPath := filepath.Join(hooksDir, "pre-commit")
	if err := installPreCommitHook(hookPath); err != nil {
		fmt.Printf("❌ Could not install the hook at %s: %v\n", hookPath, err)
		os.Exit(1)
	}

	configurationSucceeded := true
	if err := setup.CreatePerProjectConfig(path); err != nil {
		configurationSucceeded = false
		fmt.Printf("⚠️ Could not create the per-project configuration: %v\n", err)
	} else {
		fmt.Println("📄 Per-project configuration created at: .vcsentinel/vcsentinel.yml")
	}

	fmt.Println("⚓ Git Hook 'pre-commit' installed in this repository. Environment successfully secured.")
	if rulesSucceeded && skillSucceeded && configurationSucceeded {
		updateRepositoryRegistry(path, (*registry.Registry).Register)
	}
}

// runUninit reverts in this repository exactly what 'init' did: it removes
// the rules block from AGENTS.md/CLAUDE.md/.claudecode.md, removes the
// repository-local agent skill, deletes the per-project config, and removes
// the 'pre-commit' hook — but only when its recognized marker and executable
// identify it as a vcSentinel hook; otherwise it leaves the hook intact and
// warns instead of deleting something vcSentinel did not install.
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

	skillPath := filepath.Join(path, ".agents", "skills", "vcsentinel", "SKILL.md")
	if removed, err := removeVCSentinelSkillIfOwned(skillPath); err != nil {
		cleanupSucceeded = false
		fmt.Printf("⚠️ Could not clean the vcSentinel agent skill: %v\n", err)
	} else if removed {
		fmt.Println("📝 vcSentinel agent skill removed: .agents/skills/vcsentinel/SKILL.md")
	}

	configPath := filepath.Join(path, ".vcsentinel", "vcsentinel.yml")
	if err := os.Remove(configPath); err != nil {
		if !os.IsNotExist(err) {
			cleanupSucceeded = false
			fmt.Printf("⚠️ Could not remove %s: %v\n", configPath, err)
		}
	} else {
		fmt.Println("📄 Per-project configuration removed: .vcsentinel/vcsentinel.yml")
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

// installVCSentinelSkill creates the canonical agent skill when it is absent.
// Existing content is never overwritten, even if it differs from the
// canonical skill; exact content ownership is required for uninit cleanup.
func installVCSentinelSkill(path string) (bool, error) {
	current, err := os.ReadFile(path)
	switch {
	case err == nil:
		if string(current) == vcsentinelSkillContent {
			return false, nil
		}
		fmt.Printf("⚠️ An existing agent skill at %s is not the vcSentinel skill: leaving it untouched.\n", path)
		return false, nil
	case !os.IsNotExist(err):
		return false, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return false, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if os.IsExist(err) {
		current, readErr := os.ReadFile(path)
		if readErr != nil {
			return false, readErr
		}
		if string(current) != vcsentinelSkillContent {
			fmt.Printf("⚠️ An existing agent skill at %s is not the vcSentinel skill: leaving it untouched.\n", path)
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := file.WriteString(vcsentinelSkillContent); err != nil {
		_ = file.Close()
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	return true, nil
}

// removeVCSentinelSkillIfOwned removes the skill only when its content exactly
// matches the canonical content installed by vcSentinel. A missing or foreign
// skill is not an error and is left untouched.
func removeVCSentinelSkillIfOwned(path string) (bool, error) {
	current, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return false, nil
	case err != nil:
		return false, err
	case string(current) != vcsentinelSkillContent:
		fmt.Printf("⚠️ The current agent skill at %s does not match the one installed by vcSentinel: leaving it untouched.\n", path)
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	return true, nil
}

// injectRulesIntoFile appends the managed rule when it is absent and
// replaces it when an earlier managed version is present. Re-running init with
// the current rule is byte-for-byte idempotent and replaces one current marked
// block with the latest wording.
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

// removeRulesFromFile removes ALL occurrences of the current volumeRules block
// from the file when it is present and returns whether it made any change. A
// missing file or one without the block is not an error: there was simply
// nothing to remove.
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

const (
	hookOriginalSuffix            = ".vcsentinel-original"
	hookTransactionSuffix         = ".vcsentinel-transaction"
	hookTransactionMarker         = "# vcsentinel:pre-commit-transaction:v1"
	hookTransactionPhasePrefix    = "# vcsentinel:phase="
	hookTransactionWrapperPrefix  = "# vcsentinel:wrapper-sha256="
	hookTransactionOriginalPrefix = "# vcsentinel:original-sha256="
	hookTransactionModePrefix     = "# vcsentinel:original-mode="
	hookTransactionKindPrefix     = "# vcsentinel:original-kind="
	hookDirectMarker              = "# vcsentinel:pre-commit-hook:v1"
	hookDirectExecutablePrefix    = "# vcsentinel:executable="
	hookWrapperMarker             = "# vcsentinel:pre-commit-wrapper:v1"
	hookWrapperOriginalPathPrefix = "# vcsentinel:original-path="
	hookWrapperOriginalKindPrefix = "# vcsentinel:original-kind="
	hookWrapperOriginalModePrefix = "# vcsentinel:original-mode="
	hookWrapperOriginalHashPrefix = "# vcsentinel:original-sha256="
	hookWrapperExecutablePrefix   = "# vcsentinel:executable="
)

type hookWrapperMetadata struct {
	executablePath string
	originalPath   string
	originalKind   string
	originalMode   os.FileMode
	originalHash   string
}

type hookTransaction struct {
	phase        string
	wrapperHash  string
	originalHash string
	originalMode os.FileMode
	originalKind string
}

func hookTransactionPath(hookPath string) string {
	return hookPath + hookTransactionSuffix
}

func beginHookTransaction(hookPath, phase, wrapperHash, originalHash string, originalMode os.FileMode, originalKind string) error {
	path := hookTransactionPath(hookPath)
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("hook transaction already exists at %s", path)
	} else if !os.IsNotExist(err) {
		return err
	}
	content := hookTransactionMarker + "\n" + hookTransactionPhasePrefix + phase + "\n" +
		hookTransactionOriginalPrefix + originalHash + "\n" +
		fmt.Sprintf("%s%04o\n", hookTransactionModePrefix, originalMode.Perm()) +
		hookTransactionKindPrefix + originalKind + "\n"
	if wrapperHash != "" {
		content += hookTransactionWrapperPrefix + wrapperHash + "\n"
	}
	return os.WriteFile(path, []byte(content), 0600)
}

func clearHookTransaction(hookPath string) error {
	err := os.Remove(hookTransactionPath(hookPath))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func readHookTransaction(hookPath string) (hookTransaction, bool, error) {
	content, err := os.ReadFile(hookTransactionPath(hookPath))
	if os.IsNotExist(err) {
		return hookTransaction{}, false, nil
	}
	if err != nil {
		return hookTransaction{}, true, err
	}
	text := string(content)
	if !strings.HasPrefix(text, hookTransactionMarker+"\n") {
		return hookTransaction{}, true, errors.New("invalid vcSentinel hook transaction marker")
	}
	phase := wrapperValue(text, hookTransactionPhasePrefix)
	wrapperHash := wrapperValue(text, hookTransactionWrapperPrefix)
	originalHash := wrapperValue(text, hookTransactionOriginalPrefix)
	modeText := wrapperValue(text, hookTransactionModePrefix)
	originalKind := wrapperValue(text, hookTransactionKindPrefix)
	if phase != "install" && phase != "restore" || originalHash == "" || modeText == "" || originalKind == "" {
		return hookTransaction{}, true, errors.New("incomplete vcSentinel hook transaction")
	}
	if phase == "restore" && wrapperHash == "" {
		return hookTransaction{}, true, errors.New("restore transaction has no wrapper hash")
	}
	mode, err := strconv.ParseUint(modeText, 8, 32)
	if err != nil {
		return hookTransaction{}, true, fmt.Errorf("invalid transaction hook mode: %w", err)
	}
	if originalKind != "regular" && originalKind != "symlink" {
		return hookTransaction{}, true, fmt.Errorf("invalid transaction hook kind %q", originalKind)
	}
	return hookTransaction{phase, wrapperHash, originalHash, os.FileMode(mode), originalKind}, true, nil
}

func hookExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func recoverHookTransaction(hookPath string) error {
	transaction, exists, err := readHookTransaction(hookPath)
	if err != nil || !exists {
		return err
	}
	if transaction.phase == "install" {
		return recoverInstallTransaction(hookPath, transaction)
	}
	return recoverRestoreTransaction(hookPath, transaction)
}

func recoverOrphanedHookBackup(hookPath string) (bool, error) {
	hook, err := hookExists(hookPath)
	if err != nil {
		return false, err
	}
	if hook {
		return false, nil
	}
	temporary, err := hookExists(hookPath + ".vcsentinel-wrapper")
	if err != nil {
		return false, err
	}
	backup, err := hookExists(originalHookPath(hookPath))
	if err != nil {
		return false, err
	}
	if temporary || backup {
		return false, errors.New("orphaned hook state has no valid vcSentinel transaction")
	}
	return false, nil
}

// Hook markers are local coordination state, not cryptographic authentication.
// Recovery trusts them only when the recorded type, mode, and content hashes
// agree with the filesystem; every conflict fails closed for manual recovery.
func hookMatchesTransaction(path string, transaction hookTransaction) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hookFileKind(info) != transaction.originalKind || info.Mode().Perm() != transaction.originalMode.Perm() {
		return false, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	return hookContentHash(content) == transaction.originalHash, nil
}

func requireHookMatchesTransaction(path string, transaction hookTransaction) error {
	valid, err := hookMatchesTransaction(path, transaction)
	if err != nil {
		return err
	}
	if !valid {
		return errors.New("recovered hook does not match transaction original metadata")
	}
	return nil
}

func recoverInstallTransaction(hookPath string, transaction hookTransaction) error {
	backupPath := originalHookPath(hookPath)
	hook, err := hookExists(hookPath)
	if err != nil {
		return err
	}
	backup, err := hookExists(backupPath)
	if err != nil {
		return err
	}
	if backup {
		valid, err := hookMatchesTransaction(backupPath, transaction)
		if err != nil {
			return err
		}
		if !valid {
			return errors.New("interrupted hook installation has an unverified original sidecar")
		}
	}
	switch {
	case !hook && backup:
		if err := os.Rename(backupPath, hookPath); err != nil {
			return err
		}
		if err := requireHookMatchesTransaction(hookPath, transaction); err != nil {
			return err
		}
		return clearHookTransaction(hookPath)
	case hook && !backup:
		return errors.New("interrupted hook installation is missing its original sidecar")
	case hook && backup:
		current, err := os.ReadFile(hookPath)
		if err != nil {
			return err
		}
		if valid, _ := isValidChainedHook(hookPath, current); valid {
			return clearHookTransaction(hookPath)
		}
		return errors.New("interrupted hook installation has conflicting hook and backup state")
	default:
		return errors.New("interrupted hook installation has no recoverable original hook")
	}
}

func recoverRestoreTransaction(hookPath string, transaction hookTransaction) error {
	backupPath := originalHookPath(hookPath)
	temporaryPath := hookPath + ".vcsentinel-wrapper"
	hook, err := hookExists(hookPath)
	if err != nil {
		return err
	}
	temporary, err := hookExists(temporaryPath)
	if err != nil {
		return err
	}
	backup, err := hookExists(backupPath)
	if err != nil {
		return err
	}
	if backup {
		valid, err := hookMatchesTransaction(backupPath, transaction)
		if err != nil {
			return err
		}
		if !valid {
			return errors.New("interrupted hook restoration has an unverified original sidecar")
		}
	}
	switch {
	case !hook && temporary && backup:
		content, err := os.ReadFile(temporaryPath)
		if err != nil || hookContentHash(content) != transaction.wrapperHash {
			if err != nil {
				return err
			}
			return errors.New("interrupted hook restoration found a modified wrapper")
		}
		if err := os.Rename(temporaryPath, hookPath); err != nil {
			return err
		}
		current, err := os.ReadFile(hookPath)
		if err != nil {
			return err
		}
		if valid, _ := isValidChainedHook(hookPath, current); !valid {
			return errors.New("interrupted hook restoration produced an unverified wrapper")
		}
		return clearHookTransaction(hookPath)
	case !hook && !temporary && backup:
		if err := os.Rename(backupPath, hookPath); err != nil {
			return err
		}
		if err := requireHookMatchesTransaction(hookPath, transaction); err != nil {
			return err
		}
		return clearHookTransaction(hookPath)
	case hook && temporary && !backup:
		content, err := os.ReadFile(temporaryPath)
		if err != nil {
			return err
		}
		if hookContentHash(content) != transaction.wrapperHash {
			return errors.New("interrupted hook restoration found a modified wrapper")
		}
		if err := requireHookMatchesTransaction(hookPath, transaction); err != nil {
			return err
		}
		if err := os.Remove(temporaryPath); err != nil {
			return err
		}
		return clearHookTransaction(hookPath)
	case hook && !temporary && !backup:
		if err := requireHookMatchesTransaction(hookPath, transaction); err != nil {
			return err
		}
		return clearHookTransaction(hookPath)
	case hook && backup && !temporary:
		content, err := os.ReadFile(hookPath)
		if err != nil {
			return err
		}
		if valid, _ := isValidChainedHook(hookPath, content); valid {
			return clearHookTransaction(hookPath)
		}
		return errors.New("interrupted hook restoration has conflicting hook and backup state")
	default:
		return errors.New("interrupted hook restoration has no safe recovery path")
	}
}

func installPreCommitHook(hookPath string) error {
	return installPreCommitHookFor(hookPath, vcsentinelExecutablePath())
}

func installPreCommitHookFor(hookPath, executablePath string) error {
	if err := recoverHookTransaction(hookPath); err != nil {
		return err
	}
	currentInfo, err := os.Lstat(hookPath)
	if os.IsNotExist(err) {
		recovered, err := recoverOrphanedHookBackup(hookPath)
		if err != nil {
			return err
		}
		if recovered {
			return installPreCommitHookFor(hookPath, executablePath)
		}
		return createHookFile(hookPath, generateHookScriptFor(executablePath), 0755)
	}
	if err != nil {
		return err
	}
	current, err := os.ReadFile(hookPath)
	if err != nil {
		return err
	}
	currentText := string(current)
	if isVCSentinelHookScript(currentText) {
		return nil
	}
	if hasHookWrapperMarker(currentText) {
		if valid, _ := isValidChainedHook(hookPath, current); valid {
			return nil
		}
		fmt.Println("⚠️ The existing vcSentinel pre-commit wrapper was modified or could not be verified: leaving it untouched.")
		return nil
	}
	kind := hookFileKind(currentInfo)
	if kind == "" {
		fmt.Println("⚠️ The existing pre-commit hook is not a regular file or symlink: leaving it untouched.")
		return nil
	}

	backupPath := originalHookPath(hookPath)
	if _, err := os.Lstat(backupPath); err == nil {
		fmt.Printf("⚠️ A vcSentinel hook backup already exists at %s: leaving the current pre-commit hook untouched.\n", backupPath)
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := beginHookTransaction(hookPath, "install", "", hookContentHash(current), currentInfo.Mode().Perm(), kind); err != nil {
		return err
	}
	if err := os.Rename(hookPath, backupPath); err != nil {
		_ = clearHookTransaction(hookPath)
		return err
	}
	restore := func(cause error) error {
		if _, err := os.Lstat(hookPath); err == nil {
			return fmt.Errorf("%v; cannot restore the preserved hook because %s now exists", cause, hookPath)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("%v; cannot restore the preserved hook: %w", cause, err)
		}
		if err := os.Rename(backupPath, hookPath); err != nil {
			return fmt.Errorf("%v; could not restore the preserved hook: %w", cause, err)
		}
		if err := clearHookTransaction(hookPath); err != nil {
			return fmt.Errorf("%v; restored the preserved hook but could not clear transaction state: %w", cause, err)
		}
		return cause
	}
	backupInfo, err := os.Lstat(backupPath)
	if err != nil {
		return restore(err)
	}
	original, err := os.ReadFile(backupPath)
	if err != nil {
		return restore(err)
	}
	metadata := hookWrapperMetadata{executablePath, backupPath, kind, backupInfo.Mode().Perm(), hookContentHash(original)}
	if err := createHookFile(hookPath, generateChainedHookScriptFor(metadata), 0755); err != nil {
		return restore(err)
	}
	if err := clearHookTransaction(hookPath); err != nil {
		return err
	}
	fmt.Printf("⚓ Existing pre-commit hook preserved at %s and chained with vcSentinel.\n", backupPath)
	return nil
}

func createHookFile(path, content string, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode.Perm())
	if err != nil {
		return err
	}
	fail := func(err error) error {
		_ = file.Close()
		_ = os.Remove(path)
		return err
	}
	if _, err := file.WriteString(content); err != nil {
		return fail(err)
	}
	if err := file.Chmod(mode.Perm()); err != nil {
		return fail(err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func originalHookPath(hookPath string) string {
	return hookPath + hookOriginalSuffix
}

func hookFileKind(info os.FileInfo) string {
	if info.Mode()&os.ModeSymlink != 0 {
		return "symlink"
	}
	if info.Mode().IsRegular() {
		return "regular"
	}
	return ""
}

func hookContentHash(content []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(content))
}

func hasHookWrapperMarker(content string) bool {
	return strings.HasPrefix(content, "#!/bin/sh\n"+hookWrapperMarker+"\n")
}

func wrapperValue(content, prefix string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimPrefix(line, prefix)
		}
	}
	return ""
}

func parseHookWrapper(content string) (hookWrapperMetadata, error) {
	if !hasHookWrapperMarker(content) {
		return hookWrapperMetadata{}, errors.New("missing vcSentinel wrapper marker")
	}
	path := wrapperValue(content, hookWrapperOriginalPathPrefix)
	kind := wrapperValue(content, hookWrapperOriginalKindPrefix)
	modeText := wrapperValue(content, hookWrapperOriginalModePrefix)
	hash := wrapperValue(content, hookWrapperOriginalHashPrefix)
	executable := wrapperValue(content, hookWrapperExecutablePrefix)
	if path == "" || kind == "" || modeText == "" || hash == "" || executable == "" {
		return hookWrapperMetadata{}, errors.New("incomplete vcSentinel wrapper metadata")
	}
	mode, err := strconv.ParseUint(modeText, 8, 32)
	if err != nil {
		return hookWrapperMetadata{}, fmt.Errorf("invalid original hook mode: %w", err)
	}
	if kind != "regular" && kind != "symlink" {
		return hookWrapperMetadata{}, fmt.Errorf("invalid original hook kind %q", kind)
	}
	return hookWrapperMetadata{executable, filepath.FromSlash(path), kind, os.FileMode(mode), hash}, nil
}

func isValidChainedHook(hookPath string, content []byte) (bool, error) {
	metadata, err := parseHookWrapper(string(content))
	if err != nil {
		return false, err
	}
	if filepath.Clean(metadata.originalPath) != filepath.Clean(originalHookPath(hookPath)) || string(content) != generateChainedHookScriptFor(metadata) {
		return false, nil
	}
	info, err := os.Lstat(metadata.originalPath)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hookFileKind(info) != metadata.originalKind || info.Mode().Perm() != metadata.originalMode.Perm() {
		return false, nil
	}
	original, err := os.ReadFile(metadata.originalPath)
	if err != nil {
		return false, err
	}
	return hookContentHash(original) == metadata.originalHash, nil
}

// removeHookIfVCSentinelOwned removes a direct vcSentinel hook or restores a
// valid chained hook. Foreign or modified hooks remain untouched with a
// warning so uninit never deletes repository behavior it does not own.
func removeHookIfVCSentinelOwned(hookPath string) error {
	if err := recoverHookTransaction(hookPath); err != nil {
		return err
	}
	if _, err := recoverOrphanedHookBackup(hookPath); err != nil {
		return err
	}
	current, err := os.ReadFile(hookPath)
	switch {
	case os.IsNotExist(err):
		return nil
	case err != nil:
		return err
	}

	if isVCSentinelHookScript(string(current)) {
		if err := os.Remove(hookPath); err != nil {
			return err
		}
		fmt.Println("⚓ Hook 'pre-commit' removed.")
		return nil
	}

	if hasHookWrapperMarker(string(current)) {
		valid, validationErr := isValidChainedHook(hookPath, current)
		if !valid {
			if validationErr != nil {
				fmt.Printf("⚠️ The vcSentinel pre-commit wrapper could not be verified (%v): leaving it untouched.\n", validationErr)
			} else {
				fmt.Println("⚠️ The vcSentinel pre-commit wrapper was modified: leaving it untouched.")
			}
			return nil
		}
		metadata, err := parseHookWrapper(string(current))
		if err != nil {
			return err
		}
		return restoreChainedHook(hookPath, metadata, hookContentHash(current))
	}

	fmt.Println("⚠️ The current 'pre-commit' hook does not belong to vcSentinel: leaving it untouched.")
	return nil
}

func restoreChainedHook(hookPath string, metadata hookWrapperMetadata, wrapperHash string) error {
	backupPath := metadata.originalPath
	temporaryPath := hookPath + ".vcsentinel-wrapper"
	if _, err := os.Lstat(temporaryPath); err == nil {
		return fmt.Errorf("cannot restore the original hook because %s already exists", temporaryPath)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := beginHookTransaction(hookPath, "restore", wrapperHash, metadata.originalHash, metadata.originalMode, metadata.originalKind); err != nil {
		return err
	}
	if err := os.Rename(hookPath, temporaryPath); err != nil {
		_ = clearHookTransaction(hookPath)
		return err
	}
	if err := os.Rename(backupPath, hookPath); err != nil {
		if rollbackErr := os.Rename(temporaryPath, hookPath); rollbackErr == nil {
			_ = clearHookTransaction(hookPath)
		}
		return err
	}
	if err := requireHookMatchesTransaction(hookPath, hookTransaction{
		originalHash: metadata.originalHash,
		originalMode: metadata.originalMode,
		originalKind: metadata.originalKind,
	}); err != nil {
		return fmt.Errorf("restored original hook failed post-rename validation: %w", err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		return fmt.Errorf("original hook restored but wrapper cleanup failed: %w", err)
	}
	if err := clearHookTransaction(hookPath); err != nil {
		return err
	}
	fmt.Println("⚓ Original 'pre-commit' hook restored.")
	return nil
}

func isVCSentinelHookScript(content string) bool {
	lines := strings.Split(content, "\n")
	if len(lines) == 5 && lines[0] == "#!/bin/sh" && lines[1] == hookDirectMarker && strings.HasPrefix(lines[2], hookDirectExecutablePrefix) {
		declared := filepath.FromSlash(strings.TrimPrefix(lines[2], hookDirectExecutablePrefix))
		command, ok := parseHookExecutable(lines[3])
		return ok && filepath.ToSlash(command) == filepath.ToSlash(declared) && isKnownVCSentinelExecutable(declared)
	}
	if len(lines) == 3 && lines[0] == "#!/bin/sh" {
		command, ok := parseHookExecutable(lines[1])
		return ok && isKnownVCSentinelExecutable(command)
	}
	return false
}

func parseHookExecutable(line string) (string, bool) {
	if !strings.HasSuffix(line, " check --staged") {
		return "", false
	}
	token := strings.TrimSuffix(line, " check --staged")
	switch {
	case strings.HasPrefix(token, "'") && strings.HasSuffix(token, "'"):
		value := strings.TrimSuffix(strings.TrimPrefix(token, "'"), "'")
		return filepath.FromSlash(strings.ReplaceAll(value, "'\"'\"'", "'")), true
	case strings.HasPrefix(token, "\"") && strings.HasSuffix(token, "\""):
		value := strings.TrimSuffix(strings.TrimPrefix(token, "\""), "\"")
		if strings.Contains(value, "\"") {
			return "", false
		}
		return filepath.FromSlash(value), true
	default:
		return "", false
	}
}

func isKnownVCSentinelExecutable(path string) bool {
	candidate := canonicalExecutablePath(path)
	if sameExecutablePath(candidate, canonicalExecutablePath(vcsentinelExecutablePath())) {
		return true
	}
	for _, supported := range supportedVCSentinelExecutablePaths() {
		if sameExecutablePath(candidate, canonicalExecutablePath(supported)) {
			return verifyInstalledVCSentinelExecutable(path)
		}
	}
	return false
}

func canonicalExecutablePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

func sameExecutablePath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func supportedVCSentinelExecutablePaths() []string {
	if runtime.GOOS == "windows" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		return []string{filepath.Join(home, ".vcsentinel", "bin", "vcsentinel.exe")}
	}
	return []string{"/usr/local/bin/vcsentinel"}
}

func verifyInstalledVCSentinelExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	output, err := exec.Command(path, "--version").Output()
	return err == nil && strings.Contains(string(output), "vcSentinel version:")
}

func vcsentinelExecutablePath() string {
	exe, err := os.Executable()
	if err != nil {
		return "vcsentinel"
	}
	return exe
}

func generateChainedHookScriptFor(metadata hookWrapperMetadata) string {
	originalPath := filepath.ToSlash(metadata.originalPath)
	executablePath := filepath.ToSlash(metadata.executablePath)
	return "#!/bin/sh\n" +
		hookWrapperMarker + "\n" +
		hookWrapperOriginalPathPrefix + originalPath + "\n" +
		hookWrapperOriginalKindPrefix + metadata.originalKind + "\n" +
		fmt.Sprintf("%s%04o\n", hookWrapperOriginalModePrefix, metadata.originalMode.Perm()) +
		hookWrapperOriginalHashPrefix + metadata.originalHash + "\n" +
		hookWrapperExecutablePrefix + executablePath + "\n" +
		"original_status=0\n" +
		shellQuote(originalPath) + " \"$@\" || original_status=$?\n" +
		"vcsentinel_status=0\n" +
		shellQuote(executablePath) + " check --staged || vcsentinel_status=$?\n" +
		"if [ \"$original_status\" -ne 0 ]; then\n" +
		"  exit \"$original_status\"\n" +
		"fi\n" +
		"exit \"$vcsentinel_status\"\n"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func generateHookScriptFor(exe string) string {
	executable := filepath.ToSlash(exe)
	return "#!/bin/sh\n" + hookDirectMarker + "\n" + hookDirectExecutablePrefix + executable + "\n" + hookCommandFor(exe)
}

func generateLegacyHookScriptFor(exe string) string {
	return "#!/bin/sh\n\"" + filepath.ToSlash(exe) + "\" check --staged\n"
}

func hookCommandFor(exe string) string {
	exeAbs := filepath.ToSlash(exe)
	switch runtime.GOOS {
	case "windows":
		// Git for Windows runs hooks with its sh.exe and accepts slash paths.
		return shellQuote(exeAbs) + " check --staged\n"
	default:
		return shellQuote(exeAbs) + " check --staged\n"
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
