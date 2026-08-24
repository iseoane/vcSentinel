package tui

import (
	"context"
	"fmt"

	"github.com/ISeoane-Quental/vas.sentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vas.sentinel/internal/attach"
	"github.com/ISeoane-Quental/vas.sentinel/internal/execution"
)

// ObserveSnapshot rebuilds one full RunView: an Inspect snapshot for the
// projection and admitted outcomes, then Subscribe pages applied strictly
// after the collector's cursor until the stream is exhausted. The collector
// makes repeated observations idempotent, so polling between pages never
// duplicates evidence. This is the exact observe shape of the superseded
// text follow loop, extracted so the CLI snapshot mode and the TUI share one
// implementation instead of drifting apart.
func ObserveSnapshot(ctx context.Context, host execution.RepositoryHost, runID agentrun.Identity, principal string, collector *attach.ReplayCollector) (attach.RunView, bool, error) {
	before := collector.Cursor()
	inspection, err := host.Inspect(ctx, execution.InspectRequest{
		RunID:       runID,
		AuthContext: execution.AuthContext{Principal: principal},
	})
	if err != nil {
		return attach.RunView{}, false, err
	}
	pages := 0
	for {
		page, err := host.Subscribe(ctx, execution.SubscribeRequest{
			RunID:       runID,
			AfterCursor: collector.Cursor(),
			Limit:       subscribePageLimit,
			AuthContext: execution.AuthContext{Principal: principal},
		})
		if err != nil {
			return attach.RunView{}, false, err
		}
		collector.ApplyPage(page)
		if !page.HasMore {
			break
		}
		pages++
		if pages > MaxObservePages {
			return attach.RunView{}, false, fmt.Errorf("event pagination for %s did not terminate after %d pages", runID, MaxObservePages)
		}
	}
	view := attach.BuildRunView(collector.Frames(), inspection.Outcomes, inspection.Projection)
	// Cursor movement is the complete change signal: both the durable
	// projection and the admitted attempt outcomes are derived from the same
	// event stream Subscribe pages, so an unmoved cursor can only rebuild an
	// identical view (terminal arrival counts as a change worth rendering).
	return view, collector.Cursor() != before || view.IsTerminal(), nil
}
