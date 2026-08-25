package git

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

type ParentSource string

const (
	ParentSourceExplicit       ParentSource = "explicit"
	ParentSourcePullRequest    ParentSource = "pull_request"
	ParentSourceTracking       ParentSource = "tracking_upstream"
	ParentSourceLocalMergeBase ParentSource = "local_merge_base"
)

type ParentResolution struct {
	Reference string
	Source    ParentSource
	Evidence  []string
	// PublicationBranch is the verified gh --base branch name (T8.4/B).
	PublicationBranch string
}

type ParentResolutionOptions struct {
	ExplicitParent string
	Worktree       string
}

type ParentResolutionError struct {
	Reason   string
	Evidence []string
}

func (e *ParentResolutionError) Error() string {
	message := "could not resolve parent branch: " + e.Reason
	if len(e.Evidence) != 0 {
		message += "; evidence: " + strings.Join(e.Evidence, " | ")
	}
	return message
}

type parentCommandResult struct {
	stdout, stderr string
	err            error
}
type parentCommandRunner func(worktree, command string, args ...string) parentCommandResult

func ResolveParentBranch(options ParentResolutionOptions) (ParentResolution, error) {
	return resolveParentBranch(options, runParentCommand)
}

func resolveParentBranch(options ParentResolutionOptions, run parentCommandRunner) (ParentResolution, error) {
	evidence := []string{}
	if ref := strings.TrimSpace(options.ExplicitParent); ref != "" {
		return verifiedParent(options.Worktree, ref, ParentSourceExplicit, []string{"explicit parent supplied by API"}, run)
	}
	current := run(options.Worktree, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if current.err != nil || strings.TrimSpace(current.stdout) == "" || strings.TrimSpace(current.stdout) == "HEAD" {
		return ParentResolution{}, &ParentResolutionError{Reason: "current branch is unavailable", Evidence: []string{commandEvidence("git rev-parse --abbrev-ref HEAD", current)}}
	}
	branch := strings.TrimSpace(current.stdout)
	evidence = append(evidence, fmt.Sprintf("current branch=%s", branch))

	ref, proof, found, err := pullRequestParent(options.Worktree, run)
	if err != nil {
		return ParentResolution{}, &ParentResolutionError{Reason: err.Error(), Evidence: append(evidence, proof)}
	}
	evidence = append(evidence, proof)
	if found {
		return verifiedParent(options.Worktree, ref, ParentSourcePullRequest, evidence, run)
	}

	ref, proof = trackingParent(options.Worktree, branch, run)
	evidence = append(evidence, proof)
	if ref != "" {
		return verifiedParent(options.Worktree, ref, ParentSourceTracking, evidence, run)
	}

	ref, proof, err = localMergeBaseParent(options.Worktree, branch, run)
	evidence = append(evidence, proof)
	if err != nil {
		return ParentResolution{}, &ParentResolutionError{Reason: err.Error(), Evidence: evidence}
	}
	if ref != "" {
		return verifiedParent(options.Worktree, ref, ParentSourceLocalMergeBase, evidence, run)
	}
	return ParentResolution{}, &ParentResolutionError{Reason: "no reliable parent signal", Evidence: evidence}
}

func verifiedParent(worktree, ref string, source ParentSource, evidence []string, run parentCommandRunner) (ParentResolution, error) {
	check := run(worktree, "git", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	proof := append(append([]string(nil), evidence...), commandEvidence("git rev-parse --verify "+ref+"^{commit}", check))
	if check.err != nil || strings.TrimSpace(check.stdout) == "" {
		return ParentResolution{}, &ParentResolutionError{Reason: fmt.Sprintf("parent reference %q is not a commit", ref), Evidence: proof}
	}
	full := strings.TrimSpace(run(worktree, "git", "rev-parse", "--symbolic-full-name", ref).stdout)
	notPublishable := fmt.Sprintf("parent %q is not a publishable branch (tag, SHA, revision expression or ambiguity)", ref)
	publication := ref
	if strings.HasPrefix(full, "refs/remotes/") {
		rest := strings.TrimPrefix(full, "refs/remotes/")
		if _, name, ok := strings.Cut(rest, "/"); ok && rest == ref {
			publication = name
		}
	} else if full != "refs/heads/"+ref {
		publication = ""
	}
	if publication == "" {
		return ParentResolution{}, &ParentResolutionError{Reason: notPublishable, Evidence: proof}
	}
	return ParentResolution{Reference: ref, PublicationBranch: publication, Source: source, Evidence: append(proof, "resolved commit="+strings.TrimSpace(check.stdout))}, nil
}

func pullRequestParent(worktree string, run parentCommandRunner) (string, string, bool, error) {
	result := run(worktree, "gh", "pr", "view", "--json", "baseRefName")
	if result.err != nil {
		if noPullRequest(result) {
			return "", "gh pr view: no pull request found", false, nil
		}
		return "", commandEvidence("gh pr view --json baseRefName", result), false,
			fmt.Errorf("gh pr view failed: %s", commandEvidence("gh pr view --json baseRefName", result))
	}
	var payload struct {
		BaseRefName string `json:"baseRefName"`
	}
	if err := json.Unmarshal([]byte(result.stdout), &payload); err != nil {
		return "", commandEvidence("gh pr view --json baseRefName", result), false, fmt.Errorf("gh returned invalid JSON: %w", err)
	}
	ref := strings.TrimSpace(payload.BaseRefName)
	if ref == "" {
		return "", "gh pr view: baseRefName is empty", false, fmt.Errorf("gh returned an empty baseRefName")
	}
	if run(worktree, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+ref).err != nil {
		refs, listErr := gitText(run, worktree, "for-each-ref", "--format=%(refname:short)", "refs/remotes/*/"+ref)
		candidates := strings.Fields(refs)
		if listErr != nil || len(candidates) != 1 {
			return "", "gh pr view: baseRefName=" + ref, false, fmt.Errorf("pull request base %q has %d remote candidates", ref, len(candidates))
		}
		ref = candidates[0]
	}
	return ref, fmt.Sprintf("gh pr view: baseRefName=%s", ref), true, nil
}

func trackingParent(worktree, branch string, run parentCommandRunner) (string, string) {
	upstream := run(worktree, "git", "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
	ref := strings.TrimSpace(upstream.stdout)
	if upstream.err != nil || ref == "" {
		return "", "tracking upstream: none configured"
	}
	remote := strings.TrimSpace(run(worktree, "git", "config", "--get", "branch."+branch+".remote").stdout)
	merge := strings.TrimPrefix(strings.TrimSpace(run(worktree, "git", "config", "--get", "branch."+branch+".merge").stdout), "refs/heads/")
	if remote != "" && remote != "." && merge == branch && ref == remote+"/"+branch {
		return "", fmt.Sprintf("tracking upstream %s rejected: it is the current branch remote", ref)
	}
	upstreamSHA := run(worktree, "git", "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	head := run(worktree, "git", "rev-parse", "HEAD")
	base := run(worktree, "git", "merge-base", ref, "HEAD")
	if upstreamSHA.err != nil || head.err != nil || base.err != nil || strings.TrimSpace(upstreamSHA.stdout) == strings.TrimSpace(head.stdout) || strings.TrimSpace(base.stdout) != strings.TrimSpace(upstreamSHA.stdout) {
		return "", fmt.Sprintf("tracking upstream %s rejected: it is not a strict ancestor of HEAD", ref)
	}
	return ref, fmt.Sprintf("tracking upstream %s is a strict ancestor at %s", ref, strings.TrimSpace(upstreamSHA.stdout))
}

type localParentCandidate struct {
	ref, tip string
}

func localMergeBaseParent(worktree, branch string, run parentCommandRunner) (string, string, error) {
	branches, err := gitText(run, worktree, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return "", "local branches: unavailable", err
	}
	head, err := gitText(run, worktree, "rev-parse", "HEAD")
	if err != nil {
		return "", "local branches: HEAD unavailable", err
	}
	var candidates []localParentCandidate
	for _, ref := range strings.Split(branches, "\n") {
		ref = strings.TrimSpace(ref)
		if ref == "" || ref == branch || ref == "main" || ref == "master" {
			continue
		}
		tip, tipErr := gitText(run, worktree, "rev-parse", "--verify", ref+"^{commit}")
		if tipErr != nil || tip == head {
			continue
		}
		base, baseErr := gitText(run, worktree, "merge-base", ref, "HEAD")
		if baseErr == nil && base == tip {
			candidates = append(candidates, localParentCandidate{ref: ref, tip: tip})
		}
	}
	if len(candidates) == 0 {
		return "", "local branches: no non-default branch has a reliable merge-base", nil
	}
	var winners []localParentCandidate
	for _, candidate := range candidates {
		isAncestorOfAnother := false
		for _, other := range candidates {
			if candidate.tip == other.tip {
				continue
			}
			base, err := gitText(run, worktree, "merge-base", candidate.tip, other.tip)
			if err != nil {
				return "", "local branches: topology unavailable", err
			}
			if base == candidate.tip {
				isAncestorOfAnother = true
				break
			}
		}
		if !isAncestorOfAnother {
			winners = append(winners, candidate)
		}
	}
	detail := localCandidateEvidence(candidates, winners)
	if len(winners) != 1 {
		return "", detail, fmt.Errorf("ambiguous local parent candidates")
	}
	return winners[0].ref, detail, nil
}

func localCandidateEvidence(candidates, winners []localParentCandidate) string {
	parts := make([]string, len(candidates))
	for i, candidate := range candidates {
		parts[i] = fmt.Sprintf("%s(tip=%s)", candidate.ref, candidate.tip)
	}
	if len(winners) == 1 {
		return fmt.Sprintf("local merge-base candidates: %s; winner=%s", strings.Join(parts, ", "), winners[0].ref)
	}
	return "local merge-base candidates: " + strings.Join(parts, ", ")
}

func gitText(run parentCommandRunner, worktree string, args ...string) (string, error) {
	result := run(worktree, "git", args...)
	if result.err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), commandEvidence("git", result))
	}
	return strings.TrimSpace(result.stdout), nil
}

func commandEvidence(command string, result parentCommandResult) string {
	detail := strings.TrimSpace(strings.TrimSpace(result.stderr) + " " + strings.TrimSpace(result.stdout))
	if detail == "" && result.err != nil {
		detail = result.err.Error()
	}
	if detail == "" {
		detail = "ok"
	}
	return command + " => " + detail
}

func noPullRequest(result parentCommandResult) bool {
	detail := strings.ToLower(commandEvidence("", result))
	for _, marker := range []string{"no pull requests found", "no pull request found", "pull request not found"} {
		if strings.Contains(detail, marker) {
			return true
		}
	}
	return false
}

func runParentCommand(worktree, command string, args ...string) parentCommandResult {
	cmd := exec.Command(command, args...)
	cmd.Dir = worktree
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return parentCommandResult{stdout: string(out), stderr: stderr.String(), err: err}
}
