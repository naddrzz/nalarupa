package agent

// The pen.rs tests, then pens drawing through the hook over the fake API: events, nodes, and Stop.

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/engine"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// A streamed SVG becomes nodes in the frame while it grows: finished shapes get their paint, the path being
// written is an outline cut after its last finished number, gradients map to box-relative fills, and the final
// document matches what a full parse gives.
func TestStreamedSVGDrawsAsItGrows(t *testing.T) {
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 200 100"><defs><linearGradient id="g" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="#ff0000"/><stop offset="1" stop-color="#0000ff"/></linearGradient></defs><rect x="0" y="0" width="200" height="100" fill="url(#g)"/><g transform="translate(10 10)"><path fill="#00ff00" stroke="#000" stroke-width="2" d="M0 0 L50 0 L50 40 Z"/></g><circle cx="150" cy="50" r="20" fill="#123456" opacity="0.5"/></svg>`
	// Before the root tag is complete there is nothing to draw.
	if penDraw(svg[:20], 400, 400, false) != nil {
		t.Error("drawn before the root tag is complete")
	}
	f := func(v jsonv.Value) float64 { x, _ := jsonv.F64(v); return x }

	// Mid-path: the rect is done, the triangle's `d` stops at "M0 0 L50 0 L5".
	cut := strings.Index(svg, "L50 40") + 2
	d := penDraw("Sure!\n```svg\n"+svg[:cut], 400, 400, false)
	if d == nil || len(d.nodes) != 2 {
		t.Fatalf("mid-path: %v", d)
	}
	rect := d.nodes[0]
	// 200×100 fitted into 400×400: scale 2, centered vertically (offset 100).
	if got := [4]float64{f(jsonv.Get(rect, "x")), f(jsonv.Get(rect, "y")), f(jsonv.Get(rect, "width")), f(jsonv.Get(rect, "height"))}; got != [4]float64{0, 100, 400, 200} {
		t.Errorf("rect box %v", got)
	}
	fill := jsonv.Get(rect, "fill")
	if !jsonv.StrIs(jsonv.Get(fill, "gradientType"), "linear") {
		t.Errorf("rect fill %s", jsonv.Marshal(fill))
	}
	if r := f(jsonv.Get(fill, "rotation")); r != 180 && r != -180 {
		t.Errorf("top-to-bottom gradient points down: %v", r)
	}
	growing := d.nodes[1]
	if !jsonv.StrIs(jsonv.Get(growing, "stroke"), penPreview) || jsonv.Get(growing, "fill") != nil {
		t.Errorf("growing %s", jsonv.Marshal(growing))
	}
	// translate(10 10) then ×2 and +100 y, cut before the unfinished number.
	if g := jsonv.StrOr(jsonv.Get(growing, "geometry")); g != "M20 120L120 120" {
		t.Errorf("growing geometry %q", g)
	}
	if !d.hasPoint || d.point != [2]float64{120, 120} {
		t.Errorf("point %v", d.point)
	}

	// Finished: real paint, group transform applied, opacity carried.
	d = penDraw(svg, 400, 400, true)
	if d == nil || len(d.nodes) != 3 {
		t.Fatalf("finished: %v", d)
	}
	tri := d.nodes[1]
	if !jsonv.StrIs(jsonv.Get(tri, "fill"), "#00ff00") || f(jsonv.Get(tri, "strokeWidth")) != 4 {
		t.Errorf("triangle %s", jsonv.Marshal(tri))
	}
	if g := jsonv.StrOr(jsonv.Get(tri, "geometry")); g != "M20 120L120 120L120 200Z" {
		t.Errorf("triangle geometry %q", g)
	}
	if f(jsonv.Get(d.nodes[2], "opacity")) != 0.5 || !jsonv.StrIs(jsonv.Get(d.nodes[2], "fill"), "#123456") {
		t.Errorf("circle %s", jsonv.Marshal(d.nodes[2]))
	}
	// The JSON is Rust's (pen.rs writes these keys in this order, numbers as serde does).
	want := `{"type":"path","x":20.0,"y":120.0,"width":100.0,"height":80.0,"geometry":"M20 120L120 120L120 200Z","viewBox":[20.0,120.0,100.0,80.0],"fill":"#00ff00","stroke":"#000000","strokeWidth":4.0,"strokeLinecap":"butt","strokeLinejoin":"miter"}`
	if got := string(jsonv.Marshal(tri)); got != want {
		t.Errorf("triangle JSON\n got %s\nwant %s", got, want)
	}
}

// The Claude Code pen's command carries the picked model, like the agent's own process; with "Default" (unset)
// it passes no flag and the CLI's default applies.
func TestCLIPassesThePickedModelOnlyWhenSet(t *testing.T) {
	picked := penArgs("a cat", "claude-haiku-5-5", true)
	at := slices.Index(picked, "--model")
	if at < 0 || picked[at+1] != "claude-haiku-5-5" {
		t.Errorf("--model passed: %q", picked)
	}
	if slices.Contains(penArgs("a cat", "", false), "--model") {
		t.Error("no --model by default")
	}
}

// On the Claude Code backend the pen starts `claude` with the model picked in the composer, and with "Default"
// (unset) without `--model`. A stand-in `claude` (Rust: a shell script first on PATH, unix only; here the test
// binary, on every system) draws the model it got.
func TestClaudeCodePenDrawsWithThePickedModel(t *testing.T) {
	e, _ := ccEngine(t, "pen")
	draw := func(model string) (error, string) {
		e.Locked(func() {
			if model != "" {
				e.Settings.Set("agent_model", model)
			} else {
				e.Settings.Remove("agent_model")
			}
		})
		var text strings.Builder
		err := penGenerate(e, "a cat", engine.NewFlag(), func(s string) bool { text.WriteString(s); return true })
		return err, text.String()
	}
	if err, text := draw("claude-haiku-5-5"); err != nil || text != "model=claude-haiku-5-5" {
		t.Errorf("the picked model reaches the pen's `claude`: %v %q", err, text)
	}
	if err, text := draw(""); err != nil || text != "model=" {
		t.Errorf("Default passes no --model: %v %q", err, text)
	}
}

// Unfinished markup never breaks the parse: an open attribute elsewhere, comments, and an element id on the
// growing path are all handled.
func TestCloseKeepsPrefixesWellFormed(t *testing.T) {
	doc, ok := closeSVG(`<svg viewBox="0 0 10 10"><!-- hi --><g><path id="a" fill="red" d="M1 1 L9 1 L9 9`)
	if !ok || !strings.HasSuffix(doc, `<path id="nalarupa-pen" data-id="a" fill="red" d="M1 1 L9 1 L9 "/></g></svg>`) {
		t.Errorf("%q", doc)
	}
	opt := usvg.DefaultOptions()
	if _, err := usvg.FromStr(doc, &opt); err != nil {
		t.Error(err)
	}
	// An unfinished non-geometry attribute waits for the tag to end...
	if doc, _ := closeSVG(`<svg viewBox="0 0 10 10"><rect fill="re`); doc != `<svg viewBox="0 0 10 10"></svg>` {
		t.Errorf("%q", doc)
	}
	// ...but a path whose geometry is done stays an outline while its later attributes stream.
	if doc, _ := closeSVG(`<svg viewBox="0 0 10 10"><path d="M1 1 L9 9" fill="#ab`); !strings.Contains(doc, `<path id="nalarupa-pen" d="M1 1 L9 9"/>`) {
		t.Errorf("%q", doc)
	}
	if _, ok := closeSVG("<sv"); ok {
		t.Error("<sv closed")
	}
}

// penFrame is a frame in e's document for a pen to draw into, flagged as generating.
func penFrame(t *testing.T, e *engine.Engine) string {
	t.Helper()
	var id string
	e.Locked(func() {
		var err error
		id, err = e.InsertRoot(engine.Object("type", "frame", "x", jsonv.Number("10"), "y", jsonv.Number("20"), "width", jsonv.Number("400"),
			"height", jsonv.Number("200"), "placeholder", true))
		if err != nil {
			t.Fatal(err)
		}
		e.Sync()
	})
	return id
}

// startPenHook starts a pen as an `execute` call by agent does, its Stop mark taken now unless stops is given.
func startPenHook(e *engine.Engine, agent, frame string, stops ...uint64) {
	e.Locked(func() {
		mark := e.Agent.Stops
		if len(stops) > 0 {
			mark = stops[0]
		}
		e.Hooks.StartPen(agent, mark, frame, "a sunset", 400, 200)
	})
}

// waitPens waits until no pen draws.
func waitPens(t *testing.T, e *engine.Engine) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		n := 0
		e.Locked(func() { n = len(e.Agent.Pens) })
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the pen never ended")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func eventsOf(e *engine.Engine, typ string) []jsonv.Value {
	var out []jsonv.Value
	e.Locked(func() {
		for _, ev := range e.Events {
			if jsonv.StrIs(jsonv.Get(ev, "type"), typ) {
				out = append(out, ev)
			}
		}
	})
	return out
}

// A pen streams its SVG into the frame: the request is the pen's, the pen cursor moves in canvas coordinates,
// the finished drawing replaces the frame's children, the frame stops generating, and the pen's entry goes.
func TestAPenDrawsIntoItsFrame(t *testing.T) {
	svg := `<svg viewBox="0 0 200 100"><rect width="200" height="100" fill="#ff0000"/><path fill="#00ff00" d="M0 0 L50 0 L50 40 Z"/></svg>`
	api, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		var deltas []string
		for i := 0; i < len(svg); i += 20 {
			deltas = append(deltas, fmt.Sprintf(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`, svg[i:min(i+20, len(svg))]))
		}
		events := append([]string{`{"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`}, deltas...)
		events = append(events, `{"type":"content_block_stop","index":0}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`)
		return sse(events...)
	})
	e := apiEngine(t, url)
	frame := penFrame(t, e)
	startPenHook(e, "agent", frame)
	waitPens(t, e)
	body := api.requests()[0].body
	if !jsonv.StrIs(jsonv.Get(body, "system"), penSystem) || !strings.HasSuffix(jsonv.StrOr(jsonv.Get(jsonv.Items(jsonv.Get(body, "messages"))[0], "content")),
		"a sunset\n\nThe artwork fills a 400 × 200 frame: use a viewBox with that aspect ratio.") || jsonv.StrOr(jsonv.Get(body, "model")) != DefaultModel {
		t.Errorf("request %s", jsonv.Marshal(body))
	}
	pens := eventsOf(e, "pen")
	if len(pens) == 0 {
		t.Fatal("no pen cursor")
	}
	// The triangle's last point (50, 40) ×2 into the 400×200 frame at (10, 20).
	if last := pens[len(pens)-1]; string(jsonv.Marshal(last)) != fmt.Sprintf(`{"type":"pen","id":%q,"x":110.0,"y":100.0}`, frame) {
		t.Errorf("pen %s", jsonv.Marshal(last))
	}
	if ends := eventsOf(e, "pen_end"); len(ends) != 1 || !jsonv.StrIs(jsonv.Get(ends[0], "id"), frame) {
		t.Errorf("pen_end %v", ends)
	}
	e.Locked(func() {
		n := e.Doc.Find(frame)
		kids := jsonv.Items(jsonv.Get(n, "children"))
		if len(kids) != 2 || !jsonv.StrIs(jsonv.Get(kids[1], "geometry"), "M0 0L100 0L100 80Z") || !jsonv.IsFalse(jsonv.Get(n, "placeholder")) ||
			!jsonv.StrIs(jsonv.Get(n, "layout"), "none") || !jsonv.IsTrue(jsonv.Get(n, "clip")) {
			t.Errorf("frame %s", jsonv.Marshal(n))
		}
		if jsonv.StrIs(jsonv.Get(kids[0], "id"), "") || jsonv.StrIs(jsonv.Get(kids[0], "id"), jsonv.StrOr(jsonv.Get(kids[1], "id"))) {
			t.Errorf("ids %s", jsonv.Marshal(kids))
		}
	})
}

// Stop ends a pen mid-draw: the stream is cancelled, the pen still finishes its frame (pen_end, no longer
// generating), and its entry goes. A pen whose call saw a Stop starts stopped.
func TestStopEndsADrawingPen(t *testing.T) {
	_, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		w.Header().Set("content-type", "text/event-stream")
		fmt.Fprint(w, sse(`{"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"<svg viewBox=\"0 0 10 10\"><path d=\"M1 1 L9 9 L5"}}`))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return ""
	})
	e := apiEngine(t, url)
	frame := penFrame(t, e)
	startPenHook(e, "agent", frame)
	deadline := time.Now().Add(20 * time.Second)
	for len(eventsOf(e, "pen")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the pen never drew")
		}
		time.Sleep(5 * time.Millisecond)
	}
	Stop(e)
	waitPens(t, e)
	if len(eventsOf(e, "pen_end")) != 1 {
		t.Error("pen_end")
	}
	e.Locked(func() {
		if !jsonv.IsFalse(jsonv.Get(e.Doc.Find(frame), "placeholder")) {
			t.Error("the stopped pen's frame is still generating")
		}
	})

	// The call began before a Stop: its pen starts stopped and asks nothing of the model.
	var stops uint64
	e.Locked(func() { stops = e.Agent.Stops - 1 })
	startPenHook(e, "agent", frame, stops)
	e.Locked(func() {
		if len(e.Agent.Pens) == 1 && !e.Agent.Pens[0].Cancel.IsSet() {
			t.Error("a pen started after a Stop runs")
		}
	})
	waitPens(t, e)
}

// A pen without a model to ask says so in a toast and still finishes its frame.
func TestAPenWithoutAKeyShowsAToast(t *testing.T) {
	e := newEngine(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	e.Locked(func() { e.Settings.Set("agent_backend", "api") })
	frame := penFrame(t, e)
	startPenHook(e, "agent", frame)
	waitPens(t, e)
	toasts := eventsOf(e, "toast")
	if len(toasts) != 1 || !strings.HasPrefix(jsonv.StrOr(jsonv.Get(toasts[0], "text")), "AI Pen: No Anthropic API key.") {
		t.Errorf("toasts %v", toasts)
	}
	if len(eventsOf(e, "pen_end")) != 1 {
		t.Error("pen_end")
	}
}

// A pen whose stream panics ends with a toast, and its frame still finishes: pen_end, no longer generating, and
// its entry gone.
func TestAPanickingPenStillFinishesItsFrame(t *testing.T) {
	_, url := newFakeAPI(t, func(n int, w http.ResponseWriter, r *http.Request) string {
		// A block that is not an object, then a delta into it: the client panics.
		return sse(`{"type":"message_start","message":{"usage":{}}}`,
			`{"type":"content_block_start","index":0,"content_block":"x"}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi"}}`)
	})
	e := apiEngine(t, url)
	frame := penFrame(t, e)
	startPenHook(e, "agent", frame)
	waitPens(t, e)
	toasts := eventsOf(e, "toast")
	if len(toasts) != 1 || !strings.HasPrefix(jsonv.StrOr(jsonv.Get(toasts[0], "text")), "AI Pen: internal error (panic):") {
		t.Errorf("toasts %v", toasts)
	}
	if ends := eventsOf(e, "pen_end"); len(ends) != 1 || !jsonv.StrIs(jsonv.Get(ends[0], "id"), frame) {
		t.Errorf("pen_end %v", ends)
	}
	e.Locked(func() {
		if !jsonv.IsFalse(jsonv.Get(e.Doc.Find(frame), "placeholder")) {
			t.Error("the frame is still generating")
		}
	})
}
