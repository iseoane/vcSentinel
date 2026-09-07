package pr

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentadapter"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/ops"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
)

// exitOnError centralizes the CLI exit pattern: prints the error and
// leaves with code 1. Without an error it does nothing.
func exitOnError(err error) {
	if err != nil {
		fmt.Printf("? %v\n", err)
		os.Exit(1)
	}
}

// WritePRTemplate saves the PR body to a temporary file and returns its
// path. The temporary file avoids dirtying the worktree: the template is an
// ephemeral publication artifact.
func WritePRTemplate(body string) (string, error) {
	file, err := os.CreateTemp("", "sentinel_pr_*.md")
	if err != nil {
		return "", fmt.Errorf("could not create the temporary template file: %w", err)
	}
	defer file.Close()
	if _, err := file.WriteString(body); err != nil {
		return "", fmt.Errorf("could not write the template: %w", err)
	}
	return file.Name(), nil
}

// CopyToClipboardWith is the injectable version of copyToClipboard:
// available decides which tool is present; run performs the copy.
// It uses the FIRST tool of the canonical order (clip > wl-copy > xclip) that
// exists on the PATH: if that execution fails, the next one is not tried. It
// is a deliberate decision: the first available tool is the platform
// canonical one and its failure almost always indicates a broken
// environment, not a tool failure.
func CopyToClipboardWith(text string, available func(string) bool, run func(string, string) error) error {
	candidates := []string{"clip", "wl-copy", "xclip"}
	for _, name := range candidates {
		if !available(name) {
			continue
		}
		if err := run(name, text); err != nil {
			return fmt.Errorf("could not copy to the clipboard with %s: %w", name, err)
		}
		return nil
	}
	return errors.New("no clipboard tool found (clip/wl-copy/xclip)")
}

// PublishPRWith is the injectable version of publishPR (test seam). The
// three callbacks are the fields of the publishPROptions struct that
// cmd/sentinel builds: ghAvailable decides whether gh is on the PATH, runGh
// launches gh and returns its output (full args, including the worktree as
// cwd), copy is used only in the fallback (clipboard).
func PublishPRWith(worktree, templatePath, base string,
	ghAvailable func(string) bool,
	runGh func(worktree string, args ...string) ([]byte, error),
	copy func(string) error) (string, bool, error) {
	if ghAvailable("gh") {
		args := []string{"pr", "create", "--draft"}
		if base != "" {
			// The PR must target the SAME base that was audited: without an
			// explicit --base, the review and the PR could diverge silently.
			args = append(args, "--base", base)
		}
		args = append(args, "-F", templatePath)
		output, err := runGh(worktree, args...)
		if err != nil {
			return "", false, err
		}
		return strings.TrimSpace(string(output)), false, nil
	}

	body, err := os.ReadFile(templatePath)
	if err != nil {
		return "", true, fmt.Errorf("could not re-read the template for the clipboard: %w", err)
	}
	fmt.Printf("? gh is not on the PATH: the template was left at %s and is copied to the clipboard.\n", templatePath)
	if err := copy(string(body)); err != nil {
		return "", true, err
	}
	return "", true, nil
}

// VerifyForTemplateWith is the injectable version of verifyForTemplate:
// a nil verify is replaced by ops.Verify in production. The verifier must be
// the one shared by the whole invocation to avoid repeating the probe.
// newModelVerifier is wired from package main as a closure over its
// newModelVerifier var (tests swap it), so the var is read at call time.
func VerifyForTemplateWith(worktree, gitDir string, cfg config.Config, modelVerifier *modelprobe.Verifier,
	verify func(ops.VerifyOptions) (ops.VerificationResult, error),
	newModelVerifier func(worktree string) *modelprobe.Verifier) review.TemplateVerification {

	if verify == nil {
		verify = ops.Verify
	}

	profile := config.ResolveProfile(cfg, "", "")
	adapter, err := agentadapter.NewAdapterWithProfile(cfg, profile)
	if err != nil {
		// Without an agent there is no delegation path; the deterministic
		// path stays alive. ops.Verify accepts a nil Agent (it checks before
		// using it): delegation degrades to "sin_agente", never panic.
		adapter = nil
	}
	if modelVerifier == nil {
		modelVerifier = newModelVerifier(worktree)
	}
	modelVerifier.Verify(profile.Name, profile.Model, adapter)
	verif, err := verify(ops.VerifyOptions{
		Worktree: worktree,
		GitDir:   gitDir,
		Cfg:      cfg,
		Agent:    adapter,
		Questionr: func(prompt string) (string, error) {
			fmt.Println(prompt)
			fmt.Print("> ")
			var answer string
			if _, err := fmt.Scanln(&answer); err != nil {
				return "", err
			}
			return answer, nil
		},
	})
	if err != nil {
		return review.TemplateVerification{
			Mode:   ops.ModeSkipped,
			Reason: fmt.Sprintf("verification_error: %v", err),
		}
	}
	template := review.TemplateVerification{
		Mode:   verif.Mode,
		Tested: verif.Tested,
		Reason: verif.Reason,
	}
	for _, c := range verif.Commands {
		template.Comandos = append(template.Comandos, review.VerifiedCommand{Comando: c.Command, Exit: c.Exit})
	}
	return template
}
