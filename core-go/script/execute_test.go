package script_test

// The execute sandbox: script.rs's execute tests that need no engine (transaction, repair, globals, warnings,
// limits and promises), plus each row of the 5b plan's matrix. Exact parity with Rust is exec_parity_test.go.

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/script"
)

// run is one execute call of agent "agent" with the full budget.
func (a *agents) run(h *docHost, src string) *script.Outcome {
	return a.call(h, "agent", input(src), 0)
}

func names(h *docHost) []string {
	var out []string
	for _, n := range h.doc.Children() {
		out = append(out, jsonv.StrOr(jsonv.Get(n, "name")))
	}
	return out
}

func lines(text string, all ...string) int {
	n := 0
	for _, l := range strings.Split(text, "\n") {
		hit := true
		for _, s := range all {
			hit = hit && strings.Contains(l, s)
		}
		if hit {
			n++
		}
	}
	return n
}

func TestBadChildrenFailTheCallWithARepairableError(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	for _, kids := range []string{`["str"]`, "[5]", "[null]", `[{type:"nope",name:"bad"}]`} {
		out := a.run(h, fmt.Sprintf(`Insert(document, {type:"frame", width:10, height:10, children:%s})`, kids))
		if !out.Error || !strings.Contains(out.Text, "children[0]") || !strings.Contains(out.Text, "editId") {
			t.Fatalf("%s: %s", kids, out.Text)
		}
		if len(h.doc.Children()) != 0 {
			t.Fatalf("%s: not rolled back", kids)
		}
	}
}

func TestExecuteTransactionAndRepair(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	ok := a.run(h, `
            card = Insert(document, {type:"frame", name:"Card", layout:"vertical", padding:16, gap:8, x:0, y:0});
            for (const t of ["A","B"]) Insert(card, {type:"text", name:"T"+t, content:t, fill:"#111111", fontSize:14});
            Print(Get(card, {depth:1}).children.length, Get(n => n.type === "text" ? n.name : undefined));
        `)
	if ok.Error || !strings.Contains(ok.Text, `"Card"`) || !strings.Contains(ok.Text, `2 ["TA","TB"]`) {
		t.Fatal(ok.Text)
	}
	if len(h.doc.Children()) != 1 || !a.globals["agent"].Has("card") {
		t.Fatal("the card and its global")
	}
	bad := a.run(h, "Insert(document,{type:'frame',name:'X'}); Update('nope', {x:1});")
	if !bad.Error || len(h.doc.Children()) != 1 {
		t.Fatalf("rolled back: %s", bad.Text)
	}
	editID := strings.SplitN(strings.SplitN(bad.Text, `editId: "`, 2)[1], `"`, 2)[0]
	edits := jsonv.MustParse(`[{"find": "Update('nope', {x:1});", "replace": "Update(card, {x:5});"}]`)
	args := jsonv.NewObject()
	args.Set("editId", editID)
	args.Set("edits", edits)
	fixed := a.call(h, "agent", args, 0)
	if fixed.Error || len(h.doc.Children()) != 2 || fixed.Repaired != editID {
		t.Fatal(fixed.Text)
	}
	if _, ok := a.failed[editID]; ok {
		t.Fatal("a repaired snippet is forgotten")
	}
	again := a.call(h, "agent", args, 0)
	if !again.Error || again.Text != fmt.Sprintf("unknown editId '%s'", editID) {
		t.Fatal(again.Text)
	}
}

func TestArgumentErrors(t *testing.T) {
	h := emptyHost(t)
	for args, want := range map[string]string{
		`{"input": "  "}`:               "`input` (JavaScript) is required",
		`{}`:                            "`input` (JavaScript) is required",
		`{"input": "x", "edits": []}`:   "pass either `input` or `edits` (with `editId`), not both",
		`{"edits": []}`:                 "`edits` requires the `editId` from the failed call",
		`{"edits": [], "editId": "e1"}`: "unknown editId 'e1'",
		`{"edits": [{"find": "a"}], "editId": "e1", "x":1}`: "unknown editId 'e1'",
	} {
		out := script.Execute(h, jsonv.MustParse(args), script.Call{})
		if !out.Error || out.Text != want || out.Before != nil {
			t.Errorf("%s: %q", args, out.Text)
		}
	}
}

func TestApplyEdits(t *testing.T) {
	edits := func(s string) []jsonv.Value { return jsonv.Items(jsonv.MustParse(s)) }
	for _, c := range []struct{ src, edits, want string }{
		{"a b a", `[{"find":"b","replace":"c"}]`, "a c a"},
		{"a b a", `[{"find":"a","replace":"x","all":true}]`, "x b x"},
		{"a b a", `[{"find":"a","replace":"x"}]`, "error: edit 0: `find` matches more than once; make it unique or set all:true"},
		{"a b", `[{"find":"z","replace":"x"}]`, "error: edit 0: `find` text not found in the stored snippet"},
		{"a b", `[{"replace":"x"}]`, "error: edit 0: find is required"},
		{"a b", `[{"find":"a"}]`, "error: edit 0: replace is required"},
		{"one\ntwo", `[{"find":"one\\ntwo","replace":"x"}]`, "x"},
		{`say("a\nb")`, `[{"find":"a\nb","replace":"c"}]`, `say("c")`},
		{"a b", `[{"find":"a","replace":"c"},{"find":"c b","replace":"d"}]`, "d"},
	} {
		got, err := script.ApplyEdits(c.src, edits(c.edits))
		if err != nil {
			got = "error: " + err.Error()
		}
		if got != c.want {
			t.Errorf("%q %s: %q, want %q", c.src, c.edits, got, c.want)
		}
	}
}

func TestGlobalsArePerAgent(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	run := func(agent, src string) string {
		out := a.call(h, agent, input(src), 0)
		if out.Error {
			t.Fatal(out.Text)
		}
		return out.Text
	}
	run("agA", `page = Insert(document, {type:"frame", name:"A page", width:400, height:300})`)
	if b := run("agB", `page = Insert(document, {type:"frame", name:"B page", x:500, width:400, height:300})`); !strings.Contains(b, "carry over to later calls: page") {
		t.Fatal(b)
	}
	run("agA", `Insert(page, {type:"text", name:"A text", content:"from A", fill:"#000"})`)
	of := func(name string) int {
		for _, n := range h.doc.Children() {
			if jsonv.StrIs(jsonv.Get(n, "name"), name) {
				return len(jsonv.Items(jsonv.Get(n, "children")))
			}
		}
		return -1
	}
	if of("A page") != 1 || of("B page") != 0 {
		t.Fatal("A's text lands in A's frame")
	}
	if c := run("agC", "Print(typeof page)"); !strings.Contains(c, "undefined") {
		t.Fatal(c)
	}
}

func TestGlobalsCarryOver(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	first := a.run(h, `page = Insert(document, {type:"frame", name:"Page", width:100, height:100})
n = 1.5; s = "x"; o = {a: [1, {b: null}]}; f = function () {}; u = undefined; big = 10n; var v = 2; let l = 3`)
	if first.Error {
		t.Fatal(first.Text)
	}
	if !strings.Contains(first.Text, "\nGlobal variables carry over to later calls: v, page, n, s, o\n") {
		t.Fatalf("functions, undefined, unserializable values and `let` are dropped: %s", first.Text)
	}
	if got := string(jsonv.Marshal(first.Globals)); !strings.Contains(got, `"o":{"a":[1,{"b":null}]}`) {
		t.Fatal(got)
	}
	if !strings.Contains(first.Text, `"Page": "`) || !strings.Contains(first.Text, `" (= page)`) {
		t.Fatal(first.Text)
	}
	second := a.run(h, `Print(typeof page, n, s, o.a[1].b, typeof f, typeof __base, typeof Insert)
Insert(page, {type:"rectangle", name:"R", width:10, height:10})`)
	if second.Error || !strings.Contains(second.Text, "\n## Print output\nstring 1.5 x null undefined object function\n") {
		t.Fatal(second.Text)
	}
	if !strings.Contains(second.Text, "Global variables carry over to later calls: v, page, n, s, o") {
		t.Fatalf("restored globals are the call's globals again: %s", second.Text)
	}
}

func TestWarnings(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	r := a.run(h, "f = Insert(document, {type:'frame', layout:'vertical', width:100, height:50}); Insert(f, {type:'text', name:'Ghost', content:'hi', x:5});")
	if !strings.Contains(r.Text, "has no fill") || !strings.Contains(r.Text, "x/y are ignored") {
		t.Fatal(r.Text)
	}
}

func TestEachWarningIsOneLine(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	r := a.run(h, `f = Insert(document, {type:'frame', layout:'vertical', width:100, height:50}); Insert(f, {type:'text', name:'Two\nlines', content:'hi'}); Insert(f, {type:'text', name:'After', content:'hi'});`)
	_, issues, _ := strings.Cut(r.Text, "## Issues detected")
	ls := strings.Split(strings.TrimSuffix(issues, "\n"), "\n")[1:]
	for _, l := range ls {
		if !strings.HasPrefix(l, "- ") {
			t.Fatal(r.Text)
		}
	}
	if !strings.Contains(issues, `"After"`) || !strings.Contains(issues, `"Two lines"`) {
		t.Fatal(r.Text)
	}
}

func TestLowTextContrastIsReported(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	text := func(name, fill string, size int, weight string) string {
		return fmt.Sprintf("Insert(f, {type:'text', name:'%s', content:'Quarterly revenue', fill:'%s', fontSize:%d, fontWeight:'%s'});", name, fill, size, weight)
	}
	r := a.run(h, "f = Insert(document, {type:'frame', name:'Page', layout:'vertical', width:600, height:400, fill:'#FFFFFF'}); "+
		text("Faint", "#999999", 16, "400")+text("Body", "#767676", 16, "400")+text("Large", "#949494", 32, "400")+
		text("Bold", "#949494", 19, "700")+text("Small grey", "#949494", 16, "400"))
	flagged := func(name string) bool { return lines(r.Text, `"`+name+`"`, "contrast") > 0 }
	if !flagged("Faint") || !strings.Contains(r.Text, "contrast 2.84:1") || !strings.Contains(r.Text, "against its background #ffffff is below WCAG AA (4.5:1)") {
		t.Fatal(r.Text)
	}
	if flagged("Body") || flagged("Large") || flagged("Bold") || !flagged("Small grey") {
		t.Fatal(r.Text)
	}
}

func TestTextContrastIsJudgedAgainstTheNearestPaintedAncestor(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	flagged := func(out *script.Outcome, name string) bool { return lines(out.Text, `"`+name+`"`, "contrast") > 0 }
	r := a.run(h, "dark = Insert(document, {type:'frame', name:'Dark', layout:'vertical', width:400, height:200, fill:'#111111'}); inner = Insert(dark, {type:'frame', name:'Inner', layout:'vertical'}); Insert(inner, {type:'text', name:'Ink', content:'Hidden in the dark', fill:'#333333', fontSize:16}); Insert(inner, {type:'text', name:'Paper', content:'Easy to read', fill:'#F4EFE6', fontSize:16});")
	if !flagged(r, "Ink") || flagged(r, "Paper") {
		t.Fatal(r.Text)
	}
	card := a.run(h, "c = Insert(document, {type:'frame', name:'Card', layout:'none', width:300, height:200, fill:'#FFFFFF'}); Insert(c, {type:'rectangle', name:'Shade', x:0, y:0, width:300, height:120, fill:{color:'#1F2A1C'}}); Insert(c, {type:'text', name:'On shade', x:20, y:20, content:'Over a sibling', fill:'#FFFFFF', fontSize:16}); Insert(c, {type:'text', name:'Lost on shade', x:20, y:60, content:'Dark on dark', fill:'#333333', fontSize:16}); Insert(c, {type:'text', name:'Half on shade', x:20, y:110, content:'Across the edge', fill:'#333333', fontSize:16});")
	if flagged(card, "On shade") || !flagged(card, "Lost on shade") || flagged(card, "Half on shade") {
		t.Fatal(card.Text)
	}
	next := a.run(h, "Insert(dark, {type:'text', name:'Late', content:'Added later', fill:'#F4EFE6', fontSize:16})")
	if strings.Contains(next.Text, `"Ink"`) {
		t.Fatal(next.Text)
	}
	odd := a.run(h, "o = Insert(document, {type:'frame', name:'Odd', layout:'none', width:300, height:300, fill:'#FFFFFF'}); Insert(o, {type:'ellipse', name:'Disc', x:0, y:0, width:300, height:100, fill:'#111111'}); Insert(o, {type:'text', name:'Corner', x:2, y:2, content:'In the corner', fill:'#EEEEEE', fontSize:16}); Insert(o, {type:'rectangle', name:'Multiply', x:0, y:150, width:300, height:100, fill:{type:'color', color:'#111111', blendMode:'multiply'}}); Insert(o, {type:'text', name:'On multiply', x:20, y:170, content:'Blended under', fill:'#FFFFFF', fontSize:16}); Insert(o, {type:'text', name:'Clear', x:20, y:270, content:'Not painted', fill:'#00000000', fontSize:16});")
	if flagged(odd, "Corner") || flagged(odd, "On multiply") || flagged(odd, "Clear") {
		t.Fatal(odd.Text)
	}
	veil := a.run(h, "v = Insert(document, {type:'frame', name:'Veil', layout:'vertical', opacity:0.4, width:200, height:100, fill:'#000000'}); Insert(v, {type:'text', name:'Under veil', content:'Seen through', fill:'#222222', fontSize:16});")
	if strings.Contains(veil.Text, "contrast") {
		t.Fatal(veil.Text)
	}
}

func TestSmallBodyTextIsReportedButShortLabelsAreNot(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	r := a.run(h, "f = Insert(document, {type:'frame', name:'Page', layout:'vertical', width:600, height:300, fill:'#FFFFFF'}); Insert(f, {type:'text', name:'Fine print', content:'Pre-orders close on Thursday at six; collect them on Saturday morning.', fill:'#111111', fontSize:11}); Insert(f, {type:'text', name:'Badge', content:'NEW', fill:'#111111', fontSize:10}); Insert(f, {type:'text', name:'Body', content:'Pre-orders close on Thursday at six; collect them on Saturday morning.', fill:'#111111', fontSize:12});")
	flagged := func(name string) bool { return lines(r.Text, `"`+name+`"`, "below 12px") > 0 }
	if !flagged("Fine print") || flagged("Badge") || flagged("Body") || !strings.Contains(r.Text, "body text at 11px is below 12px") {
		t.Fatal(r.Text)
	}
}

func TestNearMissAlignmentIsReportedOutsideAutoLayout(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	rect := func(name string, x, y float64) string {
		return fmt.Sprintf("Insert(f, {type:'rectangle', name:'%s', x:%v, y:%v, width:100, height:40, fill:'#CCCCCC'});", name, x, y)
	}
	r := a.run(h, "f = Insert(document, {type:'frame', name:'Free', layout:'none', width:600, height:400, fill:'#FFFFFF'}); "+
		rect("A", 40, 20)+rect("Slipped", 41.5, 80)+rect("Exact", 40, 140)+rect("Apart", 48, 200))
	near := func(out *script.Outcome, x, y string) bool {
		return lines(out.Text, `"`+x+`"`, `"`+y+`"`, "nearly align") > 0
	}
	if !near(r, "A", "Slipped") || near(r, "A", "Exact") || near(r, "A", "Apart") || near(r, "Exact", "Apart") {
		t.Fatal(r.Text)
	}
	if !strings.Contains(r.Text, "their left edges are 1.5px apart") {
		t.Fatal(r.Text)
	}
	tops := a.run(h, "Insert(f, {type:'rectangle', name:'Tall', x:300, y:20, width:60, height:40, fill:'#CCCCCC'}); Insert(f, {type:'rectangle', name:'Short', x:420, y:20, width:60, height:38, fill:'#CCCCCC'});")
	if lines(tops.Text, `"Short"`, "nearly align") > 0 {
		t.Fatal(tops.Text)
	}
	centres := a.run(h, "Insert(f, {type:'rectangle', name:'Band', x:0, y:300, width:500, height:20, fill:'#CCCCCC'}); Insert(f, {type:'rectangle', name:'Dot', x:240, y:340, width:24, height:24, fill:'#CCCCCC'});")
	if lines(centres.Text, `"Dot"`, "nearly align") > 0 {
		t.Fatal(centres.Text)
	}
	flow := a.run(h, "g = Insert(document, {type:'frame', name:'Flow', layout:'vertical', gap:7, width:300, height:200, fill:'#FFFFFF'}); Insert(g, {type:'rectangle', name:'R1', width:100, height:20, fill:'#CCCCCC'}); Insert(g, {type:'rectangle', name:'R2', width:101, height:20, fill:'#CCCCCC'});")
	if strings.Contains(flow.Text, "nearly align") {
		t.Fatal(flow.Text)
	}
	far := a.run(h, "w = Insert(document, {type:'frame', name:'Wide', layout:'none', width:2000, height:1200, fill:'#FFFFFF'}); Insert(w, {type:'rectangle', name:'West', x:0, y:800, width:100, height:40, fill:'#CCCCCC'}); Insert(w, {type:'rectangle', name:'East', x:1800, y:801.5, width:100, height:40, fill:'#CCCCCC'}); Insert(w, {type:'rectangle', name:'Col1', x:0, y:200, width:100, height:40, fill:'#CCCCCC'}); Insert(w, {type:'rectangle', name:'Col2', x:1.5, y:260, width:100, height:40, fill:'#CCCCCC'}); Insert(w, {type:'rectangle', name:'Col3', x:1.5, y:320, width:100, height:40, fill:'#CCCCCC'});")
	if lines(far.Text, `"East"`, "nearly align") != 0 || lines(far.Text, `"Col1"`, "nearly align") != 1 {
		t.Fatal(far.Text)
	}
	flips := a.run(h, "m = Insert(document, {type:'frame', name:'Mirror', layout:'none', width:400, height:300, fill:'#FFFFFF'}); Insert(m, {type:'rectangle', name:'Plain', x:100, y:0, width:80, height:40, fill:'#CCCCCC'}); Insert(m, {type:'rectangle', name:'Flipped', x:101.5, y:60, width:80, height:40, flipX:true, fill:'#CCCCCC'}); Insert(m, {type:'rectangle', name:'Three off', x:103, y:120, width:80, height:40, fill:'#CCCCCC'});")
	if lines(flips.Text, `"Flipped"`, "nearly align") > 0 || lines(flips.Text, `"Three off"`, "3.0px apart") == 0 {
		t.Fatal(flips.Text)
	}
	next := a.run(h, "Insert(f, {type:'rectangle', name:'Later', x:400, y:360, width:20, height:20, fill:'#CCCCCC'})")
	if strings.Contains(next.Text, `"Slipped"`) {
		t.Fatal(next.Text)
	}
}

func TestDeadlineStopsEndlessLoop(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	start := time.Now()
	// The interrupt is uncatchable, so a try/catch around the loop cannot swallow it.
	out := a.call(h, "agent", input("Insert(document, {type:'frame', name:'X'})\nwhile (true) { try { while (true) {} } catch (err) {} }"), 300*time.Millisecond)
	if !out.Error || !strings.Contains(out.Text, "Error: the snippet ran past the 0.3 s time limit and was stopped (endless loop?)\n") || !out.Limit {
		t.Fatal(out.Text)
	}
	if time.Since(start) > 5*time.Second || len(h.doc.Children()) != 0 {
		t.Fatal("stopped in time and rolled back")
	}
	if next := a.run(h, "Insert(document, {type:'frame', name:'Y'})"); next.Error {
		t.Fatal(next.Text)
	}
}

func TestTimeLimitText(t *testing.T) {
	for budget, want := range map[time.Duration]string{script.ExecuteTimeout: "30.0", script.PreviewTimeout: "2.0", 250 * time.Millisecond: "0.2", 350 * time.Millisecond: "0.3"} {
		if got := script.TimeLimitText(budget); got != "the snippet ran past the "+want+" s time limit and was stopped (endless loop?)" {
			t.Errorf("%v: %s", budget, got)
		}
	}
}

func TestPromiseJobsRunAndRejectionsFailTheCall(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	ok := a.run(h, "(async () => { Insert(document, {type:'frame', name:'Before'}); await null; Insert(document, {type:'frame', name:'After'}) })();\nPromise.resolve().then(() => Insert(document, {type:'frame', name:'Then'}))")
	if ok.Error || !slices.Equal(names(h), []string{"Before", "After", "Then"}) {
		t.Fatal(ok.Text, names(h))
	}
	for _, src := range []string{
		"(async () => { Insert(document, {type:'frame', name:'Late'}); await null; throw new Error('late failure') })()",
		"Insert(document, {type:'frame', name:'Late'}); Promise.reject(new Error('late failure'))",
	} {
		r := a.run(h, src)
		if !r.Error || !strings.Contains(r.Text, "Error: Error: late failure (line 1)\n") || !strings.Contains(r.Text, "editId") || len(h.doc.Children()) != 3 {
			t.Fatal(r.Text)
		}
	}
	for _, src := range []string{"Promise.reject()", "(async () => { await null; throw undefined })()"} {
		if r := a.run(h, src); !r.Error || !strings.Contains(r.Text, "Error: Uncaught undefined\n") {
			t.Fatal(r.Text)
		}
	}
	if handled := a.run(h, "const p = Promise.reject(new Error('x')); p.catch(() => Print('caught'))"); handled.Error || !strings.Contains(handled.Text, "caught") {
		t.Fatal(handled.Text)
	}
	spin := a.call(h, "agent", input("const f = () => { Promise.resolve().then(f) }; f()"), 300*time.Millisecond)
	if !spin.Error || !strings.Contains(spin.Text, "time limit") {
		t.Fatal(spin.Text)
	}
	grow := a.run(h, "Insert(document, {type:'frame', name:'Late'}); const f = () => Promise.resolve().then(f); f()")
	if !grow.Error || !strings.Contains(grow.Text, "Error: InternalError: out of memory: the snippet used more than 64 MB\n") || !grow.Limit || len(h.doc.Children()) != 3 {
		t.Fatal(grow.Text)
	}
}

func TestMemoryAndStackLimits(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	oom := a.run(h, "Insert(document, {type:'frame'})\nconst a = []\nfor (;;) a.push(new Array(100000).fill(1.5))")
	if !oom.Error || !strings.Contains(oom.Text, "out of memory") || !strings.Contains(oom.Text, ": the snippet used more than 64 MB\n") || len(h.doc.Children()) != 0 {
		t.Fatal(oom.Text)
	}
	deep := a.run(h, "function f(n) { return f(n + 1) + 1 }\nf(0)")
	if !deep.Error || !strings.Contains(deep.Text, "Error: RangeError: Maximum call stack size exceeded (line 1)\n") {
		t.Fatal(deep.Text)
	}
	if next := a.run(h, "Insert(document, {type:'frame'})"); next.Error {
		t.Fatal(next.Text)
	}
}

func TestStopInterruptsTheScript(t *testing.T) {
	h := emptyHost(t)
	stop := make(chan struct{})
	time.AfterFunc(200*time.Millisecond, func() { close(stop) })
	start := time.Now()
	out := script.Execute(h, input("Insert(document, {type:'frame'})\nfor (;;) {}"), script.Call{Stop: stop})
	if !out.Error || !strings.HasPrefix(out.Text, "Error: the agent was stopped, so the snippet was interrupted\n") || !out.Limit {
		t.Fatal(out.Text)
	}
	if time.Since(start) > 5*time.Second || len(h.doc.Children()) != 0 {
		t.Fatal("stopped and rolled back")
	}
	// A Stop that comes after the call does no harm.
	stop2 := make(chan struct{})
	if out := script.Execute(h, input("Insert(document, {type:'frame'})"), script.Call{Stop: stop2}); out.Error {
		t.Fatal(out.Text)
	}
	close(stop2)
}

func TestPanicInANativeRollsBack(t *testing.T) {
	script.EnablePanicNative()
	h, a := emptyHost(t), newAgents()
	for _, src := range []string{
		"Insert(document, {type:'frame', name:'X'})\n__PANIC()",
		"Insert(document, {type:'frame', name:'X'})\ntry { __PANIC() } catch (e) { Print('swallowed') }",
		"Insert(document, {type:'frame', name:'X'})\nPromise.resolve().then(() => __PANIC())",
	} {
		out := a.run(h, src)
		if !out.Error || !strings.HasPrefix(out.Text, "Error: internal error (panic) while running the script\n") || !strings.Contains(out.Text, "editId") {
			t.Fatal(src, out.Text)
		}
		if len(h.doc.Children()) != 0 {
			t.Fatal("rolled back")
		}
	}
	if next := a.run(h, "Insert(document, {type:'frame'})"); next.Error {
		t.Fatal(next.Text)
	}
}

// TestEveryPreludeCall is the matrix's edits row: every prelude call on one document.
func TestEveryPreludeCall(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	out := a.run(h, `
page = Insert(document, {type: "frame", name: "Page", width: 400, height: 300, layout: "none", fill: "#FFFFFF"});
const box = Insert(page, {type: "rectangle", name: "Box", x: 10, y: 10, width: 50, height: 50, fill: "#FF0000"});
const copy = Copy(box, page, {name: "Copy", x: 100, y: 10});
Update(copy, {width: 80, opacity: 0.5});
const swapped = Replace(box, {type: "ellipse", name: "Round", x: 10, y: 10, width: 50, height: 50, fill: "#00FF00"});
const extra = Insert(page, {type: "rectangle", width: 5, height: 5});
Move(extra, document, 0);
Delete(extra);
SetVariables({accent: {type: "color", value: "#336699"}});
Update(swapped, {fill: "$accent"});
Print(GetVariables().variables.accent.value, Get(copy).width, Get(page, {depth: 0}).children);
Print(FindEmptySpace({width: 100, height: 100}));
TakeScreenshot(page);
Export([page, "document"], "png", "out.png", {scale: 2});
Generate("ai", copy, "a red box");
`)
	if out.Error {
		t.Fatal(out.Text)
	}
	ids := regexp.MustCompile(`"(\w+)": "(\w+)"`).FindAllStringSubmatch(out.Text, -1)
	if len(ids) != 4 || ids[0][1] != "Page" || ids[1][1] != "Box" || ids[2][1] != "Copy" || ids[3][1] != "Round" {
		t.Fatal(out.Text)
	}
	if !strings.Contains(out.Text, "\n## Print output\n#336699 80 ...\n{\"x\":400,\"y\":0}\n") {
		t.Fatal(out.Text)
	}
	if !strings.HasPrefix(out.Text, "OK\nNote: Generate(\"ai\") on "+ids[2][2]+": the image is loading in the background") {
		t.Fatal(out.Text)
	}
	if len(h.generated) != 1 || !strings.HasPrefix(h.generated[0][3], "nalarupa:pending-image-") || h.generated[0][2] != "a red box" {
		t.Fatal(h.generated)
	}
	if !slices.Equal(out.Shots, []string{ids[0][2]}) || len(out.Exports) != 1 || out.Exports[0].Format != "png" || !slices.Equal(out.Exports[0].IDs, []string{ids[0][2], "document"}) {
		t.Fatal(out.Shots, out.Exports)
	}
	if !slices.Equal(out.Read, []string{ids[2][2], ids[0][2], ids[0][2]}) {
		t.Fatal(out.Read)
	}
	if len(h.slid) != 2 || h.version != 11 {
		t.Fatal(h.slid, h.version)
	}
	kids := jsonv.Items(jsonv.Get(h.doc.Children()[0], "children"))
	if len(kids) != 2 || !jsonv.StrIs(jsonv.Get(kids[0], "type"), "ellipse") || !jsonv.StrIs(jsonv.Get(kids[0], "fill"), "$accent") {
		t.Fatal(string(jsonv.Marshal(h.doc.Root)))
	}
	if f := jsonv.Get(kids[1], "fill"); !jsonv.StrIs(jsonv.Get(f, "type"), "image") {
		t.Fatal(string(jsonv.Marshal(kids[1])))
	}
	if g := a.run(h, `Generate("svg", page, "a tree"); Print(Get(page).placeholder)`); g.Error || !strings.Contains(g.Text, "the AI Pen draws the artwork") || !strings.Contains(g.Text, "\ntrue\n") {
		t.Fatal(g.Text)
	}
	for src, want := range map[string]string{
		`Generate("svg", "nope", "x")`:          `Generate("svg") draws into a frame: Insert a frame with placeholder: true first and pass its id (got 'nope')`,
		`Generate("video", page, "x")`:          "Generate type 'video' is not available in Nalarupa; use fills, icons and paths instead",
		`Insert(document, () => 1)`:             "Error: invalid argument: expected value at line 1 column 1 (line 1)",
		`Get(document)`:                         "Error: Reading the whole document without a visitor is not allowed; pass a node id or a visitor function (line ",
		`Get("nope", {resolveInstances: true})`: "Error: node not found: nope (line 1)",
	} {
		if r := a.run(h, src); !r.Error || !strings.Contains(r.Text, want) {
			t.Errorf("%s: %s", src, r.Text)
		}
	}
}

// TestGetVisitor is the matrix's Get row: a path, options and a visitor with its context.
func TestGetVisitor(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	out := a.run(h, `
page = Insert(document, {type: "frame", name: "Page", width: 300, height: 200, layout: "none", x: 20, y: 30});
row = Insert(page, {type: "frame", name: "Row", x: 10, y: 20, width: 100, height: 50, layout: "none"});
Insert(row, {type: "rectangle", name: "Big", x: 50, y: 0, width: 100, height: 20});
Insert(row, {type: "rectangle", name: "Tiny", x: 1, y: 2, width: 3, height: 4});
Insert(page, {type: "path", name: "Line", geometry: "M0 0L10 10", width: 10, height: 10});
Print(Get(n => n.name + "@" + n.type));
Print(Get(page, (n, c) => [n.name, c.depth, c.index, c.parentCtx ? c.parentCtx.node.name : null, c.bounds, c.problems || null]));
Print(Get(page, (n, c) => { if (n.name === "Row") c.skipChildren(); return n.name }));
Print(Get(row, {depth: 0}).children, Get(page).children[1].geometry, Get(page, {includePathGeometry: true}).children[1].geometry);
`)
	if out.Error {
		t.Fatal(out.Text)
	}
	_, prints, _ := strings.Cut(out.Text, "## Print output\n")
	want := `["Page@frame","Row@frame","Big@rectangle","Tiny@rectangle","Line@path"]
[["Page",0,0,null,{"x":20,"y":30,"width":300,"height":200},null],["Row",1,0,"Page",{"x":10,"y":20,"width":100,"height":50},null],["Big",2,0,"Row",{"x":50,"y":0,"width":100,"height":20},"partially clipped"],["Tiny",2,1,"Row",{"x":1,"y":2,"width":3,"height":4},null],["Line",1,1,"Page",{"x":0,"y":0,"width":10,"height":10},null]]
["Page","Row","Line"]
... ... M0 0L10 10
`
	if !strings.HasPrefix(prints, want) {
		t.Fatalf("got\n%s\nwant\n%s", prints, want)
	}
}

// TestErrors is the matrix's errors row.
func TestErrors(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	base := a.run(h, `page = Insert(document, {type: "frame", name: "Page", width: 10, height: 10})`)
	if base.Error {
		t.Fatal(base.Text)
	}
	saved := string(jsonv.Marshal(h.doc.Root))
	for src, want := range map[string]string{
		"Insert(page, {type: 'rectangle'})\n\nthrow new TypeError('bad ' + 1)":             "TypeError: bad 1 (line 3)",
		"Insert(page, {type: 'rectangle'})\nthrow {toString() { return 'thing' }}":         "Uncaught thing",
		"Insert(page, {type: 'rectangle'})\nthrow 42":                                      "Uncaught 42",
		"Insert(page, {type: 'rectangle'})\nlet x = ;":                                     "SyntaxError: unexpected token in expression: ';' (line 2)",
		"Insert(page, {type: 'rectangle'})\nPromise.reject('no')":                          "Uncaught no",
		"Insert(page, {type: 'rectangle'})\nInsert(page, {name: 'typeless'})":              "Error: node needs a string `type` (line 2)",
		"Insert(page, {type: 'rectangle'})\nUpdate(page, 5)":                               "Error: update data must be an object (line 2)",
		"Insert(page, {type: 'rectangle'})\nnull.x":                                        "TypeError: cannot read property 'x' of null (line 2)",
		"Insert(page, {type: 'rectangle'})\nconst e = new Error('m'); e.name = 7; throw e": "Error: m (line 2)",
		"x = 1\x00": "String contained internal null bytes: nul byte found in provided data at position: 5",
	} {
		r := a.run(h, src)
		wantText := script.RollbackText(want, r.EditID)
		if !r.Error || r.Text != wantText {
			t.Errorf("%q:\n%s\nwant\n%s", src, r.Text, wantText)
		}
		if got := string(jsonv.Marshal(h.doc.Root)); got != saved {
			t.Errorf("%q: not rolled back", src)
		}
	}
}

// TestSeedsGiveRustsIDs is the matrix's seeds row: the same seed gives the same ids, different seeds different
// ones, and the ids a preview drew are found again by the resume probe.
func TestSeedsGiveRustsIDs(t *testing.T) {
	src := "a = Insert(document, {type:'frame', name:'A', children:[{type:'rectangle'}]})\nb = Copy(a, document)"
	ids := func(seed uint64) []string {
		h := emptyHost(t)
		args := input(src)
		args.(*jsonv.Object).Set("__seed", jsonv.Int(int64(seed)))
		if out := script.Execute(h, args, script.Call{}); out.Error {
			t.Fatal(out.Text)
		}
		return h.doc.AllIDs()
	}
	one, two := ids(1), ids(2)
	if !slices.Equal(one, ids(1)) || slices.Equal(one, two) || len(one) != 4 {
		t.Fatal(one, two)
	}
	// Seed 1 draws xorshift(1), ...: doc.Reseed(1) in Rust gives the same ids (testdata/ids.json).
	ref := doc.NewIDSource(1)
	first := ""
	for _, r := range []uint64{ref.Next()} {
		const abc = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
		for range 5 {
			first += string(abc[r%62])
			r /= 62
		}
	}
	if one[0] != first {
		t.Fatal(one[0], first)
	}

	// A preview step draws ids; the probe finds how many, and resuming there continues the sequence.
	h := emptyHost(t)
	h.ids.Reseed(9)
	out := script.Execute(h, input(src), script.Call{Budget: script.PreviewTimeout, Preview: true})
	if out.Error {
		t.Fatal(out.Text)
	}
	drawn, ok := h.ids.Drawn(9, 0)
	if !ok || drawn != 4 {
		t.Fatal(drawn, ok)
	}
	fresh := doc.NewIDSource(9)
	for range drawn {
		fresh.Next()
	}
	h.ids.Resume(9, drawn)
	if h.ids.Next() != fresh.Next() {
		t.Fatal("resume continues where the preview stopped")
	}
}

// TestParallelRunsKeepTheirIDs: runs in parallel with different seeds make the ids a serial run makes.
func TestParallelRunsKeepTheirIDs(t *testing.T) {
	src := "p = Insert(document, {type:'frame', name:'P', layout:'none', width:100, height:100})\nfor (let i = 0; i < 50; i++) Insert(p, {type:'rectangle', name:'r'+i, x:i, y:0, width:1, height:1})\nCopy(p, document)"
	run := func(seed uint64, own bool) string {
		d, _ := doc.FromJSON([]byte(`{"version":"2.19","children":[]}`))
		h := newHost(d)
		if own {
			h.fonts, h.assets = newOffline()
		}
		args := input(src)
		args.(*jsonv.Object).Set("__seed", jsonv.Int(int64(seed)))
		if out := script.Execute(h, args, script.Call{}); out.Error {
			return out.Text
		}
		return strings.Join(h.doc.AllIDs(), ",")
	}
	seeds := []uint64{1, 2, 3, 4}
	serial := map[uint64]string{}
	for _, s := range seeds {
		serial[s] = run(s, false)
	}
	var wg sync.WaitGroup
	got := make([]string, len(seeds))
	for i, s := range seeds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i] = run(s, true)
		}()
	}
	wg.Wait()
	for i, s := range seeds {
		if got[i] != serial[s] {
			t.Errorf("seed %d: %s, serial %s", s, got[i], serial[s])
		}
	}
	if serial[1] == serial[2] {
		t.Fatal("different seeds, different ids")
	}
}

func TestNumbersAreKeptAsSerdeKeepsThem(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	if out := a.run(h, "Insert(document, {type:'frame', x: 1e21, y: 0.1 + 0.2, width: 10, height: 2**64, opacity: 1.0, big: 2**70})"); out.Error {
		t.Fatal(out.Text)
	}
	got := string(jsonv.Marshal(h.doc.Children()[0]))
	if !strings.Contains(got, `"x":1e+21,"y":0.30000000000000004,"width":10,"height":1.8446744073709552e+19,"opacity":1,"big":1.1805916207174112e+21`) {
		t.Fatal(got)
	}
}

// bareHost is a host whose document has no generator of its own: Execute still draws the call's ids from IDs().
type bareHost struct{ *docHost }

func TestExecuteDrawsFromTheHostsIDs(t *testing.T) {
	ids := func(seed uint64) []string {
		h := bareHost{emptyHost(t)}
		h.doc.IDs = nil
		args := input("Insert(document, {type:'frame', children:[{type:'rectangle'}]})")
		args.(*jsonv.Object).Set("__seed", jsonv.Int(int64(seed)))
		if out := script.Execute(h, args, script.Call{}); out.Error {
			t.Fatal(out.Text)
		}
		if h.doc.IDs != nil {
			t.Fatal("the document's generator is restored")
		}
		return h.doc.AllIDs()
	}
	if a, b := ids(1), ids(1); !slices.Equal(a, b) || slices.Equal(a, ids(2)) {
		t.Fatal(a, b)
	}
}

func TestBadArgumentsReadLikeSerde(t *testing.T) {
	h, a := emptyHost(t), newAgents()
	for src, want := range map[string]string{
		"__I(document, 'undefined')": "Error: invalid argument: expected value at line 1 column 1 (line 1)",
		"__I(document, '  ')":        "Error: invalid argument: EOF while parsing a value at line 1 column 2 (line 1)",
		`__I(document, '"x')`:        "Error: invalid argument: ",
	} {
		if r := a.run(h, src); !r.Error || !strings.Contains(r.Text, want) || strings.Contains(r.Text, "expected value") != strings.Contains(want, "expected value") {
			t.Errorf("%s: %s", src, r.Text)
		}
	}
}

// Call.Ran comes once the JavaScript has run, before the report validates what the call touched.
func TestRanComesBeforeTheReport(t *testing.T) {
	h := emptyHost(t)
	ran, laid, early := false, 0, false
	h.laid = func() {
		laid++
		early = early || !ran
	}
	out := script.Execute(h, input(`Insert(document, {type: "frame", width: 10, height: 10})`), script.Call{Ran: func() { ran = true }})
	if out.Error || !ran || laid == 0 || early {
		t.Fatal(out.Text, ran, laid, early)
	}
}
