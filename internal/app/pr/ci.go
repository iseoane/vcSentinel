package pr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
)

var ErrCIRunDisappeared = errors.New("GitHub Actions run disappeared")

// CIRun is the bounded GitHub Actions observation used by pr create.
type CIRun struct {
	ID         int64
	URL        string
	HeadSHA    string
	Status     string
	Conclusion string
	FailedJobs []string
}

// CIClient is the only remote boundary owned by the CI state machine. Tests can
// replace it to prove push, dispatch, polling, timeout, and interruption order.
type CIClient interface {
	Push(context.Context, string, string, string, string) error
	Dispatch(context.Context, string, string, string) error
	FindRun(context.Context, string, string, string) (*CIRun, error)
}

// RunConfiguredCI pushes the exact branch before dispatching the explicitly
// configured workflow, then observes one bounded run. CI exit/status evidence is
// deterministic collection, not semantic review or a second authoring pass.
func RunConfiguredCI(ctx context.Context, out io.Writer, worktree, branch, head string, cfg config.CIConfig, client CIClient, remote func(string, string) string, now func() time.Time, sleep func(context.Context, time.Duration) error) (CIOutcome, error) {
	if strings.TrimSpace(cfg.Workflow) == "" {
		return DefaultCIOutcome(), nil
	}
	if client == nil {
		return CIOutcome{}, errors.New("CI is configured but no GitHub Actions client is available")
	}
	if cfg.WaitSeconds <= 0 {
		cfg.WaitSeconds = 900
	}
	if cfg.PollSeconds <= 0 {
		cfg.PollSeconds = 15
	}
	if remote == nil {
		remote = git.BranchRemoteFrom
	}
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = sleepContext
	}
	remoteName := strings.TrimSpace(remote(worktree, branch))
	if remoteName == "" {
		return CIOutcome{}, fmt.Errorf("cannot collect CI: branch %q has no configured remote", branch)
	}
	fmt.Fprintf(out, "⬆️ Pushing %s to %s.\n", branch, remoteName)
	if err := client.Push(ctx, worktree, remoteName, branch, head); err != nil {
		return CIOutcome{}, fmt.Errorf("push failed before CI dispatch: %w", err)
	}
	if err := client.Dispatch(ctx, worktree, cfg.Workflow, branch); err != nil {
		return CIOutcome{}, fmt.Errorf("could not dispatch workflow %q: %w", cfg.Workflow, err)
	}

	started := now()
	for {
		if ctx.Err() != nil {
			return pendingCIOutcome(cfg.Workflow, "", cfg.WaitSeconds), nil
		}
		run, err := client.FindRun(ctx, worktree, cfg.Workflow, branch)
		if ctx.Err() != nil {
			url := ""
			if run != nil {
				url = run.URL
			}
			return pendingCIOutcome(cfg.Workflow, url, cfg.WaitSeconds), nil
		}
		if errors.Is(err, ErrCIRunDisappeared) {
			return noObservableCIOutcome(cfg.Workflow), nil
		}
		if err != nil {
			return CIOutcome{}, fmt.Errorf("could not observe workflow %q: %w", cfg.Workflow, err)
		}
		if run == nil {
			return noObservableCIOutcome(cfg.Workflow), nil
		}
		if strings.EqualFold(run.Status, "completed") {
			if run.HeadSHA != head {
				return CIOutcome{
					Workflow: cfg.Workflow,
					Status:   "warning",
					Icon:     "⚠️",
					Summary:  fmt.Sprintf("the last run covers %s, not this head", shortSHA(run.HeadSHA)),
					URL:      run.URL,
				}, nil
			}
			if strings.EqualFold(run.Conclusion, "success") {
				return CIOutcome{
					Workflow: cfg.Workflow,
					Status:   "passed",
					Icon:     "✅",
					Summary:  fmt.Sprintf("%s succeeded", cfg.Workflow),
					URL:      run.URL,
				}, nil
			}
			return CIOutcome{
				Workflow:   cfg.Workflow,
				Status:     "failed",
				Icon:       "❌",
				Summary:    fmt.Sprintf("%s failed", cfg.Workflow),
				URL:        run.URL,
				FailedJobs: run.FailedJobs,
			}, nil
		}

		if elapsed := now().Sub(started); elapsed >= time.Duration(cfg.WaitSeconds)*time.Second {
			return pendingCIOutcome(cfg.Workflow, run.URL, cfg.WaitSeconds), nil
		}
		if err := sleep(ctx, time.Duration(cfg.PollSeconds)*time.Second); err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return pendingCIOutcome(cfg.Workflow, run.URL, cfg.WaitSeconds), nil
			}
			return CIOutcome{}, fmt.Errorf("could not wait for workflow %q: %w", cfg.Workflow, err)
		}
	}
}

func pendingCIOutcome(workflow, url string, seconds int) CIOutcome {
	return CIOutcome{
		Workflow: workflow,
		Status:   "pending",
		Icon:     "⏳",
		Summary:  fmt.Sprintf("still running after %ds", seconds),
		URL:      url,
	}
}

func noObservableCIOutcome(workflow string) CIOutcome {
	return CIOutcome{
		Workflow: workflow,
		Status:   "not_observed",
		Icon:     "⚠️",
		Summary:  fmt.Sprintf("%s was triggered but no run is observable", workflow),
	}
}

func shortSHA(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// CommandCIClient is the production GitHub Actions adapter. The command
// callbacks are injectable so application tests do not need a network or shell.
type CommandCIClient struct {
	RunGit func(context.Context, string, ...string) ([]byte, error)
	RunGH  func(context.Context, string, ...string) ([]byte, error)
}

func (c CommandCIClient) Push(ctx context.Context, worktree, remote, branch, head string) error {
	if c.RunGit == nil {
		return errors.New("git command runner is unavailable")
	}
	_, err := c.RunGit(ctx, worktree, "push", remote, head+":refs/heads/"+branch)
	return err
}

func (c CommandCIClient) Dispatch(ctx context.Context, worktree, workflow, ref string) error {
	if c.RunGH == nil {
		return errors.New("gh command runner is unavailable")
	}
	_, err := c.RunGH(ctx, worktree, "workflow", "run", workflow, "--ref", ref)
	return err
}

func (c CommandCIClient) FindRun(ctx context.Context, worktree, workflow, branch string) (*CIRun, error) {
	if c.RunGH == nil {
		return nil, errors.New("gh command runner is unavailable")
	}
	output, err := c.RunGH(ctx, worktree, "run", "list", "--workflow", workflow, "--branch", branch, "--limit", "1", "--json", "databaseId,url,headSha,status,conclusion")
	if err != nil {
		return nil, err
	}
	var listed []struct {
		ID         int64  `json:"databaseId"`
		URL        string `json:"url"`
		HeadSHA    string `json:"headSha"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	}
	if err := json.Unmarshal(output, &listed); err != nil {
		return nil, fmt.Errorf("decode workflow run list: %w", err)
	}
	if len(listed) == 0 {
		return nil, nil
	}
	run := &CIRun{ID: listed[0].ID, URL: listed[0].URL, HeadSHA: listed[0].HeadSHA, Status: listed[0].Status, Conclusion: listed[0].Conclusion}
	if !strings.EqualFold(run.Status, "completed") {
		return run, nil
	}
	view, err := c.RunGH(ctx, worktree, "run", "view", fmt.Sprint(run.ID), "--json", "databaseId,url,headSha,status,conclusion,jobs")
	if err != nil {
		if errors.Is(err, ErrCIRunDisappeared) || isCIRunDisappearance(err) {
			return nil, ErrCIRunDisappeared
		}
		return nil, err
	}
	var detailed struct {
		ID         int64  `json:"databaseId"`
		URL        string `json:"url"`
		HeadSHA    string `json:"headSha"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Jobs       []struct {
			Name       string `json:"name"`
			Conclusion string `json:"conclusion"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(view, &detailed); err != nil {
		return nil, fmt.Errorf("decode workflow run: %w", err)
	}
	run.ID, run.URL, run.HeadSHA, run.Status, run.Conclusion = detailed.ID, detailed.URL, detailed.HeadSHA, detailed.Status, detailed.Conclusion
	for _, job := range detailed.Jobs {
		if isFailingJobConclusion(job.Conclusion) && strings.TrimSpace(job.Name) != "" {
			run.FailedJobs = append(run.FailedJobs, job.Name)
		}
	}
	return run, nil
}

func isCIRunDisappearance(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	response := strings.ToLower(string(exitErr.Stderr))
	return strings.Contains(response, "http 404") && strings.Contains(response, "not found")
}

func isFailingJobConclusion(conclusion string) bool {
	switch strings.ToLower(strings.TrimSpace(conclusion)) {
	case "failure", "timed_out":
		return true
	default:
		return false
	}
}
