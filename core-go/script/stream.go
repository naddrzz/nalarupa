package script

// Executing an agent's `execute` call while the model is still streaming it (stream.rs and script.rs
// `stream_step`): PartialString pulls the `input` string out of a partial JSON object, CompletePrefix cuts the
// JavaScript at the last complete top-level statement, and StreamStep runs each newly completed piece once, in the
// stream's live context, against the preview document.

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

// PartialString decodes the (possibly unterminated) string value of key from a partial JSON object, e.g.
// `{"input": "a = Insert(do` → `a = Insert(do`. Incomplete escapes are dropped; false when there is no string
// value of key yet.
func PartialString(json, key string) (string, bool) {
	pat := `"` + key + `"`
	at := strings.Index(json, pat)
	if at < 0 {
		return "", false
	}
	rest := strings.TrimLeftFunc(json[at+len(pat):], unicode.IsSpace)
	rest, ok := strings.CutPrefix(rest, ":")
	if !ok {
		return "", false
	}
	rest, ok = strings.CutPrefix(strings.TrimLeftFunc(rest, unicode.IsSpace), `"`)
	if !ok {
		return "", false
	}
	chars := []rune(rest)
	var out strings.Builder
	// take is the next n chars (fewer at the end), moving past them.
	i := 0
	take := func(n int) []rune {
		end := min(i+n, len(chars))
		t := chars[i:end]
		i = end
		return t
	}
	for i < len(chars) {
		c := chars[i]
		i++
		switch c {
		case '"':
			return out.String(), true
		case '\\':
			if i >= len(chars) {
				return out.String(), true
			}
			e := chars[i]
			i++
			switch e {
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case 'r':
				out.WriteByte('\r')
			case 'b':
				out.WriteByte('\b')
			case 'f':
				out.WriteByte('\f')
			case 'u':
				hex := take(4)
				if utf8Len(hex) < 4 {
					return out.String(), true
				}
				code, ok := hexU32(hex)
				if !ok {
					return out.String(), true
				}
				// Surrogate pairs arrive as two \u escapes.
				if code >= 0xD800 && code < 0xDC00 {
					look := chars[i:min(i+6, len(chars))]
					if utf8Len(look) < 6 {
						return out.String(), true
					}
					if len(look) >= 2 && look[0] == '\\' && look[1] == 'u' {
						if low, ok := hexU32(look[2:]); ok {
							i += 6
							// Wrapping u32 arithmetic, as Rust's release build does for a low half below 0xDC00.
							if ch, ok := scalar(0x10000 + (code-0xD800)<<10 + (low - 0xDC00)); ok {
								out.WriteRune(ch)
							}
						}
					}
				} else if ch, ok := scalar(code); ok {
					out.WriteRune(ch)
				}
			default:
				out.WriteRune(e)
			}
		default:
			out.WriteRune(c)
		}
	}
	return out.String(), true
}

// utf8Len is the UTF-8 length of rs (Rust's `String::len` of the collected chars).
func utf8Len(rs []rune) int {
	n := 0
	for _, r := range rs {
		n += utf8.RuneLen(r)
	}
	return n
}

// hexU32 is Rust's `u32::from_str_radix(s, 16)`: an optional `+`, then one or more hex digits, within u32.
func hexU32(rs []rune) (uint32, bool) {
	if len(rs) > 0 && rs[0] == '+' {
		rs = rs[1:]
	}
	if len(rs) == 0 {
		return 0, false
	}
	var v uint64
	for _, r := range rs {
		var d rune
		switch {
		case r >= '0' && r <= '9':
			d = r - '0'
		case r >= 'a' && r <= 'f':
			d = r - 'a' + 10
		case r >= 'A' && r <= 'F':
			d = r - 'A' + 10
		default:
			return 0, false
		}
		v = v<<4 | uint64(d)
		if v > 0xFFFFFFFF {
			return 0, false
		}
	}
	return uint32(v), true
}

// scalar is Rust's `char::from_u32`: false for surrogates and past U+10FFFF.
func scalar(c uint32) (rune, bool) {
	if c > unicode.MaxRune || c >= 0xD800 && c < 0xE000 {
		return 0, false
	}
	return rune(c), true
}

// CompletePrefix is the byte length of the longest prefix of src that ends at a complete top-level statement: a
// newline or `;` with all brackets closed and outside strings, template literals and comments. A statement is
// held until the next token shows it does not continue (`else`, `.map(...)`, `+ x`), because each released piece
// runs on its own. 0 when no statement is complete yet.
func CompletePrefix(src string) int {
	b := src
	depth := 0
	last := 0
	// pending is the end of the previous top-level statement, waiting for the next token to confirm it (-1: none).
	pending := -1
	i := 0
	// Template literal nesting: each entry is the bracket depth at which a `${` opened.
	var tmpl []int
	inTmpl := false
	at := func(k int) byte {
		if k < len(b) {
			return b[k]
		}
		return 0
	}
	for i < len(b) {
		c := b[i]
		if inTmpl {
			switch {
			case c == '\\':
				i++
			case c == '`':
				inTmpl = false
			case c == '$' && at(i+1) == '{':
				tmpl = append(tmpl, depth)
				depth++
				inTmpl = false
				i++
			}
			i++
			continue
		}
		if pending >= 0 {
			comment := c == '/' && i+1 < len(b) && (b[i+1] == '/' || b[i+1] == '*')
			if !asciiSpace(c) && !comment {
				switch continues(src[i:]) {
				case contMaybe:
					return last
				case contNo:
					last = pending
				}
				pending = -1
			}
		}
		switch {
		case c == '"' || c == '\'':
			i++
			for i < len(b) && b[i] != c && b[i] != '\n' {
				if b[i] == '\\' {
					i++
				}
				i++
			}
			if i >= len(b) {
				return last
			}
		case c == '`':
			inTmpl = true
		case c == '/' && at(i+1) == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			continue
		case c == '/' && at(i+1) == '*':
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return last
			}
			i += end + 3
		case c == '(' || c == '[' || c == '{':
			depth++
		case c == ')' || c == ']' || c == '}':
			depth--
			if c == '}' && len(tmpl) > 0 && tmpl[len(tmpl)-1] == depth {
				tmpl = tmpl[:len(tmpl)-1]
				inTmpl = true
			}
		case (c == ';' || c == '\n') && depth == 0:
			pending = i + 1
		}
		i++
	}
	return last
}

// asciiSpace is Rust's `u8::is_ascii_whitespace` (no vertical tab).
func asciiSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\f' || c == '\r'
}

const (
	contYes = iota
	contNo
	// contMaybe: the word there may still be growing.
	contMaybe
)

// continues is whether the code starting rest continues the statement before it.
func continues(rest string) int {
	end := strings.IndexFunc(rest, func(c rune) bool { return !(alphanumeric(c) || c == '_' || c == '$') })
	if end < 0 {
		return contMaybe
	}
	if end > 0 {
		switch rest[:end] {
		case "else", "catch", "finally":
			return contYes
		}
		return contNo
	}
	if strings.IndexByte(".,?:+-*/%&|^=<>([`", rest[0]) >= 0 {
		return contYes
	}
	return contNo
}

// alphanumeric is Rust's `char::is_alphanumeric`: Alphabetic or Numeric.
func alphanumeric(c rune) bool {
	return unicode.IsLetter(c) || unicode.IsNumber(c) || unicode.Is(unicode.Other_Alphabetic, c)
}

// Live is the JS state of an `execute` call still streaming: a context that has run Ran against the preview
// document. Its runtime lives on a goroutine of its own (a runtime is confined to the goroutine that made it), which
// runs each step while the caller waits; Close ends it.
type Live struct {
	ran string
	// draws is how many ids the stream has drawn: the next step resumes the seeded id sequence right there.
	draws   uint64
	touched []string
	// broken: the last step threw, so the next update re-runs the stream from scratch.
	broken bool
	// stalled: the last step hit the time or memory limit, so the stream stays out of the preview.
	stalled bool
	ex      *exec
	work    chan func()
	done    chan struct{}
}

// newLive starts a live context with the globals earlier calls kept: the natives, the prelude, then the globals.
func newLive(globals *jsonv.Object) (*Live, error) {
	l := &Live{work: make(chan func()), done: make(chan struct{})}
	ready := make(chan error, 1)
	go l.loop(globals, ready)
	if err := <-ready; err != nil {
		return nil, err
	}
	return l, nil
}

// loop is the live context's goroutine: it makes the runtime, runs the steps sent to it, and frees the runtime when
// the stream is closed.
func (l *Live) loop(globals *jsonv.Object, ready chan<- error) {
	defer close(l.done)
	js, err := NewJS(Limits{Memory: MemoryLimit, Stack: stackLimit})
	if err != nil {
		ready <- err
		return
	}
	defer js.Close()
	l.ex = &exec{js: js, budget: PreviewTimeout, preview: true}
	if err := l.setUp(globals); err != nil {
		ready <- err
		return
	}
	ready <- nil
	for f := range l.work {
		f()
	}
}

// setUp is the sandbox of the live context; a panic is an error.
func (l *Live) setUp(globals *jsonv.Object) (err error) {
	defer func() {
		if recover() != nil {
			err = errorString("internal error (panic) while running the script")
		}
	}()
	return l.ex.sandbox(globals)
}

// do runs f on the live context's goroutine and waits for it.
func (l *Live) do(f func()) {
	ran := make(chan struct{})
	l.work <- func() {
		defer close(ran)
		f()
	}
	<-ran
}

// Close ends the stream: its runtime is freed on its own goroutine.
func (l *Live) Close() {
	close(l.work)
	<-l.done
}

// Ran is the source the context has run.
func (l *Live) Ran() string { return l.ran }

// Broken reports that the last step threw.
func (l *Live) Broken() bool { return l.broken }

// Stalled reports that the last step hit the time or memory limit.
func (l *Live) Stalled() bool { return l.stalled }

// Touched is every id the stream has touched.
func (l *Live) Touched() []string { return append([]string(nil), l.touched...) }

// Global is the JSON of the context's global name ("" when it does not serialise).
func (l *Live) Global(name string) string {
	out := ""
	l.do(func() {
		v, err := l.ex.js.Eval("JSON.stringify(globalThis["+string(jsonv.Marshal(name))+"])", "eval_script")
		if err != nil {
			return
		}
		defer l.ex.js.Free(v)
		out, _ = l.ex.js.String(v)
	})
	return out
}

// step runs chunk in the live context against h's document with the preview budget (script.rs `bind` with the
// stream's context): what succeeded stays, nothing is rolled back. A panic in a native is an error.
func (l *Live) step(h Host, chunk string) (err error) {
	js := l.ex.js
	*l.ex = exec{host: h, js: js, budget: PreviewTimeout, deadline: time.Now().Add(PreviewTimeout), preview: true}
	ex := l.ex
	js.Arm(ex.deadline)
	defer js.Arm(time.Time{})
	defer func() {
		if recover() != nil {
			err = errorString("internal error (panic) while running the script")
		}
	}()
	if i := strings.IndexByte(chunk, 0); i >= 0 {
		// rquickjs hands the source to C as a C string.
		return errorString("String contained internal null bytes")
	}
	if err := js.Exec(chunk, "input"); err != nil {
		return errorString(ex.describe(err))
	}
	return ex.settle()
}

// StreamStep is the streaming preview (script.rs `stream_step`): it runs the part of stream key's source it has not
// run yet, in the stream's live context (kept in lives), against h's document (the caller makes that the preview
// document) with ids from the sequence of seed. Nothing is committed or rolled back: what succeeded stays visible
// while the agent is still writing. globals are the agent's, for a new context.
//
// It returns every id the stream has touched, or false when it cannot continue (its prefix changed in a
// non-append way, or a statement threw): the caller then rebuilds the preview, which re-runs the stream from
// scratch in one piece (so later function declarations hoist). The caller holds the engine for the whole step.
func StreamStep(h Host, lives map[string]*Live, key string, globals *jsonv.Object, source string, seed uint64) ([]string, bool) {
	l := lives[key]
	switch {
	case l != nil && l.stalled:
		return l.Touched(), true
	case l != nil && (l.broken || !strings.HasPrefix(source, l.ran)):
		delete(lives, key)
		l.Close()
		return nil, false
	case l == nil:
		var err error
		if l, err = newLive(globals); err != nil {
			return nil, false
		}
		lives[key] = l
	}
	chunk := source[len(l.ran):]
	if strings.TrimSpace(chunk) != "" {
		ids := h.IDs()
		ids.Resume(seed, l.draws)
		// The step's ids all come from h.IDs(), whatever generator the document had.
		d := h.Doc()
		prev := d.IDs
		d.IDs = ids
		defer func() { d.IDs = prev }()
		var err error
		l.do(func() { err = l.step(h, chunk) })
		if n, ok := ids.Drawn(seed, l.draws); ok {
			l.draws = n
		} else {
			l.broken = true
		}
		l.touched = append(l.touched, l.ex.touched...)
		l.stalled = l.ex.limit
		l.broken = l.broken || err != nil
		l.ran = source
		l.ex.host = nil
	}
	return l.Touched(), true
}
