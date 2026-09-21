package review

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recordV1 is a record written before T0.2: no agent, model or effort in
// the revision. It must keep reading without error.
const recordV1 = `{
  "sha": "6c079a8",
  "message": "feat: something",
  "bucket": "backend",
  "model": "default",
  "revisions": [
    {"at": "2026-01-15T10:00:00Z", "result": "ok", "dims": [{"dim": "seguridad", "verdict": "ok"}]}
  ]
}`

// TestReadRecordV1WithoutAuthorshipFields covers the T0.2 compatibility
// acceptance: the new fields are optional.
func TestReadRecordV1WithoutAuthorshipFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vcsentinel")
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatalf("could not create the directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "6c079a8.json"), []byte(recordV1), 0644); err != nil {
		t.Fatalf("could not write the record: %v", err)
	}

	ledger := NewLedger(dir)
	record, err := ledger.ReadRecord("6c079a8")
	if err != nil {
		t.Fatalf("a v1 record must read without error: %v", err)
	}
	if record == nil {
		t.Fatal("the record was not read")
	}
	if len(record.Revisions) != 1 {
		t.Fatalf("revisions = %d, want 1", len(record.Revisions))
	}
	revision := record.Revisions[0]
	if revision.Result != "ok" {
		t.Errorf("result = %q, want ok", revision.Result)
	}
	if revision.Agent != "" || revision.Model != "" || revision.Effort != "" {
		t.Errorf("a v1 record must not invent authorship: %+v", revision)
	}
}

// TestRevisionWithoutAuthorshipOmitsTheFields: empty fields do not appear in
// the JSON, so a new record without an author stays identical to a v1 one.
func TestRevisionWithoutAuthorshipOmitsTheFields(t *testing.T) {
	data, err := json.Marshal(Revision{At: time.Now(), Result: "ok"})
	if err != nil {
		t.Fatalf("could not serialize: %v", err)
	}
	for _, field := range []string{"agent", "model", "effort"} {
		if strings.Contains(string(data), `"`+field+`"`) {
			t.Errorf("empty field %q should not be serialized: %s", field, data)
		}
	}
}

// TestRevisionWithAuthorshipSerializes: when there is an author, it is stored.
func TestRevisionWithAuthorshipSerializes(t *testing.T) {
	data, err := json.Marshal(Revision{Result: "ok", Agent: "opencode", Model: "sonnet", Effort: "high"})
	if err != nil {
		t.Fatalf("could not serialize: %v", err)
	}
	for _, expected := range []string{`"agent":"opencode"`, `"model":"sonnet"`, `"effort":"high"`} {
		if !strings.Contains(string(data), expected) {
			t.Errorf("missing %s in: %s", expected, data)
		}
	}
}
