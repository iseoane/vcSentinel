package main

// Unit coverage for `vcsentinel tui` (control-center slice 9): the daemon
// ownership decision matrix over injected fakes, the bounded readiness wait,
// the session-end shutdown contract (only an owned daemon is ever stopped),
// and the CLI surface contracts (no flags accepted, help documented).

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/daemon"
	"github.com/ISeoane-Quental/vcSentinel/internal/tui/control"
)

// recordingDaemonHost is the fake wire client handed to gates under test: it
// counts Shutdown and Close invocations so tests can pin exactly who stopped
// what, and can stage a failing graceful op.
type recordingDaemonHost struct {
	mu          sync.Mutex
	shutdowns   int
	closes      int
	shutdownErr error
}

func testDaemonOwner() daemon.Owner {
	return daemon.Owner{
		PID:              4321,
		StartedAt:        time.Unix(1700000000, 0).UTC(),
		Host:             "tui-test",
		ProtocolRevision: daemon.ProtocolRevision,
	}
}

func (h *recordingDaemonHost) Shutdown(_ context.Context, _ daemon.ShutdownRequest) (daemon.ShutdownResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.shutdowns++
	return daemon.ShutdownResult{}, h.shutdownErr
}

func (h *recordingDaemonHost) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closes++
	return nil
}

// TestTuiDaemonGateOwnershipMatrix drives every cell of the decision matrix:
// absent or unreadable records and undialable residues all spawn-and-own; a
// dialable record means a foreign daemon that is probed and immediately left
// alone; spawn failures and readiness timeouts fail infrastructure-style
// before the TUI could open half-owned.
func TestTuiDaemonGateOwnershipMatrix(t *testing.T) {
	tests := []struct {
		name           string
		recordLive     bool // endpoint record present and readable
		recordCorrupt  bool // record present but unreadable/corrupt
		dialFails      bool // recorded endpoint answers nothing (stale)
		spawnFails     bool
		ready          bool
		wantOwned      bool
		wantErr        string
		wantSpawns     int
		wantDials      int
		wantCloses     int
		wantReadyCalls int
		wantCleanups   int
	}{
		{
			name:           "absent record spawns an owned daemon",
			wantOwned:      true,
			ready:          true,
			wantSpawns:     1,
			wantReadyCalls: 1,
		},
		{
			name:           "unreadable record is reclaimable residue",
			recordCorrupt:  true,
			wantOwned:      true,
			ready:          true,
			wantSpawns:     1,
			wantReadyCalls: 1,
		},
		{
			name:           "recorded endpoint nobody answers is stale residue",
			recordLive:     true,
			dialFails:      true,
			wantOwned:      true,
			ready:          true,
			wantSpawns:     1,
			wantDials:      1,
			wantReadyCalls: 1,
		},
		{
			name:       "dialable record means a foreign daemon",
			recordLive: true,
			wantDials:  1,
			wantCloses: 1,
		},
		{
			name:       "spawn failure fails infrastructure without waiting",
			spawnFails: true,
			wantErr:    "could not start",
			wantSpawns: 1,
		},
		{
			name:           "readiness timeout fails infrastructure",
			ready:          false,
			wantErr:        "did not become reachable",
			wantSpawns:     1,
			wantReadyCalls: 1,
			wantCleanups:   1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := &recordingDaemonHost{}
			spawns, dials, readyCalls, cleanups := 0, 0, 0, 0
			expectedOwner := testDaemonOwner()
			gate := tuiDaemonGate{
				loadEndpoint: func() (daemon.Endpoint, error) {
					switch {
					case tt.recordLive:
						return daemon.Endpoint{Network: "unix", Address: "/sock"}, nil
					case tt.recordCorrupt:
						return daemon.Endpoint{}, errors.New("corrupt endpoint record")
					default:
						return daemon.Endpoint{}, os.ErrNotExist
					}
				},
				dial: func(daemon.Endpoint) (tuiDaemonHost, error) {
					dials++
					if tt.dialFails {
						return nil, errors.New("connection refused")
					}
					return host, nil
				},
				inspectOwner: func() (daemon.Owner, bool, error) {
					if spawns == 0 {
						return daemon.Owner{}, false, os.ErrNotExist
					}
					return expectedOwner, true, nil
				},
				spawn: func() (tuiDaemonProcess, error) {
					spawns++
					if tt.spawnFails {
						return tuiDaemonProcess{}, errors.New("exec failed")
					}
					return tuiDaemonProcess{
						pid: expectedOwner.PID,
						cleanup: func() error {
							cleanups++
							return nil
						},
					}, nil
				},
				waitReady: func(int) bool {
					readyCalls++
					return tt.ready
				},
			}
			owned, _, err := gate.resolveOwnership()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("resolveOwnership() error = %v, want it to contain %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("resolveOwnership() unexpected error: %v", err)
			}
			if owned != tt.wantOwned {
				t.Errorf("owned = %t, want %t", owned, tt.wantOwned)
			}
			if spawns != tt.wantSpawns {
				t.Errorf("spawns = %d, want %d", spawns, tt.wantSpawns)
			}
			if dials != tt.wantDials {
				t.Errorf("dials = %d, want %d", dials, tt.wantDials)
			}
			if host.closes != tt.wantCloses {
				t.Errorf("host closes = %d, want %d", host.closes, tt.wantCloses)
			}
			if readyCalls != tt.wantReadyCalls {
				t.Errorf("waitReady calls = %d, want %d", readyCalls, tt.wantReadyCalls)
			}
			if cleanups != tt.wantCleanups {
				t.Errorf("child cleanups = %d, want %d", cleanups, tt.wantCleanups)
			}
		})
	}
}

// TestWaitTuiDaemonReady pins the bounded poll: immediate success, success
// once the record appears, and timeout inside the budget with a bounded
// attempt count. All cases run on tiny wall-clock budgets — no fake clock.
func TestWaitTuiDaemonReady(t *testing.T) {
	t.Run("healthy loader succeeds on the first attempt", func(t *testing.T) {
		loads := 0
		ok := waitTuiDaemonReady(func() error {
			loads++
			return nil
		}, time.Second, time.Millisecond)
		if !ok {
			t.Fatal("wait reported timeout although the loader succeeded immediately")
		}
		if loads != 1 {
			t.Errorf("loads = %d, want exactly one", loads)
		}
	})
	t.Run("succeeds once the record appears", func(t *testing.T) {
		loads := 0
		ok := waitTuiDaemonReady(func() error {
			loads++
			if loads < 3 {
				return os.ErrNotExist
			}
			return nil
		}, 2*time.Second, time.Millisecond)
		if !ok {
			t.Fatal("wait timed out although the loader recovered")
		}
		if loads != 3 {
			t.Errorf("loads = %d, want 3", loads)
		}
	})
	t.Run("times out inside its budget with bounded attempts", func(t *testing.T) {
		loads := 0
		budget := 25 * time.Millisecond
		started := time.Now()
		ok := waitTuiDaemonReady(func() error {
			loads++
			return os.ErrNotExist
		}, budget, time.Millisecond)
		elapsed := time.Since(started)
		if ok {
			t.Fatal("wait reported readiness although the loader never succeeded")
		}
		// Generous CI bounds: prove the loop ended near its budget and never
		// spun unbounded, without depending on scheduler precision.
		if elapsed > 10*time.Second {
			t.Errorf("wait ran %v, far beyond its %v budget", elapsed, budget)
		}
		if loads == 0 || loads > 1000 {
			t.Errorf("attempts = %d, want a small bounded count", loads)
		}
	})
}

// TestStopOwnedTuiDaemon pins the session-end contract against fakes: a
// healthy owned daemon receives exactly one wire shutdown and one close with
// no output; a vanished record ends quietly (idempotent not-running); any
// failure prints exactly one ❌ line because the session already succeeded.
func TestStopOwnedTuiDaemon(t *testing.T) {
	t.Run("healthy owned daemon shuts down through the wire", func(t *testing.T) {
		host := &recordingDaemonHost{}
		dials := 0
		gate := tuiDaemonGate{
			inspectOwner: func() (daemon.Owner, bool, error) {
				return testDaemonOwner(), true, nil
			},
			loadEndpoint: func() (daemon.Endpoint, error) {
				return daemon.Endpoint{Network: "unix", Address: "/sock"}, nil
			},
			dial: func(daemon.Endpoint) (tuiDaemonHost, error) {
				dials++
				return host, nil
			},
		}
		var out bytes.Buffer
		stopOwnedTuiDaemon(&out, gate, testDaemonOwner())
		if host.shutdowns != 1 || host.closes != 1 {
			t.Fatalf("shutdowns = %d, closes = %d, want exactly one of each", host.shutdowns, host.closes)
		}
		if dials != 1 {
			t.Errorf("dials = %d, want 1", dials)
		}
		if out.Len() != 0 {
			t.Errorf("healthy stop printed %q", out.String())
		}
	})
	t.Run("missing record is quietly done", func(t *testing.T) {
		dials := 0
		gate := tuiDaemonGate{
			inspectOwner: func() (daemon.Owner, bool, error) {
				return daemon.Owner{}, false, os.ErrNotExist
			},
			loadEndpoint: func() (daemon.Endpoint, error) {
				return daemon.Endpoint{}, os.ErrNotExist
			},
			dial: func(daemon.Endpoint) (tuiDaemonHost, error) {
				dials++
				return nil, errors.New("must not be dialed")
			},
		}
		var out bytes.Buffer
		stopOwnedTuiDaemon(&out, gate, testDaemonOwner())
		if dials != 0 || out.Len() != 0 {
			t.Fatalf("absent endpoint dialed %d times and printed %q", dials, out.String())
		}
	})
	t.Run("unreachable daemon prints exactly one error line", func(t *testing.T) {
		gate := tuiDaemonGate{
			loadEndpoint: func() (daemon.Endpoint, error) {
				return daemon.Endpoint{Network: "unix", Address: "/sock"}, nil
			},
			dial: func(daemon.Endpoint) (tuiDaemonHost, error) {
				return nil, errors.New("connection refused")
			},
		}
		out := captureStopOutput(t, gate)
		if lines := strings.Count(out, "❌"); lines != 1 {
			t.Fatalf("printed %d ❌ lines, want exactly one:\n%s", lines, out)
		}
		if !strings.Contains(out, "Could not reach the owned daemon") {
			t.Errorf("error line does not explain the failure:\n%s", out)
		}
	})
	t.Run("failing graceful op prints exactly one error line", func(t *testing.T) {
		host := &recordingDaemonHost{shutdownErr: errors.New("drain refused")}
		gate := tuiDaemonGate{
			loadEndpoint: func() (daemon.Endpoint, error) {
				return daemon.Endpoint{Network: "unix", Address: "/sock"}, nil
			},
			dial: func(daemon.Endpoint) (tuiDaemonHost, error) { return host, nil },
		}
		out := captureStopOutput(t, gate)
		if lines := strings.Count(out, "❌"); lines != 1 {
			t.Fatalf("printed %d ❌ lines, want exactly one:\n%s", lines, out)
		}
	})
}

// TestTuiDaemonOwnershipFailsClosed proves that missing or changed owner
// identity can never authorize a shutdown of the endpoint currently on disk.
func TestTuiDaemonOwnershipFailsClosed(t *testing.T) {
	t.Run("startup without an inspectable owner is rejected", func(t *testing.T) {
		gate := tuiDaemonGate{
			loadEndpoint: func() (daemon.Endpoint, error) { return daemon.Endpoint{}, os.ErrNotExist },
			spawn:        func() (tuiDaemonProcess, error) { return tuiDaemonProcess{pid: 4321}, nil },
			waitReady:    func(int) bool { return true },
			inspectOwner: func() (daemon.Owner, bool, error) {
				return daemon.Owner{}, false, errors.New("owner claim unreadable")
			},
		}
		owned, owner, err := gate.resolveOwnership()
		if err == nil || !strings.Contains(err.Error(), "verify ownership") {
			t.Fatalf("resolveOwnership() error = %v, want an ownership verification error", err)
		}
		if owned || validTuiOwner(owner) {
			t.Fatalf("resolveOwnership() = owned=%v owner=%+v, want no shutdown authority", owned, owner)
		}
	})

	t.Run("replacement owner is never shut down", func(t *testing.T) {
		host := &recordingDaemonHost{}
		expected := testDaemonOwner()
		replacement := expected
		replacement.StartedAt = replacement.StartedAt.Add(time.Second)
		loads := 0
		gate := tuiDaemonGate{
			inspectOwner: func() (daemon.Owner, bool, error) {
				return replacement, true, nil
			},
			loadEndpoint: func() (daemon.Endpoint, error) {
				loads++
				return daemon.Endpoint{Network: "unix", Address: "/sock"}, nil
			},
			dial: func(daemon.Endpoint) (tuiDaemonHost, error) { return host, nil },
		}
		var out bytes.Buffer
		stopOwnedTuiDaemon(&out, gate, expected)
		if host.shutdowns != 0 || loads != 0 {
			t.Fatalf("replacement stop touched host: shutdowns=%d endpoint loads=%d", host.shutdowns, loads)
		}
	})

	t.Run("endpoint from a replacement generation is never dialed", func(t *testing.T) {
		host := &recordingDaemonHost{}
		expected := testDaemonOwner()
		replacement := expected
		replacement.StartedAt = replacement.StartedAt.Add(time.Second)
		loads := 0
		gate := tuiDaemonGate{
			inspectOwner: func() (daemon.Owner, bool, error) {
				return expected, true, nil
			},
			loadEndpointRecord: func() (daemon.Endpoint, daemon.Owner, error) {
				loads++
				return daemon.Endpoint{Network: "unix", Address: "/replacement"}, replacement, nil
			},
			dial: func(daemon.Endpoint) (tuiDaemonHost, error) { return host, nil },
		}
		var out bytes.Buffer
		stopOwnedTuiDaemon(&out, gate, expected)
		if host.shutdowns != 0 || loads != 1 {
			t.Fatalf("replacement endpoint was touched: shutdowns=%d endpoint loads=%d", host.shutdowns, loads)
		}
	})

	t.Run("claim from another child is not adopted and is cleaned up", func(t *testing.T) {
		expected := testDaemonOwner()
		cleanupCalls := 0
		gate := tuiDaemonGate{
			loadEndpoint: func() (daemon.Endpoint, error) {
				return daemon.Endpoint{}, os.ErrNotExist
			},
			spawn: func() (tuiDaemonProcess, error) {
				return tuiDaemonProcess{
					pid: expected.PID + 1,
					cleanup: func() error {
						cleanupCalls++
						return nil
					},
				}, nil
			},
			waitReady: func(int) bool { return true },
			inspectOwner: func() (daemon.Owner, bool, error) {
				return expected, true, nil
			},
		}
		owned, _, err := gate.resolveOwnership()
		if err == nil || !strings.Contains(err.Error(), "spawned child pid") {
			t.Fatalf("resolveOwnership() error = %v, want child identity mismatch", err)
		}
		if owned || cleanupCalls != 1 {
			t.Fatalf("resolveOwnership() = owned=%v cleanupCalls=%d, want false and one cleanup", owned, cleanupCalls)
		}
	})

	t.Run("endpoint from another generation is not adopted after spawn", func(t *testing.T) {
		expected := testDaemonOwner()
		replacement := expected
		replacement.StartedAt = replacement.StartedAt.Add(time.Second)
		spawned := false
		cleanupCalls := 0
		gate := tuiDaemonGate{
			loadEndpointRecord: func() (daemon.Endpoint, daemon.Owner, error) {
				if !spawned {
					return daemon.Endpoint{}, daemon.Owner{}, os.ErrNotExist
				}
				return daemon.Endpoint{Network: "unix", Address: "/replacement"}, replacement, nil
			},
			spawn: func() (tuiDaemonProcess, error) {
				spawned = true
				return tuiDaemonProcess{
					pid: expected.PID,
					cleanup: func() error {
						cleanupCalls++
						return nil
					},
				}, nil
			},
			waitReady: func(int) bool { return true },
			inspectOwner: func() (daemon.Owner, bool, error) {
				return expected, true, nil
			},
		}
		owned, _, err := gate.resolveOwnership()
		if err == nil || !strings.Contains(err.Error(), "endpoint owner") {
			t.Fatalf("resolveOwnership() error = %v, want endpoint-generation mismatch", err)
		}
		if owned || cleanupCalls != 1 {
			t.Fatalf("resolveOwnership() = owned=%v cleanupCalls=%d, want false and one cleanup", owned, cleanupCalls)
		}
	})
}

// captureStopOutput runs one stop against a buffer and returns the text.
func captureStopOutput(t *testing.T, gate tuiDaemonGate) string {
	t.Helper()
	gate.inspectOwner = func() (daemon.Owner, bool, error) {
		return testDaemonOwner(), true, nil
	}
	var out bytes.Buffer
	stopOwnedTuiDaemon(&out, gate, testDaemonOwner())
	return out.String()
}

// stubStartControlCenter replaces the program seam for one test and restores
// it through t.Cleanup. It records how many times the program started and
// captures the constructed model.
func stubStartControlCenter(t *testing.T, runErr error, captured *control.Model) *int {
	t.Helper()
	starts := 0
	original := startControlCenter
	startControlCenter = func(model control.Model) error {
		starts++
		*captured = model
		return runErr
	}
	t.Cleanup(func() { startControlCenter = original })
	return &starts
}

// TestExecuteTuiSessionShutdownContract drives full sessions over injected
// gates: a foreign daemon survives untouched, an owned daemon is stopped
// exactly once on exit, and a failed session still stops its own daemon
// before reporting infrastructure failure.
func TestExecuteTuiSessionShutdownContract(t *testing.T) {
	// An absent registry file opens as an empty registry: Collect succeeds.
	registryPath := filepath.Join(t.TempDir(), "repositories.json")

	t.Run("a foreign daemon survives the session", func(t *testing.T) {
		host := &recordingDaemonHost{}
		gate := tuiDaemonGate{
			loadEndpoint: func() (daemon.Endpoint, error) {
				return daemon.Endpoint{Network: "unix", Address: "/sock"}, nil
			},
			dial: func(daemon.Endpoint) (tuiDaemonHost, error) { return host, nil },
		}
		var captured control.Model
		starts := stubStartControlCenter(t, nil, &captured)
		var out bytes.Buffer
		if code := executeTuiSession(&out, registryPath, t.TempDir(), gate); code != runExitSuccess {
			t.Fatalf("exit = %d, want success:\n%s", code, out.String())
		}
		if host.shutdowns != 0 {
			t.Errorf("the foreign daemon received %d shutdowns, want none", host.shutdowns)
		}
		if host.closes != 1 {
			t.Errorf("ownership probe closes = %d, want exactly the probe close", host.closes)
		}
		if *starts != 1 {
			t.Errorf("program started %d times, want exactly once", *starts)
		}
		if captured.Init() == nil {
			t.Error("the session model is not live: the refresh loop was not wired")
		}
	})
	t.Run("an owned daemon stops exactly once on exit", func(t *testing.T) {
		spawned := false
		host := &recordingDaemonHost{}
		expectedOwner := testDaemonOwner()
		gate := tuiDaemonGate{
			loadEndpoint: func() (daemon.Endpoint, error) {
				if !spawned {
					return daemon.Endpoint{}, os.ErrNotExist
				}
				return daemon.Endpoint{Network: "unix", Address: "/sock"}, nil
			},
			dial: func(daemon.Endpoint) (tuiDaemonHost, error) { return host, nil },
			inspectOwner: func() (daemon.Owner, bool, error) {
				if !spawned {
					return daemon.Owner{}, false, os.ErrNotExist
				}
				return expectedOwner, true, nil
			},
			spawn:     func() (tuiDaemonProcess, error) { spawned = true; return tuiDaemonProcess{pid: expectedOwner.PID}, nil },
			waitReady: func(int) bool { return true },
		}
		var captured control.Model
		starts := stubStartControlCenter(t, nil, &captured)
		var out bytes.Buffer
		if code := executeTuiSession(&out, registryPath, t.TempDir(), gate); code != runExitSuccess {
			t.Fatalf("exit = %d, want success:\n%s", code, out.String())
		}
		if host.shutdowns != 1 || host.closes != 1 {
			t.Errorf("shutdowns = %d, closes = %d, want one of each", host.shutdowns, host.closes)
		}
		if *starts != 1 || captured.Init() == nil {
			t.Errorf("starts = %d, live model = %t, want one started live session", *starts, captured.Init() != nil)
		}
	})
	t.Run("a failed session still stops its own daemon", func(t *testing.T) {
		spawned := false
		host := &recordingDaemonHost{}
		expectedOwner := testDaemonOwner()
		gate := tuiDaemonGate{
			loadEndpoint: func() (daemon.Endpoint, error) {
				if !spawned {
					return daemon.Endpoint{}, os.ErrNotExist
				}
				return daemon.Endpoint{Network: "unix", Address: "/sock"}, nil
			},
			dial: func(daemon.Endpoint) (tuiDaemonHost, error) { return host, nil },
			inspectOwner: func() (daemon.Owner, bool, error) {
				if !spawned {
					return daemon.Owner{}, false, os.ErrNotExist
				}
				return expectedOwner, true, nil
			},
			spawn:     func() (tuiDaemonProcess, error) { spawned = true; return tuiDaemonProcess{pid: expectedOwner.PID}, nil },
			waitReady: func(int) bool { return true },
		}
		var captured control.Model
		stubStartControlCenter(t, errors.New("terminal exploded"), &captured)
		var out bytes.Buffer
		if code := executeTuiSession(&out, registryPath, t.TempDir(), gate); code != runExitInfrastructure {
			t.Fatalf("exit = %d, want infrastructure:\n%s", code, out.String())
		}
		if host.shutdowns != 1 {
			t.Errorf("shutdowns = %d, want the owned daemon stopped despite the failed session", host.shutdowns)
		}
	})
}

// TestExecuteTuiSessionRegistryFailureExitsInfrastructure pins the ordering
// guarantee: a registry-open failure exits infrastructure BEFORE the gate is
// ever contacted and before any program could start — the TUI never opens
// half-owned.
func TestExecuteTuiSessionRegistryFailureExitsInfrastructure(t *testing.T) {
	gate := tuiDaemonGate{
		loadEndpoint: func() (daemon.Endpoint, error) {
			t.Error("the ownership gate was contacted although the registry failed to open")
			return daemon.Endpoint{}, nil
		},
		spawn: func() (tuiDaemonProcess, error) {
			t.Error("a daemon was spawned although the registry failed to open")
			return tuiDaemonProcess{}, nil
		},
	}
	var captured control.Model
	starts := stubStartControlCenter(t, nil, &captured)
	var out bytes.Buffer
	// A directory is never a valid registry file.
	if code := executeTuiSession(&out, t.TempDir(), t.TempDir(), gate); code != runExitInfrastructure {
		t.Fatalf("exit = %d, want infrastructure:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "❌") {
		t.Errorf("registry failure printed nothing actionable:\n%s", out.String())
	}
	// Any gate or program contact above already failed the test through the
	// closures' t.Error calls; this pins the program seam explicitly.
	if *starts != 0 {
		t.Errorf("program started %d times, want none", *starts)
	}
}

// TestTuiRejectsAnyArgumentOrFlag mirrors the no-argument dispatcher
// contract: `vcsentinel tui` accepts nothing beyond its name, and the
// rejection names both the offending argument and the way out.
func TestTuiRejectsAnyArgumentOrFlag(t *testing.T) {
	if message := validateArguments("tui", nil); message != "" {
		t.Fatalf("bare 'vcsentinel tui' was rejected: %s", message)
	}
	for _, extras := range [][]string{{"--json"}, {"--staged"}, {"whatever"}} {
		message := validateArguments("tui", extras)
		if message == "" {
			t.Fatalf("'vcsentinel tui %v' was accepted silently", extras)
		}
		if !strings.Contains(message, extras[0]) {
			t.Errorf("rejection of %v does not name the argument: %s", extras, message)
		}
		if !strings.Contains(message, "tui") {
			t.Errorf("rejection of %v does not name the subcommand: %s", extras, message)
		}
		if !strings.Contains(message, "vcsentinel help") {
			t.Errorf("rejection of %v does not point to help: %s", extras, message)
		}
	}
}

// TestHelpDocumentsTuiCommand pins both help surfaces: the top-level list
// carries the tui row with its description, and the dedicated text answers
// both 'vcsentinel help tui' and 'vcsentinel tui --help'.
func TestHelpDocumentsTuiCommand(t *testing.T) {
	help := buildHelp()
	if !strings.Contains(help, "  tui ") {
		t.Errorf("the top-level help does not list the tui command:\n%s", help)
	}
	if !strings.Contains(help, "Opens the interactive dashboard") {
		t.Error("the tui help row lacks its approved description")
	}

	var dedicated bytes.Buffer
	if !writeCommandHelp(&dedicated, "tui") {
		t.Fatal("'vcsentinel help tui' resolves to nothing: no dedicated text registered")
	}
	tuiText := dedicated.String()
	if !strings.Contains(tuiText, "Purpose:") || !strings.Contains(tuiText, "vcsentinel tui") {
		t.Errorf("dedicated tui help is malformed:\n%s", tuiText)
	}
	if !strings.Contains(tuiText, "interactive dashboard") || strings.Contains(tuiText, "control center") {
		t.Errorf("dedicated tui help should use plain dashboard language:\n%s", tuiText)
	}

	var served, errs bytes.Buffer
	if !handleHelp(&served, io.Discard, "tui", []string{"--help"}) {
		t.Fatal("'vcsentinel tui --help' did not serve the dedicated text")
	}
	if !strings.Contains(served.String(), "Purpose:") {
		t.Errorf("--help interception printed unexpected content:\n%s", served.String())
	}
	if errs.Len() != 0 {
		t.Errorf("help must stay silent on stderr, got %q", errs.String())
	}
}
