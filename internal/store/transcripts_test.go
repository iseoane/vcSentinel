package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newTranscriptFixture builds one store with a single handcrafted execution
// directory so transcript helpers are exercised against the real layout:
// <root>/vas-sentinel/executions/v1/<runID>/{request.json,outcomes,transcripts}.
func newTranscriptFixture(t *testing.T) (*Store, string, string) {
	t.Helper()
	backing := NewStore(t.TempDir())
	const runID = "run-transcripts"
	execDir, err := backing.ExecutionDir(runID)
	if err != nil {
		t.Fatalf("ExecutionDir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(execDir, "outcomes"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(execDir, "request.json"), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return backing, execDir, runID
}

func writeOutcomeRecord(t *testing.T, execDir, invocationID string, outcome AttemptOutcome) {
	t.Helper()
	data, err := json.MarshalIndent(outcome, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(execDir, "outcomes", invocationID+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestWriteTranscriptProducesExactSidecarShape(t *testing.T) {
	backing, _, runID := newTranscriptFixture(t)
	const invocationID = "inv-0001"
	at := time.Date(2026, 8, 26, 10, 30, 0, 0, time.UTC)
	const output = "raw provider stdout"

	digest, size, err := backing.WriteTranscript(runID, invocationID, at, output)
	if err != nil {
		t.Fatalf("WriteTranscript() error = %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(backing.dir, "executions", "v1", runID, "transcripts", invocationID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var sidecar map[string]string
	if err := json.Unmarshal(raw, &sidecar); err != nil {
		t.Fatalf("sidecar is not a JSON object: %v", err)
	}
	if len(sidecar) != 3 {
		t.Fatalf("sidecar keys = %v, want exactly invocation_id, at, output", sidecar)
	}
	for _, key := range []string{"invocation_id", "at", "output"} {
		if _, ok := sidecar[key]; !ok {
			t.Fatalf("sidecar keys = %v, want exactly invocation_id, at, output", sidecar)
		}
	}
	if sidecar["invocation_id"] != invocationID || sidecar["output"] != output {
		t.Fatalf("sidecar = %v, want invocation_id %q and output %q", sidecar, invocationID, output)
	}
	sum := sha256.Sum256(raw)
	if digest != hex.EncodeToString(sum[:]) {
		t.Fatalf("digest = %s, want sha256 over written bytes %s", digest, hex.EncodeToString(sum[:]))
	}
	if size != int64(len(raw)) {
		t.Fatalf("size = %d, want %d", size, len(raw))
	}
}

func TestWriteTranscriptRejectsOversizedOutput(t *testing.T) {
	backing, _, runID := newTranscriptFixture(t)
	output := strings.Repeat("x", MaxTranscriptBytes+1)

	if _, _, err := backing.WriteTranscript(runID, "inv-too-large", time.Now().UTC(), output); err == nil {
		t.Fatal("WriteTranscript() accepted output beyond the retention limit")
	}
	path := filepath.Join(backing.dir, "executions", "v1", runID, "transcripts", "inv-too-large.json")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized transcript sidecar exists or could not be checked: %v", err)
	}
}

func TestVerifyTranscript(t *testing.T) {
	const invocationID = "inv-0002"
	const output = "honest verdict bytes"

	cases := []struct {
		name          string
		mutate        func(t *testing.T, execDir string)
		wantOK        bool
		reasonContain string
	}{
		{
			name:   "intact sidecar verifies",
			mutate: func(t *testing.T, execDir string) {},
			wantOK: true,
		},
		{
			name: "tampered output fails with digest mismatch",
			mutate: func(t *testing.T, execDir string) {
				path := filepath.Join(execDir, "transcripts", invocationID+".json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var sidecar map[string]string
				if err := json.Unmarshal(raw, &sidecar); err != nil {
					t.Fatal(err)
				}
				sidecar["output"] = "forged verdict"
				forged, err := json.Marshal(sidecar)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, forged, 0600); err != nil {
					t.Fatal(err)
				}
			},
			reasonContain: "digest mismatch",
		},
		{
			name: "missing sidecar fails",
			mutate: func(t *testing.T, execDir string) {
				if err := os.Remove(filepath.Join(execDir, "transcripts", invocationID+".json")); err != nil {
					t.Fatal(err)
				}
			},
			reasonContain: "sidecar unreadable",
		},
		{
			name: "missing outcome record fails",
			mutate: func(t *testing.T, execDir string) {
				if err := os.Remove(filepath.Join(execDir, "outcomes", invocationID+".json")); err != nil {
					t.Fatal(err)
				}
			},
			reasonContain: "outcome record unreadable",
		},
		{
			name: "outcome without recorded digest fails",
			mutate: func(t *testing.T, execDir string) {
				writeOutcomeRecord(t, execDir, invocationID, AttemptOutcome{
					RunID: "run-transcripts", InvocationID: invocationID,
				})
			},
			reasonContain: "no transcript digest recorded",
		},
		{
			name: "recomputed digest with stale recorded size fails",
			mutate: func(t *testing.T, execDir string) {
				path := filepath.Join(execDir, "transcripts", invocationID+".json")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(raw)
				writeOutcomeRecord(t, execDir, invocationID, AttemptOutcome{
					RunID: "run-transcripts", InvocationID: invocationID,
					TranscriptSHA256: hex.EncodeToString(sum[:]),
					TranscriptSize:   int64(len(raw)) + 7,
				})
			},
			reasonContain: "size mismatch",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			backing, _, runID := newTranscriptFixture(t)
			execDir, err := backing.ExecutionDir(runID)
			if err != nil {
				t.Fatal(err)
			}
			digest, size, err := backing.WriteTranscript(runID, invocationID, time.Now().UTC(), output)
			if err != nil {
				t.Fatal(err)
			}
			writeOutcomeRecord(t, execDir, invocationID, AttemptOutcome{
				RunID: runID, InvocationID: invocationID,
				TranscriptSHA256: digest, TranscriptSize: size,
			})
			tc.mutate(t, execDir)

			ok, reason := VerifyTranscript(execDir, invocationID)
			if ok != tc.wantOK {
				t.Fatalf("VerifyTranscript() = %v (%q), want ok=%v", ok, reason, tc.wantOK)
			}
			if !tc.wantOK && !strings.Contains(reason, tc.reasonContain) {
				t.Fatalf("reason = %q, want it to contain %q", reason, tc.reasonContain)
			}
			if tc.wantOK && reason != "" {
				t.Fatalf("reason = %q, want empty on success", reason)
			}
		})
	}
}

func TestWriteTranscriptRefusesInvalidIdentity(t *testing.T) {
	backing, _, runID := newTranscriptFixture(t)
	if _, _, err := backing.WriteTranscript(runID, "bad/../id", time.Now().UTC(), "x"); err == nil {
		t.Fatal("WriteTranscript() accepted an invocation id with a path separator")
	}
	if _, _, err := backing.WriteTranscript("missing-run", "inv-0003", time.Now().UTC(), "x"); err == nil {
		t.Fatal("WriteTranscript() accepted a run without an execution record")
	}
}
