// Package presence reports read-only runtime facts for the control center:
// whether a repository's daemon is live right now, and the recent durable-run
// rows feeding the activity pane. It only reads existing stable records; it
// never dials, starts, stops, or writes anything.
package presence

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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
	Operation string
	Commit    string
	Worktree  string
	Reason    string
}

// RecentRuns returns the globally newest limit projected durable runs anchored
// at the shared store under gitCommonDir.
func RecentRuns(gitCommonDir string, limit int) ([]RunSummary, error) {
	return RecentRunsForWorktrees(gitCommonDir, limit, nil)
}

// RecentRunsForWorktrees returns global and per-visible-worktree newest runs.
// Projection failures are hard; metadata enrichment failures are soft.
func RecentRunsForWorktrees(gitCommonDir string, limit int, visibleWorktreePaths []string) ([]RunSummary, error) {
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

	retained := make([]bool, len(summaries))
	for i := 0; i < len(summaries) && i < limit; i++ {
		retained[i] = true
	}
	remaining := worktreeQuotas(visibleWorktreePaths, limit)
	for i := range summaries {
		if !retained[i] && len(remaining) == 0 {
			break
		}
		policy, err := st.ReadRunPolicy(summaries[i].RunID)
		if err != nil {
			continue
		}
		summaries[i].Operation = policy.Operation
		summaries[i].Commit = policy.Commit
		summaries[i].Worktree = policy.Worktree
		if key := normalizeWorktreePath(policy.Worktree); key != "" {
			if quota, ok := remaining[key]; ok {
				retained[i] = true
				if quota <= 1 {
					delete(remaining, key)
				} else {
					remaining[key] = quota - 1
				}
			}
		}
	}

	union := make([]RunSummary, 0, len(summaries))
	for i := range summaries {
		if !retained[i] {
			continue
		}
		enrichRunSummary(st, &summaries[i])
		union = append(union, summaries[i])
	}
	return union, nil
}

func worktreeQuotas(paths []string, limit int) map[string]int {
	quotas := make(map[string]int, len(paths))
	for _, path := range paths {
		if key := normalizeWorktreePath(path); key != "" {
			quotas[key] = limit
		}
	}
	return quotas
}

func normalizeWorktreePath(path string) string {
	if path == "" {
		return ""
	}
	path = filepath.Clean(filepath.FromSlash(path))
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func enrichRunSummary(st *store.Store, summary *RunSummary) {
	eventLimit := max(1, int(summary.Revision))
	if page, err := st.ReadEvents(summary.RunID, 0, eventLimit); err == nil && len(page.Events) > 0 {
		summary.StartedAt = page.Events[0].At
		if summary.State == agentrun.StateFailed || summary.State == agentrun.StateCanceled || summary.State == agentrun.StateTimedOut || summary.State == agentrun.StateUnavailable {
			summary.Reason = page.Events[len(page.Events)-1].OutcomeError
		}
	}
}
