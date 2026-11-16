package figma

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"math"
	"math/bits"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

func js(v jsonv.Value) string { return string(jsonv.Marshal(v)) }

// pick is the object of keys from n, null where n lacks one.
func pick(n jsonv.Value, keys ...string) string {
	o := jsonv.NewObject()
	for _, k := range keys {
		o.Set(k, get(n, k))
	}
	return js(o)
}

func TestKiwiPrimitives(t *testing.T) {
	// 300 as varuint, -3 as varint, 1.5 as varfloat (exponent rotated), "hi".
	d := []byte{0xac, 0x02, 0x05}
	d = binary.LittleEndian.AppendUint32(d, bits.RotateLeft32(math.Float32bits(1.5), -23))
	d = append(d, "hi\x00"...)
	bb := &reader{d: d}
	u, _ := bb.uint()
	i, _ := bb.int()
	f, _ := bb.float()
	s, _ := bb.string()
	if u != 300 || i != -3 || f != 1.5 || s != "hi" {
		t.Fatalf("got %v %v %v %q", u, i, f, s)
	}
	if _, err := bb.byte(); err != errEOF {
		t.Fatalf("read past the end: %v", err)
	}
}

func (o *enc) uint64(v uint64) {
	for i := 0; i < 8 && v >= 128; i++ {
		o.byte(byte(v&127) | 128)
		v >>= 7
	}
	o.byte(byte(v))
}

// Every primitive, structs, nested messages, arrays, byte arrays and an enum value the schema lacks.
func TestKiwiMessage(t *testing.T) {
	s := tschema{
		enumDef("E", "A", "B"),
		{"S", 1, fields("x:int y:float")},
		{"Inner", 2, fields("s:string")},
		{"Message", 2, fields("b:bool by:byte i:int u:uint f:float s:string i64:int64 u64:uint64 e:E e2:E st:S in:Inner arr:uint[] bytes:byte[] ins:Inner[]")},
	}
	defs, err := readSchema(s.encode())
	if err != nil {
		t.Fatal(err)
	}
	o := &enc{}
	field := func(id uint32) { o.uint(id) }
	field(1)
	o.byte(1)
	field(2)
	o.byte(200)
	field(3)
	o.int(-3)
	field(4)
	o.uint(300)
	field(5)
	o.float(1.5)
	field(6)
	o.b = append(o.b, 'h', 0xe2, 0x82, 'i', 0)
	field(7)
	o.uint64(9) // -5 zigzagged
	field(8)
	o.uint64(1<<63 + 5)
	field(9)
	o.uint(1)
	field(10)
	o.uint(7)
	field(11)
	o.int(-1)
	o.float(0)
	field(12)
	field(1)
	o.string("x")
	o.uint(0)
	field(13)
	o.uint(2)
	o.uint(1)
	o.uint(128)
	field(14)
	o.uint(2)
	o.b = append(o.b, 0x00, 0xab)
	field(15)
	o.uint(1)
	o.uint(0)
	o.uint(0)
	msg, err := defs.decode(&reader{d: o.b}, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"b":true,"by":200,"i":-3,"u":300,"f":1.5,"s":"h` + "�" + `i","i64":-5,"u64":9223372036854775813,"e":"B","e2":null,` +
		`"st":{"x":-1,"y":0.0},"in":{"s":"x"},"arr":[1,128],"bytes":"00ab","ins":[{}]}`
	if got := js(msg); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	// An array longer than what is left, an unknown field id, and a cut message.
	for _, c := range []struct {
		data []byte
		err  string
	}{
		{[]byte{13, 50, 1}, "truncated Figma clipboard data"},
		{[]byte{40}, "bad Figma message"},
		{[]byte{6, 'a'}, "truncated Figma clipboard data"},
		{[]byte{5, 0x80}, "truncated Figma clipboard data"},
	} {
		if _, err := defs.decode(&reader{d: c.data}, 3, 0); err == nil || err.Error() != c.err {
			t.Errorf("% x: got %v, want %q", c.data, err, c.err)
		}
	}
}

func TestSchemaErrors(t *testing.T) {
	for _, c := range []struct {
		s   tschema
		raw []byte
		err string
	}{
		{raw: []byte{1, 'M', 0, 3, 0}, err: "bad Figma schema kind 3"},
		{raw: []byte{1, 'M', 0, 2, 1, 'f', 0, 17, 0, 1}, err: "bad Figma schema type -9"}, // ~8
		{raw: []byte{1, 'M', 0, 2, 1, 'f', 0, 2, 0, 1}, err: "bad Figma schema type 1"},   // a def that is not there
		{raw: []byte{2, 'M', 0}, err: "truncated Figma clipboard data"},
	} {
		if _, err := readSchema(c.raw); err == nil || err.Error() != c.err {
			t.Errorf("% x: got %v, want %q", c.raw, err, c.err)
		}
	}
	// An enum used as the message has no field types.
	defs, err := readSchema(tschema{enumDef("Message", "A")}.encode())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := defs.decode(&reader{}, 0, 0); err == nil || err.Error() != "bad Figma schema" {
		t.Fatalf("got %v", err)
	}
}

func TestLossy(t *testing.T) {
	for in, want := range map[string]string{
		"ok":                   "ok",
		"a\xffb":               "a�b",
		"\xe2\x82":             "�",
		"\xe2\x82i":            "�i",
		"\xf0\x9f\x98":         "�",
		"\xf0\x9f":             "�",
		"\xed\xa0\x80":         "���", // a surrogate: three invalid bytes
		"\xc0\xaf":             "��",
		"\xef\xbf\xbd":         "�",
		"\xf4\x90\x80\x80":     "����",
		"\xe0\xa0\x80\xe0\x80": "ࠀ��",
	} {
		if got := lossy([]byte(in)); got != want {
			t.Errorf("lossy(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHexBytes(t *testing.T) {
	if got := hexBytes("00ab+fzzF"); !bytes.Equal(got, []byte{0x00, 0xab, 0x0f}) {
		t.Fatalf("got % x", got)
	}
	// Pairs that do not parse, or would split a character, are skipped.
	if got := hexBytes("éab"); !bytes.Equal(got, []byte{0xab}) {
		t.Fatalf("got % x", got)
	}
	if got := hexBytes("aé"); !bytes.Equal(got, []byte{}) {
		t.Fatalf("got % x", got)
	}
}

func checkMark(t *testing.T) []byte {
	t.Helper()
	return hexBytes(blob([][2]float32{{0, 5}, {4, 9}, {12, 0}}, [][6]float32{{1, 0, 0, 0, 0, 0}, {1, 0, 0, 2, 0, 0}}, nil))
}

func TestVectorNetwork(t *testing.T) {
	// A check mark: 3 vertices, 2 straight segments, no regions. The first segment runs backwards and
	// is flipped so the polyline is continuous.
	d := &doc{blobs: [][]byte{checkMark(t)}, budget: 1}
	p := jsonv.MustParse(`{"vectorData": {"vectorNetworkBlob": 0}}`)
	if path, rule, ok := d.vector(p); !ok || path != "M0 5 L4 9 L12 0" || rule != "nonzero" {
		t.Fatalf("got %q %q %v", path, rule, ok)
	}
	// Two regions (the last one's rule wins), a closed triangle with a curve, an empty loop, rounding.
	b := hexBytes(blob(
		[][2]float32{{0, 0}, {10, 0}, {5, 8}, {1.0 / 3, -0.5}},
		[][6]float32{{0, 0, 0, 1, 0, 0}, {1, 0, 2, 2, 1, 0}, {2, 0, 0, 0, 0, 0}, {3, 0, 0, 0, 0, 0}},
		[][]any{{uint32(1), [][]uint32{{0, 1, 2}}}, {uint32(0), [][]uint32{{}, {3}}}},
	))
	d = &doc{blobs: [][]byte{b}}
	path, rule, ok := d.vector(p)
	if want := "M0 0 L10 0 C10 2 6 8 5 8 L0 0 Z M0.33333 -0.5 L0 0"; !ok || path != want || rule != "evenodd" {
		t.Fatalf("got %q %q %v, want %q", path, rule, ok, want)
	}
	// A segment naming a missing vertex, a loop naming a missing segment, a short blob, no blob.
	for _, bad := range [][]byte{
		hexBytes(blob([][2]float32{{0, 0}}, [][6]float32{{0, 0, 0, 1, 0, 0}}, nil)),
		hexBytes(blob([][2]float32{{0, 0}}, nil, [][]any{{uint32(1), [][]uint32{{0}}}})),
		checkMark(t)[:30],
	} {
		if _, _, ok := (&doc{blobs: [][]byte{bad}}).vector(p); ok {
			t.Errorf("accepted % x", bad)
		}
	}
	if _, _, ok := (&doc{}).vector(p); ok {
		t.Error("accepted a missing blob")
	}
}

func TestClipboardCard(t *testing.T) {
	html, err := os.ReadFile(filepath.Join(fixtureDir, "card.html"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := Decode("<span>plain</span>"); ok || err != nil {
		t.Fatal("plain HTML decoded")
	}
	nodes, ok, err := Decode(string(html))
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	if len(nodes) != 1 {
		t.Fatalf("%d nodes: one top-level layer, the internal canvas and variables stay out", len(nodes))
	}
	card := nodes[0]
	if [4]float64{card.X, card.Y, card.W, card.H} != [4]float64{40, 60, 320, 230} {
		t.Fatalf("box %v", card)
	}
	eq := func(got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
	}
	c := card.Value
	eq(pick(c, "type", "name", "x", "y", "width", "height", "layout", "gap", "padding", "cornerRadius", "clip", "fill", "stroke", "strokeWidth", "strokeAlignment"),
		`{"type":"frame","name":"Card","x":40.0,"y":60.0,"width":320.0,"height":null,"layout":"vertical","gap":12.0,"padding":24.0,"cornerRadius":16.0,`+
			`"clip":true,"fill":"#ffffffff","stroke":"#e5e7ebff","strokeWidth":1.0,"strokeAlignment":"inner"}`)
	eq(js(get(c, "effect")), `{"type":"shadow","shadowType":"outer","color":"#0000001a","offset":{"x":0.0,"y":4.0},"blur":14.0}`)
	kids := jsonv.Items(get(c, "children"))
	var names []string
	for _, k := range kids {
		names = append(names, jsonv.StrOr(get(k, "name")))
	}
	eq(strings.Join(names, ","), "Title,Body,Row")
	eq(pick(kids[0], "content", "fontWeight", "fontSize", "textGrowth", "x"), `{"content":"Hello from Figma","fontWeight":"700","fontSize":20.0,"textGrowth":null,"x":null}`)
	eq(pick(kids[1], "textGrowth", "width", "lineHeight", "fontWeight"), `{"textGrowth":"fixed-width","width":"fill_container","lineHeight":1.5,"fontWeight":"normal"}`)
	row := kids[2]
	eq(pick(row, "width", "height", "layout", "gap", "alignItems", "clip"), `{"width":"fill_container","height":null,"layout":null,"gap":12.0,"alignItems":"center","clip":null}`)
	items := jsonv.Items(get(row, "children"))
	var types []string
	for _, k := range items {
		types = append(types, jsonv.StrOr(get(k, "type")))
	}
	eq(strings.Join(types, ","), "rectangle,frame,path,ellipse")
	eq(js(get(items[0], "fill", "gradientType")), `"linear"`)
	eq(js(get(items[0], "fill", "colors")), `[{"color":"#f97316ff","position":0.0},{"color":"#ec4899ff","position":1.0}]`)
	// The instance is expanded: its component's label with the text override, colored by the variable's
	// Dark mode that the card selects.
	button := items[1]
	eq(pick(button, "name", "padding", "justifyContent", "alignItems", "cornerRadius", "fill"),
		`{"name":"Button","padding":[10.0,16.0],"justifyContent":"center","alignItems":"center","cornerRadius":8.0,"fill":"#2563ebff"}`)
	eq(pick(at(get(button, "children"), 0), "name", "content", "fill", "fontWeight"), `{"name":"Label","content":"Buy now","fill":"#0f172aff","fontWeight":"600"}`)
	eq(pick(items[2], "geometry", "stroke", "strokeWidth", "strokeLinecap", "strokeLinejoin", "width", "height"),
		`{"geometry":"M0 6 L5 11 L16 0","stroke":"#16a34aff","strokeWidth":2.0,"strokeLinecap":"round","strokeLinejoin":"round","width":16.0,"height":11.0}`)
	eq(pick(items[3], "type", "fill", "width"), `{"type":"ellipse","fill":"#22c55eff","width":12.0}`)

	// The same content with a raw-deflate message in a "fig-jam." archive, its base64 broken into lines.
	other, err := os.ReadFile(filepath.Join(fixtureDir, "card-deflate.html"))
	if err != nil {
		t.Fatal(err)
	}
	again, ok, err := Decode(string(other))
	if !ok || err != nil || js(again[0].Value) != js(c) {
		t.Fatal("the deflate card differs", ok, err)
	}
}

func TestInflateErrorsAreShort(t *testing.T) {
	// Over the cap: the message must not carry the (huge) partial output.
	big := make([]byte, maxBytes+1)
	if _, err := inflate(deflate(big)); err == nil || err.Error() != tooBig {
		t.Fatalf("deflate over the cap: %v", err)
	}
	if _, err := inflate(zstdEnc(big)); err == nil || err.Error() != tooBig {
		t.Fatalf("zstd over the cap: %v", err)
	}
	if out, err := inflate(deflate(big[:maxBytes])); err != nil || len(out) != maxBytes {
		t.Fatalf("exactly the cap: %v", err)
	}
	// Corrupt and truncated streams get a plain description, not a byte dump.
	var short bytes.Buffer
	w, _ := flate.NewWriter(&short, 6)
	w.Write(bytes.Repeat([]byte{7}, 1000))
	w.Close()
	for _, c := range [][]byte{{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, short.Bytes()[:5], zstdEnc([]byte("ok"))[:8], {0x28, 0xb5, 0x2f, 0xfd, 0}} {
		if _, err := inflate(c); err == nil || len(err.Error()) >= 100 || !strings.HasPrefix(err.Error(), "bad Figma clipboard data: ") {
			t.Errorf("% x: %v", c, err)
		}
	}
	if out, err := inflate(deflate([]byte("ok"))); err != nil || string(out) != "ok" {
		t.Fatal(out, err)
	}
	if out, err := inflate(zstdEnc([]byte("ok"))); err != nil || string(out) != "ok" {
		t.Fatal(out, err)
	}
}

func TestSizingAndTransform(t *testing.T) {
	p := jsonv.MustParse(`{"stackMode": "VERTICAL", "stackPrimarySizing": "RESIZE_TO_FIT", "stackChildAlignSelf": "STRETCH"}`)
	// Hugs its own vertical stack; stretched across its parent's vertical stack.
	if sizing(p, "VERTICAL", true, false) != fit || sizing(p, "VERTICAL", true, true) != fill {
		t.Fatal("sizing")
	}
	if sizing(jsonv.NewObject(), "", false, true) != fixed {
		t.Fatal("default sizing")
	}
	if got := js(radius(jsonv.MustParse(`{"cornerRadius": 8}`))); got != "8.0" {
		t.Fatal(got)
	}
	if got := js(radius(jsonv.MustParse(`{"rectangleTopLeftCornerRadius": 4, "rectangleBottomRightCornerRadius": 2}`))); got != "[4.0,0.0,2.0,0.0]" {
		t.Fatal(got)
	}
	if got := hex(jsonv.MustParse(`{"r": 1, "g": 0.5, "b": 0, "a": 1}`), 0.5); got != "#ff800080" {
		t.Fatal(got)
	}
	if got := mix("#00000000", "#ffffffff", 0.5); got != "#80808080" {
		t.Fatal(got)
	}
	if got := mix("#00000000", "#ffffffff", 3); got != "#ffffffff" { // saturating
		t.Fatal(got)
	}
}

func TestLimitsAndGarbage(t *testing.T) {
	for _, c := range []struct{ html, err string }{
		{deepClipboard(), "Figma data nested too deep"},
		{clipboard("ZmlnLWtpd2k="), "not a Figma archive"},
		{clipboard("ZmlnLWtpd2kPAAAA"), "truncated Figma archive"},
		{clipboard("ZmlnLWtpd2kPAAAAAQAAAA"), "bad Figma clipboard data: "},
		{clipboard("ZmlnLWtpd2kPAAAA/wAAAAAA"), "truncated Figma archive"},
	} {
		_, _, err := Decode(c.html)
		if err == nil || !strings.HasPrefix(err.Error(), c.err) || (!strings.HasSuffix(c.err, ": ") && err.Error() != c.err) {
			t.Errorf("%s: got %v, want %q", c.html[len(c.html)-60:], err, c.err)
		}
	}
	for _, html := range []string{"", "data-metadata", "data-metadata (figma)", "(figma)AAAA(/figma)"} {
		if _, ok, err := Decode(html); ok || err != nil {
			t.Errorf("%q: not Figma data, got %v %v", html, ok, err)
		}
	}
	// Expanded instances stop at the node budget.
	html, err := os.ReadFile(filepath.Join(fixtureDir, "live", "budget.html"))
	if err != nil {
		t.Fatal(err)
	}
	nodes, ok, err := Decode(string(html))
	if !ok || err != nil {
		t.Fatal(ok, err)
	}
	count := 0
	var walk func(v jsonv.Value)
	walk = func(v jsonv.Value) {
		count++
		for _, k := range jsonv.Items(get(v, "children")) {
			walk(k)
		}
	}
	for _, n := range nodes {
		walk(n.Value)
	}
	if count != maxNodes {
		t.Fatalf("%d nodes, want the budget of %d", count, maxNodes)
	}
}
