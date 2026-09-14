package pr

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
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
