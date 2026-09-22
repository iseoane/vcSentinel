package main

// Per-command help texts and central -h/--help interception (ticket 15).
//
// Every dispatched command and subcommand answers -h/--help with a dedicated
// English help text on stdout, exit 0, before any validation or side effect
// runs. The interception lives here so the dispatcher in main.go stays
// additive: it calls handleHelp once before executing anything.
//
// This file owns the registry structure and the interception logic. The long
// English help texts live in help_texts.go; only short single-command entries
// stay inline below.

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// commandHelpTexts is the single source of per-command help: 'vcsentinel help
// <command>' and '<command> --help' resolve through the same map. Long texts
// reference their named constants from help_texts.go so the registry stays
// scannable.
var commandHelpTexts = map[string]string{
	"version": `Purpose: show the installed vcSentinel version.

Usage:
  vcsentinel version

Flags:
  none. '--version' and '-v' are equivalent to this command.

Example:
  vcsentinel version
`,
	"help": `Purpose: show the command list or detailed help for one command.

Usage:
  vcsentinel help [command]

Flags:
  none.

Example:
  vcsentinel help gate
`,
	"init": `Purpose: set up vcSentinel in a Git repository.

Init creates .vcsentinel/vcsentinel.yml, adds the managed volume guidance to
agent instruction files, installs the repository pre-commit check, and creates
the repository-local skill at .agents/skills/vcsentinel/SKILL.md. If you run it
below the repository root, it redirects to that root. Content outside
vcSentinel's managed sections is preserved, and an existing foreign or modified
repository-local skill is left untouched.

Usage:
  vcsentinel init

Flags:
  none.

Example:
  vcsentinel init
`,
	"uninit": `Purpose: remove vcSentinel's setup from this Git repository.

Uninit removes the managed guidance, project configuration, repository-local
skill at .agents/skills/vcsentinel/SKILL.md, and pre-commit check installed by
init. It removes the skill and hook only when their contents prove that
vcSentinel owns them. Foreign or modified instruction files, skills, and hooks
are preserved; unrelated repository files are never removed.

Usage:
  vcsentinel uninit

Flags:
  none.

Example:
  vcsentinel uninit
`,
	"lint": `Purpose: run the lint commands configured in vcsentinel.yml. The command
exits 1 when any configured command fails.

Usage:
  vcsentinel lint

Flags:
  none.

Example:
  vcsentinel lint
`,
	"rebase": `Purpose: use the configured upstream to rebase the current branch after
you confirm. If no upstream exists, vcSentinel falls back to local main or
master. The repository must have no pending changes.

Usage:
  vcsentinel rebase

Flags:
  none.

Example:
  vcsentinel rebase
`,
	"install": `Purpose: download and install the latest published vcSentinel release
for your user account.

This is a global installation. It installs the program and global settings but
does not initialize any repository. Run 'vcsentinel init' separately in each
repository that should use vcSentinel.

Usage:
  vcsentinel install

Flags:
  none.

Example:
  vcsentinel install
`,
	"upgrade": `Purpose: replace the globally installed vcSentinel program with the
latest published release.

Upgrade changes the user-level installation only. It does not change any
repository's project settings, skill, guidance, or hook.

Usage:
  vcsentinel upgrade

Flags:
  none.

Example:
  vcsentinel upgrade
`,
	"uninstall": `Purpose: remove the globally installed vcSentinel program and its user-level
settings.

Uninstall does not remove per-repository settings, the repository-local skill,
managed guidance, or hooks. Run 'vcsentinel uninit' in each repository if that
setup should also be removed; foreign or modified files are preserved.

Usage:
  vcsentinel uninstall

Flags:
  none.

Example:
  vcsentinel uninstall
`,

	"check":        checkHelp,
	"slice":        sliceHelp,
	"slice plan":   slicePlanHelp,
	"slice apply":  sliceApplyHelp,
	"review":       reviewHelp,
	"refute":       refuteHelp,
	"accept":       acceptHelp,
	"reopen":       reopenHelp,
	"gate":         gateHelp,
	"status":       statusHelp,
	"metrics":      metricsHelp,
	"doctor":       doctorHelp,
	"explain":      explainHelp,
	"consent-diff": consentDiffHelp,
	"pr":           prHelp,
	"pr create":    prCreateHelp,
	"pr review":    prReviewHelp,

	"runs": runsHelp,

	"runs start":   runsStartHelp,
	"runs status":  runsStatusHelp,
	"runs logs":    runsLogsHelp,
	"runs respond": runsRespondHelp,
	"runs abort":   runsAbortHelp,
	"runs retry":   runsRetryHelp,
	"runs recover": runsRecoverHelp,
	"runs verify":  runsVerifyHelp,
	"runs prune":   runsPruneHelp,
	"runs attach":  runsAttachHelp,
	"runs daemon":  runsDaemonHelp,
	"tui":          tuiHelp,
}

// isHelpFlag reports whether one argument requests help.
func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "--help"
}

// containsHelpFlag reports whether any argument requests help. Help wins over
// every other argument: no command consumes -h/--help as data.
func containsHelpFlag(args []string) bool {
	for _, arg := range args {
		if isHelpFlag(arg) {
			return true
		}
	}
	return false
}

// resolveCommandPath maps subcommand invocations ("slice plan", "pr review",
// "runs logs") to their dedicated help key and returns the argument list that
// belongs to that subcommand. Unknown first arguments keep the parent key, so
// e.g. 'vcsentinel pr --help' serves the pr text instead of falling into the
// publishing path.
func resolveCommandPath(subcommand string, args []string) (string, []string) {
	if len(args) > 0 {
		switch subcommand {
		case "slice":
			if args[0] == "plan" || args[0] == "apply" {
				return "slice " + args[0], args[1:]
			}
		case "pr":
			if args[0] == "create" || args[0] == "review" {
				return "pr " + args[0], args[1:]
			}
		case "runs":
			if _, ok := runsSubcommandFlags[args[0]]; ok {
				return "runs " + args[0], args[1:]
			}
			// 'daemon' takes no flags by contract, so it is absent from
			// runsSubcommandFlags; it still owns a dedicated help key one
			// level down, matching the single-level registration of every
			// other multi-word command ('slice plan', 'pr review').
			if args[0] == "daemon" {
				return "runs daemon", args[1:]
			}
		}
	}
	return subcommand, args
}

// handleHelp intercepts -h/--help before the command executes: it prints
// the command's dedicated help on stdout and reports whether it served the
// request, in which case the caller must stop with exit 0. It writes nothing
// to errOut on success — help stays silent on stderr. When the resolved
// command has no registered text it returns false so the normal dispatch (and
// its own error paths) take over unchanged.
func handleHelp(out, errOut io.Writer, subcommand string, args []string) bool {
	key, rest := resolveCommandPath(subcommand, args)
	if !containsHelpFlag(rest) {
		return false
	}
	text, ok := commandHelpTexts[key]
	if !ok {
		return false
	}
	fmt.Fprint(out, text)
	return true
}

// writeCommandHelp prints the dedicated help of one command key and
// reports whether the key exists.
func writeCommandHelp(w io.Writer, key string) bool {
	text, ok := commandHelpTexts[key]
	if !ok {
		return false
	}
	fmt.Fprint(w, text)
	return true
}

// handleHelpCommand implements 'vcsentinel help': without arguments it keeps
// printing the historical top-level list; with a topic it resolves the same
// texts used by '<topic> --help'. An unknown topic is an error (exit 1) so a
// typo never masquerades as documentation.
func handleHelpCommand(args []string) {
	if containsHelpFlag(args) {
		_ = writeCommandHelp(os.Stdout, "help")
		return
	}
	if len(args) == 0 {
		printHelp()
		return
	}
	topic := strings.Join(args, " ")
	if writeCommandHelp(os.Stdout, topic) {
		return
	}
	fmt.Fprintf(os.Stderr, "❌ Unknown help topic: %s. Run 'vcsentinel help' to see the available commands.\n", topic)
	os.Exit(1)
}
