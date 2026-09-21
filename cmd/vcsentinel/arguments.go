package main

import (
	"fmt"
	"strings"
)

// subcommandsWithoutArguments lists the commands that accept no argument at
// all. Until T0.5 the dispatcher ignored os.Args[2:] and `vcsentinel check
// --whatever` exited 0 without complaining (H1/B6): a mistyped flag looked
// like it worked.
//
// Deliberate accepted risk: this breaks scripts that currently pass garbage
// without noticing. Noticing is exactly the desired behavior.
var subcommandsWithoutArguments = map[string]bool{
	"init":      true,
	"uninit":    true,
	"lint":      true,
	"rebase":    true,
	"install":   true,
	"upgrade":   true,
	"uninstall": true,
	"tui":       true,
}

// sliceSubcommands are the non-interactive slice paths, which do have their
// own flags and validate them themselves.
var sliceSubcommands = map[string]bool{"plan": true, "apply": true}

type flagsCheck struct {
	jsonOut bool
	staged  bool
}

func parseCheckFlags(args []string) (flagsCheck, error) {
	flags := flagsCheck{}
	for _, arg := range args {
		switch arg {
		case "--json":
			flags.jsonOut = true
		case "--staged":
			flags.staged = true
		default:
			return flags, fmt.Errorf("vcsentinel check accepts --json and --staged, received %q", arg)
		}
	}
	return flags, nil
}

// validateArguments returns the error message when the subcommand received
// arguments it does not accept, or "" when they are valid. Subcommands with
// their own parser (review, status, pr) are not touched here: they validate
// their own flags.
func validateArguments(subcommand string, extras []string) string {
	if len(extras) == 0 {
		return ""
	}
	if subcommand == "check" {
		if _, err := parseCheckFlags(extras); err == nil {
			return ""
		}
		return fmt.Sprintf(
			"❌ 'vcsentinel check' accepts '--json' and '--staged' and received: %s. Run 'vcsentinel help' to see the correct usage.",
			strings.Join(extras, " "))
	}
	if subcommand == "metrics" {
		if _, err := parseMetricsArgs(extras); err == nil {
			return ""
		}
		return fmt.Sprintf(
			"❌ 'vcsentinel metrics' accepts '--json' and received: %s. Run 'vcsentinel help metrics' to see the correct usage.",
			strings.Join(extras, " "))
	}
	if subcommand == "doctor" {
		for _, arg := range extras {
			if arg != "--check-updates" {
				return fmt.Sprintf(
					"❌ 'vcsentinel doctor' accepts '--check-updates' and received: %s. Run 'vcsentinel help doctor' to see the correct usage.",
					strings.Join(extras, " "))
			}
		}
		return ""
	}
	if subcommand == "slice" {
		if sliceSubcommands[extras[0]] {
			return ""
		}
		return fmt.Sprintf(
			"❌ 'vcsentinel slice' does not accept the argument %q. Use 'slice plan' or 'slice apply', or run 'vcsentinel help'.",
			extras[0])
	}
	if !subcommandsWithoutArguments[subcommand] {
		return ""
	}
	return fmt.Sprintf(
		"❌ 'vcsentinel %s' accepts no arguments and received: %s. Run 'vcsentinel help' to see the correct usage.",
		subcommand, strings.Join(extras, " "))
}
