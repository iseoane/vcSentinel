package daemon

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestDaemonHelperProcess is never executed as a test by the parent suite:
// it is the body of the re-executed test binary used as a child process by
// the lifecycle tests. Without VCSENTINEL_DAEMON_HELPER=1 in the environment it
// skips immediately, which makes it a deterministic short-lived child; with
// the variable set it blocks forever until the parent kills it, which makes
// it a deterministic long-lived child. Both roles run on every platform Go
// supports, so no shell builtins like `sleep` are needed.
func TestDaemonHelperProcess(t *testing.T) {
	if os.Getenv("VCSENTINEL_DAEMON_HELPER") != "1" {
		t.Skip("daemon helper process scaffold")
	}
	<-make(chan struct{}) // Blocked until the parent terminates us.
}

// spawnShortLivedChild starts the test binary as a child that exits
// immediately (the helper skips) and waits for its exit, returning a pid
// proven dead through the same liveness seam used in production.
func spawnShortLivedChild(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDaemonHelperProcess$")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start short-lived child: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("wait short-lived child: %v", err)
		}
	}
	if pidAlive(pid) {
		t.Fatalf("child pid %d still reported alive after Wait", pid)
	}
	return pid
}

// spawnLongLivedChild starts the test binary as a blocked helper child and
// registers kill+wait cleanup.
func spawnLongLivedChild(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestDaemonHelperProcess$")
	cmd.Env = append(os.Environ(), "VCSENTINEL_DAEMON_HELPER=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start long-lived child: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

// writeClaimFile crafts a claim file with an arbitrary owner pid, bypassing
// Claim, to arrange live/stale scenarios deterministically.
func writeClaimFile(t *testing.T, gitCommonDir string, pid int) Owner {
	t.Helper()
	if err := os.MkdirAll(daemonDir(gitCommonDir), 0700); err != nil {
		t.Fatalf("create daemon dir: %v", err)
	}
	owner := Owner{
		PID:              pid,
		StartedAt:        time.Now().UTC(),
		Host:             hostName(),
		ProtocolRevision: ProtocolRevision,
	}
	data, err := json.MarshalIndent(owner, "", "  ")
	if err != nil {
		t.Fatalf("marshal crafted claim: %v", err)
	}
	if err := os.WriteFile(claimPath(gitCommonDir), data, 0600); err != nil {
		t.Fatalf("write crafted claim: %v", err)
	}
	return owner
}

func TestClaimIsExclusiveAcrossParallelRacers(t *testing.T) {
	gitCommonDir := t.TempDir()
	const racers = 16

	start := make(chan struct{})
	results := make(chan error, racers)
	var wg sync.WaitGroup
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // Barrier: nobody calls Claim before everyone is ready.
			_, err := Claim(gitCommonDir)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	successes, refused := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrDaemonOwned):
			refused++
		default:
			t.Fatalf("unexpected error class from racer: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("exactly one Claim must succeed, got %d successes", successes)
	}
	if refused != racers-1 {
		t.Fatalf("expected %d ErrDaemonOwned refusals, got %d", racers-1, refused)
	}

	// The surviving claim must describe this process.
	owner, alive, err := InspectOwner(gitCommonDir)
	if err != nil {
		t.Fatalf("inspect after race: %v", err)
	}
	if !alive || owner.PID != os.Getpid() {
		t.Fatalf("surviving claim = pid %d alive %t, want pid %d alive true", owner.PID, alive, os.Getpid())
	}
}

func TestClaimReclaimsStaleOwnerWithDeadPid(t *testing.T) {
	gitCommonDir := t.TempDir()
	deadPID := spawnShortLivedChild(t)
	stale := writeClaimFile(t, gitCommonDir, deadPID)

	owner, err := Claim(gitCommonDir)
	if err != nil {
		t.Fatalf("claim over stale owner: %v", err)
	}
	if owner.PID != os.Getpid() {
		t.Fatalf("claimed pid %d, want own pid %d", owner.PID, os.Getpid())
	}
	if owner.ProtocolRevision != ProtocolRevision {
		t.Fatalf("claimed protocol revision %d, want %d", owner.ProtocolRevision, ProtocolRevision)
	}

	current, alive, err := InspectOwner(gitCommonDir)
	if err != nil || !alive || current.PID != os.Getpid() {
		t.Fatalf("after reclaim: inspect = (pid %d, alive %t, err %v), want own live pid", current.PID, alive, err)
	}
	if stale.PID != deadPID {
		t.Fatalf("crafted stale claim mutated: pid %d, want %d", stale.PID, deadPID)
	}
}

func TestClaimRefusesLiveOwner(t *testing.T) {
	gitCommonDir := t.TempDir()
	child := spawnLongLivedChild(t)
	writeClaimFile(t, gitCommonDir, child.Process.Pid)

	owner, err := Claim(gitCommonDir)
	if !errors.Is(err, ErrDaemonOwned) {
		t.Fatalf("claim over live owner error = %v, want ErrDaemonOwned", err)
	}
	var owned *OwnedError
	if !errors.As(err, &owned) {
		t.Fatalf("error %T does not carry *OwnedError: %v", err, err)
	}
	if owned.Owner.PID != child.Process.Pid {
		t.Fatalf("OwnedError carries pid %d, want owning child pid %d", owned.Owner.PID, child.Process.Pid)
	}
	if owner != (Owner{}) {
		t.Fatalf("refused Claim must return zero Owner, got %+v", owner)
	}

	// The refusal must not clobber or rewrite the existing claim.
	current, alive, err := InspectOwner(gitCommonDir)
	if err != nil || !alive || current.PID != child.Process.Pid {
		t.Fatalf("claim file after refusal = (pid %d, alive %t, err %v), want untouched live child claim",
			current.PID, alive, err)
	}
}

func TestClaimReportsEmptyExistingClaim(t *testing.T) {
	gitCommonDir := t.TempDir()
	if err := os.MkdirAll(daemonDir(gitCommonDir), 0700); err != nil {
		t.Fatalf("create daemon dir: %v", err)
	}
	path := claimPath(gitCommonDir)
	// Manually create the poison pill: an existing owner.json with no
	// payload at all, as a concurrent winner's exclusive create would
	// briefly expose (or as filesystem corruption leaves behind).
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatalf("write empty claim: %v", err)
	}

	_, err := Claim(gitCommonDir)
	var empty *EmptyClaimError
	if !errors.As(err, &empty) {
		t.Fatalf("error %T does not carry *EmptyClaimError: %v", err, err)
	}
	if !errors.Is(err, ErrEmptyOwnerClaim) {
		t.Fatalf("error %v does not unwrap to ErrEmptyOwnerClaim", err)
	}
	if empty.Path != path {
		t.Fatalf("EmptyClaimError path = %q, want %q", empty.Path, path)
	}

	// The empty claim must remain untouched: Claim never judges liveness
	// from it nor deletes a file it cannot read.
	if data, err := os.ReadFile(path); err != nil || len(data) != 0 {
		t.Fatalf("empty claim after refusal read = (%d bytes, %v), want untouched empty file", len(data), err)
	}
	if _, _, inspectErr := InspectOwner(gitCommonDir); inspectErr == nil {
		t.Fatalf("inspecting an empty claim must stay an explicit error")
	}
}

func TestInspectOwnerRejectsNonPositivePidClaims(t *testing.T) {
	payloads := map[string]string{
		"empty object": `{}`,
		"zero pid":     `{"pid":0,"started_at":"0001-01-01T00:00:00Z","host":"h","protocol_revision":1}`,
		"negative pid": `{"pid":-7,"started_at":"0001-01-01T00:00:00Z","host":"h","protocol_revision":1}`,
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			gitCommonDir := t.TempDir()
			if err := os.MkdirAll(daemonDir(gitCommonDir), 0700); err != nil {
				t.Fatalf("create daemon dir: %v", err)
			}
			if err := os.WriteFile(claimPath(gitCommonDir), []byte(payload), 0600); err != nil {
				t.Fatalf("write crafted claim: %v", err)
			}
			if _, _, err := InspectOwner(gitCommonDir); err == nil {
				t.Fatalf("payload with non-positive pid must be an explicit error")
			} else if !strings.Contains(err.Error(), "unreadable") || !strings.Contains(err.Error(), claimPath(gitCommonDir)) {
				t.Fatalf("non-positive-pid error should name the claim unreadable at its path, got %q", err.Error())
			}
			if _, err := Claim(gitCommonDir); err == nil {
				t.Fatalf("claim over non-positive-pid claim must be an explicit error, never a reclaim")
			}
		})
	}
}

func TestInspectOwnerReportsPresenceAndLiveness(t *testing.T) {
	gitCommonDir := t.TempDir()

	owner, alive, err := InspectOwner(gitCommonDir)
	if err != nil {
		t.Fatalf("inspect without claim: %v", err)
	}
	if alive || owner != (Owner{}) {
		t.Fatalf("missing claim must yield zero Owner and false, got (%+v, %t)", owner, alive)
	}

	writeClaimFile(t, gitCommonDir, os.Getpid())
	owner, alive, err = InspectOwner(gitCommonDir)
	if err != nil || !alive || owner.PID != os.Getpid() {
		t.Fatalf("live self claim = (pid %d, alive %t, err %v), want own live pid", owner.PID, alive, err)
	}

	deadPID := spawnShortLivedChild(t)
	writeClaimFile(t, gitCommonDir, deadPID)
	_, alive, err = InspectOwner(gitCommonDir)
	if err != nil {
		t.Fatalf("inspect dead claim: %v", err)
	}
	if alive {
		t.Fatalf("dead-pid claim reported alive")
	}
}

func TestInspectOwnerRejectsCorruptClaim(t *testing.T) {
	gitCommonDir := t.TempDir()
	if err := os.MkdirAll(daemonDir(gitCommonDir), 0700); err != nil {
		t.Fatalf("create daemon dir: %v", err)
	}
	if err := os.WriteFile(claimPath(gitCommonDir), []byte("{not-json"), 0600); err != nil {
		t.Fatalf("write corrupt claim: %v", err)
	}
	if _, _, err := InspectOwner(gitCommonDir); err == nil {
		t.Fatalf("corrupt claim must be an explicit error, not absence")
	}
	if _, err := Claim(gitCommonDir); err == nil {
		t.Fatalf("claim over corrupt claim must be an explicit error, never a reclaim")
	}
}
