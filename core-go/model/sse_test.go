package model

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// sse.rs has no tests of its own; these cover its framing, its line cap and its error statuses.

// serve answers every request with status and body.
func serve(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// collect posts to url and returns every event's data, stopping after stopAfter events (0: never).
func collect(url string, stopAfter int) ([]string, error) {
	var got []string
	err := post(url, nil, jsonv.NewObject(), stallLimit, "the test", never, providerError, func(d string) (bool, error) {
		got = append(got, d)
		return stopAfter == 0 || len(got) < stopAfter, nil
	})
	return got, err
}

func TestEventsJoinDataLinesAndSkipCommentsAndOtherFields(t *testing.T) {
	body := "data: a\r\ndata:b\r\n: ping\r\n\r\n\r\nevent: x\nid: 1\ndata:  c\n\ndata: last"
	got, err := collect(serve(t, 200, body), 0)
	if err != nil {
		t.Fatal(err)
	}
	// One space after `data:` goes; a second stays. A last event without its blank line still counts.
	if want := []string{"a\nb", " c", "last"}; !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
	got, err = collect(serve(t, 200, body), 1)
	if err != nil || !slices.Equal(got, []string{"a\nb"}) {
		t.Fatalf("a handler that is done ends the stream: %q %v", got, err)
	}
	failing := errors.New("bad event")
	err = post(serve(t, 200, body), nil, nil, stallLimit, "the test", never, providerError, func(string) (bool, error) { return false, failing })
	if err != failing {
		t.Fatal(err)
	}
}

func TestALineOverTheCapFails(t *testing.T) {
	long := strings.Repeat("x", lineMax-len("data: "))
	// The longest line accepted, with its CRLF.
	got, err := collect(serve(t, 200, "data: "+long+"\r\n\r\n"), 0)
	if err != nil || len(got) != 1 || len(got[0]) != len(long) {
		t.Fatal(len(got), err)
	}
	_, err = collect(serve(t, 200, "data: "+long+"xx\r\n\r\n"), 0)
	if err == nil || err.Error() != "stream interrupted: a line over 8 MB" {
		t.Fatal(err)
	}
}

func TestAnErrorStatusReadsTheMessageWhenThereIsOne(t *testing.T) {
	for _, c := range []struct {
		status    int
		body, err string
	}{
		{500, `{"error": {"message": "boom"}}`, "Provider error 500: boom"},
		{502, `Bad gateway`, "Provider error 502: Bad gateway"},
		{401, `{"error": {"message": "no"}}`, "The provider rejected this key (no). Check it in Agent settings (gear icon)."},
		{500, strings.Repeat("x", errorBodyMax+1), "Provider error 500: "},
	} {
		_, err := collect(serve(t, c.status, c.body), 0)
		if err == nil || err.Error() != c.err {
			t.Errorf("%d %.20s: %v", c.status, c.body, err)
		}
	}
}

func TestLossyReplacesEachInvalidSequenceOnce(t *testing.T) {
	if got := lossy([]byte("a\xf0\x9f\x98b\xffc")); got != "a�b�c" {
		t.Fatalf("%q", got)
	}
}
