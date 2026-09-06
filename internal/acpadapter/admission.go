package acpadapter

import (
	"fmt"
	"runtime"
)

// Enforcement declares which backend is expected to guarantee the
// restriction capabilities (FilesystemRead/Write, Network, ChildAgents)
// that an acpx-based run may demand. It implements capability admission
// constraint C6 (docs/design/acpx-capability-mapping.md): restrictions
// are only honest when a declared backend can actually enforce them on the
// current platform; otherwise the run must be grant-by-design.
const (
	// EnforcementNone is the default: no restriction backend is declared,
	// so the run is admitted as grant-by-design. Nothing is rejected, but
	// nothing is promised either.
	EnforcementNone = "none"
	// EnforcementClaudeSandbox means the project's Claude Code native
	// sandbox settings are expected to contain the run. Verified headless
	// on Linux/WSL2 through A1 follow-up probes; NOT supported on native
	// Windows, where declaring it fails construction.
	EnforcementClaudeSandbox = "claude-sandbox"
)

// validateEnforcement checks one enforcement declaration against the platform
// identified by goos. Construction-time errors here implement the roadmap
// rule "reject unsupported required capabilities before launch": a bad or
// unsatisfiable declaration must fail before any child process can start.
func validateEnforcement(value, goos string) error {
	switch value {
	case "", EnforcementNone:
		return nil
	case EnforcementClaudeSandbox:
		if goos == "windows" {
			return fmt.Errorf("acpadapter: enforcement %q is unsupported on native Windows: the Claude Code native sandbox has no verified containment there (Linux/macOS/WSL2 only); declare enforcement: none and treat the run as grant-by-design, or run under WSL2", EnforcementClaudeSandbox)
		}
		return nil
	default:
		return fmt.Errorf("acpadapter: unknown enforcement %q (known values: %q, %q)", value, EnforcementNone, EnforcementClaudeSandbox)
	}
}

// validateEnforcementOnHost validates against the running platform.
func validateEnforcementOnHost(value string) error {
	return validateEnforcement(value, runtime.GOOS)
}
