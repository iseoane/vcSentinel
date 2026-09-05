// Package pr hosts the orchestration of the `sentinel pr review` and
// `sentinel pr create` subcommands. It was moved verbatim from
// cmd/sentinel/comandos_pr.go (FU-2/C6, one-subcommand split): cmd/sentinel
// keeps the package-main names the tests drive, the thin dispatch wrappers
// and the production wiring; this package owns the flows themselves.
//
// Movement hazard cleared: the 21 live block fichas cite snapshot.go,
// comandos_runs.go and siblings — none cites cmd/sentinel/comandos_pr.go —
// and there are no standing dispositions (no dispositions.jsonl), so moving
// that file orphans nothing.
//
// The collaborators shared with the review and gate commands (the shared
// ledger, the dispositions loader, the review transport factory, the
// model-probe constructor, the short-SHA formatter, the refuter-option
// assembler, the default gate profile, the validation-finding projection and
// the binary version) stay in package main and travel through Wiring: a
// package-main import from here would be a cycle.
package pr

import (
	"github.com/ISeoane-Quental/vas.sentinel/internal/config"
	"github.com/ISeoane-Quental/vas.sentinel/internal/git"
	"github.com/ISeoane-Quental/vas.sentinel/internal/modelprobe"
	"github.com/ISeoane-Quental/vas.sentinel/internal/review"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
	"github.com/ISeoane-Quental/vas.sentinel/internal/validation"
)

// Wiring carries the production collaborators that remain in package main
// because the review and gate commands share them. cmd/sentinel stays the
// wiring owner: it builds one Wiring per invocation and hands it to the flows
// below, so this package never reaches back into package main.
type Wiring struct {
	// NuevoVerificadorModelo must be wired as a closure over package main's
	// nuevoVerificadorModelo var (tests swap it), so the var is read at call
	// time, never captured at wiring time.
	NuevoVerificadorModelo   func(worktree string) *modelprobe.Verificador
	SharedReviewLedger       func(worktree string) (*review.Ledger, error)
	LoadDispositions         func(worktree string) ([]review.FindingDisposition, error)
	TransportFactory         func(cfg config.Config, worktree string) func(sha string, paths []string) review.ReviewTransport
	OpcionesRamaConRefutador func(cfg config.Config, verificador *modelprobe.Verificador, opts review.OpcionesRama) review.OpcionesRama
	ShaCorto                 func(sha string) string
	PerfilGatePorDefecto     string
	ProyectarHallazgos       func(hallazgos []validation.Hallazgo) []review.Hallazgo
	Version                  string
}

// stackOwnDiff maps CLI stack signals; explicit --parent wins.
func stackOwnDiff(parent string, chainPR bool) *review.OwnDiffOptions {
	switch {
	case parent != "":
		return &review.OwnDiffOptions{Parent: parent}
	case chainPR:
		return &review.OwnDiffOptions{ResolveParent: true}
	}
	return nil
}

// ResolveBlobStore resolves the blob store AnalizarRama needs to reuse reviews
// by CONTENT instead of by SHA (F2 exit criterion), which is what makes
// rebasing the base of a stacked PR cheap (F8 exit criterion 2): without it
// every rewritten SHA looks unreviewed and the whole stack is audited again.
//
// It MUST receive the git COMMON dir, never the per-worktree git dir: linked
// worktrees share one store, and store.NuevoStore documents that contract.
//
// Reuse never fabricates a verdict: AnalizarRama adopts an EXISTING ficha
// under the new SHA, exactly as the ledger cache already did per SHA. The
// store is therefore optional, and the error travels to the caller instead of
// to os.Stderr so every command routes the warning through the writer it
// already uses for its own diagnostics; a nil store restores the SHA-only
// behaviour rather than aborting a real review.
func ResolveBlobStore(worktree string) (review.StoreBlobs, error) {
	commonDir, err := git.ObtenerGitCommonDir(worktree)
	if err != nil {
		return nil, err
	}
	return store.NuevoStore(commonDir), nil
}
