package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestRunConsentDiffNonInteractiveCycle(t *testing.T) {
	repo := t.TempDir()
	if err := exec.Command("git", "-C", repo, "init").Run(); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		action string
		text   string
	}{
		{"status", "revoked"},
		{"grant", "granted"},
		{"status", "granted"},
		{"revoke", "revoked"},
	} {
		var out bytes.Buffer
		if code := runConsentDiff(&out, repo, []string{scenario.action}); code != 0 {
			t.Fatalf("%s exit = %d: %s", scenario.action, code, out.String())
		}
		if !strings.Contains(strings.ToLower(out.String()), scenario.text) {
			t.Fatalf("%s does not report %q: %s", scenario.action, scenario.text, out.String())
		}
	}
}
