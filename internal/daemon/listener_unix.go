//go:build unix

package daemon

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// socketFileName is the fixed name of the unix socket inside the daemon
// directory.
const socketFileName = "daemon.sock"

// DefaultEndpoint returns the unix transport for the daemon directory: a
// socket path inside daemonDir, protected by the directory's 0700
// permissions instead of a handshake secret.
func DefaultEndpoint(daemonDir string) Endpoint {
	return Endpoint{Network: "unix", Address: filepath.Join(daemonDir, socketFileName)}
}

// listenUnix binds the unix socket at address after a liveness probe.
//
// Stale-socket recovery works like this: if a socket file already exists,
// the binder dials it with a short timeout BEFORE binding. A live daemon
// answers the probe dial, so binding is refused with an error wrapping
// ErrDaemonOwned — a healthy owner is never displaced. If nothing answers
// (connection refused, timeout, or any other dial failure), the file can
// only be a socket left behind by a dead owner, so it is removed and the
// bind proceeds deterministically. The socket is then chmod 0600 so only
// the owning user may connect; the enclosing daemon directory stays 0700,
// which scopes discovery as well as connection.
//
// Residual risk accepted for D2 slice 2a: the probe cannot distinguish a
// momentarily overloaded live daemon from a dead one within the probe
// budget; a slow-but-alive owner would be misjudged stale and its socket
// removed while it still serves. The lifecycle slice binds this window to
// the ownership claim protocol.
func listenUnix(address string) (net.Listener, error) {
	dir := filepath.Dir(address)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(address); err == nil {
		conn, dialErr := net.DialTimeout("unix", address, staleSocketProbeTimeout)
		if dialErr == nil {
			_ = conn.Close()
			return nil, fmt.Errorf("%w: a live daemon already answers %s", ErrDaemonOwned, address)
		}
		if err := os.Remove(address); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", address)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(address, 0600); err != nil {
		_ = listener.Close()
		_ = os.Remove(address)
		return nil, err
	}
	return listener, nil
}
