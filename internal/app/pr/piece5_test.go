package pr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

const piece5Head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func piece5Entry(t *testing.T, verdict string) store.PRReviewEntry {
	t.Helper()
	attestation := review.Attestation{
		Branch:  "feature/piece5",
		HeadSHA: piece5Head,
		Verdict: verdict,
		Steps: []review.AttestationStep{
			{Step: "slice", Status: "passed"},
			{Step: "review", Status: statusForVerdict(verdict)},
			{Step: "gate", Status: "not_run"},
			{Step: "lint", Status: "not_configured"},
			{Step: "test", Status: "not_configured"},
			{Step: "build", Status: "not_configured"},
			{Step: "pr review", Status: "authored"},
			{Step: "ci", Status: "not_observed"},
		},
	}
	marker, err := review.RenderAttestation(attestation)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(attestation)
	if err != nil {
		t.Fatal(err)
	}
	body := "## Intent\n- preserve the authored bytes\n## What Changed\n- publish the stored judgement\n## Risk Assessment\n" + verdict + "\n## Testing\n- not run\n\n" + marker + "\n\n## Pipeline\n" +
		"<details><summary>✅ <b>slice</b> — intent trailers present</summary>\n\n</details>\n\n" +
		"<details><summary>✅ <b>review</b> — complete</summary>\n\n</details>\n\n" +
		"<details><summary>⚪ <b>gate</b> — not run</summary>\n\n</details>\n\n" +
		"<details><summary>⚪ <b>lint</b> — not configured</summary>\n\n</details>\n\n" +
		"<details><summary>⚪ <b>test</b> — not configured</summary>\n\n</details>\n\n" +
		"<details><summary>⚪ <b>build</b> — not configured</summary>\n\n</details>\n\n" +
		"<details><summary>✅ <b>pr review</b> — body authored</summary>\n\n</details>\n\n" +
		"<details><summary>⚪ <b>ci</b> — not observed by Sentinel</summary>\n\n</details>\n\n"
	return store.PRReviewEntry{Branch: attestation.Branch, HeadSHA: piece5Head, Title: "Keep: title / unchanged", Verdict: verdict, Body: body, Attestation: raw, Evidence: []string{".vas_sentinel/evidence/feature-piece5-abc/pr-review.log"}, At: time.Time{}}
}

func statusForVerdict(verdict string) string {
	switch verdict {
	case review.VerdictBlock:
		return "blocked"
	case review.VerdictWarn:
		return "warning"
	default:
		return "passed"
	}
}

func TestComposePRBodyPreservesNonCIPayload(t *testing.T) {
	entry := piece5Entry(t, review.VerdictBlock)
	oldBody := entry.Body
	outcome := CIOutcome{Status: "passed", Icon: "✅", Summary: "verify.yml succeeded", URL: "https://github.com/example/repo/actions/runs/42"}
	got, err := ComposePRBody(entry, outcome)
	if err != nil {
		t.Fatalf("ComposePRBody: %v", err)
	}
	if !strings.Contains(got, "✅ <b>ci</b> — verify.yml succeeded") || !strings.Contains(got, "Run: https://github.com/example/repo/actions/runs/42") {
		t.Fatalf("composed CI evidence missing:\n%s", got)
	}
	if !strings.Contains(got, "## Risk Assessment\nblock\n") {
		t.Fatal("semantic block was not preserved")
	}
	if strings.Replace(got, "not observed by Sentinel", "verify.yml succeeded", 1) == oldBody {
		t.Fatal("composition did not change the CI block")
	}
	oldAtt, _ := review.ParseAttestation(oldBody)
	newAtt, err := review.ParseAttestation(got)
	if err != nil {
		t.Fatalf("new attestation: %v", err)
	}
	if !reflectStepsExceptCI(oldAtt.Steps, newAtt.Steps) || newAtt.Steps[len(newAtt.Steps)-1].Status != "passed" {
		t.Fatalf("non-CI attestation steps changed: old=%+v new=%+v", oldAtt.Steps, newAtt.Steps)
	}
}

func TestComposePRBodyIgnoresCILiteralsOutsideTheCIHeader(t *testing.T) {
	entry := piece5Entry(t, review.VerdictOK)
	const reviewBlock = "<details><summary>✅ <b>review</b> — complete</summary>\n\n</details>"
	const reviewWithSubject = "<details><summary>✅ <b>review</b> — complete</summary>\n\nfix: <b>ci</b>\n</details>"
	entry.Body = strings.Replace(entry.Body, reviewBlock, reviewWithSubject, 1)
	got, err := ComposePRBody(entry, CIOutcome{Status: "passed", Icon: "✅", Summary: "verify.yml succeeded"})
	if err != nil {
		t.Fatalf("ComposePRBody: %v", err)
	}
	if !strings.Contains(got, reviewWithSubject) || !strings.Contains(got, "✅ <b>ci</b> — verify.yml succeeded") {
		t.Fatalf("CI composition changed the review block or missed the CI block:\n%s", got)
	}
}

func reflectStepsExceptCI(left, right []review.AttestationStep) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Step != right[i].Step {
			return false
		}
		if left[i].Step != "ci" && left[i].Status != right[i].Status {
			return false
		}
	}
	return true
}

func TestComposePRBodyBoundsCIEvidence(t *testing.T) {
	entry := piece5Entry(t, review.VerdictOK)
	jobs := make([]string, 40)
	for i := range jobs {
		jobs[i] = strings.Repeat("&", 60)
	}
	got, err := ComposePRBody(entry, CIOutcome{
		Status:     "failed",
		Icon:       "❌",
		Summary:    strings.Repeat("&", 500),
		URL:        "https://" + strings.Repeat("&", 192),
		FailedJobs: jobs,
	})
	if err != nil {
		t.Fatalf("bounded composition: %v", err)
	}
	if len(got) > review.PRBodyLimit || !strings.Contains(got, "… and 35 more") {
		t.Fatalf("bounded output len=%d or suffix missing", len(got))
	}
}

func TestValidatePRReviewEntryRejectsNonTerminalAndDivergence(t *testing.T) {
	for _, verdict := range []string{review.VerdictQuestion, review.VerdictUnavailable, "invented"} {
		entry := piece5Entry(t, verdict)
		if _, err := ValidatePRReviewEntry(&entry, entry.Branch, entry.HeadSHA); err == nil {
			t.Errorf("verdict %q was accepted", verdict)
		}
	}
	entry := piece5Entry(t, review.VerdictOK)
	if _, err := ValidatePRReviewEntry(&entry, entry.Branch, strings.Repeat("b", 40)); err == nil {
		t.Fatal("head divergence was accepted")
	}
}

type fakeCIClient struct {
	events      []string
	remoteURL   string
	remoteErr   error
	pushErr     error
	dispatchErr error
	dispatchRef string
	pushRef     string
	findErr     error
	onFind      func()
	runs        []*CIRun
}

func (f *fakeCIClient) RemoteURL(_ context.Context, _ string, _ string) (string, error) {
	f.events = append(f.events, "remote")
	if f.remoteErr != nil {
		return "", f.remoteErr
	}
	if f.remoteURL == "" {
		return "https://github.com/example/repo.git", nil
	}
	return f.remoteURL, nil
}

func (f *fakeCIClient) Push(_ context.Context, _ string, _ string, _ string, head string) error {
	f.events = append(f.events, "push")
	f.pushRef = head
	return f.pushErr
}

func (f *fakeCIClient) Dispatch(_ context.Context, _ string, _ string, ref string) error {
	f.events = append(f.events, "dispatch")
	f.dispatchRef = ref
	return f.dispatchErr
}

func (f *fakeCIClient) FindRun(context.Context, string, string, string) (*CIRun, error) {
	f.events = append(f.events, "poll")
	if f.onFind != nil {
		f.onFind()
	}
	if f.findErr != nil {
		return nil, f.findErr
	}
	if len(f.runs) == 0 {
		return nil, nil
	}
	run := f.runs[0]
	if len(f.runs) > 1 {
		f.runs = f.runs[1:]
	}
	return run, nil
}

func TestRunConfiguredCIOrdersPushDispatchPollAndReportsOutcomes(t *testing.T) {
	cases := []struct {
		name       string
		run        *CIRun
		wantStatus string
		wantIcon   string
	}{
		{"success", &CIRun{URL: "https://run", HeadSHA: piece5Head, Status: "completed", Conclusion: "success"}, "passed", "✅"},
		{"failure", &CIRun{URL: "https://run", HeadSHA: piece5Head, Status: "completed", Conclusion: "failure", FailedJobs: []string{"unit"}}, "failed", "❌"},
		{"wrong head", &CIRun{URL: "https://run", HeadSHA: strings.Repeat("b", 40), Status: "completed", Conclusion: "success"}, "warning", "⚠️"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := &fakeCIClient{runs: []*CIRun{tc.run}}
			var output bytes.Buffer
			got, err := RunConfiguredCI(context.Background(), &output, "worktree", "feature/piece5", piece5Head, config.CIConfig{Workflow: "verify.yml", WaitSeconds: 10, PollSeconds: 1}, client, func(string, string) string { return "origin" }, nil, func(context.Context, time.Duration) error { return nil })
			if err != nil || got.Status != tc.wantStatus || got.Icon != tc.wantIcon {
				t.Fatalf("outcome=%+v err=%v", got, err)
			}
			if !reflect.DeepEqual(client.events, []string{"remote", "push", "dispatch", "poll"}) {
				t.Fatalf("events=%v, want remote push dispatch poll", client.events)
			}
			if !strings.Contains(output.String(), "Pushing feature/piece5 to origin") {
				t.Fatal("push announcement missing")
			}
			if client.dispatchRef != "feature/piece5" {
				t.Fatalf("dispatch ref=%q, want pushed branch", client.dispatchRef)
			}
			if client.pushRef != piece5Head {
				t.Fatalf("push ref=%q, want reviewed head %q", client.pushRef, piece5Head)
			}
		})
	}
}

func TestRunConfiguredCIReportsOneProgressLinePerPoll(t *testing.T) {
	client := &fakeCIClient{runs: []*CIRun{
		{URL: "https://run", HeadSHA: piece5Head, Status: "in_progress"},
		{URL: "https://run", HeadSHA: piece5Head, Status: "completed", Conclusion: "success"},
	}}
	var output bytes.Buffer
	_, err := RunConfiguredCI(context.Background(), &output, "worktree", "feature/piece5", piece5Head, config.CIConfig{Workflow: "verify.yml", WaitSeconds: 10, PollSeconds: 1}, client, func(string, string) string { return "origin" }, nil, func(context.Context, time.Duration) error { return nil })
	if err != nil {
		t.Fatalf("RunConfiguredCI: %v", err)
	}
	if got := strings.Count(output.String(), "Waiting for verify.yml.\n"); got != 2 {
		t.Fatalf("progress lines=%d, want one for each of two polls; output=%q", got, output.String())
	}
}

func TestRunConfiguredCIWaitsForAnAsynchronouslyCreatedRun(t *testing.T) {
	clock := time.Unix(100, 0)
	client := &fakeCIClient{runs: []*CIRun{
		nil,
		{URL: "https://run", HeadSHA: piece5Head, Status: "completed", Conclusion: "success"},
	}}
	got, err := RunConfiguredCI(context.Background(), io.Discard, "worktree", "feature/piece5", piece5Head, config.CIConfig{Workflow: "verify.yml", WaitSeconds: 2, PollSeconds: 1}, client, func(string, string) string { return "origin" }, func() time.Time { return clock }, func(context.Context, time.Duration) error {
		clock = clock.Add(time.Second)
		return nil
	})
	if err != nil || got.Status != "passed" || !reflect.DeepEqual(client.events, []string{"remote", "push", "dispatch", "poll", "poll"}) {
		t.Fatalf("outcome=%+v err=%v events=%v", got, err, client.events)
	}
}

func TestCommandCIClientPreservesObservationErrorsAndFiltersFailedJobs(t *testing.T) {
	viewErr := errors.New("GitHub API unavailable")
	calls := 0
	client := CommandCIClient{RunGH: func(context.Context, string, ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`[{"databaseId":42,"url":"https://run","headSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"completed","conclusion":"failure"}]`), nil
		}
		return nil, viewErr
	}}
	if _, err := client.FindRun(context.Background(), "worktree", "verify.yml", "feature/piece5"); !errors.Is(err, viewErr) {
		t.Fatalf("view error=%v, want %v", err, viewErr)
	}

	calls = 0
	notFound := &exec.ExitError{Stderr: []byte("HTTP 404: Not Found")}
	client.RunGH = func(context.Context, string, ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`[{"databaseId":42,"url":"https://run","headSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"completed","conclusion":"failure"}]`), nil
		}
		return nil, notFound
	}
	if _, err := client.FindRun(context.Background(), "worktree", "verify.yml", "feature/piece5"); !errors.Is(err, ErrCIRunDisappeared) {
		t.Fatalf("not-found error=%v, want disappeared run", err)
	}

	calls = 0
	client.RunGH = func(context.Context, string, ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(`[{"databaseId":42,"url":"https://run","headSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"completed","conclusion":"failure"}]`), nil
		}
		return []byte(`{"databaseId":42,"url":"https://run","headSha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","status":"completed","conclusion":"failure","jobs":[{"name":"failed","conclusion":"failure"},{"name":"timed out","conclusion":"timed_out"},{"name":"skipped","conclusion":"skipped"},{"name":"cancelled","conclusion":"cancelled"}]}`), nil
	}
	run, err := client.FindRun(context.Background(), "worktree", "verify.yml", "feature/piece5")
	if err != nil || !reflect.DeepEqual(run.FailedJobs, []string{"failed", "timed out"}) {
		t.Fatalf("run=%+v err=%v", run, err)
	}

	var pushArgs []string
	client.RunGit = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		pushArgs = args
		if reflect.DeepEqual(args, []string{"remote", "get-url", "origin"}) {
			return []byte("https://github.com/example/repo.git\n"), nil
		}
		return nil, nil
	}
	if err := client.Push(context.Background(), "worktree", "origin", "feature/piece5", piece5Head); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if want := []string{"push", "origin", piece5Head + ":refs/heads/feature/piece5"}; !reflect.DeepEqual(pushArgs, want) {
		t.Fatalf("push args=%v, want %v", pushArgs, want)
	}
	remoteURL, err := client.RemoteURL(context.Background(), "worktree", "origin")
	if err != nil || remoteURL != "https://github.com/example/repo.git" || !reflect.DeepEqual(pushArgs, []string{"remote", "get-url", "origin"}) {
		t.Fatalf("remote URL=%q err=%v args=%v", remoteURL, err, pushArgs)
	}
}

func TestRunConfiguredCIRefusesNonGitHubRemoteBeforePush(t *testing.T) {
	client := &fakeCIClient{remoteURL: "https://gitlab.com/example/repo.git"}
	_, err := RunConfiguredCI(context.Background(), io.Discard, "worktree", "feature/piece5", piece5Head, config.CIConfig{Workflow: "verify.yml"}, client, func(string, string) string { return "origin" }, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "github.com") {
		t.Fatalf("err=%v", err)
	}
	if !reflect.DeepEqual(client.events, []string{"remote"}) {
		t.Fatalf("events=%v, want remote only", client.events)
	}
}

func TestRunConfiguredCIAcceptsGitHubSSHRemote(t *testing.T) {
	client := &fakeCIClient{remoteURL: "git@github.com:example/repo.git", runs: []*CIRun{{HeadSHA: piece5Head, Status: "completed", Conclusion: "success"}}}
	got, err := RunConfiguredCI(context.Background(), io.Discard, "worktree", "feature/piece5", piece5Head, config.CIConfig{Workflow: "verify.yml"}, client, func(string, string) string { return "origin" }, nil, nil)
	if err != nil || got.Status != "passed" {
		t.Fatalf("outcome=%+v err=%v", got, err)
	}
	if !reflect.DeepEqual(client.events, []string{"remote", "push", "dispatch", "poll"}) {
		t.Fatalf("events=%v", client.events)
	}
}

func TestRunConfiguredCIFailureStopsBeforeDispatch(t *testing.T) {
	client := &fakeCIClient{pushErr: errors.New("network down")}
	got, err := RunConfiguredCI(context.Background(), io.Discard, "worktree", "feature/piece5", piece5Head, config.CIConfig{Workflow: "verify.yml"}, client, func(string, string) string { return "origin" }, nil, nil)
	if err == nil || got.Status != "" {
		t.Fatalf("push error outcome=%+v err=%v", got, err)
	}
	if !reflect.DeepEqual(client.events, []string{"remote", "push"}) {
		t.Fatalf("events=%v, want remote then push only", client.events)
	}
}

func TestRunConfiguredCITimeoutAndInterruptionArePublishableOutcomes(t *testing.T) {
	clock := time.Unix(100, 0)
	client := &fakeCIClient{runs: []*CIRun{{URL: "https://run", HeadSHA: piece5Head, Status: "in_progress"}}}
	got, err := RunConfiguredCI(context.Background(), io.Discard, "worktree", "feature/piece5", piece5Head, config.CIConfig{Workflow: "verify.yml", WaitSeconds: 2, PollSeconds: 1}, client, func(string, string) string { return "origin" }, func() time.Time { return clock }, func(context.Context, time.Duration) error { clock = clock.Add(2 * time.Second); return nil })
	if err != nil || got.Status != "pending" || got.Icon != "⏳" {
		t.Fatalf("timeout outcome=%+v err=%v", got, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	client = &fakeCIClient{onFind: cancel, findErr: context.Canceled}
	got, err = RunConfiguredCI(ctx, io.Discard, "worktree", "feature/piece5", piece5Head, config.CIConfig{Workflow: "verify.yml", WaitSeconds: 900, PollSeconds: 1}, client, func(string, string) string { return "origin" }, time.Now, nil)
	if err != nil || got.Status != "pending" || !reflect.DeepEqual(client.events, []string{"remote", "push", "dispatch", "poll"}) {
		t.Fatalf("interrupted outcome=%+v err=%v events=%v", got, err, client.events)
	}
}

func TestRunConfiguredCIMissingRunAndDisabledDoNotPush(t *testing.T) {
	client := &fakeCIClient{}
	got, err := RunConfiguredCI(context.Background(), io.Discard, "worktree", "feature/piece5", piece5Head, config.CIConfig{}, client, func(string, string) string { t.Fatal("remote must not be resolved"); return "" }, nil, nil)
	if err != nil || got.Status != "not_observed" || len(client.events) != 0 {
		t.Fatalf("disabled outcome=%+v err=%v events=%v", got, err, client.events)
	}

	client = &fakeCIClient{}
	clock := time.Unix(100, 0)
	got, err = RunConfiguredCI(context.Background(), io.Discard, "worktree", "feature/piece5", piece5Head, config.CIConfig{Workflow: "verify.yml", WaitSeconds: 1, PollSeconds: 1}, client, func(string, string) string { return "origin" }, func() time.Time { return clock }, func(context.Context, time.Duration) error {
		clock = clock.Add(time.Second)
		return nil
	})
	if err != nil || got.Status != "not_observed" || !strings.Contains(got.Summary, "no run is observable") {
		t.Fatalf("missing-run outcome=%+v err=%v", got, err)
	}

	client = &fakeCIClient{findErr: ErrCIRunDisappeared}
	got, err = RunConfiguredCI(context.Background(), io.Discard, "worktree", "feature/piece5", piece5Head, config.CIConfig{Workflow: "verify.yml"}, client, func(string, string) string { return "origin" }, nil, nil)
	if err != nil || got.Status != "not_observed" || !strings.Contains(got.Summary, "no run is observable") {
		t.Fatalf("disappeared-run outcome=%+v err=%v", got, err)
	}
}

func TestRunPrCreatePreflightRejectsBeforeValidationOrPublication(t *testing.T) {
	entry := piece5Entry(t, review.VerdictOK)
	var events []string
	deps := DepsPrCreate{
		CurrentBranch:   func(string) (string, error) { events = append(events, "branch"); return entry.Branch, nil },
		GetHeadSHAAt:    func(string) (string, error) { events = append(events, "head"); return entry.HeadSHA, nil },
		GetGitCommonDir: func(string) (string, error) { events = append(events, "common"); return "common", nil },
		ReadPRReview:    func(string, string) (*store.PRReviewEntry, error) { events = append(events, "entry"); return nil, nil },
		RunValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
			events = append(events, "validation")
			return nil, nil
		},
		PublishStored: func(string, string, string, string) (string, bool, error) {
			events = append(events, "publish")
			return "", false, nil
		},
	}
	var output bytes.Buffer
	if code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{}, deps, Wiring{}); code != 1 {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
	if !reflect.DeepEqual(events, []string{"branch", "head", "common", "entry"}) {
		t.Fatalf("preflight events=%v", events)
	}
}

func TestRunPrCreatePublishesSemanticBlockWithoutCallingSemanticReview(t *testing.T) {
	entry := piece5Entry(t, review.VerdictBlock)
	var published bool
	deps := DepsPrCreate{
		CurrentBranch:   func(string) (string, error) { return entry.Branch, nil },
		GetHeadSHAAt:    func(string) (string, error) { return entry.HeadSHA, nil },
		GetGitCommonDir: func(string) (string, error) { return "common", nil },
		ReadPRReview:    func(string, string) (*store.PRReviewEntry, error) { return &entry, nil },
		EvidenceAtHEAD:  func(string, string) (bool, string, error) { return true, "", nil },
		LoadConfig:      func(string) (config.Config, error) { return config.Config{}, nil },
		GetGitDirAt:     func(string) (string, error) { return "gitdir", nil },
		RunValidation:   func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) { return nil, nil },
		ComposeBody:     func(got store.PRReviewEntry, outcome CIOutcome) (string, error) { return got.Body, nil },
		WriteTemplate:   func(string) (string, error) { return "template", nil },
		PublishStored: func(_ string, title, path, base string) (string, bool, error) {
			published = title == entry.Title && path == "template" && base == "main"
			return "https://github.com/example/repo/pull/5", false, nil
		},
	}
	var output bytes.Buffer
	if code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{}, deps, Wiring{}); code != 0 {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
	if !published {
		t.Fatal("the persisted blocking review should still publish")
	}
}

func TestRunPrCreatePublishesStackedPRAgainstResolvedParent(t *testing.T) {
	entry := piece5Entry(t, review.VerdictOK)
	for _, tc := range []struct {
		name       string
		flags      FlagsPrCreate
		wantParent string
		wantBase   string
	}{
		{name: "explicit remote parent", flags: FlagsPrCreate{Parent: "origin/feature-a"}, wantParent: "origin/feature-a", wantBase: "feature-a"},
		{name: "inferred parent", flags: FlagsPrCreate{ChainPR: true}, wantBase: "feature-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var resolved git.ParentResolutionOptions
			var publishedBase string
			deps := DepsPrCreate{
				CurrentBranch:   func(string) (string, error) { return entry.Branch, nil },
				GetHeadSHAAt:    func(string) (string, error) { return entry.HeadSHA, nil },
				GetGitCommonDir: func(string) (string, error) { return "common", nil },
				ReadPRReview:    func(string, string) (*store.PRReviewEntry, error) { return &entry, nil },
				EvidenceAtHEAD:  func(string, string) (bool, string, error) { return true, "", nil },
				LoadConfig:      func(string) (config.Config, error) { return config.Config{}, nil },
				GetGitDirAt:     func(string) (string, error) { return "gitdir", nil },
				RunValidation:   func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) { return nil, nil },
				ResolveParent: func(options git.ParentResolutionOptions) (git.ParentResolution, error) {
					resolved = options
					return git.ParentResolution{Reference: "origin/feature-a", PublicationBranch: "feature-a"}, nil
				},
				ComposeBody:   func(store.PRReviewEntry, CIOutcome) (string, error) { return entry.Body, nil },
				WriteTemplate: func(string) (string, error) { return "template", nil },
				PublishStored: func(_ string, _ string, _ string, base string) (string, bool, error) {
					publishedBase = base
					return "https://github.com/example/repo/pull/5", false, nil
				},
			}
			if code := RunPrCreateWith(io.Discard, "worktree", tc.flags, deps, Wiring{}); code != 0 {
				t.Fatalf("exit=%d", code)
			}
			if resolved.Worktree != "worktree" || resolved.ExplicitParent != tc.wantParent || publishedBase != tc.wantBase {
				t.Fatalf("resolved=%+v published base=%q", resolved, publishedBase)
			}
		})
	}
}

func TestRunPrCreateRejectsStaleAndEvidenceBeforeValidation(t *testing.T) {
	entry := piece5Entry(t, review.VerdictOK)
	for _, tc := range []struct {
		name     string
		read     func(*store.PRReviewEntry) *store.PRReviewEntry
		evidence func(string, string) (bool, string, error)
		want     string
	}{
		{"stale", func(e *store.PRReviewEntry) *store.PRReviewEntry {
			copy := *e
			copy.HeadSHA = strings.Repeat("b", 40)
			return &copy
		}, func(string, string) (bool, string, error) { return true, "", nil }, "covers"},
		{"untracked evidence", func(e *store.PRReviewEntry) *store.PRReviewEntry { return e }, func(string, string) (bool, string, error) { return false, "not in HEAD", nil }, "not committed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var validated bool
			deps := DepsPrCreate{
				CurrentBranch:   func(string) (string, error) { return entry.Branch, nil },
				GetHeadSHAAt:    func(string) (string, error) { return entry.HeadSHA, nil },
				GetGitCommonDir: func(string) (string, error) { return "common", nil },
				ReadPRReview:    func(string, string) (*store.PRReviewEntry, error) { return tc.read(&entry), nil },
				EvidenceAtHEAD:  tc.evidence,
				LoadConfig:      func(string) (config.Config, error) { return config.Config{}, nil },
				RunValidation: func(string, []string, validation.RunOptions) ([]validation.ValidationRun, error) {
					validated = true
					return nil, nil
				},
			}
			var output bytes.Buffer
			if code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{}, deps, Wiring{}); code != 1 || validated || !strings.Contains(output.String(), tc.want) {
				t.Fatalf("code=%d validated=%v output=%q", code, validated, output.String())
			}
		})
	}
}

func TestComposePRBodyChangesOnlyCIStepAndAttestation(t *testing.T) {
	entry := piece5Entry(t, review.VerdictWarn)
	oldBody := entry.Body
	outcome := CIOutcome{
		Status:     "failed",
		Icon:       "❌",
		Summary:    "verify.yml failed",
		URL:        "https://github.com/example/repo/actions/runs/42",
		FailedJobs: []string{"unit", "integration"},
	}
	got, err := ComposePRBody(entry, outcome)
	if err != nil {
		t.Fatalf("ComposePRBody: %v", err)
	}
	oldAttestation, err := review.ParseAttestation(oldBody)
	if err != nil {
		t.Fatal(err)
	}
	newAttestation, err := review.ParseAttestation(got)
	if err != nil {
		t.Fatal(err)
	}
	oldMarker, err := review.RenderAttestation(oldAttestation)
	if err != nil {
		t.Fatal(err)
	}
	newMarker, err := review.RenderAttestation(newAttestation)
	if err != nil {
		t.Fatal(err)
	}
	oldCI := detailsBlock(t, oldBody, "<b>ci</b>")
	newCI := detailsBlock(t, got, "<b>ci</b>")
	normalize := func(body, marker, ci string) string {
		body = strings.Replace(body, marker, "<attestation>", 1)
		return strings.Replace(body, ci, "<ci-details>", 1)
	}
	if normalize(oldBody, oldMarker, oldCI) != normalize(got, newMarker, newCI) {
		t.Fatal("composition changed bytes outside the CI details block and attestation")
	}
}

func detailsBlock(t *testing.T, body, marker string) string {
	t.Helper()
	start := strings.LastIndex(body, "<details><summary>")
	for start >= 0 {
		end := strings.Index(body[start:], "</details>")
		if end < 0 {
			break
		}
		end += start + len("</details>")
		if strings.Contains(body[start:end], marker) {
			if strings.HasPrefix(body[end:], "\\n\\n") {
				end += 2
			}
			return body[start:end]
		}
		start = strings.LastIndex(body[:start], "<details><summary>")
	}
	t.Fatalf("details block %q not found", marker)
	return ""
}

func TestComposePRBodyAtLimitReplacesReservedCIBlock(t *testing.T) {
	entry := piece5Entry(t, review.VerdictOK)
	oldCI := detailsBlock(t, entry.Body, "<b>ci</b>")
	padding := strings.Repeat("x", review.PRBodyLimit-len(entry.Body))
	entry.Body = strings.Replace(entry.Body, oldCI, oldCI[:len(oldCI)-len("</details>")]+padding+"</details>", 1)
	if len(entry.Body) != review.PRBodyLimit {
		t.Fatalf("fixture body len=%d, want %d", len(entry.Body), review.PRBodyLimit)
	}
	got, err := ComposePRBody(entry, CIOutcome{
		Status:  "passed",
		Icon:    "✅",
		Summary: "verify.yml succeeded",
	})
	if err != nil {
		t.Fatalf("reserved body should compose: %v", err)
	}
	if len(got) > review.PRBodyLimit || !strings.Contains(got, "verify.yml succeeded") {
		t.Fatalf("composed body len=%d or CI result missing", len(got))
	}
}

func TestRunPrCreatePublishesWhenCIHasNoObservableRun(t *testing.T) {
	entry := piece5Entry(t, review.VerdictBlock)
	var published bool
	var gotBody string
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
			return noObservableCIOutcome("verify.yml"), nil
		},
		ComposeBody: func(got store.PRReviewEntry, outcome CIOutcome) (string, error) {
			body, err := ComposePRBody(got, outcome)
			gotBody = body
			return body, err
		},
		WriteTemplate: func(string) (string, error) { return "template", nil },
		PublishStored: func(_ string, title, templatePath, base string) (string, bool, error) {
			published = title == entry.Title && templatePath == "template" && base == "main"
			return "https://github.com/example/repo/pull/5", false, nil
		},
	}
	var output bytes.Buffer
	if code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{}, deps, Wiring{}); code != 0 {
		t.Fatalf("exit=%d output=%s", code, output.String())
	}
	if !published || !strings.Contains(gotBody, "verify.yml was triggered but no run is observable") {
		t.Fatalf("published=%v body=%q", published, gotBody)
	}
}

func TestRunPrCreateForceRequiresReasonAndPublishesRedValidation(t *testing.T) {
	entry := piece5Entry(t, review.VerdictOK)
	baseDeps := func() DepsPrCreate {
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
				return []validation.ValidationRun{{Capability: "test", Command: "go test ./...", Exit: 1, Output: "FAIL"}}, nil
			},
			ComposeBody:   func(store.PRReviewEntry, CIOutcome) (string, error) { return entry.Body, nil },
			WriteTemplate: func(string) (string, error) { return "template", nil },
			PublishStored: func(string, string, string, string) (string, bool, error) { return "url", false, nil },
		}
	}
	var output bytes.Buffer
	if code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{Force: true}, baseDeps(), Wiring{}); code != 1 || !strings.Contains(output.String(), "requires --reason") {
		t.Fatalf("missing reason: code=%d output=%q", code, output.String())
	}
	output.Reset()
	if code := RunPrCreateWith(&output, "worktree", FlagsPrCreate{Force: true, Reason: "approved exception"}, baseDeps(), Wiring{}); code != 0 || !strings.Contains(output.String(), "overridden") {
		t.Fatalf("forced red validation: code=%d output=%q", code, output.String())
	}
}
