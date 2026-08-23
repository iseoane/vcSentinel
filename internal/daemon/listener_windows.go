//go:build windows

package daemon

import (
	"errors"
	"net"
	"path/filepath"
)

// DefaultEndpoint returns the Windows v1 transport for the daemon directory:
// loopback TCP bound to 127.0.0.1 with an ephemeral port, authenticated by a
// bearer token file written under daemonDir.
//
// Why TCP plus token for Windows v1 instead of named pipes: the transport is
// stdlib-only (no new go.mod dependencies, no cgo, no syscall surface), the
// 127.0.0.1 bind keeps the endpoint unreachable off-host, and the handshake
// bearer token compensates for TCP's lack of filesystem-style connection
// scoping. The ticket allows golang.org/x/sys/windows (already a direct
// dependency) for a current-user-scoped named pipe, but pipe security ACL
// setup through that package is nontrivial and unproven here; the upgrade is
// deliberately deferred until evidence justifies leaving TCP. Residual risk
// accepted: any local user can attempt a TCP connection and must then present
// the token; the token file itself relies on NTFS ACL inheritance under the
// user profile because Go cannot set POSIX modes on Windows.
func DefaultEndpoint(daemonDir string) Endpoint {
	return Endpoint{
		Network:   "tcp",
		Address:   "127.0.0.1:0",
		TokenFile: filepath.Join(daemonDir, "bearer-token"),
	}
}

// listenUnix reports that the unix transport does not exist on Windows. The
// shared Listen seam references it unconditionally; the tcp branch is the
// only reachable path here.
func listenUnix(string) (net.Listener, error) {
	return nil, errors.New("daemon: unix domain sockets are not supported on windows")
}
