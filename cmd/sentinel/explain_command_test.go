package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
)

func TestRunExplainJSONExposesProfileDetectorsRiskAndCohesion(t *testing.T) {
	profile := change.ChangeProfile{
		Base: "BASE", Head: "HEAD", Kind: "feature",
		Size:    change.ChangeSize{Files: 2, Added: 2, Hunks: 1},
		Symbols: change.ChangeSymbols{ExportedTouched: 1, Complete: true}, Modules: []string{"internal/auth"},
		FileClasses: map[string]int{"source": 1, "docs": 1},
	}
	diffReads := 0
	reader := func(args ...string) (string, error) {
		command := strings.Join(args, " ")
		switch {
		case strings.Contains(command, "diff --name-only"):
			return "internal/auth/login.go\x00docs/guide.md\x00", nil
		case strings.Contains(command, "diff --no-color --unified=0"):
			diffReads++
			if !strings.Contains(command, "--src-prefix=a/") || !strings.Contains(command, "--dst-prefix=b/") {
				t.Errorf("unified diff command does not force stable prefixes: %s", command)
			}
			return "diff --git a/internal/auth/login.go b/internal/auth/login.go\n+++ b/internal/auth/login.go\n@@ -0,0 +1 @@\n+func ValidateToken() { go process() }\ndiff --git a/docs/guide.md b/docs/guide.md\n+++ b/docs/guide.md\n@@ -0,0 +1 @@\n+guide\n", nil
		case strings.HasPrefix(command, "show "):
			return "", fmt.Errorf("without .gitattributes")
		case strings.HasPrefix(command, "log "):
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git: %s", command)
		}
	}

	var out bytes.Buffer
	err := runExplainWith(&out, []string{"BASE..HEAD", "--json"},
		func(base, head string) (change.ChangeProfile, error) { return profile, nil }, reader)
	if err != nil {
		t.Fatalf("runExplainWith returned an error: %v", err)
	}
	if diffReads != 1 {
		t.Fatalf("unified diff reads = %d, want 1", diffReads)
	}

	var got struct {
		Profile         change.ChangeProfile                     `json:"profile"`
		Characteristics []struct{ Name, State, Detector string } `json:"characteristics"`
		Risk            struct{ Level, Explain string }          `json:"risk"`
		Cohesion        struct {
			Clusters       int  `json:"clusters"`
			SuggestedSplit bool `json:"suggested_split"`
		} `json:"cohesion"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, out.String())
	}
	if got.Profile.Kind != "feature" || got.Profile.Symbols != profile.Symbols {
		t.Errorf("profile = %+v", got.Profile)
	}
	securityFound := false
	for _, characteristic := range got.Characteristics {
		if characteristic.Name == "security_sensitive" {
			securityFound = characteristic.State == "present" && characteristic.Detector != ""
		}
	}
	if !securityFound {
		t.Errorf("security_sensitive does not expose state and detector: %+v", got.Characteristics)
	}
	concurrencyFound := false
	for _, characteristic := range got.Characteristics {
		if characteristic.Name == "concurrency" {
			concurrencyFound = characteristic.State == "present" && characteristic.Detector != ""
		}
	}
	if !concurrencyFound {
		t.Errorf("concurrency did not observe the added source content: %+v", got.Characteristics)
	}
	if got.Risk.Level != "high" || !strings.Contains(got.Risk.Explain, "security_sensitive") {
		t.Errorf("risk = %+v", got.Risk)
	}
	if got.Cohesion.Clusters != 2 || !got.Cohesion.SuggestedSplit {
		t.Errorf("cohesion = %+v", got.Cohesion)
	}
}

func TestRunExplainKeepsAddedLinesForPathStartingWithB(t *testing.T) {
	const path = "b/cmd/sentinel/x.go"
	profile := change.ChangeProfile{
		Base: "BASE", Head: "HEAD", Kind: "feature",
		Size:        change.ChangeSize{Files: 1, Added: 1, Hunks: 1},
		FileClasses: map[string]int{"source": 1},
	}
	reader := func(args ...string) (string, error) {
		command := strings.Join(args, " ")
		switch {
		case strings.Contains(command, "diff --name-only"):
			return path + "\x00", nil
		case strings.Contains(command, "diff --no-color --unified=0"):
			prefix := path
			if strings.Contains(command, "--src-prefix=a/") && strings.Contains(command, "--dst-prefix=b/") {
				prefix = "b/" + path
			}
			return "diff --git a/" + path + " b/" + path + "\n+++ " + prefix + "\n@@ -0,0 +1 @@\n+func Added() { go process() }\n", nil
		case strings.HasPrefix(command, "show "):
			return "", fmt.Errorf("missing .gitattributes")
		case strings.HasPrefix(command, "log "):
			return "", nil
		default:
			return "", fmt.Errorf("unexpected git command: %s", command)
		}
	}

	var out bytes.Buffer
	err := runExplainWith(&out, []string{"BASE..HEAD", "--json"},
		func(base, head string) (change.ChangeProfile, error) { return profile, nil }, reader)
	if err != nil {
		t.Fatalf("runExplainWith returned an error: %v", err)
	}

	var got struct {
		Characteristics []struct {
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"characteristics"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, out.String())
	}
	for _, characteristic := range got.Characteristics {
		if characteristic.Name == "concurrency" && characteristic.State == "present" {
			return
		}
	}
	t.Fatalf("concurrency did not observe added lines for %q: %+v", path, got.Characteristics)
}

func TestParseExplainRejectsComponentsThatLookLikeOptions(t *testing.T) {
	for _, rangeExpr := range []string{"--output=robo..HEAD", "HEAD..--output=robo"} {
		t.Run(rangeExpr, func(t *testing.T) {
			if _, _, _, err := parseExplain([]string{rangeExpr}); err == nil {
				t.Fatalf("parseExplain accepted the dangerous range %q", rangeExpr)
			}
		})
	}
}
