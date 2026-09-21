package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/git"
)

func TestCheckCriticalVolumeIsAdvisory(t *testing.T) {
	var output bytes.Buffer
	exitCode := runCheckWith(&output, t.TempDir(), false, func() (git.PendingVolume, error) {
		return git.PendingVolume{Blocking: 401, State: "CRITICAL"}, nil
	})

	if exitCode != 0 {
		t.Fatalf("critical advisory check exit code = %d, want 0\n%s", exitCode, output.String())
	}
	if !strings.Contains(output.String(), "CRITICAL") {
		t.Errorf("text output does not preserve the critical state: %s", output.String())
	}
	if !strings.Contains(output.String(), "vcsentinel slice plan --json") {
		t.Errorf("critical output does not recommend a split plan: %s", output.String())
	}
}

func TestCheckJSONPreservesVolumeAndAdvisoryState(t *testing.T) {
	var output bytes.Buffer
	exitCode := runCheckWith(&output, "worktree", true, func() (git.PendingVolume, error) {
		return git.PendingVolume{Blocking: 450, Informational: 12, State: "CRITICAL"}, nil
	})
	if exitCode != 0 {
		t.Fatalf("JSON check exit code = %d", exitCode)
	}

	var report struct {
		Worktree           string `json:"worktree"`
		AuthoredLines      int    `json:"authored_lines"`
		InformationalLines int    `json:"informational_lines"`
		State              string `json:"state"`
		Advisory           bool   `json:"advisory"`
		Recommendation     string `json:"recommendation"`
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("invalid check JSON: %v\n%s", err, output.String())
	}
	if report.Worktree != "worktree" || report.AuthoredLines != 450 || report.InformationalLines != 12 {
		t.Errorf("JSON volume = %+v", report)
	}
	if report.State != "CRITICAL" || !report.Advisory {
		t.Errorf("JSON advisory state = %+v", report)
	}
	if report.Recommendation != "vcsentinel slice plan --json" {
		t.Errorf("JSON recommendation = %q", report.Recommendation)
	}
}

func TestCheckMeasurementFailureRemainsBlockingAndDistinct(t *testing.T) {
	var output bytes.Buffer
	exitCode := runCheckWith(&output, "worktree", true, func() (git.PendingVolume, error) {
		return git.PendingVolume{State: "ERROR"}, errors.New("git diff failed")
	})
	if exitCode != 1 {
		t.Fatalf("measurement failure exit code = %d, want 1", exitCode)
	}

	var report struct {
		State    string `json:"state"`
		Advisory bool   `json:"advisory"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("invalid failure JSON: %v\n%s", err, output.String())
	}
	if report.State != "ERROR" || report.Advisory || !strings.Contains(report.Error, "git diff failed") {
		t.Errorf("JSON failure report = %+v", report)
	}
}
