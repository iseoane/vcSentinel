package main

import (
	"fmt"
	"io"

	"github.com/ISeoane-Quental/vcSentinel/internal/consent"
)

func runConsentDiff(out io.Writer, path string, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(out, "❌ "+consentDiffUsage)
		return 1
	}
	switch args[0] {
	case "grant":
		status, err := consent.GrantExternalDiff(path)
		if err != nil {
			fmt.Fprintf(out, "❌ Could not grant consent: %v\n", err)
			return 1
		}
		printConsentStatus(out, status)
	case "revoke":
		if err := consent.RevokeExternalDiff(path); err != nil {
			fmt.Fprintf(out, "❌ Could not revoke consent: %v\n", err)
			return 1
		}
		status, err := consent.ExternalDiffStatus(path)
		if err != nil {
			fmt.Fprintf(out, "❌ Could not read the status: %v\n", err)
			return 1
		}
		printConsentStatus(out, status)
	case "status":
		status, err := consent.ExternalDiffStatus(path)
		if err != nil {
			fmt.Fprintf(out, "❌ Could not read the status: %v\n", err)
			return 1
		}
		printConsentStatus(out, status)
	default:
		fmt.Fprintf(out, "❌ Unknown action %q. Use grant, revoke, or status.\n", args[0])
		return 1
	}
	return 0
}

func printConsentStatus(out io.Writer, status consent.ExternalDiffState) {
	value := "revoked"
	if status.Granted {
		value = "granted"
	}
	fmt.Fprintf(out, "External diff consent: %s\nRepository: %s\nLocal user: %s\n", value, status.Repository, status.User)
	if status.Granted {
		fmt.Fprintf(out, "Granted at: %s\n", status.GrantedAt.Format("2006-01-02T15:04:05Z"))
	}
}
