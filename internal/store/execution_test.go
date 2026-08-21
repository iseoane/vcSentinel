package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

func testJob() agentrun.LogicalJob {
	request := agentrun.NewRunRequest(
		"candidate-id",
		"private prompt",
		[]agentrun.Capability{
			agentrun.NewCapability("snapshot", map[string]string{"scope": "read"}),
		},
	)
	return agentrun.NewLogicalJob(request)
}

func TestCreateRunCreatesIdentityRecords(t *testing.T) {
	store := NuevoStore(t.TempDir())
	job := testJob()
	policy := RunPolicy{ID: "policy-id"}

	if err := store.CreateRun(job, policy); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	directory, err := store.executionDir(string(job.RunID()))
	if err != nil {
		t.Fatal(err)
	}

	requestData, err := os.ReadFile(filepath.Join(directory, "request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var request executionRequest
	if err := json.Unmarshal(requestData, &request); err != nil {
		t.Fatalf("request.json is not valid JSON: %v", err)
	}
	if !reflect.DeepEqual(request, requestFor(job)) {
		t.Fatalf("request record = %+v, want %+v", request, requestFor(job))
	}
	if bytes.Contains(requestData, []byte("candidate-id")) ||
		bytes.Contains(requestData, []byte("private prompt")) ||
		bytes.Contains(requestData, []byte("snapshot")) {
		t.Fatalf("request.json contains non-identity content: %s", requestData)
	}

	policyData, err := os.ReadFile(filepath.Join(directory, "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	var storedPolicy RunPolicy
	if err := json.Unmarshal(policyData, &storedPolicy); err != nil {
		t.Fatalf("policy.json is not valid JSON: %v", err)
	}
	if storedPolicy != policy {
		t.Fatalf("policy record = %+v, want %+v", storedPolicy, policy)
	}

	eventsData, err := os.ReadFile(filepath.Join(directory, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(eventsData) != 0 {
		t.Fatalf("initial event log = %q, want empty", eventsData)
	}
}

func TestCreateRunAcceptsIdenticalRetry(t *testing.T) {
	store := NuevoStore(t.TempDir())
	job := testJob()
	policy := RunPolicy{ID: "policy-id"}

	if err := store.CreateRun(job, policy); err != nil {
		t.Fatal(err)
	}
	directory, err := store.executionDir(string(job.RunID()))
	if err != nil {
		t.Fatal(err)
	}
	requestPath := filepath.Join(directory, "request.json")
	policyPath := filepath.Join(directory, "policy.json")
	eventsPath := filepath.Join(directory, "events.jsonl")
	requestBefore, err := os.ReadFile(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	policyBefore, err := os.ReadFile(policyPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eventsPath, []byte("future event\n"), 0600); err != nil {
		t.Fatal(err)
	}
	eventsBefore, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.CreateRun(job, policy); err != nil {
		t.Fatalf("identical retry returned error: %v", err)
	}
	requestAfter, _ := os.ReadFile(requestPath)
	policyAfter, _ := os.ReadFile(policyPath)
	eventsAfter, _ := os.ReadFile(eventsPath)
	if !bytes.Equal(requestBefore, requestAfter) ||
		!bytes.Equal(policyBefore, policyAfter) ||
		!bytes.Equal(eventsBefore, eventsAfter) {
		t.Fatal("identical retry changed an immutable record or the event log")
	}
}

func TestCreateRunRejectsImmutableConflicts(t *testing.T) {
	tests := []struct {
		name        string
		fileName    string
		replacement func(agentrun.LogicalJob) any
	}{
		{
			name:     "request",
			fileName: "request.json",
			replacement: func(job agentrun.LogicalJob) any {
				request := requestFor(job)
				request.PromptID = "different-prompt"
				return request
			},
		},
		{
			name:     "policy",
			fileName: "policy.json",
			replacement: func(agentrun.LogicalJob) any {
				return RunPolicy{ID: "different-policy"}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := NuevoStore(t.TempDir())
			job := testJob()
			if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
				t.Fatal(err)
			}
			directory, err := store.executionDir(string(job.RunID()))
			if err != nil {
				t.Fatal(err)
			}
			replacement, err := marshalRecord(tt.replacement(job))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, tt.fileName)
			if err := os.WriteFile(path, replacement, 0600); err != nil {
				t.Fatal(err)
			}

			err = store.CreateRun(job, RunPolicy{ID: "policy-id"})
			if !errors.Is(err, ErrImmutableConflict) {
				t.Fatalf("CreateRun() error = %v, want immutable conflict", err)
			}
		})
	}
}

func TestExecutionDirRejectsUnsafeRunIDs(t *testing.T) {
	tests := []struct {
		name  string
		runID string
	}{
		{name: "empty", runID: ""},
		{name: "current directory", runID: "."},
		{name: "parent directory", runID: ".."},
		{name: "parent traversal", runID: "../outside"},
		{name: "nested path", runID: "nested/run"},
		{name: "windows path", runID: `C:\outside`},
		{name: "absolute path", runID: `/outside`},
		{name: "null byte", runID: "run\x00id"},
	}
	store := NuevoStore(t.TempDir())

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if directory, err := store.executionDir(tt.runID); err == nil {
				t.Fatalf("executionDir() = %q, want an error", directory)
			}
		})
	}
}

func TestCreateRunUsesLinkedWorktreeCommonDir(t *testing.T) {
	principal := t.TempDir()
	runGit(t, principal, "init", "-q")
	runGit(t, principal, "config", "user.email", "test@vas.sentinel")
	runGit(t, principal, "config", "user.name", "vas-sentinel-test")
	if err := os.WriteFile(filepath.Join(principal, "tracked.txt"), []byte("tracked"), 0600); err != nil {
		t.Fatal(err)
	}
	runGit(t, principal, "add", "tracked.txt")
	runGit(t, principal, "commit", "-q", "-m", "initial")

	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, principal, "worktree", "add", "-q", linked, "-b", "linked")
	principalCommon, err := git.ObtenerGitCommonDir(principal)
	if err != nil {
		t.Fatal(err)
	}
	linkedCommon, err := git.ObtenerGitCommonDir(linked)
	if err != nil {
		t.Fatal(err)
	}
	if principalCommon != linkedCommon {
		t.Fatalf("common directories differ: %s != %s", principalCommon, linkedCommon)
	}

	job := testJob()
	policy := RunPolicy{ID: "policy-id"}
	if err := NuevoStore(principalCommon).CreateRun(job, policy); err != nil {
		t.Fatal(err)
	}
	linkedStore := NuevoStore(linkedCommon)
	if err := linkedStore.CreateRun(job, policy); err != nil {
		t.Fatalf("linked-worktree retry returned error: %v", err)
	}
	directory, err := linkedStore.executionDir(string(job.RunID()))
	if err != nil {
		t.Fatal(err)
	}
	wantDirectory := filepath.Join(principalCommon, "vas-sentinel", "executions", "v1", string(job.RunID()))
	if directory != wantDirectory {
		t.Fatalf("execution directory = %s, want %s", directory, wantDirectory)
	}
	if _, err := os.Stat(filepath.Join(directory, "request.json")); err != nil {
		t.Fatalf("linked worktree cannot see request record: %v", err)
	}
}

func TestAppendReadAndPageEvents(t *testing.T) {
	store := NuevoStore(t.TempDir())
	job := testJob()
	runID := string(job.RunID())
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}

	steps := []struct {
		from, to agentrun.LifecycleState
	}{
		{agentrun.StateCreated, agentrun.StateQueued},
		{agentrun.StateQueued, agentrun.StateAdmitted},
		{agentrun.StateAdmitted, agentrun.StateRunning},
	}
	var receipts []EventReceipt
	for revision, step := range steps {
		event := testEvent(t, job, step.from, step.to)
		receipt, err := store.AppendEvent(runID, event, uint64(revision))
		if err != nil {
			t.Fatalf("AppendEvent(%d): %v", revision, err)
		}
		receipts = append(receipts, receipt)
		if receipt.Sequence != uint64(revision+1) || receipt.Revision != uint64(revision+1) {
			t.Fatalf("receipt = %+v, want sequence and revision %d", receipt, revision+1)
		}
	}
	if receipts[1].PredecessorHash != receipts[0].ContentHash {
		t.Fatal("second receipt does not point to the first content hash")
	}

	first, err := store.ReadEvents(runID, 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Events) != 2 || !first.HasMore || first.NextRevision != 2 {
		t.Fatalf("first page = %+v, want two events and revision cursor 2", first)
	}
	second, err := store.ReadEventPage(runID, first.NextRevision, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Events) != 1 || second.HasMore || second.Events[0].To != agentrun.StateRunning {
		t.Fatalf("second page = %+v, want the final running event", second)
	}
}

func TestExpectedRevisionAllowsOnlyOneConcurrentWriter(t *testing.T) {
	root := t.TempDir()
	firstStore := NuevoStore(root)
	secondStore := NuevoStore(root)
	job := testJob()
	runID := string(job.RunID())
	if err := firstStore.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	event := testEvent(t, job, agentrun.StateCreated, agentrun.StateQueued)

	var wait sync.WaitGroup
	wait.Add(2)
	results := make(chan error, 2)
	go func() {
		defer wait.Done()
		_, err := firstStore.AppendEvent(runID, event, 0)
		results <- err
	}()
	go func() {
		defer wait.Done()
		_, err := secondStore.AppendEvent(runID, event, 0)
		results <- err
	}()
	wait.Wait()
	close(results)

	var successes, conflicts int
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrRevisionConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected concurrent append error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes = %d, conflicts = %d, want one of each", successes, conflicts)
	}
	page, err := firstStore.ReadEvents(runID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("event count = %d, want one committed event", len(page.Events))
	}
}

func TestRebuildProjectionFromValidatedEvents(t *testing.T) {
	store := NuevoStore(t.TempDir())
	job := testJob()
	runID := string(job.RunID())
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	first, err := store.AppendEvent(runID, testEvent(t, job, agentrun.StateCreated, agentrun.StateQueued), 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.AppendEvent(runID, testEvent(t, job, agentrun.StateQueued, agentrun.StateAdmitted), 1)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := store.executionDir(runID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "state.json"), []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := store.RebuildProjection(runID); err != nil {
		t.Fatal(err)
	}
	projection, err := store.ReadProjection(runID)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Revision != 2 || projection.State != agentrun.StateAdmitted || projection.LastEventHash != second.ContentHash {
		t.Fatalf("projection = %+v, want revision 2, admitted, hash %s", projection, second.ContentHash)
	}
	if first.ContentHash == second.ContentHash {
		t.Fatal("distinct event contents must have distinct hashes")
	}
}

func TestEventLogCorruptionFailsClosed(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]EventFrame)
	}{
		{name: "sequence", mutate: func(frames []EventFrame) { frames[0].Sequence = 9 }},
		{name: "predecessor", mutate: func(frames []EventFrame) { frames[1].PredecessorHash = "wrong" }},
		{name: "lifecycle", mutate: func(frames []EventFrame) {
			frames[1].From = agentrun.StateSucceeded
			frames[1].ContentHash = hashEventContent(frames[1].content())
		}},
		{name: "content hash", mutate: func(frames []EventFrame) { frames[0].ContentHash = "wrong" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, runID, path := twoEventLog(t)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			frames := decodeFrames(t, data)
			tt.mutate(frames)
			writeFrames(t, path, frames)
			if _, err := store.ReadEvents(runID, 0, 10); !errors.Is(err, ErrEventCorrupt) {
				t.Fatalf("ReadEvents() error = %v, want corruption", err)
			}
			if err := store.RecoverEventTail(runID); !errors.Is(err, ErrEventCorrupt) {
				t.Fatalf("RecoverEventTail() error = %v, want corruption", err)
			}
		})
	}
}

func TestRecoverIncompleteTailButNotCompleteCorruption(t *testing.T) {
	store := NuevoStore(t.TempDir())
	job := testJob()
	runID := string(job.RunID())
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(runID, testEvent(t, job, agentrun.StateCreated, agentrun.StateQueued), 0); err != nil {
		t.Fatal(err)
	}
	directory, err := store.executionDir(runID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte(`{"sequence":2`)...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadEvents(runID, 0, 10); !errors.Is(err, ErrIncompleteEventTail) {
		t.Fatalf("ReadEvents() error = %v, want incomplete tail", err)
	}
	if err := store.RecoverEventTail(runID); err != nil {
		t.Fatal(err)
	}
	page, err := store.ReadEvents(runID, 0, 10)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("recovered page = %+v, error = %v", page, err)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append([]byte(nil), before...), []byte(`{"sequence":2}`+"\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	corrupt, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecoverEventTail(runID); !errors.Is(err, ErrEventCorrupt) {
		t.Fatalf("RecoverEventTail() error = %v, want corruption", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(corrupt, after) {
		t.Fatal("complete corruption was modified by recovery")
	}
}

func TestWriterTerminationLeavesRecoverableTail(t *testing.T) {
	if os.Getenv("STORE_TAIL_WRITER") == "1" {
		path := os.Getenv("STORE_TAIL_PATH")
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(18)
		}
		_, err = file.Write([]byte(`{"sequence":2`))
		_ = file.Sync()
		_ = file.Close()
		if err != nil {
			os.Exit(19)
		}
		os.Exit(17)
	}

	store := NuevoStore(t.TempDir())
	job := testJob()
	runID := string(job.RunID())
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(runID, testEvent(t, job, agentrun.StateCreated, agentrun.StateQueued), 0); err != nil {
		t.Fatal(err)
	}
	directory, err := store.executionDir(runID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "events.jsonl")
	command := exec.Command(os.Args[0], "-test.run", "^TestWriterTerminationLeavesRecoverableTail$")
	command.Env = append(os.Environ(), "STORE_TAIL_WRITER=1", "STORE_TAIL_PATH="+path)
	err = command.Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 17 {
		t.Fatalf("writer termination error = %v, want exit code 17", err)
	}
	if _, err := store.ReadEvents(runID, 0, 10); !errors.Is(err, ErrIncompleteEventTail) {
		t.Fatalf("ReadEvents() error = %v, want incomplete tail", err)
	}
	if err := store.RecoverEventTail(runID); err != nil {
		t.Fatal(err)
	}
	page, err := store.ReadEvents(runID, 0, 10)
	if err != nil || len(page.Events) != 1 {
		t.Fatalf("readback after writer termination = %+v, error = %v", page, err)
	}
}

func TestExecutionLockDoesNotExpireWhileOwnerIsAlive(t *testing.T) {
	if os.Getenv("STORE_LOCK_HOLDER") == "1" {
		runLockHolder()
	}

	directory := t.TempDir()
	ready := filepath.Join(directory, "ready")
	command := startLockHolder(t, filepath.Join(directory, ".events.lock"), ready, false)
	defer command.Process.Kill()
	waitForFile(t, ready)

	acquired := make(chan error, 1)
	go func() {
		acquired <- withExecutionLock(directory, func() error { return nil })
	}()

	select {
	case err := <-acquired:
		t.Fatalf("lock acquired while its owner was alive: %v", err)
	case <-time.After(5500 * time.Millisecond):
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("lock holder failed: %v", err)
	}
	if err := <-acquired; err != nil {
		t.Fatalf("lock was not acquired after its owner exited: %v", err)
	}
}

func TestExecutionLockIsReleasedWhenOwnerTerminates(t *testing.T) {
	if os.Getenv("STORE_LOCK_HOLDER") == "1" {
		runLockHolder()
	}

	directory := t.TempDir()
	ready := filepath.Join(directory, "ready")
	command := startLockHolder(t, filepath.Join(directory, ".events.lock"), ready, true)
	waitForFile(t, ready)
	if err := command.Wait(); err != nil {
		t.Fatalf("lock holder failed: %v", err)
	}

	started := time.Now()
	if err := withExecutionLock(directory, func() error { return nil }); err != nil {
		t.Fatalf("lock was not released after owner termination: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("lock release took %s, want less than two seconds", elapsed)
	}
}

func startLockHolder(t *testing.T, lockPath, ready string, exitImmediately bool) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run", "^TestExecutionLockDoesNotExpireWhileOwnerIsAlive$")
	command.Env = append(
		os.Environ(),
		"STORE_LOCK_HOLDER=1",
		"STORE_LOCK_PATH="+lockPath,
		"STORE_LOCK_READY="+ready,
		fmt.Sprintf("STORE_LOCK_EXIT=%t", exitImmediately),
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	return command
}

func runLockHolder() {
	lock, err := acquireExecutionLock(os.Getenv("STORE_LOCK_PATH"), time.Second)
	if err != nil {
		os.Exit(18)
	}
	if err := os.WriteFile(os.Getenv("STORE_LOCK_READY"), []byte("ready"), 0600); err != nil {
		os.Exit(19)
	}
	if os.Getenv("STORE_LOCK_EXIT") == "true" {
		os.Exit(0)
	}
	time.Sleep(6 * time.Second)
	if err := lock.Close(); err != nil {
		os.Exit(20)
	}
	os.Exit(0)
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func testEvent(t *testing.T, job agentrun.LogicalJob, from, to agentrun.LifecycleState) agentrun.NormalizedEvent {
	t.Helper()
	invocation, err := agentrun.NewRootInvocation(job, 1, agentrun.DecisionStart)
	if err != nil {
		t.Fatal(err)
	}
	event, err := agentrun.NewNormalizedEvent(invocation, from, to, agentrun.DecisionStart, time.Unix(100, int64(to[0])))
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func twoEventLog(t *testing.T) (*Store, string, string) {
	t.Helper()
	store := NuevoStore(t.TempDir())
	job := testJob()
	runID := string(job.RunID())
	if err := store.CreateRun(job, RunPolicy{ID: "policy-id"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(runID, testEvent(t, job, agentrun.StateCreated, agentrun.StateQueued), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AppendEvent(runID, testEvent(t, job, agentrun.StateQueued, agentrun.StateAdmitted), 1); err != nil {
		t.Fatal(err)
	}
	directory, err := store.executionDir(runID)
	if err != nil {
		t.Fatal(err)
	}
	return store, runID, filepath.Join(directory, "events.jsonl")
}

func decodeFrames(t *testing.T, data []byte) []EventFrame {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	frames := make([]EventFrame, len(lines))
	for i, line := range lines {
		if err := json.Unmarshal(line, &frames[i]); err != nil {
			t.Fatal(err)
		}
	}
	return frames
}

func writeFrames(t *testing.T, path string, frames []EventFrame) {
	t.Helper()
	data := make([]byte, 0)
	for _, frame := range frames {
		encoded, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, encoded...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func runGit(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
