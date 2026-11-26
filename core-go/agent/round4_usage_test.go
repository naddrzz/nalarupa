package agent

import (
	"io"
	"net/http"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

func TestMultiTurnFailurePreservesCompletedTurnUsage(t *testing.T) {
	_, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		if n == 0 {
			return sse(
				`{"type":"message_start","message":{"usage":{"input_tokens":1250,"output_tokens":0}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t1","name":"get_editor_state","input":{}}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":340}}`,
				`{"type":"message_stop"}`,
			)
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"context window exceeded on turn 2"}}`)
		return ""
	})

	e := apiEngine(t, url)
	if err := Send(e, "Design multi-turn", 1, "split", 1, "layout"); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, e)

	evs := agentEvents(e)
	if lastOf(evs, "error") == nil {
		t.Fatalf("expected error event on turn 2 failure")
	}
	done := lastOf(evs, "done")
	if done == nil {
		t.Fatalf("expected done event")
	}
	usage := jsonv.Get(done, "usage")
	inp := asU64(jsonv.Get(usage, "input"))
	out := asU64(jsonv.Get(usage, "output"))
	turns := asU64(jsonv.Get(usage, "turns"))
	if inp != 1250 || out != 340 || turns != 1 {
		t.Fatalf("completed turn 1 usage was lost on turn 2 failure: got %s, want input=1250 output=340 turns=1",
			string(jsonv.Marshal(usage)))
	}
}
