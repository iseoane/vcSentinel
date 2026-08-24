package acpadapter

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/process"
	"github.com/ISeoane-Quental/vas.sentinel/internal/reviewsnapshot"
)

// EjecutarPrompt runs one arbitrary prompt through acpx and returns the
// normalized assistant output. It mirrors the prompt-adapter shape of
// internal/agentadapter so upper layers can treat both adapter kinds alike.
// Non-success outcomes return an *OutcomeError carrying the outcome class;
// callers classify by Outcome(), never by error presence. The returned
// string is the assistant output ONLY, by legacy contract: programmatic
// consumers holding *AcpxAdapter receive the full evidence Result (raw
// stream, observed model, stop reason, usage, enforcement declaration)
// through Run.
// EjecutarPrompt and EjecutarRevision keep their Spanish names by
// INTERFACE-CONFORMANCE EXCEPTION: cmd/sentinel and the review engine assert
// these exact method names structurally (legacy AuditorAgente contract).
// AGENTS.md's English rule governs new vocabulary; these identifiers are a
// legacy protocol this adapter must speak to be substitutable.
func (a *AcpxAdapter) EjecutarPrompt(prompt string) (string, error) {
	return a.EjecutarPromptWithContext(context.Background(), prompt)
}

// EjecutarPromptWithContext runs the same arbitrary-prompt core as
// EjecutarPrompt but honors the caller-supplied context instead of a
// detached background one, so controller abort reaches the spawned acpx
// tree through cancellation. Nil contexts fall back to Background. The
// result contract matches EjecutarPrompt: assistant output only, with
// non-success outcomes carried as *OutcomeError.
func (a *AcpxAdapter) EjecutarPromptWithContext(ctx context.Context, prompt string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	res, err := a.Run(ctx, prompt)
	if err != nil {
		return "", err
	}
	return res.Output, nil
}

// EjecutarRevision runs a semantic review under the SAME snapshot discipline
// as CLIAdapter.EjecutarRevision: the audited paths are materialized read-only
// from committed content into an isolated snapshot directory, acpx is pointed
// at that directory through its --cwd global option (before the agent token),
// and the snapshot is cleaned up when the turn ends. The legacy contract
// carries no context; context-carrying callers go through ReviewWithContext.
func (a *AcpxAdapter) EjecutarRevision(prompt, sha string, paths []string) (string, error) {
	return a.ReviewWithContext(context.Background(), prompt, sha, paths)
}

// ReviewWithContext runs EjecutarRevision's restricted review but derives the
// execution budget from the caller-supplied context: the adapter runtime
// budget still applies, and an earlier caller cancellation wins. Cancellation
// reaches the child through the owned-tree containment watchdog (and the
// controller's escalation when one is wired), and classifies as the canceled
// outcome class wrapping the context error.
//
// The method name pairs with the legacy EjecutarRevision entry point it
// extends; it satisfies reviewexec.ContextualReviewer structurally.
func (a *AcpxAdapter) ReviewWithContext(ctx context.Context, prompt, sha string, paths []string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	snapshot, _, cleanup, err := reviewsnapshot.Create("", sha, paths)
	if err != nil {
		return "", fmt.Errorf("acpx: create review snapshot: %w", err)
	}
	defer cleanup()
	return a.outputOf(a.run(ctx, a.buildArgs(prompt, snapshot)))
}

// outputOf reduces a full Result to its assistant output for the legacy
// string-returning review contracts. That reduction is the legacy contract,
// not evidence loss: callers holding *AcpxAdapter get the complete Result —
// raw stream, observed model, stop reason, usage, and enforcement
// declaration — through Run, so nothing observed on the wire is discarded
// before those consumers can retain it durably.
func (a *AcpxAdapter) outputOf(res Result, err error) (string, error) {
	if err != nil {
		return "", err
	}
	return res.Output, nil
}

// run executes one fully built acpx command line and normalizes its stream.
//
// Ownership (R7): the child is born into an owned process group/job through
// process.Spawn — exactly like the CLI adapter's reviewer spawn — so the
// whole npx -> node(acpx) -> npm exec -> node(<agent>-acp) chain stays
// accounted for from birth. The live tree is published through OwnedTree for
// controller escalation, and a containment watchdog hard-terminates the tree
// if the context stays canceled past the shared grace budget plus margin.
// Cooperative session/cancel is OUT OF SCOPE by design: acpx exec is a
// stateless one-shot, so context-driven containment plus controller
// escalation covers cancellation completely.
func (a *AcpxAdapter) run(parent context.Context, args []string) (Result, error) {
	if parent == nil {
		parent = context.Background()
	}
	// Defense in depth: --timeout bounds the child from its own side while
	// the same runtime budget bounds our context, so whichever fires first
	// still ends the turn deterministically.
	runCtx, cancel := context.WithTimeout(parent, time.Duration(a.maxRuntimeSeconds)*time.Second)
	defer cancel()

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return Result{}, &OutcomeError{
			Class:  agentrun.OutcomeProcessError,
			Detail: fmt.Sprintf("acpx: stdout pipe unavailable: %v", err),
		}
	}
	var stderr bytes.Buffer
	raw := &bytes.Buffer{}

	cmd, tree, err := process.Spawn(runCtx, a.launcher[0], args, func(c *exec.Cmd) {
		if len(a.childEnv) > 0 {
			c.Env = append(os.Environ(), a.childEnv...)
		}
		c.Stdout = stdoutW
		c.Stderr = &stderr
	})
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		return Result{}, &OutcomeError{
			Class:  agentrun.OutcomeProcessError,
			Detail: fmt.Sprintf("acpx: launch %q failed: %v", filepath.Base(a.launcher[0]), err),
		}
	}
	a.registerTree(tree)
	defer a.unregisterTree(tree)

	// Drop the parent's write end so the drain sees EOF once the child exits.
	stdoutW.Close()

	exceeded := false
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		defer stdoutR.Close()
		chunk := make([]byte, 64*1024)
		var total int64
		for {
			n, readErr := stdoutR.Read(chunk)
			if n > 0 {
				total += int64(n)
				retain := int64(n)
				if a.maxOutputBytes > 0 && total > a.maxOutputBytes {
					// Exact retention: never hold more than the configured
					// budget in memory. The final chunk is truncated at the
					// budget boundary while `total` keeps counting true bytes,
					// so the breach verdict below still reflects everything
					// the child actually produced.
					if room := a.maxOutputBytes - (total - int64(n)); room < retain {
						retain = room
					}
				}
				if retain > 0 {
					raw.Write(chunk[:retain])
				}
				if a.maxOutputBytes > 0 && total > a.maxOutputBytes {
					// Budget breach: stop reading, break the pipe, and take
					// the whole tree down. Closing the read end also makes
					// any further child write fail on its own. The verdict
					// below is failure regardless of what the truncated
					// stream claimed afterwards.
					exceeded = true
					_ = process.Terminate(tree)
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	go containAfterCancellation(runCtx, tree)

	waitErr := cmd.Wait()
	tree.MarkExited()
	tree.Release()
	<-drained

	stream := ParseStream(bytes.NewReader(raw.Bytes()), DefaultLineCapBytes)
	res := Result{
		Output:        stream.Output,
		RawStream:     raw.String(),
		Violations:    stream.Violations,
		ObservedModel: stream.ObservedModel,
		StopReason:    stream.StopReason,
		UsageJSON:     stream.UsageJSON,
		Enforcement:   a.enforcement,
	}
	a.recordObserved(stream.ObservedModel)

	switch {
	case exceeded:
		return res, &OutcomeError{
			Class:  agentrun.OutcomeFailure,
			Detail: "acpx: output budget exceeded",
		}
	case res.StopReason == "":
		class := agentrun.OutcomeFailure
		detail := "acpx: no terminal result"
		if waitErr != nil {
			detail = fmt.Sprintf("acpx: no terminal result (child error: %v)", waitErr)
		}
		switch {
		case parent.Err() != nil:
			class = agentrun.OutcomeCancellation
			detail = fmt.Sprintf("acpx: canceled before terminal result (%v)", parent.Err())
		case runCtx.Err() != nil:
			class = agentrun.OutcomeTimeout
			detail = fmt.Sprintf("acpx: no terminal result before the runtime budget (%v)", runCtx.Err())
		}
		return res, &OutcomeError{Class: class, Detail: detail}
	}
	if class := res.Class(); class != agentrun.OutcomeSuccess {
		return res, &OutcomeError{
			Class:  class,
			Detail: fmt.Sprintf("acpx: terminal stopReason %q", res.StopReason),
		}
	}
	return res, nil
}

// containAfterCancellation is the adapter-side safety net mirroring the CLI
// adapter's reviewer containment: when the context fires, the tree gets the
// shared grace budget plus margin to die through the controller's escalation
// path first, then this watchdog hard-terminates whatever remains so no
// descendant of the deep npx chain can outlive its budget silently. When the
// stamped policy restricts kills to the direct child, the watchdog must never
// fire and no code path here signals the tree.
func containAfterCancellation(ctx context.Context, tree *process.Tree) {
	select {
	case <-ctx.Done():
	case <-tree.Exited():
		return
	}
	if !process.WholeTreeTermination(ctx) {
		return
	}
	select {
	case <-tree.Exited():
	case <-time.After(process.ContainmentDeadline(ctx)):
		_ = process.Terminate(tree)
	}
}
