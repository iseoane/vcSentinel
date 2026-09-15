package pr

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

func TestRunPrCreateRejectsSnapshotDriftBeforeCI(t *testing.T) {
	entry := piece5Entry(t, review.VerdictOK)
	changedHead := strings.Repeat("b", 40)
	headCalls := 0
	var ranCI, published bool
	deps := DepsPrCreate{
		CurrentBranch: func(string) (string, error) { return entry.Branch, nil },
		GetHeadSHAAt: func(string) (string, error) {
			headCalls++
			if headCalls <= 1 {
				return entry.HeadSHA, nil
			}
			return changedHead, nil
		},
		GetGitCommonDir: func(string) (string, error) { return "common", nil },
		ReadPRReview:    func(string, string) (*store.PRReviewEntry, error) { return &entry, nil },
		EvidenceAtHEAD:  func(string, string) (bool, string, error) { return true, "", nil },
		LoadConfig: func(string) (config.Config, error) {
			return config.Config{CI: config.CIConfig{Workflow: "verify.yml"}}, nil
		},
		GetGitDirAt:   func(string) (string, error) { return "gitdir", nil },
		RunValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) { return nil, nil },
		RunCI: func(context.Context, io.Writer, string, string, string, config.CIConfig) (CIOutcome, error) {
			ranCI = true
			return CIOutcome{}, nil
		},
		RemoteBranchHead: func(string, string) (string, error) { return "", nil },
		PublishStored:    func(string, string, string, string) (string, bool, error) { published = true; return "", false, nil },
	}
	var output bytes.Buffer
	if code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{}, deps, Wiring{}); code != 1 || ranCI || published || !strings.Contains(output.String(), "HEAD changed") {
		t.Fatalf("code=%d ci=%v published=%v output=%q", code, ranCI, published, output.String())
	}
}

func TestRunPrCreateReportsPublicationFallbackFailure(t *testing.T) {
	entry := piece5Entry(t, review.VerdictOK)
	deps := DepsPrCreate{
		CurrentBranch:    func(string) (string, error) { return entry.Branch, nil },
		GetHeadSHAAt:     func(string) (string, error) { return entry.HeadSHA, nil },
		GetGitCommonDir:  func(string) (string, error) { return "common", nil },
		ReadPRReview:     func(string, string) (*store.PRReviewEntry, error) { return &entry, nil },
		EvidenceAtHEAD:   func(string, string) (bool, string, error) { return true, "", nil },
		LoadConfig:       func(string) (config.Config, error) { return config.Config{}, nil },
		GetGitDirAt:      func(string) (string, error) { return "gitdir", nil },
		RunValidation:    func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) { return nil, nil },
		ComposeBody:      func(store.PRReviewEntry, CIOutcome) (string, error) { return entry.Body, nil },
		WriteTemplate:    func(string) (string, error) { return "template", nil },
		RemoteBranchHead: func(string, string) (string, error) { return "", nil },
		PublishStored: func(string, string, string, string) (string, bool, error) {
			return "", true, errors.New("clipboard fallback failed")
		},
	}
	var output bytes.Buffer
	if code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{}, deps, Wiring{}); code != 1 || !strings.Contains(output.String(), "clipboard fallback failed") {
		t.Fatalf("code=%d output=%q", code, output.String())
	}
}

func TestRunPrCreateComparesTheRemoteBranchOnlyForThePushedCandidate(t *testing.T) {
	entry := piece5Entry(t, review.VerdictOK)
	foreignHead := strings.Repeat("c", 40)
	published := false
	compared := false
	deps := DepsPrCreate{
		CurrentBranch:   func(string) (string, error) { return entry.Branch, nil },
		GetHeadSHAAt:    func(string) (string, error) { return entry.HeadSHA, nil },
		GetGitCommonDir: func(string) (string, error) { return "common", nil },
		ReadPRReview:    func(string, string) (*store.PRReviewEntry, error) { return &entry, nil },
		EvidenceAtHEAD:  func(string, string) (bool, string, error) { return true, "", nil },
		LoadConfig: func(string) (config.Config, error) {
			return config.Config{CI: config.CIConfig{Workflow: "verify.yml"}}, nil
		},
		GetGitDirAt:   func(string) (string, error) { return "gitdir", nil },
		RunValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) { return nil, nil },
		RunCI: func(context.Context, io.Writer, string, string, string, config.CIConfig) (CIOutcome, error) {
			return DefaultCIOutcome(), nil
		},
		ComposeBody:   func(store.PRReviewEntry, CIOutcome) (string, error) { return entry.Body, nil },
		WriteTemplate: func(string) (string, error) { return "template", nil },
		RemoteBranchHead: func(string, string) (string, error) {
			compared = true
			return foreignHead, nil
		},
		PublishStored: func(string, string, string, string) (string, bool, error) {
			published = true
			return "url", false, nil
		},
	}
	var output bytes.Buffer
	if code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{}, deps, Wiring{}); code != 1 || published || !strings.Contains(output.String(), "not the reviewed") {
		t.Fatalf("pushed candidate: code=%d published=%v output=%q", code, published, output.String())
	}

	published, compared = false, false
	deps.LoadConfig = func(string) (config.Config, error) { return config.Config{}, nil }
	output.Reset()
	if code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{}, deps, Wiring{}); code != 0 || !published || compared {
		t.Fatalf("unpushed candidate: code=%d published=%v compared=%v output=%q", code, published, compared, output.String())
	}
}

func TestRemoteBranchHeadWithParsesLsRemoteAndSkipsAnAbsentRef(t *testing.T) {
	var seen []string
	head, err := RemoteBranchHeadWith(context.Background(), "worktree", "feature/piece5", func(string, string) string { return "origin" },
		func(_ context.Context, _ string, args ...string) ([]byte, error) {
			seen = args
			return []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\trefs/heads/feature/piece5\n"), nil
		})
	if err != nil || head != strings.Repeat("a", 40) {
		t.Fatalf("head=%q err=%v", head, err)
	}
	if !reflect.DeepEqual(seen, []string{"ls-remote", "origin", "refs/heads/feature/piece5"}) {
		t.Fatalf("args=%v", seen)
	}

	head, err = RemoteBranchHeadWith(context.Background(), "worktree", "feature/piece5", func(string, string) string { return "origin" },
		func(context.Context, string, ...string) ([]byte, error) { return []byte("\n"), nil })
	if err != nil || head != "" {
		t.Fatalf("absent ref: head=%q err=%v", head, err)
	}

	head, err = RemoteBranchHeadWith(context.Background(), "worktree", "feature/piece5", func(string, string) string { return "" },
		func(context.Context, string, ...string) ([]byte, error) {
			t.Fatal("no remote must not run git")
			return nil, nil
		})
	if err != nil || head != "" {
		t.Fatalf("no remote: head=%q err=%v", head, err)
	}
}

func TestRunPrCreateRecordsTheForceBypassDecisionOnlyWhenValidationIsRed(t *testing.T) {
	entry := piece5Entry(t, review.VerdictOK)
	redRun := []validation.ValidationRun{{Capability: "test", Command: "go test ./...", Exit: 1, Output: "FAIL"}}
	var recorded []store.Decision
	var recordedCommonDir string
	recordErr := error(nil)
	baseDeps := func(runs []validation.ValidationRun) DepsPrCreate {
		return DepsPrCreate{
			CurrentBranch:   func(string) (string, error) { return entry.Branch, nil },
			GetHeadSHAAt:    func(string) (string, error) { return entry.HeadSHA, nil },
			GetGitCommonDir: func(string) (string, error) { return "common", nil },
			ReadPRReview:    func(string, string) (*store.PRReviewEntry, error) { return &entry, nil },
			EvidenceAtHEAD:  func(string, string) (bool, string, error) { return true, "", nil },
			LoadConfig: func(string) (config.Config, error) {
				return config.Config{Validation: config.ValidationConfig{Capabilities: map[string]config.CapabilityConfig{"test": {Command: "go test ./..."}}}}, nil
			},
			GetGitDirAt: func(string) (string, error) { return "gitdir", nil },
			RunValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
				return runs, nil
			},
			ResolveActor: func(string) string { return "captain" },
			RecordDecision: func(commonDir string, d *store.Decision) error {
				recordedCommonDir = commonDir
				recorded = append(recorded, *d)
				return recordErr
			},
			ComposeBody:      func(store.PRReviewEntry, CIOutcome) (string, error) { return entry.Body, nil },
			WriteTemplate:    func(string) (string, error) { return "template", nil },
			RemoteBranchHead: func(string, string) (string, error) { return "", nil },
			PublishStored:    func(string, string, string, string) (string, bool, error) { return "url", false, nil },
		}
	}

	var output bytes.Buffer
	if code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{Force: true, Reason: "approved exception"}, baseDeps(redRun), Wiring{}); code != 0 {
		t.Fatalf("red + force: code=%d output=%q", code, output.String())
	}
	if len(recorded) != 1 {
		t.Fatalf("decisions=%+v, want exactly one force bypass", recorded)
	}
	got := recorded[0]
	if got.Decision != store.DecisionForceBypass || got.Scope != store.ScopePrCreate || got.Reason != "approved exception" || got.Actor != "captain" {
		t.Fatalf("decision=%+v", got)
	}
	if recordedCommonDir != "common" {
		t.Fatalf("decision written to %q, want the git common dir", recordedCommonDir)
	}

	recorded = nil
	output.Reset()
	if code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{Force: true, Reason: "approved exception"}, baseDeps(nil), Wiring{}); code != 0 || len(recorded) != 0 {
		t.Fatalf("green + force: code=%d decisions=%+v output=%q", code, recorded, output.String())
	}

	recorded = nil
	recordErr = errors.New("disk full")
	output.Reset()
	code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{Force: true, Reason: "approved exception"}, baseDeps(redRun), Wiring{})
	if code != 0 || len(recorded) != 1 || !strings.Contains(output.String(), "disk full") {
		t.Fatalf("record failure: code=%d decisions=%+v output=%q", code, recorded, output.String())
	}
}

func TestRunConfiguredCIPendingSummaryReportsTheObservedElapsedTime(t *testing.T) {
	clock := time.Unix(100, 0)
	ctx, cancel := context.WithCancel(context.Background())
	client := &fakeCIClient{onFind: func() {
		clock = clock.Add(3 * time.Second)
		cancel()
	}, findErr: context.Canceled}
	got, err := RunConfiguredCI(ctx, io.Discard, "worktree", "feature/piece5", piece5Head, config.CIConfig{Workflow: "verify.yml", WaitSeconds: 900, PollSeconds: 1}, client, func(string, string) string { return "origin" }, func() time.Time { return clock }, nil)
	if err != nil || got.Status != "pending" || got.Summary != "still running after 3s" {
		t.Fatalf("interrupted outcome=%+v err=%v", got, err)
	}

	clock = time.Unix(100, 0)
	client = &fakeCIClient{runs: []*CIRun{{URL: "https://run", HeadSHA: piece5Head, Status: "in_progress"}}}
	got, err = RunConfiguredCI(context.Background(), io.Discard, "worktree", "feature/piece5", piece5Head, config.CIConfig{Workflow: "verify.yml", WaitSeconds: 2, PollSeconds: 1}, client, func(string, string) string { return "origin" }, func() time.Time { return clock }, func(context.Context, time.Duration) error {
		clock = clock.Add(time.Second)
		return nil
	})
	if err != nil || got.Status != "pending" || got.Summary != "still running after 2s" {
		t.Fatalf("expiry outcome=%+v err=%v", got, err)
	}
}

func TestRunPrCreateExitsNonZeroWhenGhPublicationFailsDespiteTheClipboardFallback(t *testing.T) {
	entry := piece5Entry(t, review.VerdictOK)
	recordedExit := -1
	deps := DepsPrCreate{
		CurrentBranch:    func(string) (string, error) { return entry.Branch, nil },
		GetHeadSHAAt:     func(string) (string, error) { return entry.HeadSHA, nil },
		GetGitCommonDir:  func(string) (string, error) { return "common", nil },
		ReadPRReview:     func(string, string) (*store.PRReviewEntry, error) { return &entry, nil },
		EvidenceAtHEAD:   func(string, string) (bool, string, error) { return true, "", nil },
		LoadConfig:       func(string) (config.Config, error) { return config.Config{}, nil },
		GetGitDirAt:      func(string) (string, error) { return "gitdir", nil },
		RunValidation:    func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) { return nil, nil },
		ComposeBody:      func(store.PRReviewEntry, CIOutcome) (string, error) { return entry.Body, nil },
		WriteTemplate:    func(string) (string, error) { return "template", nil },
		RemoteBranchHead: func(string, string) (string, error) { return "", nil },
		PublishStored: func(string, string, string, string) (string, bool, error) {
			return "", true, errors.New("gh pr create failed: a pull request already exists")
		},
		RecordEvent: func(_, _ string, exit int, _ []string, _ ops.EventDetail, _ string) error {
			recordedExit = exit
			return nil
		},
	}
	var output bytes.Buffer
	code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{}, deps, Wiring{})
	if code != 1 || !strings.Contains(output.String(), "a pull request already exists") {
		t.Fatalf("code=%d output=%q", code, output.String())
	}
	if recordedExit != -1 {
		t.Fatalf("a failed publication must not record a pr-create success event, got exit %d", recordedExit)
	}
}

func TestRunPrCreateBlocksPublicationOnRedValidationWithoutForce(t *testing.T) {
	entry := piece5Entry(t, review.VerdictOK)
	redRuns := []validation.ValidationRun{{Capability: "test", Command: "go test ./...", Exit: 1, Output: "FAIL internal/app/pr"}}
	var published, recordedDecision bool
	deps := DepsPrCreate{
		CurrentBranch:   func(string) (string, error) { return entry.Branch, nil },
		GetHeadSHAAt:    func(string) (string, error) { return entry.HeadSHA, nil },
		GetGitCommonDir: func(string) (string, error) { return "common", nil },
		ReadPRReview:    func(string, string) (*store.PRReviewEntry, error) { return &entry, nil },
		EvidenceAtHEAD:  func(string, string) (bool, string, error) { return true, "", nil },
		LoadConfig: func(string) (config.Config, error) {
			return config.Config{Validation: config.ValidationConfig{Capabilities: map[string]config.CapabilityConfig{"test": {Command: "go test ./..."}}}}, nil
		},
		GetGitDirAt: func(string) (string, error) { return "gitdir", nil },
		RunValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			return redRuns, nil
		},
		RecordDecision: func(string, *store.Decision) error { recordedDecision = true; return nil },
		ComposeBody:    func(store.PRReviewEntry, CIOutcome) (string, error) { return entry.Body, nil },
		WriteTemplate:  func(string) (string, error) { return "template", nil },
		PublishStored:  func(string, string, string, string) (string, bool, error) { published = true; return "url", false, nil },
	}
	var output bytes.Buffer
	code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{}, deps, Wiring{})
	if code != 1 || published || recordedDecision {
		t.Fatalf("code=%d published=%v decision=%v output=%q", code, published, recordedDecision, output.String())
	}
	for _, want := range []string{"Red validation", "test", "go test ./...", "FAIL internal/app/pr", "--force"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output %q misses %q", output.String(), want)
		}
	}
}

func TestRunPrCreateExitsOnConfigLoadErrorWithoutValidatingOrPublishing(t *testing.T) {
	entry := piece5Entry(t, review.VerdictOK)
	var validated, published bool
	deps := DepsPrCreate{
		CurrentBranch:   func(string) (string, error) { return entry.Branch, nil },
		GetHeadSHAAt:    func(string) (string, error) { return entry.HeadSHA, nil },
		GetGitCommonDir: func(string) (string, error) { return "common", nil },
		ReadPRReview:    func(string, string) (*store.PRReviewEntry, error) { return &entry, nil },
		EvidenceAtHEAD:  func(string, string) (bool, string, error) { return true, "", nil },
		LoadConfig: func(string) (config.Config, error) {
			return config.Config{}, errors.New("vassentinel.yml: unknown key")
		},
		GetGitDirAt: func(string) (string, error) { return "gitdir", nil },
		RunValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			validated = true
			return nil, nil
		},
		ComposeBody:   func(store.PRReviewEntry, CIOutcome) (string, error) { return entry.Body, nil },
		WriteTemplate: func(string) (string, error) { return "template", nil },
		PublishStored: func(string, string, string, string) (string, bool, error) { published = true; return "url", false, nil },
	}
	var output bytes.Buffer
	code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{}, deps, Wiring{})
	if code != 1 || validated || published || !strings.Contains(output.String(), "vassentinel.yml: unknown key") {
		t.Fatalf("code=%d validated=%v published=%v output=%q", code, validated, published, output.String())
	}
}
