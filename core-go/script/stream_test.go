// The stream.rs tests, tables of statement boundaries and partial JSON, and parity with Rust's `complete_prefix`
// and `partial_string` over a generated corpus: every char-boundary prefix of a set of samples, plus seeded random
// token soups. Without NALARUPA_CLI the corpus is compared with the committed results (testdata/stream/golden.json);
// with it, with `nalarupa-cli stream-prefix`. NALARUPA_UPDATE_GOLDEN=1 (with NALARUPA_CLI) rewrites them.
package script_test

import (
	"encoding/json"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/script"
)

func TestPartialJSONStrings(t *testing.T) {
	for _, c := range []struct {
		json, want string
		ok         bool
	}{
		{`{"input": "a = 1;\nb = \"x`, "a = 1;\nb = \"x", true},
		{`{"input":"done"}`, "done", true},
		{`{"input":"trail\`, "trail", true},
		{`{"input":"é\u00`, "é", true},
		{`{"editId":"e1"}`, "", false},
		// More of the same rules.
		{`{"input" :  "tab\there"`, "tab\there", true},
		{`{"input":5}`, "", false},
		{`{"input"`, "", false},
		{`{"input": `, "", false},
		{`{"input": "\u00e9\ud83d\ude00!"}`, "é😀!", true},
		{`{"input": "\ud83d\ude0`, "", true},
		{`{"input": "\ud83d`, "", true},
		{`{"input": "\ud83dxx\u0041"`, "xx\u0041", true},
		{`{"input": "\ud800\u0041"`, "\u2441", true},
		{`{"input": "\udc00ok"`, "ok", true},
		{`{"input": "\u+041!"`, "A!", true},
		{`{"input": "\uzzzz"`, "", true},
		{`{"input": "\/\\\q"`, "/\\q", true},
		{`{"editId": "e1", "input": "x"}`, "x", true},
	} {
		got, ok := script.PartialString(c.json, "input")
		if got != c.want || ok != c.ok {
			t.Errorf("%s: %q %v, want %q %v", c.json, got, ok, c.want, c.ok)
		}
	}
}

func TestStatementBoundaries(t *testing.T) {
	s := "a = Insert(document, {\n  type: \"frame\"\n})\nb = Insert(a, {type: \"te"
	if got := s[:script.CompletePrefix(s)]; got != "a = Insert(document, {\n  type: \"frame\"\n})\n" {
		t.Error(got)
	}
	tl := "x = `a ${f({y: 1})}\n` ; y = 2\n"
	if got := tl[:script.CompletePrefix(tl)]; got != "x = `a ${f({y: 1})}\n` ;" {
		t.Error(got)
	}
	for src, want := range map[string]string{
		"for (const k of [1,2]) {\n Insert(a, {})\n": "",
		"s = \"semi; colon\"\n// c; (\nz = 1":        "s = \"semi; colon\"\n// c; (\n",
		"a = [1]\n  .map(f)\nb = 2\n":                "a = [1]\n  .map(f)\n",
		"if (x) f()\nelse g()\n":                     "",
		"if (x) f(); else g()\nh()":                  "if (x) f(); else g()\n",
		"a = 1\nel":                                  "",
		// More of the same rules; `q(` ends the word before it, which may still be growing.
		"try { f() }\ncatch (e) {}\nx = 1\nq(": "try { f() }\ncatch (e) {}\nx = 1\n",
		"a = 1\n+ 2\nb = 3\nq(":                "a = 1\n+ 2\nb = 3\n",
		"a = b / c\nd = 1\nq(":                 "a = b / c\nd = 1\n",
		"a = 1 /* x; */\nb = 2\nq(":            "a = 1 /* x; */\nb = 2\n",
		"a = 1 /* open":                        "",
		"a = 1\n/* c */ b = 2\nq(":             "a = 1\n/* c */ b = 2\n",
		"a = `x ${`y ${z}`} w`\nq(":            "a = `x ${`y ${z}`} w`\n",
		"a = 'it\\'s'\nq(":                     "a = 'it\\'s'\n",
		"a = 'open\nb = 2\nq(":                 "a = 'open\nb = 2\n",
		"$x = 1\n$y":                           "",
		"a = 1\n$y = 2\nq(":                    "a = 1\n$y = 2\n",
		"a = 1\né = 2\nq(":                     "a = 1\né = 2\n",
		"a = 1\n→":                             "a = 1\n",
		"a = 1\r\nb = 2\r\nq(":                 "a = 1\r\nb = 2\r\n",
		"a = 1\n\vb":                           "a = 1\n",
		"a = 1;":                               "",
		"a = 1; b":                             "",
		"a = 1; b(":                            "a = 1;",
		"f(\n1\n)\nq(":                         "f(\n1\n)\n",
		"x = {a: [1, 2]}\n.y":                  "",
		"}\na = 1\nq(":                         "",
		"a = 1\nfinally_x = 2\nq(":             "a = 1\nfinally_x = 2\n",
	} {
		if got := src[:script.CompletePrefix(src)]; got != want {
			t.Errorf("%q: %q, want %q", src, got, want)
		}
	}
}

// streamCase is one case of the oracle: {src} for complete_prefix or {json, key} for partial_string.
type streamCase struct {
	Src  *string `json:"src,omitempty"`
	JSON *string `json:"json,omitempty"`
	Key  *string `json:"key,omitempty"`
}

// streamResult is the oracle's answer to a case.
type streamResult struct {
	Prefix  *int    `json:"prefix,omitempty"`
	Partial *string `json:"partial"`
}

// prefixes is every char-boundary prefix of s (a Rust &str can only be cut there), s itself included.
func prefixes(s string) []string {
	var out []string
	for i := range s {
		out = append(out, s[:i])
	}
	return append(out, s)
}

var prefixSamples = []string{
	"a = Insert(document, {\n  type: \"frame\", name: \"A; b\"\n})\nb = Insert(a, {type: \"text\", content: 'it\\'s'})\n",
	"x = `a ${f({y: `in ${z} }`})}\n` ; y = 2\nif (x) f()\nelse g()\ntry { h() } catch (e) {} finally { k() }\n",
	"// lead; (\n/* block; ) */ a = [1]\n  .map(f)\n  .filter(Boolean)\nb = a / 2 / 3\nc = /re;g/.test(s)\n",
	"for (const k of [1,2]) {\n Insert(a, {})\n}\nconst s = 'open\nz = 1 + \n 2\n$v = $w\n_u = 1\n",
	"card = Insert(page, {name: \"Caf\u00e9\", content: \"\u2192 go\"})\n\u00e9t\u00e9 = 1\nUpdate(card, {x: 1})\r\nDelete(card)\r\n",
	"a = 1;b = 2;\tc = 3\f\nd = 4\v\ne = 5\nlet q = x ? y : z\nq\n? 1 : 2\n",
}

var partialSamples = []string{
	`{"input": "a = 1;\nb = \"x\" \\ \/ \t\r\b\f end", "editId": "e1"}`,
	`{"editId":"e2","input":"\u00e9\ud83d\ude00\u2192 \ud800\u0041 \udc00 \u+41 \uZZ"}`,
	"{\"input\" \t:\n \"caf\u00e9 \u2192 \U0001F600\"}",
	`{"inputs": "no", "input": "yes"}`,
	`{"input": 5, "x": "input"}`,
}

// tokens are the pieces the random soups are made of.
var tokens = []string{
	"a", "b1", "$x", "_y", "else", "catch", "finally", "el", "\u00e9", "\u2192", " ", "\t", "\n", "\r\n", "\v", "\f", ";",
	"(", ")", "[", "]", "{", "}", "\"", "'", "`", "${", "\\", "/", "//", "/*", "*/", "*", ".", ",", "?", ":", "+", "-", "=",
	"<", ">", "%", "&", "|", "^", "!", "1", "Insert(a, {x: 1})",
}

var jsonTokens = []string{"\\", "\"", "u", "d83d", "de00", "00e9", "+", "n", "t", "x", "\u00e9", " ", "\\u", "\\ud8", "0", "f"}

func soup(r *rand.Rand, from []string, n int) string {
	var b strings.Builder
	for range n {
		b.WriteString(from[r.IntN(len(from))])
	}
	return b.String()
}

// streamCorpus is the generated corpus, the same on every run.
func streamCorpus() []streamCase {
	var cases []streamCase
	src := func(s string) { cases = append(cases, streamCase{Src: &s}) }
	partial := func(j, k string) { cases = append(cases, streamCase{JSON: &j, Key: &k}) }
	for _, s := range prefixSamples {
		for _, p := range prefixes(s) {
			src(p)
		}
	}
	for _, s := range partialSamples {
		for _, p := range prefixes(s) {
			partial(p, "input")
		}
	}
	partial(partialSamples[0], "editId")
	partial(partialSamples[0], "missing")
	r := rand.New(rand.NewPCG(8, 4))
	for range 1500 {
		src(soup(r, tokens, 1+r.IntN(40)))
	}
	for range 500 {
		partial(`{"input": "`+soup(r, jsonTokens, 1+r.IntN(12)), "input")
	}
	return cases
}

func goStream(c streamCase) streamResult {
	if c.Src != nil {
		n := script.CompletePrefix(*c.Src)
		return streamResult{Prefix: &n}
	}
	if s, ok := script.PartialString(*c.JSON, *c.Key); ok {
		return streamResult{Partial: &s}
	}
	return streamResult{}
}

func TestStreamParity(t *testing.T) {
	bin := os.Getenv("NALARUPA_CLI")
	update := os.Getenv("NALARUPA_UPDATE_GOLDEN") != ""
	if update && bin == "" {
		t.Fatal("NALARUPA_UPDATE_GOLDEN needs NALARUPA_CLI")
	}
	cases := streamCorpus()
	for _, c := range cases {
		for _, s := range []*string{c.Src, c.JSON} {
			if s != nil && !utf8.ValidString(*s) {
				t.Fatalf("not UTF-8: %q", *s)
			}
		}
	}
	golden := filepath.Join("..", "testdata", "stream", "golden.json")
	var want []streamResult
	if bin != "" {
		if err := os.WriteFile(".parity-live", nil, 0o666); err != nil {
			t.Fatal(err)
		}
		in := filepath.Join(t.TempDir(), "cases.json")
		b, err := json.Marshal(cases)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(in, b, 0o666); err != nil {
			t.Fatal(err)
		}
		out := cli(t, bin, "stream-prefix", in)
		if err := json.Unmarshal(out, &want); err != nil {
			t.Fatal(err)
		}
		if update {
			b, err := json.MarshalIndent(struct {
				Cases   []streamCase   `json:"cases"`
				Results []streamResult `json:"results"`
			}{cases, want}, "", " ")
			if err != nil {
				t.Fatal(err)
			}
			writeGolden(t, golden, b)
		}
	} else {
		b, err := os.ReadFile(golden)
		if err != nil {
			t.Fatal(err)
		}
		var g struct {
			Cases   []streamCase   `json:"cases"`
			Results []streamResult `json:"results"`
		}
		if err := json.Unmarshal(b, &g); err != nil {
			t.Fatal(err)
		}
		gc, _ := json.Marshal(g.Cases)
		cc, _ := json.Marshal(cases)
		if string(gc) != string(cc) {
			t.Fatal("the golden corpus is not the generated one: rewrite it with NALARUPA_UPDATE_GOLDEN")
		}
		want = g.Results
	}
	if len(want) != len(cases) {
		t.Fatalf("%d results, want %d", len(want), len(cases))
	}
	bad := 0
	for i, c := range cases {
		got, _ := json.Marshal(goStream(c))
		w, _ := json.Marshal(want[i])
		if string(got) != string(w) {
			bad++
			if bad <= 20 {
				in, _ := json.Marshal(c)
				t.Errorf("%s: %s, want %s", in, got, w)
			}
		}
	}
	if bad > 0 {
		t.Errorf("%d of %d cases differ", bad, len(cases))
	}
}

// A panic in a native breaks the stream (the caller rebuilds) instead of unwinding past it; what it wrote before
// panicking still counts (panic_in_streamed_host_call_breaks_the_stream_and_recovers).
func TestAPanicInAStreamedNativeBreaksTheStream(t *testing.T) {
	script.EnablePanicNative()
	h := emptyHost(t)
	lives := map[string]*script.Live{}
	a := "Insert(document, {type:'frame', name:'A', width:10, height:10})\n"
	if touched, ok := script.StreamStep(h, lives, "k", nil, a, 7); !ok || len(touched) != 1 {
		t.Fatal(touched, ok)
	}
	touched, ok := script.StreamStep(h, lives, "k", nil, a+"Insert(document, {type:'frame', name:'B', width:10, height:10}); __PANIC()\n", 7)
	if !ok || len(touched) != 2 || !lives["k"].Broken() || len(h.doc.Children()) != 2 {
		t.Fatal(touched, ok)
	}
	// Broken: the next update asks for a rebuild and frees the context.
	if _, ok := script.StreamStep(h, lives, "k", nil, a+"x = 1\n", 7); ok || len(lives) != 0 {
		t.Fatal("rebuild")
	}
}

// A stream runs only what it has not run, resumes the seeded ids where it stopped, refuses a non-append and keeps
// a whitespace-only chunk for later.
func TestStreamStepRunsEachPieceOnce(t *testing.T) {
	h := emptyHost(t)
	lives := map[string]*script.Live{}
	defer func() {
		for _, l := range lives {
			l.Close()
		}
	}()
	one := "n = 0\nInsert(document, {type:'frame', name:'A', width:10, height:10}); n++\n"
	two := one + "Insert(document, {type:'frame', name:'B', width:10, height:10}); n++\n"
	script.StreamStep(h, lives, "k", nil, one, 3)
	// Other code draws ids between steps.
	h.ids.Next()
	h.ids.Next()
	script.StreamStep(h, lives, "k", nil, two+"  \n", 3)
	l := lives["k"]
	if l.Global("n") != "2" || l.Ran() != two+"  \n" || len(h.doc.Children()) != 2 {
		t.Fatal(l.Global("n"), l.Ran())
	}
	// The same ids as one run of the whole source with the seed.
	cold := emptyHost(t)
	args := input(two).(*jsonv.Object)
	args.Set("__seed", jsonv.Int(3))
	if out := script.Execute(cold, args, script.Call{}); out.Error {
		t.Fatal(out.Text)
	}
	if !jsonv.Equal(h.doc.Root, cold.doc.Root) {
		t.Fatalf("%s\n%s", jsonv.Marshal(h.doc.Root), jsonv.Marshal(cold.doc.Root))
	}
	if _, ok := script.StreamStep(h, lives, "k", nil, "m = 1\n", 3); ok || len(lives) != 0 {
		t.Fatal("not an append")
	}
	// A NUL byte breaks the stream, as rquickjs refuses it.
	script.StreamStep(h, lives, "z", nil, "a = 1\x00\n", 3)
	if !lives["z"].Broken() {
		t.Fatal("NUL")
	}
}

// Generate in a preview is skipped (no download, no pending fill), whatever the type; the agent's globals are there.
func TestGenerateIsSkippedInAPreview(t *testing.T) {
	h := emptyHost(t)
	lives := map[string]*script.Live{}
	g := jsonv.NewObject()
	g.Set("prompt", "a cat")
	touched, ok := script.StreamStep(h, lives, "k", g, "f = Insert(document, {type:'frame', name:'G', width:10, height:10})\nGenerate('ai', f, prompt)\nGenerate('svg', f, prompt)\nGenerate('nope', f, prompt)\n", 1)
	if !ok || len(touched) != 1 || lives["k"].Broken() || len(h.generated) != 0 || h.version != 1 {
		t.Fatal(touched, ok, h.generated, h.version)
	}
	lives["k"].Close()
}
