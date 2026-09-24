package cli_e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/review"
)

const (
	reviewDispositionModel    = "fake-claude"
	reviewDispositionFile     = "target.go"
	reviewDispositionLine     = "3"
	reviewDispositionEvidence = "// deterministic critical evidence marker"
)

func TestCLIReviewDispositions(t *testing.T) {
	t.Run("refute clears the blocking review", func(t *testing.T) {
		fixture := newReviewDispositionFixture(t)
		refute := runReviewDispositionRefute(t, fixture, "the deterministic fixture marker is safe")
		assertReviewDispositionCommand(t, refute, 0, "Human refutation recorded", fixture.fingerprint)

		cleared := fixture.runner.run("review", "HEAD", "--dims", "logic", "--gate")
		assertReviewDispositionReview(t, cleared, 0, "warn")
	})

	t.Run("reopen restores the blocking review", func(t *testing.T) {
		fixture := newReviewDispositionFixture(t)
		refute := runReviewDispositionRefute(t, fixture, "the marker was initially considered safe")
		assertReviewDispositionCommand(t, refute, 0, "Human refutation recorded", fixture.fingerprint)

		reopen := fixture.runner.run(
			"reopen",
			"--sha", fixture.sha,
			"--fingerprint", fixture.fingerprint,
			"--reason", "the deterministic fixture marker applies again",
			"--line-start", reviewDispositionLine,
			"--line-end", reviewDispositionLine,
		)
		assertReviewDispositionCommand(t, reopen, 0, "Human reopen recorded", "blocks again")

		reopened := fixture.runner.run("review", "HEAD", "--dims", "logic", "--gate")
		assertReviewDispositionReview(t, reopened, 1, "block")
	})

	t.Run("accept records judgement without clearing the block", func(t *testing.T) {
		fixture := newReviewDispositionFixture(t)
		accept := fixture.runner.run(
			"accept",
			"--sha", fixture.sha,
			"--fingerprint", fixture.fingerprint,
			"--reason", "the remaining risk is acknowledged for this fixture",
		)
		assertReviewDispositionCommand(t, accept, 0, "Human acceptance recorded", "block stands")

		accepted := fixture.runner.run("review", "HEAD", "--dims", "logic", "--gate")
		assertReviewDispositionReview(t, accepted, 1, "block")
	})
}

type reviewDispositionFixture struct {
	runner      *cliRunner
	sha         string
	fingerprint string
}

func newReviewDispositionFixture(t *testing.T) reviewDispositionFixture {
	t.Helper()
	runner := newCLIRunner(t)
	before := captureCLIExternalState(t, runner)
	t.Cleanup(func() { assertCLIExternalStateUnchanged(t, runner, before) })

	stageReviewDispositionClaude(t, runner)
	seedCLIRepository(t, runner, "test: seed review disposition fixture")
	runPublicInit(t, runner)
	writeProjectConfig(t, runner, reviewDispositionConfig())
	runner.writeFile(reviewDispositionFile, "package main\n\n"+reviewDispositionEvidence+"\nfunc Target() {}\n")
	sha := runner.commit("test: add deterministic review target")

	first := runner.run("review", "HEAD", "--dims", "logic", "--gate")
	assertReviewDispositionReview(t, first, 1, "block")

	return reviewDispositionFixture{
		runner:      runner,
		sha:         sha,
		fingerprint: readReviewDispositionFingerprint(t, runner, sha),
	}
}

func runReviewDispositionRefute(t *testing.T, fixture reviewDispositionFixture, reason string) commandResult {
	t.Helper()
	return fixture.runner.run(
		"refute",
		"--sha", fixture.sha,
		"--fingerprint", fixture.fingerprint,
		"--reason", reason,
		"--line-start", reviewDispositionLine,
		"--line-end", reviewDispositionLine,
	)
}

func reviewDispositionConfig() string {
	return "version: \"2.0\"\nactive_agent: \"claude\"\nagents:\n  claude:\n    model: \"" + reviewDispositionModel + "\"\n    reasoning_effort: \"low\"\nreview:\n  parallel: 1\n"
}

func stageReviewDispositionClaude(t *testing.T, runner *cliRunner) {
	t.Helper()
	directory := t.TempDir()
	reviewEnvelope := reviewDispositionClaudeEnvelope(t)
	const refuterResponse = `{"refuted":false,"reason":"deterministic fixture keeps the finding blocking","sha":"","file":"","line_start":0,"line_end":0,"evidence":""}`

	if runtime.GOOS == "windows" {
		source := reviewDispositionWindowsClaudeSource(reviewEnvelope, refuterResponse)
		sourcePath := filepath.Join(directory, "fake_claude.go")
		if err := os.WriteFile(sourcePath, []byte(source), 0o644); err != nil {
			t.Fatalf("write fake Claude source: %v", err)
		}
		output := filepath.Join(directory, "claude.exe")
		buildEnv := append([]string(nil), runner.env...)
		buildEnv = append(buildEnv,
			"GO111MODULE=off",
			"GOCACHE="+t.TempDir(),
			"GOPROXY=off",
			"GOSUMDB=off",
			"GOTOOLCHAIN=local",
		)
		build := exec.Command(lookupTool(t, "go"), "build", "-o", output, sourcePath)
		build.Dir = directory
		build.Env = buildEnv
		if combined, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build fake Claude executable: %v\n%s", err, combined)
		}
	} else {
		script := reviewDispositionPOSIXClaudeScript(reviewEnvelope, refuterResponse)
		path := filepath.Join(directory, "claude")
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			t.Fatalf("write fake Claude executable: %v", err)
		}
	}
	prependRunnerPath(t, runner, directory)
}

func reviewDispositionClaudeEnvelope(t *testing.T) string {
	t.Helper()
	result := "BEGIN_REVIEW\n{" +
		"\"dim\":\"logic\",\"verdict\":\"block\",\"findings\":[{" +
		"\"dimension\":\"logic\",\"file\":\"" + reviewDispositionFile + "\"," +
		"\"line\":" + reviewDispositionLine + ",\"severity\":\"CRITICAL\"," +
		"\"description\":\"deterministic CRITICAL finding\"," +
		"\"evidence\":\"" + reviewDispositionEvidence + "\"," +
		"\"title\":\"deterministic rule\",\"confidence\":\"high\"" +
		"}]}\nEND_REVIEW\n"
	payload := struct {
		Type       string `json:"type"`
		Subtype    string `json:"subtype"`
		IsError    bool   `json:"is_error"`
		StopReason string `json:"stop_reason"`
		Result     string `json:"result"`
	}{
		Type:       "result",
		Subtype:    "success",
		StopReason: "end_turn",
		Result:     result,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal fake Claude result: %v", err)
	}
	return string(encoded)
}

func reviewDispositionPOSIXClaudeScript(reviewEnvelope, refuterResponse string) string {
	return "#!/bin/sh\n" +
		"prompt=\n" +
		"while IFS= read -r line; do\n" +
		"  prompt=\"${prompt}${line}\"\n" +
		"done\n" +
		"case \"$prompt\" in\n" +
		"  *\"What model are you actually using?\"*)\n" +
		"    printf '%s\\n' " + shellSingleQuote(reviewDispositionModel) + "\n" +
		"    ;;\n" +
		"  *\"Try to disprove the semantic CRITICAL finding\"*)\n" +
		"    printf '%s\\n' " + shellSingleQuote(refuterResponse) + "\n" +
		"    ;;\n" +
		"  *)\n" +
		"    printf '%s\\n' " + shellSingleQuote(reviewEnvelope) + "\n" +
		"    ;;\n" +
		"esac\n"
}

func reviewDispositionWindowsClaudeSource(reviewEnvelope, refuterResponse string) string {
	return "package main\n\n" +
		"import (\n" +
		"\t\"fmt\"\n" +
		"\t\"io\"\n" +
		"\t\"os\"\n" +
		"\t\"strings\"\n" +
		")\n\n" +
		"const reviewEnvelope = " + strconv.Quote(reviewEnvelope) + "\n" +
		"const refuterResponse = " + strconv.Quote(refuterResponse) + "\n\n" +
		"func main() {\n" +
		"\tinput, err := io.ReadAll(os.Stdin)\n" +
		"\tif err != nil {\n" +
		"\t\tos.Exit(2)\n" +
		"\t}\n" +
		"\tprompt := string(input)\n" +
		"\tswitch {\n" +
		"\tcase strings.Contains(prompt, \"What model are you actually using?\"):\n" +
		"\t\tfmt.Println(\"" + reviewDispositionModel + "\")\n" +
		"\tcase strings.Contains(prompt, \"Try to disprove the semantic CRITICAL finding\"):\n" +
		"\t\tfmt.Println(refuterResponse)\n" +
		"\tdefault:\n" +
		"\t\tfmt.Printf(\"{\\\"type\\\":\\\"result\\\",\\\"subtype\\\":\\\"success\\\",\\\"is_error\\\":false,\\\"stop_reason\\\":\\\"end_turn\\\",\\\"result\\\":%q}\\n\", reviewEnvelope)\n" +
		"\t}\n" +
		"}\n"
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func readReviewDispositionFingerprint(t *testing.T, runner *cliRunner, sha string) string {
	t.Helper()
	ledger := review.NewLedger(cliCommonDir(t, runner))
	record, err := ledger.ReadRecord(sha)
	if err != nil {
		t.Fatalf("read persisted review record: %v", err)
	}
	if record == nil || len(record.Revisions) == 0 {
		t.Fatalf("persisted review record for %s is missing revisions", sha)
	}
	for index := len(record.Revisions) - 1; index >= 0; index-- {
		for _, finding := range record.Revisions[index].AggregatedFindings {
			if finding.Dimension != "logic" || finding.Location.File != reviewDispositionFile || finding.Severity != review.SevCritical {
				continue
			}
			if fingerprint := strings.TrimSpace(finding.Fingerprint); fingerprint != "" {
				return fingerprint
			}
		}
	}
	t.Fatalf("persisted review record for %s has no stable logic CRITICAL fingerprint", sha)
	return ""
}

func assertReviewDispositionReview(t *testing.T, result commandResult, wantExit int, wantVerdict string) {
	t.Helper()
	if result.ExitCode != wantExit {
		t.Fatalf("review exit code = %d, want %d:\n%s", result.ExitCode, wantExit, result.Diagnostic())
	}
	output := result.Stdout + "\n" + result.Stderr
	if !strings.Contains(output, "Review of") || !strings.Contains(output, ": "+wantVerdict) {
		t.Fatalf("review output omitted verdict %q:\n%s", wantVerdict, result.Diagnostic())
	}
	if !strings.Contains(output, "logic") {
		t.Fatalf("review output omitted the logic dimension:\n%s", result.Diagnostic())
	}
}

func assertReviewDispositionCommand(t *testing.T, result commandResult, wantExit int, evidence ...string) {
	t.Helper()
	if result.ExitCode != wantExit {
		t.Fatalf("disposition command exit code = %d, want %d:\n%s", result.ExitCode, wantExit, result.Diagnostic())
	}
	output := result.Stdout + "\n" + result.Stderr
	for _, expected := range evidence {
		if !strings.Contains(output, expected) {
			t.Fatalf("disposition output omitted %q:\n%s", expected, result.Diagnostic())
		}
	}
}
