package daemon

import (
	"context"
	"reflect"
	"testing"

	"github.com/ISeoane-Quental/vcSentinel/internal/agentrun"
	"github.com/ISeoane-Quental/vcSentinel/internal/execution"
	"github.com/ISeoane-Quental/vcSentinel/internal/store"
)

func TestServerSubscribePagesStrictlyAfterCursorHonoringLimit(t *testing.T) {
	controller := newTestController(t, immediateAdapter("done"))
	ep := startTestServer(t, controller)
	conn := connectClient(t, ep, testFingerprint(), "")

	started := callOp(t, conn, OpStart, testStartEnvelope())
	if !started.OK {
		t.Fatalf("wire start failed: %+v", started.Error)
	}
	var handle execution.Handle
	decodeBodyInto(t, started.Body, &handle)
	waitForStateViaWire(t, conn, handle.RunID, agentrun.StateSucceeded)

	collected := 0
	cursor := uint64(0)
	for page := 0; ; page++ {
		response := callOp(t, conn, OpSubscribe, execution.SubscribeRequest{
			RunID: handle.RunID, AfterCursor: cursor, Limit: 2, AuthContext: testAuth(),
		})
		if !response.OK {
			t.Fatalf("wire subscribe failed: %+v", response.Error)
		}
		var got store.EventPage
		decodeBodyInto(t, response.Body, &got)
		want, err := controller.ReadEventPage(context.Background(), handle.RunID, cursor, 2)
		if err != nil {
			t.Fatalf("direct read event page: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("wire page at cursor %d differs from direct controller page", cursor)
		}
		for _, frame := range got.Events {
			if frame.Sequence <= cursor {
				t.Fatalf("page violated strictly-after semantics: sequence %d after cursor %d", frame.Sequence, cursor)
			}
		}
		collected += len(got.Events)
		if len(got.Events) > 2 {
			t.Fatalf("page returned %d events, limit was 2", len(got.Events))
		}
		if !got.HasMore {
			break
		}
		if page > 50 {
			t.Fatal("pagination never terminated")
		}
		cursor = got.NextRevision
	}
	if collected < 4 {
		t.Fatalf("collected %d events across pages, want at least the four lifecycle events", collected)
	}
}
