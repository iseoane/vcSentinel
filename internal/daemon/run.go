package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
)

// Run executes the whole foreground daemon lifecycle for one repository:
//
//  1. Claim exclusive ownership (a live rival owner fails with an error
//     wrapping ErrDaemonOwned that names its pid).
//  2. Run ReconcileOnBoot over the injected controller so the R8 machinery
//     settles auto-recoverable evidence before any request arrives.
//  3. Bind DefaultEndpoint, persist endpoint.json bound to this owner, and
//     print one ready line naming transport, address, and pid.
//  4. Serve until SIGINT/SIGTERM (signal.NotifyContext) or an authenticated
//     OpShutdown over the wire triggers Server.Shutdown; both graceful paths
//     make Serve return nil. On Windows only console Ctrl+C events reach the
//     handler — service-manager stop signals require a service wrapper and
//     are out of scope at D2.
//  5. After Serve returns — whichever way it returned — remove endpoint.json,
//     release the owner claim, and print a stopped summary with the orphaned
//     count the graceful sequence settled.
//
// The controller is dependency-injected by the caller (cmd/vcsentinel builds it
// through buildRunsController with the configured prompt adapter), so a real
// foreground daemon serves OpStart with a fully wired execution path instead
// of the historical ErrControllerNotReady refusal. Boot reconciliation reads
// the backing store through controller.Backing(), which structurally keeps
// the reconciliation scan and the serving controller on one shared store.
//
// Endpoint cleanup is guaranteed on EVERY exit through the deferred cleanup:
// early failures after the claim, listener failures, signal-driven stops, and
// wire-driven stops all flow either through Serve returning into the same
// cleanup tail or through the deferred call itself. The deferred release also
// covers the reconciliation-failure path, so a daemon that refuses to boot can
// never leave its claim behind for the next starter to trip over.
func Run(gitCommonDir string, controller *execution.Controller, grace time.Duration, out io.Writer) (runErr error) {
	if controller == nil {
		return errors.New("daemon: cannot start: no execution controller was provided")
	}
	dir := Dir(gitCommonDir)
	owner, claimErr := Claim(gitCommonDir)
	if claimErr != nil {
		var owned *OwnedError
		if errors.As(claimErr, &owned) {
			return fmt.Errorf("daemon: cannot start: another live daemon owns this repository (pid %d): %w",
				owned.Owner.PID, claimErr)
		}
		return fmt.Errorf("daemon: cannot start: %w", claimErr)
	}
	defer func() {
		removeEndpointArtifacts(dir)
		if releaseErr := Release(gitCommonDir); releaseErr != nil && runErr == nil {
			runErr = releaseErr
		}
	}()

	if reconcileErr := ReconcileOnBoot(controller, controller.Backing(), out); reconcileErr != nil {
		return reconcileErr
	}

	endpoint := DefaultEndpoint(dir)
	listener, listenErr := Listen(endpoint)
	if listenErr != nil {
		return fmt.Errorf("daemon: cannot bind the repository endpoint: %w", listenErr)
	}
	// Resolve the concrete bound address before persisting discovery: the
	// Windows tcp default binds an ephemeral port, and clients must read the
	// real port from endpoint.json rather than the ":0" placeholder.
	endpoint.Address = listener.Addr().String()
	if saveErr := SaveEndpoint(dir, endpoint, owner); saveErr != nil {
		_ = listener.Close()
		return fmt.Errorf("daemon: cannot persist the discovery record: %w", saveErr)
	}

	server := NewServerWithGrace(controller, FingerprintRepository(gitCommonDir), grace)
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	serveDone := make(chan struct{})
	go func() {
		select {
		case <-signalCtx.Done():
			_ = server.Shutdown()
		case <-serveDone:
		}
	}()

	fmt.Fprintf(out, "🟢 daemon ready: %s://%s (pid %d)\n", endpoint.Network, endpoint.Address, owner.PID)
	serveErr := server.Serve(listener)
	close(serveDone)

	fmt.Fprintf(out, "🛑 daemon stopped (orphaned runs: %d)\n", server.orphanedAtStop())
	if serveErr != nil {
		return fmt.Errorf("daemon: serve loop failed: %w", serveErr)
	}
	return nil
}

// removeEndpointArtifacts deletes the discovery record and, when it names a
// bearer token file (the Windows tcp transport), that token file too. Both
// removals are best-effort: they clear residue on every exit path, and a
// missing artifact is already the desired state.
func removeEndpointArtifacts(dir string) {
	path := filepath.Join(dir, endpointFileName)
	if endpoint, _, err := LoadEndpoint(dir); err == nil && endpoint.TokenFile != "" {
		_ = os.Remove(endpoint.TokenFile)
	}
	_ = os.Remove(path)
}
