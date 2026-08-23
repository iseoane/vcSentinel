package daemon

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// supportsUnixSockets skips the caller unless the platform can bind a unix
// socket. CI is Linux, where this always holds; the skip keeps the suite
// honest on platforms without unix sockets.
func supportsUnixSockets(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "probe.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("unix domain sockets unavailable: %v", err)
	}
	_ = listener.Close()
}

func TestWriteFrameReadFrameRoundTrip(t *testing.T) {
	var buffer bytes.Buffer
	bodies := [][]byte{
		[]byte(`{"op":"start"}`),
		bytes.Repeat([]byte("x"), MaxFrameSize),
		nil,
	}
	for _, body := range bodies {
		buffer.Reset()
		if err := WriteFrame(&buffer, body); err != nil {
			t.Fatalf("WriteFrame(%d bytes): %v", len(body), err)
		}
		got, err := ReadFrame(&buffer)
		if err != nil {
			t.Fatalf("ReadFrame(%d bytes): %v", len(body), err)
		}
		if !bytes.Equal(got, body) {
			t.Fatalf("round-tripped body differs: got %d bytes, want %d bytes", len(got), len(body))
		}
	}
}

func TestWriteFrameRejectsOversizedBody(t *testing.T) {
	var buffer bytes.Buffer
	body := make([]byte, MaxFrameSize+1)
	err := WriteFrame(&buffer, body)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized write error = %v, want ErrFrameTooLarge", err)
	}
	if buffer.Len() != 0 {
		t.Fatalf("oversized write emitted %d bytes, want none", buffer.Len())
	}
}

func TestReadFrameRejectsOversizedPrefix(t *testing.T) {
	frame := make([]byte, 4)
	binary.BigEndian.PutUint32(frame, uint32(MaxFrameSize)+1)
	reader := bytes.NewReader(frame)
	got, err := ReadFrame(reader)
	if !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized prefix error = %v, want ErrFrameTooLarge", err)
	}
	if got != nil {
		t.Fatalf("oversized prefix returned a body of %d bytes, want nil", len(got))
	}
	// The 4-byte prefix is necessarily consumed to learn the size; the
	// guarantee is that no body byte was ever allocated or read.
	if reader.Len() != 0 {
		t.Fatalf("oversized prefix left %d unread bytes, want the prefix fully consumed", reader.Len())
	}
}

func TestReadFrameTruncatedStreamFailsWithoutPanic(t *testing.T) {
	tests := []struct {
		name    string
		frame   []byte
		wantErr error
	}{
		{name: "missing prefix entirely", frame: nil, wantErr: io.EOF},
		{name: "partial length prefix", frame: []byte{0x00, 0x00}, wantErr: io.ErrUnexpectedEOF},
		{name: "body shorter than announced", frame: []byte{0x00, 0x00, 0x00, 0x05, 'a', 'b'}, wantErr: io.ErrUnexpectedEOF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("ReadFrame panicked: %v", recovered)
				}
			}()
			_, err := ReadFrame(bytes.NewReader(tt.frame))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("truncated stream error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestWireSentinelRegistryRoundTrip proves the registry property that makes
// errors.Is survive the wire: every listed sentinel classifies to exactly
// one canonical code even through fmt.Errorf wrapping, and decoding that
// code back into a RemoteError satisfies errors.Is against the original
// sentinel. This covers the sentinels no current operation can raise over
// the four wired ops yet (stale revision, execution-not-found, daemon
// owned): their vocabulary is pinned here at the codec level instead. The
// same holds for run_not_retryable and run_not_recoverable until a retry or
// recover op joins the wire.
func TestWireSentinelRegistryRoundTrip(t *testing.T) {
	for _, entry := range wireSentinels {
		wrapped := wrapForRegistry(entry.sentinel)
		code := errorCodeFor(wrapped)
		if code != entry.code {
			t.Fatalf("errorCodeFor(%v) = %q, want %q", entry.sentinel, code, entry.code)
		}
		remote := &RemoteError{Code: code, Message: wrapped.Error()}
		if !errors.Is(remote, entry.sentinel) {
			t.Fatalf("remote error with code %q does not resolve errors.Is against %v", code, entry.sentinel)
		}
	}
	unknown := &RemoteError{Code: "no.such.code", Message: "mystery"}
	if unwrap := unknown.Unwrap(); unwrap != nil {
		t.Fatalf("unknown code unwrapped to %v, want nil", unwrap)
	}
	if code := errorCodeFor(errors.New("unregistered failure")); code != "" {
		t.Fatalf("unregistered error classified as %q, want empty code", code)
	}
}

// wrapForRegistry adds one wrapping layer around sentinel so the test
// exercises errors.Is traversal, not pointer equality.
func wrapForRegistry(sentinel error) error {
	return fmt.Errorf("wrapped for registry: %w", sentinel)
}

// TestSaveLoadEndpointRoundTrip pins the discovery record contract.
func TestSaveLoadEndpointRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "daemon")
	ep := Endpoint{Network: "tcp", Address: "127.0.0.1:4711", TokenFile: filepath.Join(dir, "bearer-token")}
	owner := Owner{PID: 4242, StartedAt: time.Now().UTC().Truncate(time.Second), Host: "test-host"}
	if err := SaveEndpoint(dir, ep, owner); err != nil {
		t.Fatalf("SaveEndpoint: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "endpoint.json"))
	if err != nil {
		t.Fatalf("endpoint.json missing: %v", err)
	}
	if runtime.GOOS == "windows" {
		t.Logf("permission bits are best-effort on windows; mode observed %v", info.Mode())
	} else if info.Mode().Perm() != 0600 {
		t.Fatalf("endpoint.json mode = %v, want -rw-------", info.Mode().Perm())
	}

	loadedEP, loadedOwner, err := LoadEndpoint(dir)
	if err != nil {
		t.Fatalf("LoadEndpoint: %v", err)
	}
	if loadedEP != ep {
		t.Fatalf("loaded endpoint = %+v, want %+v", loadedEP, ep)
	}
	if loadedOwner.PID != owner.PID || !loadedOwner.StartedAt.Equal(owner.StartedAt) || loadedOwner.Host != owner.Host {
		t.Fatalf("loaded owner = %+v, want pid %d started %v host %q",
			loadedOwner, owner.PID, owner.StartedAt, owner.Host)
	}
	if loadedOwner.ProtocolRevision != ProtocolRevision {
		t.Fatalf("loaded protocol_revision = %d, want %d", loadedOwner.ProtocolRevision, ProtocolRevision)
	}
}

// TestSaveEndpointOverwritesAtomically proves a second save replaces the
// record instead of failing on the existing destination.
func TestSaveEndpointOverwritesAtomically(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "daemon")
	first := Owner{PID: 1, StartedAt: time.Now()}
	second := Owner{PID: 2, StartedAt: time.Now()}
	if err := SaveEndpoint(dir, DefaultEndpoint(dir), first); err != nil {
		t.Fatalf("first SaveEndpoint: %v", err)
	}
	if err := SaveEndpoint(dir, DefaultEndpoint(dir), second); err != nil {
		t.Fatalf("second SaveEndpoint: %v", err)
	}
	_, owner, err := LoadEndpoint(dir)
	if err != nil {
		t.Fatalf("LoadEndpoint: %v", err)
	}
	if owner.PID != second.PID {
		t.Fatalf("endpoint pid = %d, want the overwritten pid %d", owner.PID, second.PID)
	}
}

func TestLoadEndpointMissingWrapsNotExist(t *testing.T) {
	_, _, err := LoadEndpoint(filepath.Join(t.TempDir(), "daemon"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing endpoint error = %v, want os.ErrNotExist chain", err)
	}
}

func TestLoadEndpointRejectsIncompleteRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "daemon")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "endpoint.json")
	if err := os.WriteFile(path, []byte(`{"pid":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadEndpoint(dir); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete record error = %v, want explicit incomplete failure", err)
	}
}

func TestFingerprintRepositoryStableAcrossPathSpellings(t *testing.T) {
	dir := t.TempDir()
	base := FingerprintRepository(dir)
	spellings := []string{
		dir,
		dir + string(filepath.Separator),
		filepath.Join(dir, ".", "sub", ".."),
	}
	for _, spelling := range spellings {
		if got := FingerprintRepository(spelling); got != base {
			t.Fatalf("FingerprintRepository(%q) = %q, want stable %q", spelling, got, base)
		}
	}
	other := FingerprintRepository(filepath.Join(dir, "other"))
	if other == base {
		t.Fatalf("distinct directories share fingerprint %q", base)
	}
	if len(base) != 64 {
		t.Fatalf("fingerprint length = %d, want sha256 hex (64)", len(base))
	}
}

// TestListenUnixReplacesStaleSocketButNeverDisplacesLiveOwner pins the
// stale-socket probe contract. The stale artifact here is an ordinary dead
// file; a socket orphaned by a killed daemon behaves identically because
// the probe dial fails the same way.
func TestListenUnixReplacesStaleSocketButNeverDisplacesLiveOwner(t *testing.T) {
	supportsUnixSockets(t)
	path := filepath.Join(t.TempDir(), "daemon.sock")

	live, err := Listen(Endpoint{Network: "unix", Address: path})
	if err != nil {
		t.Fatalf("initial listen: %v", err)
	}
	t.Cleanup(func() { _ = live.Close() })

	if _, err := Listen(Endpoint{Network: "unix", Address: path}); !errors.Is(err, ErrDaemonOwned) {
		t.Fatalf("second listen under live owner error = %v, want ErrDaemonOwned chain", err)
	}

	_ = live.Close() // A killed owner leaves nothing behind on clean close...
	if err := os.WriteFile(path, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	replacement, err := Listen(Endpoint{Network: "unix", Address: path})
	if err != nil {
		t.Fatalf("listen did not replace stale socket: %v", err)
	}
	defer func() { _ = replacement.Close() }()

	if info, err := os.Stat(path); err != nil {
		t.Fatalf("socket file missing after rebind: %v", err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("socket mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestListenTCPRejectsNonLoopbackAndMissingTokenFile(t *testing.T) {
	dir := t.TempDir()
	if _, err := Listen(Endpoint{Network: "tcp", Address: "127.0.0.1:0"}); err == nil {
		t.Fatal("tcp listen without token file succeeded, want explicit error")
	}
	if _, err := Listen(Endpoint{Network: "tcp", Address: "10.0.0.1:0", TokenFile: filepath.Join(dir, "t")}); err == nil {
		t.Fatal("tcp listen on non-loopback address succeeded, want explicit error")
	}
	if _, err := Listen(Endpoint{Network: "pipe", Address: "whatever"}); err == nil {
		t.Fatal("unsupported network succeeded, want explicit error")
	}
	if _, err := Dial(Endpoint{Network: "pipe", Address: "whatever"}); err == nil {
		t.Fatal("unsupported network dial succeeded, want explicit error")
	}
}
