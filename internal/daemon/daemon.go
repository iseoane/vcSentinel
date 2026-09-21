// Package daemon implements the repository-local daemon for D2: an
// exclusive owner claim stored under <git-common-dir>/vcsentinel/daemon/,
// stale detection through pid liveness behind a platform seam,
// deterministic reclaim, inspection, and self-release (slice 1), plus the
// repository-local transport of slice 2a — framed JSON connections over a
// unix socket or loopback TCP with bearer token, endpoint discovery through
// endpoint.json, and server-side dispatch of the four repository-host
// operations against one execution.Controller. The client adapter, runs-
// command wiring, and graceful lifecycle semantics arrive in later slices.
//
// Like store.NuevoStore, every entry point takes the git common directory
// (not the per-worktree git dir) so the daemon slot is shared across linked
// worktrees. All filesystem paths are built with filepath.Join.
package daemon

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// ProtocolRevision identifies the ownership-claim wire format. It is
	// bumped only when the claim schema changes incompatibly; endpoint
	// transport revisions belong to later slices.
	ProtocolRevision = 1

	// claimFileName is the fixed name of the owner claim file inside the
	// daemon directory.
	claimFileName = "owner.json"

	// maxReclaimAttempts bounds the stale-claim reclaim loop: each attempt
	// re-reads the claim, removes it when stale, and retries the exclusive
	// create once. Losing to a concurrent creator consumes one attempt.
	maxReclaimAttempts = 5

	// reclaimRetryDelay spaces out transient retries (lost create races and
	// existing-but-empty observations) so a loser cannot burn every attempt
	// inside the winner's exclusive-create window. A torn NON-empty payload
	// is not retried: it fails explicitly at parse, which is the safe
	// direction. Same pattern as the event-lock retry loop in internal/store.
	reclaimRetryDelay = 2 * time.Millisecond
)

// ErrDaemonOwned reports that the repository already has a live daemon
// owner. Use errors.Is to detect it; errors.As with *OwnedError recovers the
// owning details.
var ErrDaemonOwned = errors.New("daemon: repository already has a live owner")

// OwnedError carries the live owner observed while attempting to claim the
// repository daemon slot. It always unwraps to ErrDaemonOwned.
type OwnedError struct {
	Owner Owner
}

func (e *OwnedError) Error() string {
	return fmt.Sprintf("daemon: repository already has a live owner: pid %d started %s on host %q",
		e.Owner.PID, e.Owner.StartedAt.UTC().Format(time.RFC3339Nano), e.Owner.Host)
}

func (e *OwnedError) Unwrap() error { return ErrDaemonOwned }

// ErrEmptyOwnerClaim reports that an existing owner claim file carries no
// payload, so it names no owner and cannot be judged live or stale. Use
// errors.Is to detect it; errors.As with *EmptyClaimError recovers the path.
var ErrEmptyOwnerClaim = errors.New("daemon: owner claim is empty and carries no owner")

// EmptyClaimError carries the path of the existing-but-empty owner claim
// file that blocked claiming. It always unwraps to ErrEmptyOwnerClaim.
type EmptyClaimError struct {
	Path string
}

func (e *EmptyClaimError) Error() string {
	return fmt.Sprintf("daemon: owner claim at %s is empty and carries no owner", e.Path)
}

func (e *EmptyClaimError) Unwrap() error { return ErrEmptyOwnerClaim }

// Owner is the durable payload of the owner claim file. StartedAt serializes
// as RFC3339 with nanosecond precision because that is encoding/json's
// default representation for time.Time.
type Owner struct {
	PID              int       `json:"pid"`
	StartedAt        time.Time `json:"started_at"`
	Host             string    `json:"host"`
	ProtocolRevision int       `json:"protocol_revision"`
}

// daemonDir returns <git-common-dir>/vcsentinel/daemon, mirroring how
// store anchors its root at <git-common-dir>/vcsentinel.
func daemonDir(gitCommonDir string) string {
	return filepath.Join(gitCommonDir, "vcsentinel", "daemon")
}

// Dir is the exported spelling of the repository daemon directory:
// <git-common-dir>/vcsentinel/daemon. CLI discovery (endpoint.json
// resolution) and the package itself must agree on this single location, so
// callers join through this helper instead of re-deriving the layout.
func Dir(gitCommonDir string) string {
	return daemonDir(gitCommonDir)
}

func claimPath(gitCommonDir string) string {
	return filepath.Join(daemonDir(gitCommonDir), claimFileName)
}

// Claim atomically takes single ownership of the repository daemon slot.
//
// Exclusivity is proven single-process under concurrency by the test suite;
// across separate processes it rests on O_EXCL semantics alone: creating the
// claim file with O_CREATE|O_EXCL means the operating system admits exactly
// one creator, and any racer's create fails with EEXIST while the winner's
// descriptor is still being written, so exclusion is observable before the
// payload is readable. Write-then-rename is deliberately NOT used for the
// claim because rename would silently overwrite a rival's claim.
//
// If the existing claim belongs to a live process, Claim fails with
// *OwnedError (wrapping ErrDaemonOwned) carrying that owner. If the claim is
// stale — its pid is provably dead — Claim removes it and retries the
// exclusive create, up to maxReclaimAttempts attempts. An existing but
// still-empty claim file is the observable signature of a concurrent winner
// inside its exclusive create; Claim consumes an attempt and re-observes
// instead of judging liveness from an absent payload.
//
// Residual risks, documented once for D2 slice 1 and accepted for local
// developer tooling:
//
//   - Reclaim race window (theoretical): between observing a stale claim and
//     winning the exclusive create, the previous owner may be finishing its
//     own shutdown, and a peer reclaimer may win the create instead. In both
//     cases this caller observes either ErrDaemonOwned or its own fresh
//     claim; the window never yields two claim files, but the previous owner
//     may briefly believe it still owns the slot after a reclaimer removed
//     its stale-by-liveness claim.
//   - PID reuse: a dead owner's pid recycled by an unrelated live process
//     makes the stale claim look alive (refusing reclaim until that process
//     exits) or, inversely, a live claim whose pid died and was recycled can
//     be misjudged. Detection of reuse is out of scope for this slice.
//   - Windows liveness ambiguity: GetExitCodeProcess reports exit code 259
//     (STILL_ACTIVE) both for running processes and for processes that
//     happened to terminate with that very exit code, so such an exited pid
//     can be misjudged as alive (see process_windows.go).
func Claim(gitCommonDir string) (Owner, error) {
	dir := daemonDir(gitCommonDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Owner{}, err
	}
	path := claimPath(gitCommonDir)
	emptyObservation := false
	var lastCause error
	for attempt := 0; attempt < maxReclaimAttempts; attempt++ {
		data, found, err := readClaimFile(path)
		if err != nil {
			return Owner{}, err
		}
		if found && len(bytes.TrimSpace(data)) == 0 {
			// The file exists but has no payload yet: a concurrent winner
			// holds the exclusive create and is still writing. It owns the
			// slot; back off and observe the completed claim.
			emptyObservation = true
			time.Sleep(reclaimRetryDelay)
			continue
		}
		emptyObservation = false
		if found {
			existing, err := parseClaim(path, data)
			if err != nil {
				return Owner{}, err
			}
			if pidAlive(existing.PID) {
				return Owner{}, &OwnedError{Owner: existing}
			}
			// Stale claim: remove it so the exclusive create below can
			// proceed. A concurrent reclaimer may remove it first; that is
			// indistinguishable from the file already being gone.
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				return Owner{}, err
			}
		}
		owner, err := createClaimExclusive(path)
		if err == nil {
			return owner, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return Owner{}, err
		}
		// Another creator won the race between our read and our create;
		// back off and re-evaluate the claim they wrote. Without this
		// delay a loser can exhaust every attempt inside the winner's
		// create-to-write window under aggressive scheduling.
		lastCause = err
		time.Sleep(reclaimRetryDelay)
	}
	if emptyObservation {
		return Owner{}, &EmptyClaimError{Path: path}
	}
	if lastCause != nil {
		return Owner{}, fmt.Errorf("daemon: could not take ownership after %d attempts at %s: %w",
			maxReclaimAttempts, path, lastCause)
	}
	return Owner{}, fmt.Errorf("daemon: could not take ownership after %d attempts at %s", maxReclaimAttempts, path)
}

// createClaimExclusive writes a fresh claim payload through a single
// O_CREATE|O_EXCL open. The error is os.ErrExist when a claim already exists.
func createClaimExclusive(path string) (Owner, error) {
	owner := Owner{
		PID:              os.Getpid(),
		StartedAt:        time.Now(),
		Host:             hostName(),
		ProtocolRevision: ProtocolRevision,
	}
	data, err := json.MarshalIndent(owner, "", "  ")
	if err != nil {
		return Owner{}, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return Owner{}, err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return Owner{}, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return Owner{}, err
	}
	return owner, nil
}

// readClaim loads the owner claim at path. A missing file yields
// (zero Owner, false, nil); an unreadable or malformed claim — including an
// empty one — is an explicit error, never silently treated as absent or
// stale, so a corrupted claim can never enable dual ownership.
func readClaim(path string) (Owner, bool, error) {
	data, found, err := readClaimFile(path)
	if err != nil || !found {
		return Owner{}, false, err
	}
	owner, err := parseClaim(path, data)
	if err != nil {
		return Owner{}, false, err
	}
	return owner, true, nil
}

// readClaimFile returns the raw bytes of the claim file at path. A missing
// file yields (nil, false, nil).
func readClaimFile(path string) ([]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return data, true, nil
}

// parseClaim decodes the raw payload of a claim file. A payload that decodes
// but names no process (pid <= 0, e.g. the literal `{}`) is rejected as an
// unreadable claim so it can never yield a platform-divergent liveness
// verdict.
func parseClaim(path string, data []byte) (Owner, error) {
	var owner Owner
	if err := json.Unmarshal(data, &owner); err != nil {
		return Owner{}, fmt.Errorf("daemon: owner claim at %s is unreadable: %w", path, err)
	}
	if owner.PID <= 0 {
		return Owner{}, fmt.Errorf("daemon: owner claim at %s is unreadable: decoded pid %d is not a valid process id", path, owner.PID)
	}
	return owner, nil
}

// InspectOwner reports the current owner claim and whether its pid is alive.
// When no claim exists it returns the zero Owner, false, and nil error.
func InspectOwner(gitCommonDir string) (Owner, bool, error) {
	owner, found, err := readClaim(claimPath(gitCommonDir))
	if err != nil || !found {
		return Owner{}, false, err
	}
	return owner, pidAlive(owner.PID), nil
}

// Release removes the owner claim only when it belongs to the current
// process. A missing claim or a claim held by another pid is a deterministic
// error, never silent success.
//
// Residual risk (theoretical): between readClaim and Remove the claim
// identity could change, so the pid check and the removal may not observe
// the same file generation.
func Release(gitCommonDir string) error {
	path := claimPath(gitCommonDir)
	owner, found, err := readClaim(path)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("daemon: no owner claim exists at %s", path)
	}
	if owner.PID != os.Getpid() {
		return fmt.Errorf("daemon: refusing to release owner claim held by pid %d (this process is %d)",
			owner.PID, os.Getpid())
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// hostName returns the local hostname, falling back to a stable placeholder
// when the platform cannot provide one.
func hostName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "unknown"
	}
	return name
}
