package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/doctor"
	"github.com/ISeoane-Quental/vas.sentinel/internal/graph"
)

func stubDoctorEnv(t *testing.T) doctor.Env {
	t.Helper()
	common := t.TempDir()
	conds := []graph.Condition{{Name: graph.CondBinary, OK: false, Detail: "codegraph not found on PATH"}}
	return doctor.Env{
		GitCommonDir:  func(string) (string, error) { return common, nil },
		LatestRelease: func() (string, error) { return "v9.9.9", nil },
		Codegraph:     func(string) []graph.Condition { return conds },
		Probe:         func(agent, prompt string) (string, error) { return "", errors.New("probe refused") },
	}
}
func TestParseDoctorArgs(t *testing.T) {
	updates, err := parseDoctorArgs(nil)
	if err != nil || updates {
		t.Errorf("parseDoctorArgs(nil) = (%v, %v), want (false, nil)", updates, err)
	}
	updates, err = parseDoctorArgs([]string{"--check-updates"})
	if err != nil || !updates {
		t.Errorf("parseDoctorArgs(--check-updates) = (%v, %v), want (true, nil)", updates, err)
	}
	if _, err := parseDoctorArgs([]string{"--json"}); err == nil {
		t.Errorf("parseDoctorArgs(--json) = nil, want an error")
	}
}

func TestValidateArgumentsDoctor(t *testing.T) {
	if msg := validateArguments("doctor", nil); msg != "" {
		t.Errorf("valid doctor args rejected: %s", msg)
	}
	if msg := validateArguments("doctor", []string{"--check-updates"}); msg != "" {
		t.Errorf("valid doctor flag rejected: %s", msg)
	}
	if msg := validateArguments("doctor", []string{"--json"}); msg == "" {
		t.Errorf("invalid doctor flag accepted")
	} else if !strings.Contains(msg, "doctor") {
		t.Errorf("rejection = %q, want it to name the command", msg)
	}
}

func TestRunDoctorIsAdvisory(t *testing.T) {
	worktree := t.TempDir()
	out := &strings.Builder{}
	if code := runDoctorWith(out, worktree, "0.2.0", false, stubDoctorEnv(t)); code != 0 {
		t.Fatalf("exit = %d on an all-warn report, want 0 (advisory)", code)
	}
	text := out.String()
	if !strings.Contains(text, "WARN") || !strings.Contains(text, "remedy") {
		t.Errorf("report misses warnings or remedies:\n%s", text)
	}
}
