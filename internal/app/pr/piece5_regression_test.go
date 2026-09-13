package pr

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
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
			if headCalls <= 2 {
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
		PublishStored: func(string, string, string, string) (string, bool, error) { published = true; return "", false, nil },
	}
	var output bytes.Buffer
	if code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{}, deps, Wiring{}); code != 1 || ranCI || published || !strings.Contains(output.String(), "HEAD changed") {
		t.Fatalf("code=%d ci=%v published=%v output=%q", code, ranCI, published, output.String())
	}
}

func TestRunPrCreateReportsPublicationFallbackFailure(t *testing.T) {
	entry := piece5Entry(t, review.VerdictOK)
	deps := DepsPrCreate{
		CurrentBranch:   func(string) (string, error) { return entry.Branch, nil },
		GetHeadSHAAt:    func(string) (string, error) { return entry.HeadSHA, nil },
		GetGitCommonDir: func(string) (string, error) { return "common", nil },
		ReadPRReview:    func(string, string) (*store.PRReviewEntry, error) { return &entry, nil },
		EvidenceAtHEAD:  func(string, string) (bool, string, error) { return true, "", nil },
		LoadConfig:      func(string) (config.Config, error) { return config.Config{}, nil },
		GetGitDirAt:     func(string) (string, error) { return "gitdir", nil },
		RunValidation:   func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) { return nil, nil },
		ComposeBody:     func(store.PRReviewEntry, CIOutcome) (string, error) { return entry.Body, nil },
		WriteTemplate:   func(string) (string, error) { return "template", nil },
		PublishStored: func(string, string, string, string) (string, bool, error) {
			return "", true, errors.New("clipboard fallback failed")
		},
	}
	var output bytes.Buffer
	if code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{}, deps, Wiring{}); code != 1 || !strings.Contains(output.String(), "clipboard fallback failed") {
		t.Fatalf("code=%d output=%q", code, output.String())
	}
}
