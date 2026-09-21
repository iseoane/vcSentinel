// Historical-readability contract for ticket 13 (R11): the review ledger
// written by older binaries stays fully inspectable after the switch removal.
// The fixture hand-writes a minimal pre-v2 record — no agent/model/effort
// attribution (pre-T0.2 shape) and no aggregated findings (pre-T6.1 shape) —
// and proves the current readers list it, decode it, and project its legacy
// v1 findings without any conversion step.
package review

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyLedgerRevisionRemainsReadable(t *testing.T) {
	t.Helper()
	gitDir := t.TempDir()
	ledger := NewLedger(gitDir)
	if err := os.MkdirAll(filepath.Join(gitDir, "vcsentinel"), 0o700); err != nil {
		t.Fatal(err)
	}

	const sha = "legacy0000000000000000000000000000000"
	// Minimal pre-v2 record bytes: only the fields the oldest writer emitted.
	legacyJSON := `{
	  "sha": "` + sha + `",
	  "message": "feat: historical change",
	  "bucket": "",
	  "model": "default",
	  "revisions": [
	    {
	      "at": "2024-06-01T10:00:00Z",
	      "result": "block",
	      "dims": [
	        {
	          "dim": "logic",
	          "verdict": "block",
	          "findings": [
	            {
	              "dimension": "logic",
	              "file": "a.go",
	              "line": 1,
	              "severity": "CRITICAL",
	              "description": "historical bug"
	            }
	          ]
	        }
	      ]
	    }
	  ]
	}`
	if err := os.WriteFile(ledger.RecordPath(sha), []byte(legacyJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	listed, err := ledger.ListRecords()
	if err != nil || len(listed) != 1 || listed[0] != sha {
		t.Fatalf("ListRecords() = %v/%v, want exactly [%s]", listed, err, sha)
	}

	record, err := ledger.ReadRecord(sha)
	if err != nil || record == nil {
		t.Fatalf("ReadRecord(%s) = %+v/%v, want the decoded legacy record", sha, record, err)
	}
	if record.Message != "feat: historical change" || len(record.Revisions) != 1 {
		t.Fatalf("record = %+v, want one historical revision", record)
	}
	revision := record.Revisions[0]
	if revision.Result != "block" || revision.Agent != "" || len(revision.AggregatedFindings) != 0 {
		t.Fatalf("revision = %+v, want the untouched pre-v2 shape", revision)
	}
	if want := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC); !revision.At.Equal(want) {
		t.Fatalf("revision.At = %v, want %v", revision.At, want)
	}

	// The effective-findings projection must still convert the legacy v1
	// per-dimension finding into the uniform v2 shape used everywhere today.
	findings := revision.EffectiveFindings()
	if len(findings) != 1 {
		t.Fatalf("EffectiveFindings() = %+v, want the single projected v1 finding", findings)
	}
	finding := findings[0]
	if finding.Dimension != DimLogic || finding.Severity != SevCritical ||
		finding.Description != "historical bug" || finding.Location.File != "a.go" || finding.Location.LineStart != 1 {
		t.Fatalf("projected finding = %+v, want the v1 finding carried over intact", finding)
	}
}
