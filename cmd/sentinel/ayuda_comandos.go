package main

// Per-command help texts and central -h/--help interception (ticket 15).
//
// Every dispatched command and subcommand answers -h/--help with a dedicated
// English help text on stdout, exit 0, before any validation or side effect
// runs. The interception lives here so the dispatcher in main.go stays
// additive: it calls gestionarAyuda once before executing anything.
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

// ayudaComandos is the single source of per-command help: 'sentinel help
// <command>' and '<command> --help' resolve through the same map. Long texts
// reference their named constants from ayuda_textos.go so the registry stays
// scannable.
var ayudaComandos = map[string]string{
	"version": `Purpose: print the installed VAS Sentinel version.

Usage:
  sentinel version

Flags:
  none. '--version' and '-v' are equivalent to this command.

Example:
  sentinel version
`,
	"help": `Purpose: show the top-level command list or the dedicated help of one command.

Usage:
  sentinel help [command]

Flags:
  none.

Example:
  sentinel help gate
`,
	"init": `Purpose: inject the volume rule into agent instruction files, create the project configuration (.vas_sentinel/vassentinel.yml), and install the repository pre-commit hook. Always runs at the worktree root (redirects automatically from a subdirectory).

Usage:
  sentinel init

Flags:
  none.

Example:
  sentinel init
`,
	"uninit": `Purpose: revert init for this repository: remove the injected volume rule, delete the project configuration, and uninstall the pre-commit hook (only if it is byte-for-byte the one VAS Sentinel installed).

Usage:
  sentinel uninit

Flags:
  none.

Example:
  sentinel uninit
`,
	"lint": `Purpose: run the configured lint_commands from vassentinel.yml; exits 1 if any command fails.

Usage:
  sentinel lint

Flags:
  none.

Example:
  sentinel lint
`,
	"rebase": `Purpose: fetch and rebase the current branch against its upstream after confirmation.

Usage:
  sentinel rebase

Flags:
  none.

Example:
  sentinel rebase
`,
	"install": `Purpose: download and install the latest published release from GitHub.

Usage:
  sentinel install

Flags:
  none.

Example:
  sentinel install
`,
	"upgrade": `Purpose: replace the installed binary with the latest published release from GitHub.

Usage:
  sentinel upgrade

Flags:
  none.

Example:
  sentinel upgrade
`,
	"uninstall": `Purpose: remove the installed binary and the global configuration.

Usage:
  sentinel uninstall

Flags:
  none.

Example:
  sentinel uninstall
`,

	"check":               textoAyudaCheck,
	"slice":               textoAyudaSlice,
	"slice plan":          textoAyudaSlicePlan,
	"slice apply":         textoAyudaSliceApply,
	"review":              textoAyudaReview,
	"gate":                textoAyudaGate,
	"status":              textoAyudaStatus,
	"explain":             textoAyudaExplain,
	"consentimiento-diff": textoAyudaConsentimientoDif,
	"pr":                  textoAyudaPr,
	"pr create":           textoAyudaPrCreate,
	"pr review":           textoAyudaPrReview,

	// 'sentinel runs' integrates the centralized runsUsage text instead of
	// duplicating it: subcommand matrix, exit codes, and docs pointer live
	// there already.
	"runs": runsUsage,

	"runs start":   textoAyudaRunsStart,
	"runs status":  textoAyudaRunsStatus,
	"runs logs":    textoAyudaRunsLogs,
	"runs respond": textoAyudaRunsRespond,
	"runs abort":   textoAyudaRunsAbort,
	"runs retry":   textoAyudaRunsRetry,
	"runs recover": textoAyudaRunsRecover,
	"runs verify":  textoAyudaRunsVerify,
	"runs prune":   textoAyudaRunsPrune,
	"runs attach":  textoAyudaRunsAttach,
	"runs daemon":  textoAyudaRunsDaemon,
}

// esFlagAyuda reports whether one argument requests help.
func esFlagAyuda(arg string) bool {
	return arg == "-h" || arg == "--help"
}

// contieneFlagAyuda reports whether any argument requests help. Help wins over
// every other argument: no command consumes -h/--help as data.
func contieneFlagAyuda(args []string) bool {
	for _, arg := range args {
		if esFlagAyuda(arg) {
			return true
		}
	}
	return false
}

// resolverRutaComando maps subcommand invocations ("slice plan", "pr review",
// "runs logs") to their dedicated help key and returns the argument list that
// belongs to that subcommand. Unknown first arguments keep the parent key, so
// e.g. 'sentinel pr --help' serves the pr text instead of falling into the gh
// passthrough.
func resolverRutaComando(subcomando string, args []string) (string, []string) {
	if len(args) > 0 {
		switch subcomando {
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
	return subcomando, args
}

// gestionarAyuda intercepts -h/--help before the command executes: it prints
// the command's dedicated help on stdout and reports whether it served the
// request, in which case the caller must stop with exit 0. It writes nothing
// to errores on success — help stays silent on stderr. When the resolved
// command has no registered text it returns false so the normal dispatch (and
// its own error paths) take over unchanged.
func gestionarAyuda(salida, errores io.Writer, subcomando string, args []string) bool {
	clave, resto := resolverRutaComando(subcomando, args)
	if !contieneFlagAyuda(resto) {
		return false
	}
	texto, ok := ayudaComandos[clave]
	if !ok {
		return false
	}
	fmt.Fprint(salida, texto)
	return true
}

// escribirAyudaComando prints the dedicated help of one command key and
// reports whether the key exists.
func escribirAyudaComando(w io.Writer, clave string) bool {
	texto, ok := ayudaComandos[clave]
	if !ok {
		return false
	}
	fmt.Fprint(w, texto)
	return true
}

// manejarComandoHelp implements 'sentinel help': without arguments it keeps
// printing the historical top-level list; with a topic it resolves the same
// texts used by '<topic> --help'. An unknown topic is an error (exit 1) so a
// typo never masquerades as documentation.
func manejarComandoHelp(args []string) {
	if contieneFlagAyuda(args) {
		_ = escribirAyudaComando(os.Stdout, "help")
		return
	}
	if len(args) == 0 {
		imprimirAyuda()
		return
	}
	tema := strings.Join(args, " ")
	if escribirAyudaComando(os.Stdout, tema) {
		return
	}
	fmt.Fprintf(os.Stderr, "❌ Unknown help topic: %s. Run 'sentinel help' to see the available commands.\n", tema)
	os.Exit(1)
}
