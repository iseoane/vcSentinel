// Package presence reports read-only runtime facts for the control center:
// whether a repository's daemon is live right now, and the recent durable-run
// rows feeding the activity pane. It only reads existing stable records; it
// never dials, starts, stops, or writes anything.
package presence

import (
	"fmt"
	"sort"
	"time"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/daemon"
	"github.com/ISeoane-Quental/vas.sentinel/internal/store"
)

// Presence is the classified daemon state of one repository. The zero value
// is the honest Stopped answer.
type Presence struct {
	Live      bool
	PID       int
	Network   string
	Address   string
	StartedAt time.Time
}

// Probe classifies the repository daemon as Live or Stopped from the
// discovery record under daemon.Dir(gitCommonDir).
//
// Degradation contract — Probe NEVER returns an error: a missing, unreadable,
// corrupt, or incomplete record degrades to Stopped (zero value), because
// crash residue must never report Live; so does a record whose PID is
// provably dead. A live PID yields Live with PID, Network, Address, and
// StartedAt copied verbatim. Liveness is process existence only — no
// handshake is attempted.
func Probe(gitCommonDir string) Presence {
	endpoint, owner, err := daemon.LoadEndpoint(daemon.Dir(gitCommonDir))
	if err != nil {
		return Presence{}
	}
	if !pidAlive(owner.PID) {
		return Presence{}
	}
	return Presence{
		Live:      true,
		PID:       owner.PID,
		Network:   endpoint.Network,
		Address:   endpoint.Address,
		StartedAt: owner.StartedAt,
	}
}

// RunSummary is one projected durable run as shown in the activity pane.
type RunSummary struct {
	RunID     string
	State     agentrun.LifecycleState
	Revision  uint64
	StartedAt time.Time
	UpdatedAt time.Time
	// Operation is the operator-facing label admitted with the run
	// ("review logic", "gate pre-push", "run"); empty for legacy records and
	// when the policy record cannot be read.
	Operation string
	// Commit is the audited commit short SHA (7 runes) that the run
	// audits, when the producer knew it at admission time. Empty for
	// legacy records.
	Commit string
	// Worktree is the worktree path that launched the run, when known.
	// Empty for legacy records.
	Worktree string
	// Reason is the terminal error for failed/canceled runs, when available.
	// Empty for non-terminal or legacy records.
	Reason string
}

// RecentRuns returns at most limit projected durable runs anchored at the
// shared store under gitCommonDir.
//
// Ordering contract: run identifiers are sha256 hex digests of the request
// identity (agentrun.NewLogicalJob), carrying NO time information. RecentRuns
// therefore reads every projection before selecting rows, returns timestamped
// runs newest-first, breaks equal timestamps by ascending RunID, and places
// zero-time projections after every timestamped run with the same RunID
// tie-break. A zero timestamp is never replaced with a time derived from the
// identifier. The projection scan is O(history) so corruption anywhere in the
// repository remains visible; policy and event metadata enrichment happens only
// after sorting and trimming, so it is bounded by limit. limit <= 0 and an
// empty store yield an empty slice and nil; store failures propagate wrapped
// with the path.
//
// Metadata contract: after ordering and trimming, each retained summary costs
// up to three small policy.json reads (operation, commit, and worktree) and one
// bounded event-log read for its start time and terminal reason, so enrichment
// is O(limit). Read failures degrade SOFTLY — an unreadable or corrupt policy
// or event stream must not fail the whole listing over cosmetic metadata, so
// the summary survives with empty metadata and the renderer falls back to the
// id prefix. Only projection reads remain hard failures.
func RecentRuns(gitCommonDir string, limit int) ([]RunSummary, error) {
	if limit <= 0 {
		return []RunSummary{}, nil
	}
	st := store.NuevoStore(gitCommonDir)
	ids, err := st.ListExecutionIDs()
	if err != nil {
		return nil, fmt.Errorf("presence: list executions under %s: %w", gitCommonDir, err)
	}
	summaries := make([]RunSummary, 0, len(ids))
	for _, id := range ids {
		projection, err := st.ReadProjection(id)
		if err != nil {
			return nil, fmt.Errorf("presence: read projection under %s: %w", gitCommonDir, err)
		}
		summaries = append(summaries, RunSummary{
			RunID:     projection.RunID,
			State:     projection.State,
			Revision:  projection.Revision,
			UpdatedAt: projection.UpdatedAt,
		})
	}
	sort.Slice(summaries, func(i, j int) bool {
		left, right := summaries[i], summaries[j]
		leftZero, rightZero := left.UpdatedAt.IsZero(), right.UpdatedAt.IsZero()
		if leftZero != rightZero {
			return !leftZero
		}
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.UpdatedAt.After(right.UpdatedAt)
		}
		return left.RunID < right.RunID
	})
	if len(summaries) > limit {
		summaries = summaries[:limit]
	}
	for i := range summaries {
		id := summaries[i].RunID
		operation, opErr := st.ReadRunOperation(id)
		if opErr != nil {
			operation = ""
		}
		commit, cErr := st.ReadRunCommit(id)
		if cErr != nil {
			commit = ""
		}
		worktree, wErr := st.ReadRunWorktree(id)
		if wErr != nil {
			worktree = ""
		}
		reason := ""
		eventLimit := max(1, int(summaries[i].Revision))
		if page, err := st.ReadEvents(id, 0, eventLimit); err == nil && len(page.Events) > 0 {
			summaries[i].StartedAt = page.Events[0].At
			if summaries[i].State == agentrun.StateFailed || summaries[i].State == agentrun.StateCanceled || summaries[i].State == agentrun.StateTimedOut || summaries[i].State == agentrun.StateUnavailable {
				reason = page.Events[len(page.Events)-1].OutcomeError
			}
		}
		summaries[i].Operation = operation
		summaries[i].Commit = commit
		summaries[i].Worktree = worktree
		summaries[i].Reason = reason
	}
	return summaries, nil
}
