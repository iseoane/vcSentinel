package acpadapter

import (
	"strings"
	"testing"
)

// TestValidateEnforcement is the C6 admission table: an unknown value fails
// explicitly, claude-sandbox is admitted only off native Windows (WSL2
// reports linux and is covered by it), and empty/none admit as
// grant-by-design.
func TestValidateEnforcement(t *testing.T) {
	cases := []struct {
		name        string
		enforcement string
		goos        string
		wantErr     bool
		wantWindows bool // error must name the platform limitation
	}{
		{name: "empty admits grant-by-design", enforcement: "", goos: "linux"},
		{name: "none admits grant-by-design", enforcement: EnforcementNone, goos: "linux"},
		{name: "claude-sandbox admitted on linux", enforcement: EnforcementClaudeSandbox, goos: "linux"},
		{name: "claude-sandbox admitted on darwin", enforcement: EnforcementClaudeSandbox, goos: "darwin"},
		{name: "claude-sandbox admitted under WSL2 (linux goos)", enforcement: EnforcementClaudeSandbox, goos: "linux"},
		{
			name:        "claude-sandbox rejected on native windows",
			enforcement: EnforcementClaudeSandbox,
			goos:        "windows",
			wantErr:     true, wantWindows: true,
		},
		{name: "unknown value rejected", enforcement: "docker-nsjail", goos: "linux", wantErr: true},
		{name: "unknown value rejected on windows too", enforcement: "sandbox-exec", goos: "windows", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateEnforcement(tc.enforcement, tc.goos)
			if tc.wantErr && err == nil {
				t.Fatalf("enforcement %q on %s: want explicit admission error, got nil", tc.enforcement, tc.goos)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("enforcement %q on %s: unexpected error: %v", tc.enforcement, tc.goos, err)
			}
			if tc.wantWindows && !strings.Contains(err.Error(), "native Windows") {
				t.Errorf("error %q must name the platform limitation (native Windows)", err)
			}
		})
	}
}

// TestNewAcpxAdmissionFailFast pins that admission problems are CONSTRUCTION
// errors: NewAcpx returns before any launcher state exists, so nothing can be
// launched with an unsatisfiable declaration.
func TestNewAcpxAdmissionFailFast(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{name: "missing agent token", cfg: Config{Enforcement: EnforcementNone}},
		{name: "unknown enforcement", cfg: Config{Agent: "claude", Enforcement: "seatbelt"}},
		{name: "sandbox pairing mismatch", cfg: Config{Agent: "opencode", Enforcement: EnforcementClaudeSandbox}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, err := NewAcpx(tc.cfg)
			if err == nil || a != nil {
				t.Fatalf("NewAcpx(%+v) = %T, %v; want a construction error", tc.cfg, a, err)
			}
		})
	}
}

// TestNewAcpxAcceptsValidDeclarations mirrors the happy side of the table.
func TestNewAcpxAcceptsValidDeclarations(t *testing.T) {
	for _, enforcement := range []string{"", EnforcementNone} {
		a, err := NewAcpx(Config{Agent: "opencode", Enforcement: enforcement})
		if err != nil || a == nil {
			t.Fatalf("NewAcpx enforcement=%q: unexpected construction failure: %v", enforcement, err)
		}
	}
}
