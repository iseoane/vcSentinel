// Package presence reports read-only runtime facts for the control center:
// whether a repository's daemon is live right now, and the recent durable-run
// rows feeding the activity pane. It only reads existing stable records; it
// never dials, starts, stops, or writes anything.
package presence

import (
	"fmt"
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
	UpdatedAt time.Time
	// Operation is the operator-facing label admitted with the run
	// ("review logic", "gate pre-push", "run"); empty for legacy records and
	// when the policy record cannot be read.
	Operation string
	// Commit is the audited commit short SHA (7 runes) that the run
	// audits, when the producer knew it at admission time. Empty for
	// legacy records.
	Commit string
}

// RecentRuns returns at most limit projected durable runs anchored at the
// shared store under gitCommonDir.
//
// Ordering contract: run identifiers are sha256 hex digests of the request
// identity (agentrun.NewLogicalJob), carrying NO time information, so this
// deterministically returns the lexicographic TAIL of the sorted identifier
// list (last limit identifiers, ascending order kept). True recency ordering
// belongs to callers consulting richer projection fields. limit <= 0 and an
// empty store yield an empty slice and nil; store failures propagate wrapped
// with the path.
//
// Operation contract: each summary costs one extra small policy.json read
// through store.ReadRunOperation (bounded by limit; the control center asks
// for 3), which is acceptable for an operator-facing pane. Read failures
// degrade SOFTLY — an unreadable or corrupt policy must not fail the whole
// listing over cosmetic metadata, so the summary survives with an empty
// Operation and the renderer falls back to the id prefix. Only the
// projection read remains a hard failure.
func RecentRuns(gitCommonDir string, limit int) ([]RunSummary, error) {
	if limit <= 0 {
		return []RunSummary{}, nil
	}
	st := store.NuevoStore(gitCommonDir)
	ids, err := st.ListExecutionIDs()
	if err != nil {
		return nil, fmt.Errorf("presence: list executions under %s: %w", gitCommonDir, err)
	}
	if len(ids) > limit {
		ids = ids[len(ids)-limit:]
	}
	summaries := make([]RunSummary, 0, len(ids))
	for _, id := range ids {
		projection, err := st.ReadProjection(id)
		if err != nil {
			return nil, fmt.Errorf("presence: read projection under %s: %w", gitCommonDir, err)
		}
		operation, opErr := st.ReadRunOperation(id)
		if opErr != nil {
			operation = ""
		}
		commit, cErr := st.ReadRunCommit(id)
		if cErr != nil {
			commit = ""
		}
		summaries = append(summaries, RunSummary{
			RunID:     projection.RunID,
			State:     projection.State,
			Revision:  projection.Revision,
			UpdatedAt: projection.UpdatedAt,
			Operation: operation,
			Commit:    commit,
		})
	}
	return summaries, nil
}
