package main

// Per-command help texts and central -h/--help interception (ticket 15).
//
// Every dispatched command and subcommand answers -h/--help with a dedicated
// English help text on stdout, exit 0, before any validation or side effect
// runs. The interception lives here so the dispatcher in main.go stays
// additive: it calls handleHelp once before executing anything.
//
// This file owns the registry structure and the interception logic. The long
// English help texts live in ayuda_textos.go; only trivial single-command
// entries stay inline below.

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// commandHelpTexts is the single source of per-command help: 'vcsentinel help
// <command>' and '<command> --help' resolve through the same map. Long texts
// reference their named constants from ayuda_textos.go so the registry stays
// scannable.
var commandHelpTexts = map[string]string{
	"version": `Purpose: print the installed vcSentinel version.

Usage:
  vcsentinel version

Flags:
  none. '--version' and '-v' are equivalent to this command.

Example:
  vcsentinel version
`,
	"help": `Purpose: show the top-level command list or the dedicated help of one command.

Usage:
  vcsentinel help [command]

Flags:
  none.

Example:
  vcsentinel help gate
`,
	"init": `Purpose: inject the volume rule into agent instruction files, create the project configuration (.vas_sentinel/vassentinel.yml), and install the repository pre-commit hook. Always runs at the worktree root (redirects automatically from a subdirectory).

Usage:
  vcsentinel init

Flags:
  none.

Example:
  vcsentinel init
`,
	"uninit": `Purpose: revert init for this repository: remove the injected volume rule, delete the project configuration, and uninstall the pre-commit hook (only if it is byte-for-byte the one vcSentinel installed).

Usage:
  vcsentinel uninit

Flags:
  none.

Example:
  vcsentinel uninit
`,
	"lint": `Purpose: run the configured lint_commands from vassentinel.yml; exits 1 if any command fails.

Usage:
  vcsentinel lint

Flags:
  none.

Example:
  vcsentinel lint
`,
	"rebase": `Purpose: fetch and rebase the current branch against its upstream after confirmation.

Usage:
  vcsentinel rebase

Flags:
  none.

Example:
  vcsentinel rebase
`,
	"install": `Purpose: download and install the latest published release from GitHub.

Usage:
  vcsentinel install

Flags:
  none.

Example:
  vcsentinel install
`,
	"upgrade": `Purpose: replace the installed binary with the latest published release from GitHub.

Usage:
  vcsentinel upgrade

Flags:
  none.

Example:
  vcsentinel upgrade
`,
	"uninstall": `Purpose: remove the installed binary and the global configuration.

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

	// 'vcsentinel runs' integrates the centralized runsUsage text instead of
	// duplicating it: subcommand matrix, exit codes, and docs pointer live
	// there already.
	"runs": runsUsage,

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
// e.g. 'vcsentinel pr --help' serves the pr text instead of falling into the gh
// passthrough.
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
