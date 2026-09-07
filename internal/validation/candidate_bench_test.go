package validation

import (
	"path/filepath"
	"testing"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentshell"
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/graph"
)

// BenchmarkResolveCommand_ScopePartialVsFull is the reproducible evidence of
// F4's exit criterion "the gate --stage pre-push time on a single-package
// change drops measurably compared to the full validation": a decision
// already made, not a pass/fail timing test (fragile in CI), but a Go
// benchmark. "go test -bench=. -run=^$ ./internal/validation/" prints two
// comparable ns/op figures, one per sub-benchmark.
//
// It runs against the vas.sentinel repository itself (which already has many
// real Go packages), using the real native GraphProvider
// (graph.NewNativeProvider), not a double: the partial scope comes from
// resolveCommand with the authorization that this real graph produces for a
// change in internal/store/store.go, a leaf package with no dependents inside
// the repo (nobody else in the module imports it), exactly as
// RunProfileOnCandidate would do in production. The snapshot is obtained with
// git.Freeze + git.CreateSnapshot, the same path RunProfileOnCandidate uses
// in worktree mode: that way the benchmark is deterministic even if the real
// worktree is dirty.
//
// go vet is the reference command: cheap and deterministic; go test is not
// needed to demonstrate the scope difference.
func BenchmarkResolveCommand_ScopePartialVsFull(b *testing.B) {
	candidate, err := git.Freeze()
	if err != nil {
		b.Fatalf("could not freeze the candidate of the real repository: %v", err)
	}
	snapshot, err := git.CreateSnapshot(candidate.Tree)
	if err != nil {
		b.Fatalf("could not create the snapshot of the real repository: %v", err)
	}

	leafPath := filepath.ToSlash(filepath.Join("internal", "store", "store.go"))
	provider := graph.NewNativeProvider(snapshot, candidate.Tree)
	result, err := provider.Analyze([]string{leafPath})
	if err != nil {
		b.Fatalf("the native graph failed to analyze %s: %v", leafPath, err)
	}
	authorization, ok := graph.AuthorizePartialScope(result)
	if !ok {
		b.Fatalf("the native graph did not authorize a partial scope over the real repository: complete=%v reason=%q", result.Complete(), result.ReasonIncomplete())
	}

	capability := config.CapabilityConfig{Command: "go vet ./...", SupportsScope: true, ScopedCommand: "go vet {packages}"}
	partialCommand, partialScope, _ := resolveCommand(capability, authorization)
	if partialScope != ScopePartial {
		b.Fatalf("resolveCommand did not produce a partial scope over the real repository: command=%q", partialCommand)
	}
	fullCommand, _, _ := resolveCommand(capability, graph.ScopeAuthorization{})

	b.Run("partial", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if exit, output, err := agentshell.Run(snapshot, partialCommand); err != nil || exit != 0 {
				b.Fatalf("%q failed: exit=%d err=%v output=%s", partialCommand, exit, err, output)
			}
		}
	})
	b.Run("full", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if exit, output, err := agentshell.Run(snapshot, fullCommand); err != nil || exit != 0 {
				b.Fatalf("%q failed: exit=%d err=%v output=%s", fullCommand, exit, err, output)
			}
		}
	})
}
