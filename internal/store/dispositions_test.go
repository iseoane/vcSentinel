package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

func dispositionFixture(sha, fingerprint, status string) *review.FindingDisposition {
	return &review.FindingDisposition{
		SHA: sha, Fingerprint: fingerprint, Status: status,
		Reason: "verified safe", Path: "a.go", LineStart: 1, LineEnd: 2,
		Evidence: "criticalCall()", RangeHash: "1f49",
		Actor: review.RefutationActorHuman, Source: review.DispositionSourceHuman,
		At:              time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC),
		TargetDimension: review.DimLogic, TargetLine: 2, TargetDescription: "bug",
	}
}

// FU-6: the dispositions log is append-only and separate from the immutable
// review revisions: two appends accumulate in order, never overwrite.
func TestAppendDispositionAccumulatesInOrder(t *testing.T) {
	st := NuevoStore(t.TempDir())
	first := dispositionFixture("sha-a", "fp-1", review.StatusRefuted)
	second := dispositionFixture("sha-b", "fp-2", review.StatusAcceptedByUser)

	if err := st.AppendDisposition(first); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := st.AppendDisposition(second); err != nil {
		t.Fatalf("append: %v", err)
	}

	got, err := st.ReadDispositions()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 2 || got[0].Fingerprint != "fp-1" || got[1].Fingerprint != "fp-2" {
		t.Fatalf("dispositions = %+v, want both records in order", got)
	}
	if got[0].Actor != review.RefutationActorHuman || got[0].Source != review.DispositionSourceHuman {
		t.Fatalf("first = %+v, want human provenance persisted", got[0])
	}
	if got[1].Status != review.StatusAcceptedByUser {
		t.Fatalf("second status = %q, want the recorded status", got[1].Status)
	}
}

// FU-6: an absent log reads as no answers, never as an error.
func TestReadDispositionsAbsentLogIsEmpty(t *testing.T) {
	st := NuevoStore(t.TempDir())
	got, err := st.ReadDispositions()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("dispositions = %+v, want empty", got)
	}
}

// FU-6: corrupt persistence fails closed: a corrupt line and a record
// missing its address both abort the whole read.
func TestReadDispositionsRejectsCorruptPersistence(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"unparseable line", "{not json"},
		{"missing address", `{"sha":"","fingerprint":"","status":"refuted"}`},
		{"missing status", `{"sha":"a","fingerprint":"b","status":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "vas-sentinel", "dispositions.jsonl")
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.line+"\n"), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := NuevoStore(dir).ReadDispositions(); err == nil {
				t.Fatal("corrupt persistence was accepted")
			}
		})
	}
}

// FU-6: appending validates the address, vocabulary, and provenance instead
// of persisting a record no consumer could interpret.
func TestAppendDispositionRejectsInvalidRecords(t *testing.T) {
	valid := func() *review.FindingDisposition {
		return dispositionFixture("sha-a", "fp-1", review.StatusRefuted)
	}
	cases := []struct {
		name   string
		mutate func(*review.FindingDisposition)
	}{
		{"missing sha", func(d *review.FindingDisposition) { d.SHA = "  " }},
		{"missing fingerprint", func(d *review.FindingDisposition) { d.Fingerprint = "" }},
		{"unknown status", func(d *review.FindingDisposition) { d.Status = "ignored" }},
		{"missing actor", func(d *review.FindingDisposition) { d.Actor = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := NuevoStore(t.TempDir())
			d := valid()
			tc.mutate(d)
			if err := st.AppendDisposition(d); err == nil {
				t.Fatal("invalid record was accepted")
			}
			got, err := st.ReadDispositions()
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if len(got) != 0 {
				t.Fatalf("dispositions = %+v, want nothing persisted on failure", got)
			}
		})
	}
}

// FU-6: per-SHA selection serves every consumer the answers recorded
// against the revision it evaluates.
func TestReadDispositionsForSHAFilters(t *testing.T) {
	st := NuevoStore(t.TempDir())
	if err := st.AppendDisposition(dispositionFixture("sha-a", "fp-1", review.StatusRefuted)); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendDisposition(dispositionFixture("sha-b", "fp-2", review.StatusFixed)); err != nil {
		t.Fatal(err)
	}

	got, err := st.ReadDispositionsForSHA("sha-b")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || got[0].Fingerprint != "fp-2" {
		t.Fatalf("dispositions = %+v, want only the sha-b answer", got)
	}
	absent, err := st.ReadDispositionsForSHA("sha-absent")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(absent) != 0 {
		t.Fatalf("dispositions = %+v, want none for an unanswered SHA", absent)
	}
}

// FU-6: statuses are canonicalised on write, so padded producer spellings
// still join the same lifecycle every consumer compares through
// NormalizeStatus.
func TestAppendDispositionCanonicalisesStatus(t *testing.T) {
	st := NuevoStore(t.TempDir())
	d := dispositionFixture("sha-a", "fp-1", "  REFUTED  ")
	if err := st.AppendDisposition(d); err != nil {
		t.Fatalf("append: %v", err)
	}
	if d.Status != review.StatusRefuted {
		t.Fatalf("status = %q, want canonical", d.Status)
	}
	if !strings.Contains(d.RangeHash, "1f49") {
		t.Fatalf("record = %+v, want the evidence hash preserved", d)
	}
}
