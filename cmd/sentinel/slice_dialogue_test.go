package main

import (
	"bufio"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

// withInput replaces stdinReader with a reader that returns s, and restores
// the original reader when the test ends (t.Cleanup). It lets the tests of
// package main steer the answers of the interactive flows without touching
// the test process's real standard input.
func withInput(t *testing.T, s string) {
	t.Helper()
	original := stdinReader
	t.Cleanup(func() { stdinReader = original })
	stdinReader = bufio.NewReader(strings.NewReader(s))
}

// --- approveAndExecute ---

// TestApproveAndExecute_Cancel: option C cancels and returns false without
// trying to execute the plan (nor touching git).
func TestApproveAndExecute_Cancel(t *testing.T) {
	withInput(t, "c\n")
	plan := &git.FragmentationPlan{}

	if approveAndExecute(plan, ".") {
		t.Fatal("expected false when canceling, got true")
	}
}

// TestApproveAndExecute_InvalidOptionReprompts: an unrecognized option does
// not abort the dialog; it must show the menu again and accept the next
// valid answer.
func TestApproveAndExecute_InvalidOptionReprompts(t *testing.T) {
	withInput(t, "xyz\nc\n")
	plan := &git.FragmentationPlan{}

	if approveAndExecute(plan, ".") {
		t.Fatal("expected false after an invalid option followed by cancel, got true")
	}
}

// TestApproveAndExecute_EmptyEnterApprovesWithEmptyPlan: an empty Enter is
// equivalent to approving (case "", "a", "approve"). A plan without batches
// is used so runApprovedPlan creates no real commit (RunFragmentationPlan
// with an empty Batches runs no write git command).
func TestApproveAndExecute_EmptyEnterApprovesWithEmptyPlan(t *testing.T) {
	withInput(t, "\n")
	plan := &git.FragmentationPlan{}

	if !approveAndExecute(plan, ".") {
		t.Fatal("expected true when approving with an empty Enter, got false")
	}
}

// TestApproveAndExecute_EOFCancels pins the correct behavior after B9
// (CRITICAL): an EOF from stdin (empty input, not even a '\n') must CANCEL
// the plan, not approve it. readLine() returns an error on EOF; unlike a real
// empty Enter (line = "", which must approve, it is the default the menu
// itself announces, "Choice [A]:"), a closed stdin is not a user answer and
// cannot be interpreted as "yes, execute". A closed stdin (CI, nohup, a
// pipeline that ends, an agent without a console) must not skip the only
// human control over unlocking the guardian.
func TestApproveAndExecute_EOFCancels(t *testing.T) {
	withInput(t, "") // empty reader: ReadString('\n') returns io.EOF immediately
	plan := &git.FragmentationPlan{}

	result := approveAndExecute(plan, ".")
	if result {
		t.Fatal("B9: an EOF from stdin must not approve the plan, it must cancel it (false)")
	}
}

// TestApproveAndExecute_DoesNotLoopForeverAfterEOF ensures an EOF does not
// leave approveAndExecute retrying readLine indefinitely: the function must
// return on the first iteration. If readLine() ever returned EOF but the loop
// did not recognize the error, this test would hang (the test runner would
// kill it on timeout), betraying the defect.
func TestApproveAndExecute_DoesNotLoopForeverAfterEOF(t *testing.T) {
	withInput(t, "")
	plan := &git.FragmentationPlan{}

	done := make(chan bool, 1)
	go func() {
		done <- approveAndExecute(plan, ".")
	}()

	select {
	case <-done:
		// No hang: correct, regardless of whether the result is approve or
		// cancel (the previous test already covers that).
	case <-time.After(5 * time.Second):
		t.Fatal("approveAndExecute did not return after EOF: possible infinite loop")
	}
}

// --- buildOversizedDecision ---

func testOversizedFile() git.ModifiedFile {
	return git.ModifiedFile{Path: "backend/giant.go", Lines: 900, Layer: "backend"}
}

// TestBuildOversizedDecision_AbortsWithOption3: option 3 aborts the
// operation by returning (false, nil): BuildFragmentationPlan turns it into
// the "fragmentation aborted" error.
func TestBuildOversizedDecision_AbortsWithOption3(t *testing.T) {
	withInput(t, "3\n")
	decide := buildOversizedDecision(".")

	bypass, err := decide(testOversizedFile())
	if err != nil {
		t.Fatalf("aborting should not return an error of its own, returned: %v", err)
	}
	if bypass {
		t.Fatal("expected bypass=false when aborting with option 3")
	}
}

// TestBuildOversizedDecision_Bypass: option 2 bypasses and returns (true,
// nil) without asking for anything else.
func TestBuildOversizedDecision_Bypass(t *testing.T) {
	withInput(t, "2\n")
	decide := buildOversizedDecision(".")

	bypass, err := decide(testOversizedFile())
	if err != nil {
		t.Fatalf("bypass should not return an error, returned: %v", err)
	}
	if !bypass {
		t.Fatal("expected bypass=true with option 2")
	}
}

// TestBuildOversizedDecision_InvalidOptionReprompts: an option outside 1-3
// does not abort the menu; it must ask again and accept the next valid
// option (here, 3 to abort).
func TestBuildOversizedDecision_InvalidOptionReprompts(t *testing.T) {
	withInput(t, "9\n3\n")
	decide := buildOversizedDecision(".")

	bypass, err := decide(testOversizedFile())
	if err != nil {
		t.Fatalf("it should not return an error: %v", err)
	}
	if bypass {
		t.Fatal("expected bypass=false after an invalid option followed by abort")
	}
}

// --- decideAction (level 2: pure function extracted from approveAndExecute) ---

// TestDecideAction covers, in a table and without stdin, the mapping of each
// read line to the action approveAndExecute must take. It separates the pure
// decision from stdin reading and from execution, so it can be tested without
// simulating input or touching the plan or git.
func TestDecideAction(t *testing.T) {
	scenarios := []struct {
		name string
		line string
		want planAction
	}{
		{"empty_approves", "", actionApprove},
		{"a_approves", "a", actionApprove},
		{"A_uppercase_approves", "A", actionApprove},
		{"approve_full_word", "approve", actionApprove},
		{"spaces_around_a_approve", "  a  ", actionApprove},
		{"r_regenerates", "r", actionRegenerate},
		{"regenerate_full_word", "regenerate", actionRegenerate},
		{"e_edits", "e", actionEdit},
		{"edit_full_word", "edit", actionEdit},
		{"c_cancels", "c", actionCancel},
		{"cancel_full_word", "cancel", actionCancel},
		{"unknown_option_is_invalid", "xyz", actionInvalid},
	}

	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			if got := decideAction(scenario.line); got != scenario.want {
				t.Errorf("decideAction(%q) = %v, want %v", scenario.line, got, scenario.want)
			}
		})
	}
}

// TestBuildOversizedDecision_EOFReturnsError: unlike approveAndExecute,
// buildOversizedDecision DOES propagate the readLine error instead of
// translating it into a default option: an EOF here neither approves nor
// bypasses, it returns an explicit error.
func TestBuildOversizedDecision_EOFReturnsError(t *testing.T) {
	withInput(t, "")
	decide := buildOversizedDecision(".")

	_, err := decide(testOversizedFile())
	if err == nil {
		t.Fatal("expected an error when reading the option after EOF, got nil")
	}
}
