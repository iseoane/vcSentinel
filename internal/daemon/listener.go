package daemon

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Endpoint names one daemon transport address. Network is "unix" (Address is
// the socket path inside the daemon directory) or "tcp" (Address is a
// loopback host:port). TokenFile is required for tcp: it names the bearer
// token file the listener writes and clients read to authenticate their
// handshake; unix endpoints leave it empty because filesystem permissions
// already scope the socket.
type Endpoint struct {
	Network   string `json:"network"`
	Address   string `json:"address"`
	TokenFile string `json:"token_file,omitempty"`
}

// staleSocketProbeTimeout bounds how long the unix stale-socket probe waits
// for a live daemon to answer before judging the socket stale.
const staleSocketProbeTimeout = 250 * time.Millisecond

// Listen binds ep and returns its listener. The tcp transport always
// generates a fresh bearer token, persists it to ep.TokenFile with
// best-effort 0600 semantics, and makes every connection present it during
// the handshake; the unix transport protects itself with filesystem
// permissions instead (0700 directory, 0600 socket) and probes for liveness
// before replacing a stale socket left by a dead owner.
func Listen(ep Endpoint) (net.Listener, error) {
	switch ep.Network {
	case "unix":
		return listenUnix(ep.Address)
	case "tcp":
		return listenLoopbackTCP(ep)
	default:
		return nil, fmt.Errorf("daemon: unsupported endpoint network %q", ep.Network)
	}
}

// Dial connects to ep. It only opens the transport; handshake authentication
// happens one layer up.
func Dial(ep Endpoint) (net.Conn, error) {
	switch ep.Network {
	case "unix", "tcp":
		return net.Dial(ep.Network, ep.Address)
	default:
		return nil, fmt.Errorf("daemon: unsupported endpoint network %q", ep.Network)
	}
}

// tcpListener wraps a loopback TCP listener with the bearer token every
// connecting client must present during its handshake.
type tcpListener struct {
	*net.TCPListener
	token string
}

// bearerToken exposes the token to the server without widening the
// net.Listener contract.
func (l *tcpListener) bearerToken() string { return l.token }

// listenLoopbackTCP binds 127.0.0.1 only, generates a fresh bearer token,
// and persists it atomically to ep.TokenFile. A non-loopback host is
// rejected outright: the daemon is repository-local by contract and must
// never widen its reach because someone passed 0.0.0.0.
func listenLoopbackTCP(ep Endpoint) (net.Listener, error) {
	if strings.TrimSpace(ep.TokenFile) == "" {
		return nil, errors.New("daemon: tcp endpoints require a token file path")
	}
	host, port, err := net.SplitHostPort(ep.Address)
	if err != nil {
		return nil, fmt.Errorf("daemon: invalid tcp endpoint %q: %w", ep.Address, err)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("daemon: tcp endpoints must bind a loopback address, got %q", host)
	}
	tcp, err := net.Listen("tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, err
	}
	token, err := newBearerToken()
	if err != nil {
		_ = tcp.Close()
		return nil, err
	}
	if err := writeFileAtomic(ep.TokenFile, []byte(token), 0600); err != nil {
		_ = tcp.Close()
		return nil, err
	}
	return &tcpListener{TCPListener: tcp.(*net.TCPListener), token: token}, nil
}

// ReadBearerToken loads the token persisted for a tcp endpoint so a client
// can present it in its handshake. Whitespace is trimmed defensively.
func ReadBearerToken(ep Endpoint) (string, error) {
	data, err := os.ReadFile(ep.TokenFile)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", fmt.Errorf("daemon: bearer token file at %s is empty", ep.TokenFile)
	}
	return token, nil
}

// newBearerToken returns 256 bits of fresh randomness as lowercase hex.
func newBearerToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("daemon: cannot generate bearer token: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// endpointFileName is the fixed name of the discovery record inside the
// daemon directory.
const endpointFileName = "endpoint.json"

// endpointRecord is the durable discovery payload. It mirrors Owner's pid,
// host, and started_at so LoadEndpoint can reconstruct who is serving
// without a second file read.
type endpointRecord struct {
	Network          string    `json:"network"`
	Address          string    `json:"address"`
	TokenFile        string    `json:"token_file,omitempty"`
	Host             string    `json:"host,omitempty"`
	PID              int       `json:"pid"`
	StartedAt        time.Time `json:"started_at"`
	ProtocolRevision int       `json:"protocol_revision"`
}

// SaveEndpoint persists the discovery record endpoint.json under daemonDir
// atomically (temp file + rename, best-effort 0600) so clients can discover
// a running daemon. Nothing auto-connects on its own: reading this file is
// always an explicit caller decision.
//
// Residual risk accepted for D2 slice 2a: the record is written after bind,
// so a crash between Listen and SaveEndpoint leaves no endpoint.json even
// though the process still serves; recovery of that window belongs to the
// lifecycle slice.
func SaveEndpoint(daemonDir string, ep Endpoint, owner Owner) error {
	if err := os.MkdirAll(daemonDir, 0700); err != nil {
		return err
	}
	record := endpointRecord{
		Network:          ep.Network,
		Address:          ep.Address,
		TokenFile:        ep.TokenFile,
		Host:             owner.Host,
		PID:              owner.PID,
		StartedAt:        owner.StartedAt,
		ProtocolRevision: ProtocolRevision,
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(daemonDir, endpointFileName), data, 0600)
}

// LoadEndpoint reads the discovery record from daemonDir. A missing record
// reports an error wrapping os.ErrNotExist so callers separate "no daemon
// announced itself" from real damage.
func LoadEndpoint(daemonDir string) (Endpoint, Owner, error) {
	path := filepath.Join(daemonDir, endpointFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return Endpoint{}, Owner{}, err
	}
	var record endpointRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return Endpoint{}, Owner{}, fmt.Errorf("daemon: endpoint record at %s is unreadable: %w", path, err)
	}
	if record.PID <= 0 || record.Network == "" || record.Address == "" {
		return Endpoint{}, Owner{}, fmt.Errorf("daemon: endpoint record at %s is incomplete", path)
	}
	if record.Network != "unix" && record.Network != "tcp" {
		return Endpoint{}, Owner{}, fmt.Errorf("daemon: endpoint record at %s names unsupported network %q", path, record.Network)
	}
	ep := Endpoint{Network: record.Network, Address: record.Address, TokenFile: record.TokenFile}
	owner := Owner{PID: record.PID, StartedAt: record.StartedAt, Host: record.Host, ProtocolRevision: record.ProtocolRevision}
	return ep, owner, nil
}

// writeFileAtomic writes data to path through a temp file plus rename with
// the given permissions, mirroring store's atomic-write discipline. On POSIX
// systems os.Rename replaces the destination atomically, so the plain rename
// runs first and readers never observe a missing or partial file; only when
// that rename fails does it fall back to remove-then-rename for platforms
// such as Windows where os.Rename cannot overwrite an existing file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, "tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Chmod(perm); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err == nil {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(tempPath, path)
}

// FingerprintRepository derives the stable repository binding used by the
// wire handshake: the sha256 hex of the cleaned absolute git common
// directory. Linked worktrees share their git common dir, so they share one
// fingerprint and one daemon by construction. It does NOT resolve symlinks
// nor normalize case: both sides must derive their path from git output
// (git rev-parse --git-common-dir) for the fingerprints to agree.
func FingerprintRepository(gitCommonDir string) string {
	cleaned := filepath.Clean(gitCommonDir)
	if absolute, err := filepath.Abs(cleaned); err == nil {
		cleaned = absolute
	}
	digest := sha256.Sum256([]byte(cleaned))
	return hex.EncodeToString(digest[:])
}
