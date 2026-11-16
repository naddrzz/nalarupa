package model

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// The ports of claude.rs's tests.

// silentServer sends one text event (or nothing, not even its headers) and then goes silent without closing.
func silentServer(t *testing.T, firstEvent bool) string {
	t.Helper()
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		if firstEvent {
			w.Header().Set("content-type", "text/event-stream")
			io.WriteString(w, "data: "+`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
			w.(http.Flusher).Flush()
		}
		select {
		case <-r.Context().Done():
		case <-hang:
		case <-time.After(30 * time.Second):
		}
	}))
	t.Cleanup(func() {
		close(hang)
		srv.Close()
	})
	return srv.URL + "/v1/messages"
}

func claudeClient(url string, stall time.Duration) *Claude {
	c := NewClaude("k")
	c.url, c.stall = url, stall
	return c
}

func TestStopInterruptsASilentStreamAndASilentHeaderWait(t *testing.T) {
	for _, firstEvent := range []bool{true, false} {
		c := claudeClient(silentServer(t, firstEvent), stallLimit)
		var stop atomic.Bool
		start := time.Now()
		time.AfterFunc(500*time.Millisecond, func() { stop.Store(true) })
		_, err := c.Stream(jsonv.NewObject(), "", stop.Load, ignore)
		if err == nil || err.Error() != "stopped" {
			t.Fatalf("first event %v: %v", firstEvent, err)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("stop took %v", d)
		}
	}
}

func TestARejectedKeyPointsAtTheSettings(t *testing.T) {
	if e := claudeError(401, "invalid x-api-key"); !strings.Contains(e, "Agent settings") || strings.Contains(e, "401") {
		t.Fatal(e)
	}
	if e := claudeError(529, "Overloaded"); e != "Claude API error 529: Overloaded" {
		t.Fatal(e)
	}
}

func TestASilentStreamFailsAsStalled(t *testing.T) {
	c := claudeClient(silentServer(t, true), 600*time.Millisecond)
	_, err := c.Stream(jsonv.NewObject(), "", never, ignore)
	if err == nil || !strings.HasPrefix(err.Error(), "connection stalled") {
		t.Fatal(err)
	}
}

func TestACleanlyClosedStreamReturnsItsContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("content-type", "text/event-stream")
		w.Header().Set("connection", "close")
		io.WriteString(w, "data: "+`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hi"}}`+"\n\n")
	}))
	defer srv.Close()
	r, err := claudeClient(srv.URL+"/v1/messages", stallLimit).Stream(jsonv.NewObject(), "", never, ignore)
	if err != nil {
		t.Fatal(err)
	}
	if got := at(r, "content", 0, "text"); !jsonv.StrIs(got, "hi") {
		t.Fatal(string(jsonv.Marshal(r)))
	}
}

// Beyond the Rust tests: a keyless client sends no key header, and an HTTP error reads the body's message.
func TestAKeylessClientSendsNoKeyAndErrorsReadTheMessage(t *testing.T) {
	keys := make(chan []string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keys <- r.Header.Values("x-api-key")
		w.WriteHeader(529)
		io.WriteString(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	}))
	defer srv.Close()
	_, err := ClaudeAt(" ", srv.URL).Stream(jsonv.NewObject(), "", never, ignore)
	if err == nil || err.Error() != "Claude API error 529: Overloaded" {
		t.Fatal(err)
	}
	if key := <-keys; len(key) != 0 {
		t.Fatal("a blank key is not sent", key)
	}
}

func TestToolInputParsesOrKeepsTheRawText(t *testing.T) {
	eq(t, ToolInput("  "), `{}`)
	eq(t, ToolInput(`{"a":1}`), `{"a":1}`)
	eq(t, ToolInput(`{"a":`), `{"__invalid_json":"{\"a\":"}`)
}

// Beyond the Rust tests: a delta or stop for a block index past the end (even
// one above i64) is skipped, as Rust's `get_mut` skips it, instead of panicking.
func TestAnOutOfRangeBlockIndexIsSkipped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("content-type", "text/event-stream")
		for _, ev := range []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hi"}}`,
			`{"type":"content_block_delta","index":9223372036854775808,"delta":{"type":"text_delta","text":"x"}}`,
			`{"type":"content_block_delta","index":5,"delta":{"type":"text_delta","text":"x"}}`,
			`{"type":"content_block_stop","index":18446744073709551615}`,
		} {
			io.WriteString(w, "data: "+ev+"\n\n")
		}
	}))
	defer srv.Close()
	r, err := claudeClient(srv.URL+"/v1/messages", stallLimit).Stream(jsonv.NewObject(), "", never, ignore)
	if err != nil {
		t.Fatal(err)
	}
	if got := at(r, "content", 0, "text"); !jsonv.StrIs(got, "hi") {
		t.Fatal(string(jsonv.Marshal(r)))
	}
}
