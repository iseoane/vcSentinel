package main

import (
	"errors"
	"os"
	"path/filepath"
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

// TestRunDoctorWithPrintsProgressBeforeProbe pins the wiring: the
// announcement for each long step reaches the writer before the blocking
// call starts, so the operator sees motion during the 60s probes. The stub
// probe snapshots the writer at probe time; a missing announcement means
// the operator stared at silence.
func TestRunDoctorWithPrintsProgressBeforeProbe(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep the host's global agents out of the run
	worktree := t.TempDir()
	dir := filepath.Join(worktree, ".vas_sentinel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "version: \"2.0\"\nactive_agent: \"auto\"\nagents:\n  claude:\n    model: \"claude-5-sonnet\"\n    reasoning_effort: \"high\"\n"
	if err := os.WriteFile(filepath.Join(dir, "vassentinel.yml"), []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	env := stubDoctorEnv(t)
	seenAtProbe := ""
	env.Probe = func(agent, prompt string) (string, error) {
		if seenAtProbe == "" { // snapshot the first probe (declaration order)
			seenAtProbe = out.String()
		}
		return "", errors.New("probe refused")
	}
	out.Reset()
	if code := runDoctorWith(&out, worktree, "0.2.0", false, env); code != 0 {
		t.Fatalf("exit = %d, want 0 (advisory)", code)
	}
	if seenAtProbe != "probing claude…\n" {
		t.Fatalf("writer held %q when the probe started, want the flushed announcement", seenAtProbe)
	}
	if !strings.Contains(out.String(), "probing claude…\n") {
		t.Fatalf("output misses the announcement:\n%s", out.String())
	}
}
