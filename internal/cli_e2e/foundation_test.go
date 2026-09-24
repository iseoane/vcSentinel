package cli_e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCLIFoundationSetup(t *testing.T) {
	runner := newCLIRunner(t)
	rootStatusBefore := runner.gitStatus(repositoryRoot(t))
	realHomeStateBefore := pathState(t, filepath.Join(realHome(t), ".vcsentinel", "repositories.json"))

	runner.writeFile("AGENTS.md", "# Agent instructions\n")
	runner.writeFile("CLAUDE.md", "# Claude instructions\n")
	runner.writeFile("base.txt", "base\n")
	initialHead := strings.TrimSpace(runner.commit("test: seed foundation repository"))

	nested := filepath.Join(runner.repository, "nested", "working", "directory")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("create nested repository directory: %v", err)
	}

	firstInit := runner.runInDir(runner.binary, nested, "init")
	if firstInit.ExitCode != 0 {
		t.Fatalf("init from nested directory failed:\n%s", firstInit.Diagnostic())
	}

	artifacts := newFoundationArtifacts(t, runner)
	assertFoundationInstalled(t, artifacts)
	assertHeadIs(t, runner, initialHead, "after init")
	firstState := readFoundationState(t, artifacts)

	secondInit := runner.runInDir(runner.binary, nested, "init")
	if secondInit.ExitCode != 0 {
		t.Fatalf("repeated init from nested directory failed:\n%s", secondInit.Diagnostic())
	}
	assertFoundationInstalled(t, artifacts)
	assertFoundationStateEqual(t, firstState, readFoundationState(t, artifacts))
	assertHeadIs(t, runner, initialHead, "after repeated init")

	t.Run("pre-commit hook rejects oversized staged change", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("POSIX pre-commit shell-hook enforcement is skipped on Windows; setup assertions remain active")
		}

		const oversizedName = "oversized.go"
		runner.writeFile(oversizedName, foundationOversizedGoSource(405))
		runner.mustGit("add", "--", oversizedName)
		defer func() {
			cleanup := runner.git("rm", "--cached", "--force", "--", oversizedName)
			if cleanup.ExitCode != 0 {
				t.Errorf("clean the rejected file from the index failed:\n%s", cleanup.Diagnostic())
				return
			}
			if staged := runner.git("diff", "--cached", "--quiet"); staged.ExitCode != 0 {
				t.Errorf("rejected change remained staged:\n%s", staged.Diagnostic())
			}
		}()

		commit := runner.git("commit", "--quiet", "--message", "test: reject oversized staged change")
		if commit.ExitCode == 0 {
			t.Fatalf("the pre-commit hook accepted an oversized change:\n%s", commit.Diagnostic())
		}
		output := commit.Stdout + "\n" + commit.Stderr
		if !strings.Contains(output, "Staged commit rejected") || !strings.Contains(output, "400-line review budget") {
			t.Fatalf("hook rejection did not report the staged review budget:\n%s", commit.Diagnostic())
		}
		assertHeadIs(t, runner, initialHead, "after rejected oversized commit")
	})

	uninit := runner.runInDir(runner.binary, nested, "uninit")
	if uninit.ExitCode != 0 {
		t.Fatalf("uninit from nested directory failed:\n%s", uninit.Diagnostic())
	}
	assertFoundationRemoved(t, artifacts)
	if got := readFoundationFile(t, artifacts.rules["AGENTS.md"]); got != "# Agent instructions\n" {
		t.Fatalf("uninit changed the original AGENTS.md content: %q", got)
	}
	if got := readFoundationFile(t, artifacts.rules["CLAUDE.md"]); got != "# Claude instructions\n" {
		t.Fatalf("uninit changed the original CLAUDE.md content: %q", got)
	}
	assertHeadIs(t, runner, initialHead, "after uninit")

	if got := runner.gitStatus(repositoryRoot(t)); got != rootStatusBefore {
		t.Fatalf("real repository status changed:\nbefore: %q\nafter:  %q", rootStatusBefore, got)
	}
	if got := pathState(t, filepath.Join(realHome(t), ".vcsentinel", "repositories.json")); got != realHomeStateBefore {
		t.Fatalf("real home repository registry changed:\nbefore: %q\nafter:  %q", realHomeStateBefore, got)
	}
}

const (
	foundationMarkerBegin = "<!-- vcsentinel:begin -->"
	foundationMarkerEnd   = "<!-- vcsentinel:end -->"
	foundationRuleText    = "CRITICAL VOLUME RULE"
	foundationSkillMarker = "<!-- vcsentinel:managed-skill -->"
	foundationHookMarker  = "# vcsentinel:pre-commit-hook:v1"
)

type foundationArtifacts struct {
	config string
	rules  map[string]string
	skill  string
	hook   string
}

type foundationState struct {
	config []byte
	rules  map[string][]byte
	skill  []byte
	hook   []byte
}

func newFoundationArtifacts(t *testing.T, runner *cliRunner) foundationArtifacts {
	t.Helper()
	commonDirResult := runner.git("rev-parse", "--git-common-dir")
	if commonDirResult.ExitCode != 0 {
		t.Fatalf("resolve the repository common directory:\n%s", commonDirResult.Diagnostic())
	}
	commonDir := filepath.FromSlash(strings.TrimSpace(commonDirResult.Stdout))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(runner.repository, commonDir)
	}
	commonDir = filepath.Clean(commonDir)
	return foundationArtifacts{
		config: filepath.Join(runner.repository, ".vcsentinel", "vcsentinel.yml"),
		rules: map[string]string{
			"AGENTS.md":      filepath.Join(runner.repository, "AGENTS.md"),
			"CLAUDE.md":      filepath.Join(runner.repository, "CLAUDE.md"),
			".claudecode.md": filepath.Join(runner.repository, ".claudecode.md"),
		},
		skill: filepath.Join(runner.repository, ".agents", "skills", "vcsentinel", "SKILL.md"),
		hook:  filepath.Join(commonDir, "hooks", "pre-commit"),
	}
}

func assertFoundationInstalled(t *testing.T, artifacts foundationArtifacts) {
	t.Helper()
	config := readFoundationFile(t, artifacts.config)
	if strings.TrimSpace(config) == "" {
		t.Error("project configuration is empty")
	}

	for name, path := range artifacts.rules {
		content := readFoundationFile(t, path)
		if strings.Count(content, foundationMarkerBegin) != 1 {
			t.Errorf("%s has %d begin markers, want 1", name, strings.Count(content, foundationMarkerBegin))
		}
		if strings.Count(content, foundationMarkerEnd) != 1 {
			t.Errorf("%s has %d end markers, want 1", name, strings.Count(content, foundationMarkerEnd))
		}
		if !strings.Contains(content, foundationRuleText) {
			t.Errorf("%s does not contain the guardian rule", name)
		}
	}

	skill := readFoundationFile(t, artifacts.skill)
	if !strings.Contains(skill, foundationSkillMarker) || !strings.Contains(skill, "name: vcsentinel") {
		t.Errorf("managed skill does not contain its ownership markers: %q", skill)
	}

	hook := readFoundationFile(t, artifacts.hook)
	if !strings.HasPrefix(hook, "#!/bin/sh\n") {
		t.Errorf("pre-commit hook has no POSIX shell header: %q", hook)
	}
	if !strings.Contains(hook, foundationHookMarker) {
		t.Errorf("pre-commit hook has no vcSentinel ownership marker: %q", hook)
	}
	if !strings.Contains(hook, "check --staged") {
		t.Errorf("pre-commit hook does not invoke check --staged: %q", hook)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(artifacts.hook)
		if err != nil {
			t.Fatalf("stat the installed pre-commit hook: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Errorf("pre-commit hook permissions = %o, want 0755", got)
		}
	}
}

func assertFoundationRemoved(t *testing.T, artifacts foundationArtifacts) {
	t.Helper()
	assertFoundationFileAbsent(t, "project configuration", artifacts.config)
	assertFoundationFileAbsent(t, "managed skill", artifacts.skill)
	assertFoundationFileAbsent(t, "owned pre-commit hook", artifacts.hook)

	for name, path := range artifacts.rules {
		if name == ".claudecode.md" {
			assertFoundationFileAbsent(t, name, path)
			continue
		}
		content, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			t.Errorf("uninit removed the pre-existing %s", name)
			continue
		}
		if err != nil {
			t.Fatalf("read %s after uninit: %v", name, err)
		}
		text := string(content)
		if strings.Contains(text, foundationMarkerBegin) || strings.Contains(text, foundationMarkerEnd) || strings.Contains(text, foundationRuleText) {
			t.Errorf("uninit left managed rules in %s: %q", name, text)
		}
	}
}

func assertFoundationFileAbsent(t *testing.T, label, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("uninit did not remove %s at %s: %v", label, path, err)
	}
}

func readFoundationState(t *testing.T, artifacts foundationArtifacts) foundationState {
	t.Helper()
	rules := make(map[string][]byte, len(artifacts.rules))
	for name, path := range artifacts.rules {
		rules[name] = []byte(readFoundationFile(t, path))
	}
	return foundationState{
		config: []byte(readFoundationFile(t, artifacts.config)),
		rules:  rules,
		skill:  []byte(readFoundationFile(t, artifacts.skill)),
		hook:   []byte(readFoundationFile(t, artifacts.hook)),
	}
}

func assertFoundationStateEqual(t *testing.T, want, got foundationState) {
	t.Helper()
	if string(want.config) != string(got.config) {
		t.Error("repeated init changed the project configuration")
	}
	if len(want.rules) != len(got.rules) {
		t.Fatalf("repeated init changed the rule file set: got %d files, want %d", len(got.rules), len(want.rules))
	}
	for name, wantContent := range want.rules {
		if string(wantContent) != string(got.rules[name]) {
			t.Errorf("repeated init changed %s", name)
		}
	}
	if string(want.skill) != string(got.skill) {
		t.Error("repeated init changed the managed skill")
	}
	if string(want.hook) != string(got.hook) {
		t.Error("repeated init changed the pre-commit hook")
	}
}

func assertHeadIs(t *testing.T, runner *cliRunner, want, context string) {
	t.Helper()
	result := runner.git("rev-parse", "HEAD")
	if result.ExitCode != 0 {
		t.Fatalf("resolve HEAD %s:\n%s", context, result.Diagnostic())
	}
	if got := strings.TrimSpace(result.Stdout); got != want {
		t.Fatalf("HEAD %s = %q, want %q", context, got, want)
	}
}

func foundationOversizedGoSource(declarations int) string {
	var source strings.Builder
	source.WriteString("package main\n\n")
	for index := 0; index < declarations; index++ {
		fmt.Fprintf(&source, "var foundationValue%d = %d\n", index, index)
	}
	return source.String()
}

func readFoundationFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read foundation artifact %s: %v", path, err)
	}
	return string(content)
}
