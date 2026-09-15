package pr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
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
	RemoteURL(context.Context, string, string) (string, error)
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
	remoteURL, err := client.RemoteURL(ctx, worktree, remoteName)
	if err != nil {
		return CIOutcome{}, fmt.Errorf("cannot collect CI: could not resolve remote %q: %w", remoteName, err)
	}
	if !isGitHubActionsRemote(remoteURL) {
		return CIOutcome{}, fmt.Errorf("cannot collect CI: remote %q is not hosted on github.com, which is required for GitHub Actions", remoteName)
	}
	fmt.Fprintf(out, "⬆️ Pushing %s to %s.\n", branch, remoteName)
	if err := client.Push(ctx, worktree, remoteName, branch, head); err != nil {
		return CIOutcome{}, fmt.Errorf("push failed before CI dispatch: %w", err)
	}
	if err := client.Dispatch(ctx, worktree, cfg.Workflow, branch); err != nil {
		return CIOutcome{}, fmt.Errorf("could not dispatch workflow %q: %w", cfg.Workflow, err)
	}

	started := now()
	elapsedSeconds := func() int { return int(now().Sub(started).Seconds()) }
	for {
		if ctx.Err() != nil {
			return pendingCIOutcome(cfg.Workflow, "", elapsedSeconds()), nil
		}
		fmt.Fprintf(out, "⏳ Waiting for %s.\n", cfg.Workflow)
		run, err := client.FindRun(ctx, worktree, cfg.Workflow, branch)
		if ctx.Err() != nil {
			url := ""
			if run != nil {
				url = run.URL
			}
			return pendingCIOutcome(cfg.Workflow, url, elapsedSeconds()), nil
		}
		if errors.Is(err, ErrCIRunDisappeared) {
			return noObservableCIOutcome(cfg.Workflow), nil
		}
		if err != nil {
			return CIOutcome{}, fmt.Errorf("could not observe workflow %q: %w", cfg.Workflow, err)
		}
		runURL := ""
		staleHead := ""
		if run != nil {
			runURL = run.URL
			if strings.EqualFold(run.Status, "completed") {
				if run.HeadSHA == head {
					return completedCIOutcome(cfg.Workflow, run), nil
				}
				staleHead = run.HeadSHA
			}
		}

		if elapsed := now().Sub(started); elapsed >= time.Duration(cfg.WaitSeconds)*time.Second {
			if staleHead != "" {
				return CIOutcome{
					Status:  "warning",
					Icon:    "⚠️",
					Summary: fmt.Sprintf("the last run covers %s, not this head", shortSHA(staleHead)),
					URL:     runURL,
				}, nil
			}
			if run == nil {
				return noObservableCIOutcome(cfg.Workflow), nil
			}
			return pendingCIOutcome(cfg.Workflow, runURL, elapsedSeconds()), nil
		}
		if err := sleep(ctx, time.Duration(cfg.PollSeconds)*time.Second); err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return pendingCIOutcome(cfg.Workflow, runURL, elapsedSeconds()), nil
			}
			return CIOutcome{}, fmt.Errorf("could not wait for workflow %q: %w", cfg.Workflow, err)
		}
	}
}

// completedCIOutcome renders a terminal run for this head. GitHub Actions
// concludes runs that never failed as cancelled, skipped, or neutral; those are
// reported as warnings rather than as a CI failure.
func completedCIOutcome(workflow string, run *CIRun) CIOutcome {
	switch strings.ToLower(strings.TrimSpace(run.Conclusion)) {
	case "success":
		return CIOutcome{
			Status:  "passed",
			Icon:    "✅",
			Summary: fmt.Sprintf("%s succeeded", workflow),
			URL:     run.URL,
		}
	case "cancelled", "skipped", "neutral":
		return CIOutcome{
			Status:  "warning",
			Icon:    "⚠️",
			Summary: fmt.Sprintf("%s concluded as %s", workflow, strings.ToLower(strings.TrimSpace(run.Conclusion))),
			URL:     run.URL,
		}
	default:
		return CIOutcome{
			Status:     "failed",
			Icon:       "❌",
			Summary:    fmt.Sprintf("%s failed", workflow),
			URL:        run.URL,
			FailedJobs: run.FailedJobs,
		}
	}
}

func pendingCIOutcome(workflow, url string, seconds int) CIOutcome {
	return CIOutcome{
		Status:  "pending",
		Icon:    "⏳",
		Summary: fmt.Sprintf("still running after %ds", seconds),
		URL:     url,
	}
}

func noObservableCIOutcome(workflow string) CIOutcome {
	return CIOutcome{
		Status:  "not_observed",
		Icon:    "⚠️",
		Summary: fmt.Sprintf("%s was triggered but no run is observable", workflow),
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

func (c CommandCIClient) RemoteURL(ctx context.Context, worktree, remote string) (string, error) {
	if c.RunGit == nil {
		return "", errors.New("git command runner is unavailable")
	}
	output, err := c.RunGit(ctx, worktree, "remote", "get-url", remote)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func isGitHubActionsRemote(remote string) bool {
	remote = strings.TrimSpace(remote)
	if !strings.Contains(remote, "://") {
		host, _, found := strings.Cut(remote, ":")
		if found {
			if _, candidate, hasUser := strings.Cut(host, "@"); hasUser {
				host = candidate
			}
			return strings.EqualFold(host, "github.com")
		}
	}
	parsed, err := url.Parse(remote)
	return err == nil && strings.EqualFold(parsed.Hostname(), "github.com")
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
		if isCIRunDisappearance(err) {
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

// RemoteBranchHeadWith resolves the SHA the remote branch currently points at,
// using the same remote resolution as the CI push. An empty result means the
// remote does not carry that branch yet.
func RemoteBranchHeadWith(ctx context.Context, worktree, branch string, remote func(string, string) string, runGit func(context.Context, string, ...string) ([]byte, error)) (string, error) {
	if remote == nil {
		remote = git.BranchRemoteFrom
	}
	if runGit == nil {
		return "", errors.New("git command runner is unavailable")
	}
	remoteName := strings.TrimSpace(remote(worktree, branch))
	if remoteName == "" {
		return "", nil
	}
	output, err := runGit(ctx, worktree, "ls-remote", remoteName, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(output))
	if line == "" {
		return "", nil
	}
	sha, _, _ := strings.Cut(line, "\t")
	return strings.TrimSpace(sha), nil
}
