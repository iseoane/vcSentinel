package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/change"
	"github.com/ISeoane-Quental/vas.sentinel/internal/risk"
	"github.com/ISeoane-Quental/vas.sentinel/internal/secret"
)

type explainedCharacteristic struct {
	Name      string               `json:"name"`
	State     change.FeatureStatus `json:"state"`
	Heuristic bool                 `json:"heuristic,omitempty"`
	Detector  string               `json:"detector"`
}

type explainOutput struct {
	Profile         change.ChangeProfile      `json:"profile"`
	Characteristics []explainedCharacteristic `json:"characteristics"`
	// FU-11: deterministic exposed-credential incidents, independent of the
	// change profile. Omitted when empty: absence is data, never verdict.
	ExposedCredentials []exposedCredential `json:"exposed_credentials,omitempty"`
	// Paths the credential scanner could not read. Omitted when empty.
	CredentialScanUnknown []string `json:"credential_scan_unknown,omitempty"`
	Risk                  struct {
		Level   risk.Level `json:"level"`
		Explain string     `json:"explain"`
	} `json:"risk"`
	Cohesion struct {
		Clusters       int     `json:"clusters"`
		Score          float64 `json:"score"`
		SuggestedSplit bool    `json:"suggested_split"`
	} `json:"cohesion"`
}

// exposedCredential names the path, shape and added-line location of one
// credential match. It never carries the matched value.
type exposedCredential struct {
	Path  string `json:"path"`
	Shape string `json:"shape"`
	Line  int    `json:"line"`
}

var explainDetectors = map[string]string{
	"public_api":         "exported signature or declaration changed in AST",
	"database":           "migration, SQL, or declared data path",
	"security_sensitive": "sensitive path or auth/token/crypto/password/secret identifier",
	"concurrency":        "go, sync, chan, or context appearing in added lines",
	"behavior_change":    "added non-test, non-comment code",
	"test_covered":       "package tests confirmed by diff or test map",
	"cross_module":       "two or more top-level modules touched",
	"generated_code":     "generated class, .gitattributes, or generation marker",
	"ci_cd":              "path classified as CI/CD",
	"infrastructure":     "path classified as infrastructure",
}

func runExplain(out io.Writer, args []string) error {
	return runExplainWith(out, args, change.ComputeChangeProfile, runGitForChange)
}

func runExplainWith(out io.Writer, args []string, profile func(string, string) (change.ChangeProfile, error), reader change.GitReader) error {
	base, head, jsonOut, err := parseExplain(args)
	if err != nil {
		return err
	}
	changeProfile, err := profile(base, head)
	if err != nil {
		return fmt.Errorf("could not compute the profile: %w", err)
	}
	rangeExpr := base + ".." + head
	paths, err := explainPaths(reader, rangeExpr)
	if err != nil {
		return err
	}
	diff, err := reader(explainDiffArgs(rangeExpr)...)
	if err != nil {
		return fmt.Errorf("could not read unified diff for %s: %w", rangeExpr, err)
	}
	gitattributes, _ := reader("show", head+":.gitattributes")
	input := change.NewCharacteristicsInput(changeProfile, paths, diff, gitattributes)
	characteristics := change.DetectFeatures(input)
	riskResult := risk.Evaluate(changeProfile, characteristics)
	cohesion, err := change.Cohesion(paths, reader)
	if err != nil {
		return err
	}

	result := explainOutput{Profile: changeProfile}
	// FU-11: credential scan over the same diff, independent of the change
	// profile and of every class filter. No agent involved.
	secretIncidents, secretScanUnreadable := secret.Scan(paths, diff)
	for _, incident := range secretIncidents {
		result.ExposedCredentials = append(result.ExposedCredentials, exposedCredential{
			Path: incident.Path, Shape: incident.Shape, Line: incident.Line,
		})
	}
	result.CredentialScanUnknown = secretScanUnreadable

	for _, feature := range characteristics {
		result.Characteristics = append(result.Characteristics, explainedCharacteristic{
			Name: feature.Name, State: feature.State,
			Heuristic: feature.Heuristic, Detector: explainDetectors[feature.Name],
		})
	}
	result.Risk.Level, result.Risk.Explain = riskResult.Level, riskResult.Explanation
	result.Cohesion.Clusters, result.Cohesion.Score = cohesion.Clusters, cohesion.Score
	result.Cohesion.SuggestedSplit = cohesion.SuggestSplit

	if jsonOut {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(result)
	}
	fmt.Fprintf(out, "Profile: kind=%s, files=%d, +%d/-%d, modules=%s\n", changeProfile.Kind, changeProfile.Size.Files, changeProfile.Size.Added, changeProfile.Size.Deleted, strings.Join(changeProfile.Modules, ", "))
	fmt.Fprintln(out, "Characteristics:")
	for _, characteristic := range result.Characteristics {
		fmt.Fprintf(out, "- %s=%s — %s\n", characteristic.Name, characteristic.State, characteristic.Detector)
	}
	fmt.Fprintf(out, "Risk: %s — %s\n", result.Risk.Level, result.Risk.Explain)
	fmt.Fprintf(out, "Cohesion: clusters=%d score=%.2f suggested_split=%t\n", result.Cohesion.Clusters, result.Cohesion.Score, result.Cohesion.SuggestedSplit)
	for _, incident := range result.ExposedCredentials {
		fmt.Fprintf(out, "  ⚠️ exposed credential: %s:%d %s (value withheld)\n", incident.Path, incident.Line, incident.Shape)
	}
	if len(result.CredentialScanUnknown) > 0 {
		fmt.Fprintf(out, "  ⚠️ credential scan unavailable for: %s\n", strings.Join(result.CredentialScanUnknown, ", "))
	}
	return nil
}

func parseExplain(args []string) (base, head string, jsonOut bool, err error) {
	rangeExpr := "HEAD^..HEAD"
	for _, arg := range args {
		if arg == "--json" {
			jsonOut = true
			continue
		}
		if strings.HasPrefix(arg, "-") || rangeExpr != "HEAD^..HEAD" {
			return "", "", false, fmt.Errorf("%s", explainUsage)
		}
		rangeExpr = arg
	}
	base, head, ok := strings.Cut(rangeExpr, "..")
	if !ok || base == "" || head == "" || strings.Contains(head, "..") ||
		strings.HasPrefix(base, "-") || strings.HasPrefix(head, "-") {
		return "", "", false, fmt.Errorf("invalid range %q: use <base>..<head>", rangeExpr)
	}
	return base, head, jsonOut, nil
}

// explainDiffArgs pins the diff invocation the added-line parser depends
// on. The prefixes are forced rather than left to configuration: diff.noprefix,
// diff.mnemonicPrefix and diff.srcPrefix/dstPrefix each change the header
// format, and a header the parser does not recognise loses its added lines with
// no error. Exposed as one function so the test that exercises real Git cannot
// drift from the invocation it claims to cover.
func explainDiffArgs(rangeExpr string) []string {
	return []string{"diff", "--no-color", "--unified=0", "--src-prefix=a/", "--dst-prefix=b/", "-M", rangeExpr}
}

func explainPaths(reader change.GitReader, rangeExpr string) ([]string, error) {
	output, err := reader("diff", "--name-only", "-z", "-M", rangeExpr)
	if err != nil {
		return nil, fmt.Errorf("could not list the paths of %s: %w", rangeExpr, err)
	}
	var paths []string
	for _, path := range strings.Split(strings.TrimSuffix(output, "\x00"), "\x00") {
		if path != "" {
			paths = append(paths, filepath.ToSlash(path))
		}
	}
	return paths, nil
}

func runGitForChange(args ...string) (string, error) {
	output, err := exec.Command("git", args...).Output()
	return string(output), err
}
