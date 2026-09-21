package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/doctor"
	"github.com/ISeoane-Quental/vcSentinel/internal/graph"
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

// TestRunDoctorWithPrintsProgressBeforeProbe pins the wiring over a
// two-agent yml: each configured agent's announcement reaches the writer
// before ITS blocking probe starts, so the operator sees motion during the
// 60s probes. The stub probe snapshots the writer at probe time; a missing
// announcement means the operator stared at silence. The assertions are
// containment over that snapshot, not the whole rendered report: only the
// flush-before-probe property is the contract here.
func TestRunDoctorWithPrintsProgressBeforeProbe(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // keep the host's global agents out of the run
	stageDoctorBinaries(t)        // resolution must not depend on the host PATH
	worktree := t.TempDir()
	dir := filepath.Join(worktree, ".vas_sentinel")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	yml := "version: \"2.0\"\nactive_agent: \"auto\"\nagents:\n  claude:\n    model: \"claude-5-sonnet\"\n    reasoning_effort: \"high\"\n  opencode:\n    model: \"deepseek-v4-flash-free\"\n    reasoning_effort: \"default\"\n"
	if err := os.WriteFile(filepath.Join(dir, "vassentinel.yml"), []byte(yml), 0o600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	env := stubDoctorEnv(t)
	// Each probe snapshots the writer and is recorded explicitly: the slice
	// length pins one probe per configured agent, so a probe that never ran
	// is a loud failure instead of an invisible empty-string sentinel.
	type probeSnapshot struct {
		agent  string
		writer string
	}
	var probes []probeSnapshot
	env.Probe = func(agent, prompt string) (string, error) {
		probes = append(probes, probeSnapshot{agent: agent, writer: out.String()})
		return "", errors.New("probe refused")
	}
	if code := runDoctorWith(&out, worktree, "0.2.0", false, env); code != 0 {
		t.Fatalf("exit = %d, want 0 (advisory)", code)
	}
	if len(probes) != 2 {
		t.Fatalf("probes recorded = %+v, want exactly one per configured agent (claude, opencode)", probes)
	}
	for _, probe := range probes {
		if !strings.Contains(probe.writer, "probing "+probe.agent+"…\n") {
			t.Fatalf("writer held %q when the %s probe started, want its flushed announcement", probe.writer, probe.agent)
		}
	}
}

// stageDoctorBinaries replaces PATH with a directory holding only the
// executable stubs doctor resolution needs (claude, opencode, rg).
// Replacement, not prepend, keeps real host binaries from leaking into
// resolution (same discipline as internal/doctor's stageBinaries): the test
// must pass on hosts that have none of the agents installed.
func stageDoctorBinaries(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"claude", "opencode", "rg"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}
