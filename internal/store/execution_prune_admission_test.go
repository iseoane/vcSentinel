// Prune admission-window tests (ticket 14): CreateRun's record-write section
// now runs under the same cross-process execution event lock that prune
// removals hold, so an admission racing a removal of the same directory can
// only end refused or survived — never deleted out from under a successful
// admission verdict. The remnant cleanup additionally refuses when real
// content appeared inside its locked section, keeping pure lock-only
// remnants self-healing.
package store

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
)

// writeRemnantShape leaves a directory holding only its event-lock file: the
// exact on-disk shape of a crash-interrupted removal, placed under the run
// identity the given job will admit with.
func writeRemnantShape(t *testing.T, s *Store, job agentrun.LogicalJob) string {
	t.Helper()
	directory := filepath.Join(s.dir, "executions", "v1", string(job.RunID()))
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, ".events.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	return directory
}

// rebirthJob returns the logical job whose admission will repopulate the
// remnant directory.
func rebirthJob(name string) agentrun.LogicalJob {
	return agentrun.NewLogicalJob(agentrun.NewRunRequest(
		agentrun.Candidate("candidate:"+name), agentrun.Prompt(name), nil))
}

// TestPruneExecutionsRefusesRemnantThatGrewAdmissionRecords proves the closed
// admission window for the crash-remnant path: a full admission landing
// between classification ("lock-only remnant") and the locked cleanup must
// survive. Without the in-lock content guard the cleanup wiped every child
// unconditionally and destroyed exactly what had just been admitted.
func TestPruneExecutionsRefusesRemnantThatGrewAdmissionRecords(t *testing.T) {
	s := NuevoStore(t.TempDir())
	job := rebirthJob("zombie-rebirth")
	runID := string(job.RunID())
	writeRemnantShape(t, s, job)

	setRaceWindowHook(t, func(s *Store) {
		// The admission lands after classification saw a lock-only remnant
		// and before any removal runs.
		if err := s.CreateRun(job, RunPolicy{ID: "policy:test"}); err != nil {
			t.Fatalf("CreateRun over the remnant error = %v", err)
		}
	})

	report, err := s.PruneExecutions(time.Now().Add(-24*time.Hour), nil)
	if err != nil {
		t.Fatal(err)
	}
	assertKept(t, report, runID, PruneReasonIncompleteAdmission)
	if report.Pruned != 0 {
		t.Fatalf("pruned %d runs, want zero: %+v", report.Pruned, report.Decisions)
	}
	request, requestErr := s.ReadExecutionRequest(runID)
	if requestErr != nil || request.CandidateID == "" {
		t.Fatalf("admitted records did not survive the racing removal: %+v, %v", request, requestErr)
	}
}

// TestCreateRunRacingSameRecordRemovalNeverLosesAdmission hammers the true
// concurrency seam: one goroutine finishes a crash-interrupted removal while
// another re-admits the same identity. Every interleaving must satisfy the
// honest-outcome invariant: whenever CreateRun reports success, the admitted
// records exist afterwards; a removal that ran after admission refused or
// failed instead of destroying them.
func TestCreateRunRacingSameRecordRemovalNeverLosesAdmission(t *testing.T) {
	const iterations = 20
	for i := 0; i < iterations; i++ {
		t.Run(fmt.Sprintf("iteration-%02d", i), func(t *testing.T) {
			s := NuevoStore(t.TempDir())
			job := rebirthJob("racing-admission")
			runID := string(job.RunID())
			writeRemnantShape(t, s, job)

			var wg sync.WaitGroup
			admitted := make(chan error, 1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				admitted <- s.CreateRun(job, RunPolicy{ID: "policy:test"})
			}()
			wg.Add(1)
			go func() {
				defer wg.Done()
				// The concurrent cleaner: exactly what the next prune pass
				// would do with a lock-only remnant.
				_ = s.removeExecutionRemnant(runID)
			}()
			wg.Wait()

			if err := <-admitted; err != nil {
				t.Fatalf("CreateRun error = %v; the immutable identities are fresh, so admission must succeed", err)
			}
			request, requestErr := s.ReadExecutionRequest(runID)
			if requestErr != nil {
				t.Fatalf("admission reported success but the record is gone: %v", requestErr)
			}
			if request.CandidateID == "" {
				t.Fatal("admitted request record decodes empty")
			}
		})
	}
}
