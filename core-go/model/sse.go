// Package model holds the model clients (sse.rs, claude.rs, openai_chat.rs, openai_responses.rs, provider.rs):
// server-sent events from a POST, the Claude Messages client, the OpenAI Chat Completions and Responses clients
// that translate to and from the Claude shape, and the provider resolution that does not read settings (the
// engine reads them and calls in here).
package model

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// lineMax is the longest event line accepted (model events are small; this only stops a runaway line).
const lineMax = 8 << 20

// poll is how often a silent stream re-checks cancel.
const poll = 200 * time.Millisecond

// errorBodyMax caps how much of an HTTP error body is read.
const errorBodyMax = 1 << 20

// httpClient has the clients' overall timeout. A silent connect or header wait needs no timeout of its own: the
// caller reports the stall and cancels the request.
var httpClient = &http.Client{Timeout: 900 * time.Second}

// line is one read of the worker: a line, the end of the stream (end), or a failure (err).
type line struct {
	text string
	end  bool
	err  error
}

// post sends POST url with headers and body (as JSON) and hands each event's data (multi-line data joined) to on,
// until the stream ends or on returns false. An HTTP error status becomes apiError(status, message), the message
// taken from `{"error": {"message"}}` when the body has one. who names the server in the stall error.
//
// The socket work runs on its own goroutine, so a silent connection cannot hold the caller: it polls cancel and
// the stall limit between lines. Returning cancels the request, which ends the goroutine.
func post(urlStr string, headers [][2]string, body jsonv.Value, stall time.Duration, who string, cancel func() bool,
	apiError func(int, string) string, on func(string) (bool, error)) error {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(jsonv.Marshal(body)))
	if err != nil {
		return fmt.Errorf("network error: %v", err)
	}
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	// Bounded, so a slow on backpressures the socket instead of queueing every line. The worker sends end when the
	// stream ends, so a closed channel without it is an error.
	ch := make(chan line, 64)
	go read(ctx, req, apiError, ch)

	var data string
	last := time.Now()
	for {
		if cancel() {
			return errors.New("stopped")
		}
		var l line
		select {
		case got, ok := <-ch:
			if !ok {
				return errors.New("stream interrupted: network worker ended unexpectedly")
			}
			l = got
		case <-time.After(poll):
			if time.Since(last) < stall {
				continue
			}
			return fmt.Errorf("connection stalled: no data from %s for %d s", who, int64(stall/time.Second))
		}
		if l.err != nil {
			return l.err
		}
		if l.end {
			break
		}
		last = time.Now()
		if d, ok := strings.CutPrefix(l.text, "data:"); ok {
			if data != "" {
				data += "\n"
			}
			if d2, ok := strings.CutPrefix(d, " "); ok {
				d = d2
			}
			data += d
			continue
		}
		// Comments (`: ping`) and other fields are ignored; a blank line ends the event.
		if l.text != "" || data == "" {
			continue
		}
		ev := data
		data = ""
		more, err := on(ev)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
	// A last event without its blank line still counts.
	if data != "" {
		if _, err := on(data); err != nil {
			return err
		}
	}
	return nil
}

// read is the worker: it sends the request and each line of the reply on ch, then end, and closes ch. A panic
// closes ch without end, which the caller reports.
func read(ctx context.Context, req *http.Request, apiError func(int, string) string, ch chan<- line) {
	defer close(ch)
	defer func() { _ = recover() }()
	send := func(l line) bool {
		select {
		case ch <- l:
			return true
		case <-ctx.Done():
			return false
		}
	}
	res, err := httpClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		send(line{err: fmt.Errorf("network error: %v", err)})
		return
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		text := errorBody(res.Body)
		msg := text
		if v, err := jsonv.Parse([]byte(text)); err == nil {
			if m, ok := jsonv.Str(jsonv.Get(jsonv.Get(v, "error"), "message")); ok {
				msg = m
			}
		}
		send(line{err: errors.New(apiError(res.StatusCode, msg))})
		return
	}
	r := bufio.NewReader(res.Body)
	var buf []byte
	for {
		buf = buf[:0]
		// One line at most lineMax bytes: a server that never sends a newline cannot grow memory without bound.
		// Room for the payload plus a CRLF terminator; no newline within that means the line is too long.
		var err error
		buf, err = readLine(r, buf, lineMax+2)
		var l line
		switch {
		case len(buf) == 0 && err == io.EOF:
			send(line{end: true})
			return
		case len(buf) > lineMax+1 && buf[len(buf)-1] != '\n':
			l = line{err: fmt.Errorf("stream interrupted: a line over %d MB", lineMax>>20)}
		case err != nil && err != io.EOF:
			l = line{err: fmt.Errorf("stream interrupted: %v", err)}
		default:
			l = line{text: strings.TrimRight(lossy(buf), "\r\n")}
		}
		if !send(l) || l.err != nil {
			return
		}
	}
}

// readLine appends to buf up to and including the next '\n', reading at most limit bytes (Rust's
// `take(limit).read_until(b'\n')`). At the end of the stream it returns what it read with io.EOF.
func readLine(r *bufio.Reader, buf []byte, limit int) ([]byte, error) {
	for {
		chunk, err := r.ReadSlice('\n')
		if room := limit - len(buf); len(chunk) >= room {
			// The limit is reached: a newline exactly at it still ends the line.
			return append(buf, chunk[:room]...), nil
		}
		buf = append(buf, chunk...)
		if err != bufio.ErrBufferFull {
			return buf, err
		}
	}
}

// errorBody is an HTTP error body as text: empty when it is over errorBodyMax or not UTF-8, as ureq's limited
// `read_to_string` fails then.
func errorBody(r io.Reader) string {
	b, err := io.ReadAll(io.LimitReader(r, errorBodyMax+1))
	if err != nil || len(b) > errorBodyMax || !utf8.Valid(b) {
		return ""
	}
	return string(b)
}

// lossy is Rust's String::from_utf8_lossy: each maximal invalid subpart becomes one U+FFFD.
func lossy(s []byte) string {
	if utf8.Valid(s) {
		return string(s)
	}
	var out bytes.Buffer
	for i := 0; i < len(s); {
		if r, n := utf8.DecodeRune(s[i:]); r != utf8.RuneError || n > 1 {
			out.Write(s[i : i+n])
			i += n
			continue
		}
		out.WriteRune(utf8.RuneError)
		i += invalidLen(s[i:])
	}
	return out.String()
}

// invalidLen is the length of the invalid sequence at the start of s (Rust's Utf8Chunks).
func invalidLen(s []byte) int {
	in := func(i int, lo, hi byte) bool { return i < len(s) && s[i] >= lo && s[i] <= hi }
	c := s[0]
	switch {
	case c >= 0xe0 && c <= 0xef:
		ok := (c == 0xe0 && in(1, 0xa0, 0xbf)) || (c >= 0xe1 && c <= 0xec && in(1, 0x80, 0xbf)) ||
			(c == 0xed && in(1, 0x80, 0x9f)) || (c >= 0xee && in(1, 0x80, 0xbf))
		if !ok {
			return 1
		}
		return 2
	case c >= 0xf0 && c <= 0xf4:
		ok := (c == 0xf0 && in(1, 0x90, 0xbf)) || (c >= 0xf1 && c <= 0xf3 && in(1, 0x80, 0xbf)) || (c == 0xf4 && in(1, 0x80, 0x8f))
		if !ok {
			return 1
		}
		if !in(2, 0x80, 0xbf) {
			return 2
		}
		return 3
	}
	return 1
}
