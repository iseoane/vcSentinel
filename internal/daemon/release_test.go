package daemon

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestReleaseRemovesOwnClaim(t *testing.T) {
	gitCommonDir := t.TempDir()
	if _, err := Claim(gitCommonDir); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := Release(gitCommonDir); err != nil {
		t.Fatalf("release own claim: %v", err)
	}
	if _, err := os.Stat(claimPath(gitCommonDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("claim file after release stat err = %v, want not-exist", err)
	}

	// The slot is free again: a second claim must succeed.
	owner, err := Claim(gitCommonDir)
	if err != nil {
		t.Fatalf("re-claim after release: %v", err)
	}
	if owner.PID != os.Getpid() {
		t.Fatalf("re-claimed pid %d, want own pid %d", owner.PID, os.Getpid())
	}
}

func TestReleaseRejectsMissingClaim(t *testing.T) {
	gitCommonDir := t.TempDir()
	err := Release(gitCommonDir)
	if err == nil {
		t.Fatalf("release without claim must fail deterministically")
	}
	if strings.Contains(err.Error(), "no owner claim") {
		return
	}
	t.Fatalf("missing-claim release error should name the absent claim, got %q", err.Error())
}

func TestReleaseRefusesForeignClaim(t *testing.T) {
	gitCommonDir := t.TempDir()
	foreignPID := spawnShortLivedChild(t) // Dead but provably not this process.
	crafted := writeClaimFile(t, gitCommonDir, foreignPID)

	err := Release(gitCommonDir)
	if err == nil {
		t.Fatalf("release of foreign claim must fail, not succeed silently")
	}
	if !strings.Contains(err.Error(), "pid") || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("foreign-release error should identify the holder pid, got %q", err.Error())
	}

	// The foreign claim must remain untouched.
	current, alive, inspectErr := InspectOwner(gitCommonDir)
	if inspectErr != nil {
		t.Fatalf("inspect after refused release: %v", inspectErr)
	}
	if current.PID != crafted.PID || alive {
		t.Fatalf("claim after refused release = (pid %d, alive %t), want untouched dead pid %d",
			current.PID, alive, crafted.PID)
	}
}
