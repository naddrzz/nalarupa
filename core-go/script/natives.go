package script

// The natives the prelude calls (script.rs NATIVES). They take their arguments as strings: the prelude
// JSON-encodes objects, and the JSON is read as serde_json reads it.

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/doc"
	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/svg"
)

type native struct {
	name string
	fn   func(ex *exec, a []string) (string, error)
}

var natives = [15]native{
	{"__I", (*exec).insert}, {"__C", (*exec).copy}, {"__U", (*exec).update}, {"__R", (*exec).replace},
	{"__M", (*exec).move}, {"__D", (*exec).delete}, {"__SV", (*exec).setVars}, {"__GV", (*exec).getVars},
	{"__P", (*exec).print}, {"__T", (*exec).shot}, {"__X", (*exec).export}, {"__F", (*exec).findSpace},
	{"__GEN", (*exec).generate}, {"__G", (*exec).get}, {"__GT", (*exec).getTree},
}

func arg(a []string, i int) string {
	if i < len(a) {
		return a[i]
	}
	return ""
}

// jsonArg is argument i as JSON; "" is null.
func jsonArg(a []string, i int) (jsonv.Value, error) {
	t := arg(a, i)
	if t == "" {
		return nil, nil
	}
	v, err := jsonv.Parse([]byte(t))
	if err != nil {
		return nil, fmt.Errorf("invalid argument: %v", err)
	}
	return serdeNumbers(v), nil
}

// serdeNumbers rewrites v's numbers as serde_json holds them once it has parsed JSON that JavaScript wrote: an
// integer that fits 64 bits stays one; anything else becomes the f64 serde's parser makes of it (without the
// `float_roundtrip` feature it is not always the nearest one: `1.1805916207174113e+21` reads one step below
// 2**70), written back as serde writes an f64.
func serdeNumbers(v jsonv.Value) jsonv.Value {
	switch x := v.(type) {
	case jsonv.Number:
		if n, ok := serdeNumber(string(x)); ok {
			return n
		}
	case *jsonv.Array:
		for i, it := range x.Items {
			x.Items[i] = serdeNumbers(it)
		}
	case *jsonv.Object:
		for _, k := range x.Keys() {
			p := x.Ref(k)
			*p = serdeNumbers(*p)
		}
	}
	return v
}

// pow10 is serde_json's POW10: 1e0 to 1e308, each the nearest f64.
var pow10 = func() (t [309]float64) {
	for i := range t {
		t[i], _ = strconv.ParseFloat("1e"+strconv.Itoa(i), 64)
	}
	return t
}()

// serdeNumber is how serde_json 1.0.154 (no `float_roundtrip`, no `arbitrary_precision`) reads the JSON number s
// and writes it again; false when it would refuse it (out of range).
func serdeNumber(s string) (jsonv.Number, bool) {
	positive := !strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	i := 0
	digit := func() (uint64, bool) {
		if i < len(s) && s[i] >= '0' && s[i] <= '9' {
			return uint64(s[i] - '0'), true
		}
		return 0, false
	}
	overflows := func(a, b uint64) bool {
		return a >= math.MaxUint64/10 && (a > math.MaxUint64/10 || b > math.MaxUint64%10)
	}
	var sig uint64
	exp := int32(0)
	// The integer part; past u64 its further digits only count in the exponent.
	for d, ok := digit(); ok; d, ok = digit() {
		if overflows(sig, d) {
			for _, ok := digit(); ok; _, ok = digit() {
				i++
				exp++
			}
			break
		}
		sig = sig*10 + d
		i++
	}
	isFloat := exp != 0
	if i < len(s) && s[i] == '.' {
		isFloat = true
		i++
		for d, ok := digit(); ok; d, ok = digit() {
			if overflows(sig, d) {
				// Further digits are ignored.
				for _, ok := digit(); ok; _, ok = digit() {
					i++
				}
				break
			}
			sig = sig*10 + d
			exp--
			i++
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		isFloat = true
		i++
		positiveExp := true
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			positiveExp = s[i] == '+'
			i++
		}
		var e int32
		for d, ok := digit(); ok; d, ok = digit() {
			if e >= math.MaxInt32/10 && (e > math.MaxInt32/10 || int32(d) > math.MaxInt32%10) {
				if sig != 0 && positiveExp {
					return "", false
				}
				return jsonv.Number(map[bool]string{true: "0.0", false: "-0.0"}[positive]), true
			}
			e = e*10 + int32(d)
			i++
		}
		if positiveExp {
			exp = satAdd(exp, e)
		} else {
			exp = satAdd(exp, -e)
		}
	}
	if !isFloat {
		switch {
		case positive:
			return jsonv.Number(strconv.FormatUint(sig, 10)), true
		case int64(-sig) < 0:
			return jsonv.Int(int64(-sig)), true
		}
		// `-0`, or below i64: an f64.
		return jsonv.Float(-float64(sig)).(jsonv.Number), true
	}
	// f64_from_parts.
	f := float64(sig)
	for {
		if a := int(exp); a < len(pow10) && a > -len(pow10) {
			if exp >= 0 {
				if f *= pow10[a]; math.IsInf(f, 0) {
					return "", false
				}
			} else {
				f /= pow10[-a]
			}
			break
		}
		if f == 0 {
			break
		}
		if exp >= 0 {
			return "", false
		}
		f /= 1e308
		exp += 308
	}
	if !positive {
		f = -f
	}
	return jsonv.Float(f).(jsonv.Number), true
}

// satAdd is Rust's i32 saturating_add.
func satAdd(a, b int32) int32 {
	s := int64(a) + int64(b)
	return int32(max(min(s, math.MaxInt32), math.MinInt32))
}

// strList is the strings of an array value.
func strList(v jsonv.Value) []string {
	var out []string
	for _, it := range jsonv.Items(v) {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (ex *exec) record(node jsonv.Value, id string) {
	if name, ok := jsonv.Get(node, "name").(string); ok {
		ex.names = append(ex.names, [2]string{name, id})
	}
	ex.touched = append(ex.touched, id)
}

func (ex *exec) insert(a []string) (string, error) {
	parent := arg(a, 0)
	node, err := jsonArg(a, 1)
	if err != nil {
		return "", err
	}
	if err := ex.host.CheckEditTarget("insert", parent, parent == doc.Root && jsonv.IsTrue(jsonv.Get(node, "reusable"))); err != nil {
		return "", err
	}
	id, err := ex.host.Doc().Insert(parent, node, -1)
	if err != nil {
		return "", err
	}
	ex.record(node, id)
	if parent == doc.Root {
		ex.host.AfterRootInsert(id)
	} else {
		ex.host.SlideIn(id)
	}
	ex.host.Changed()
	return id, nil
}

func (ex *exec) copy(a []string) (string, error) {
	path, parent := arg(a, 0), arg(a, 1)
	data, err := jsonArg(a, 2)
	if err != nil {
		return "", err
	}
	if err := ex.host.CheckEditTarget("copy", parent, false); err != nil {
		return "", err
	}
	d := ex.host.Doc()
	var source jsonv.Value
	if n := d.Find(path); n != nil && !strings.Contains(path, "/") {
		source = jsonv.Clone(n)
	} else if source, err = ex.nodeJSON(path, getOpts{resolveInst: true, geometry: true, depth: anyDepth}); err != nil {
		return "", err
	}
	id, err := d.Copy(source, parent, data)
	if err != nil {
		return "", err
	}
	named := source
	if _, ok := jsonv.Get(data, "name").(string); ok {
		named = data
	}
	ex.record(named, id)
	if parent == doc.Root {
		ex.host.AfterRootInsert(id)
	} else {
		ex.host.AfterModify("", id)
	}
	ex.host.Changed()
	return id, nil
}

func (ex *exec) update(a []string) (string, error) {
	path := arg(a, 0)
	data, err := jsonArg(a, 1)
	if err != nil {
		return "", err
	}
	if err := ex.host.CheckEditTarget("update", path, false); err != nil {
		return "", err
	}
	ex.host.BeforeModify(path)
	if err := ex.host.Doc().Update(path, data); err != nil {
		return "", err
	}
	ex.touched = append(ex.touched, path)
	ex.host.AfterModify(path, path)
	ex.host.Changed()
	return "", nil
}

func (ex *exec) replace(a []string) (string, error) {
	path := arg(a, 0)
	node, err := jsonArg(a, 1)
	if err != nil {
		return "", err
	}
	if err := ex.host.CheckEditTarget("replace", path, false); err != nil {
		return "", err
	}
	ex.host.BeforeModify(path)
	id, err := ex.host.Doc().Replace(path, node)
	if err != nil {
		return "", err
	}
	ex.record(node, id)
	ex.host.AfterModify(path, id)
	ex.host.Changed()
	return id, nil
}

func (ex *exec) move(a []string) (string, error) {
	path, parent := arg(a, 0), arg(a, 1)
	if err := ex.host.CheckEditTarget("move", path, false); err != nil {
		return "", err
	}
	if parent != doc.Root {
		if err := ex.host.CheckEditTarget("insert", parent, false); err != nil {
			return "", err
		}
	}
	// Rust's `str::parse::<f64>`, -1 when it fails; a negative or NaN index is the end, a huge one saturates.
	index, err := strconv.ParseFloat(arg(a, 2), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		index = -1
	}
	idx := -1
	if index >= 0 {
		idx = int(math.Min(index, 1<<62))
	}
	ex.host.BeforeModify(path)
	if err := ex.host.Doc().MoveNode(path, parent, idx); err != nil {
		return "", err
	}
	ex.touched = append(ex.touched, path)
	ex.host.AfterModify(path, path)
	ex.host.Changed()
	return "", nil
}

func (ex *exec) delete(a []string) (string, error) {
	target := arg(a, 0)
	if err := ex.host.CheckEditTarget("delete", target, false); err != nil {
		return "", err
	}
	if err := ex.host.Doc().Delete(target); err != nil {
		return "", err
	}
	ex.host.Changed()
	return "", nil
}

func (ex *exec) setVars(a []string) (string, error) {
	vars, err := jsonArg(a, 0)
	if err != nil {
		return "", err
	}
	filteredVars, replace := ex.host.FilterSetVars(vars, arg(a, 1) == "true")
	if err := ex.host.Doc().SetVariables(filteredVars, replace); err != nil {
		return "", err
	}
	ex.host.AfterSetVars()
	ex.host.Changed()
	return "", nil
}

func (ex *exec) getVars([]string) (string, error) {
	return string(jsonv.Marshal(ex.host.Doc().Variables())), nil
}

func (ex *exec) print(a []string) (string, error) {
	ex.prints = append(ex.prints, arg(a, 0))
	return "", nil
}

func (ex *exec) shot(a []string) (string, error) {
	ids, err := jsonArg(a, 0)
	if err != nil {
		return "", err
	}
	list := strList(ids)
	for _, id := range list {
		if id != "document" {
			ex.read = append(ex.read, id)
		}
	}
	ex.shots = append(ex.shots, list...)
	return "", nil
}

func (ex *exec) export(a []string) (string, error) {
	ids, err := jsonArg(a, 0)
	if err != nil {
		return "", err
	}
	format, out := arg(a, 1), arg(a, 2)
	opts, err := jsonArg(a, 3)
	if err != nil {
		return "", err
	}
	ex.exports = append(ex.exports, Export{IDs: strList(ids), Format: format, Out: out, Opts: opts})
	return "", nil
}

func (ex *exec) findSpace(a []string) (string, error) {
	o, err := jsonArg(a, 0)
	if err != nil {
		return "", err
	}
	num := func(k string, or float64) float64 {
		if f, ok := jsonv.F64(jsonv.Get(o, k)); ok {
			return f
		}
		return or
	}
	dir, ok := jsonv.Get(o, "direction").(string)
	if !ok {
		dir = "right"
	}
	anchor, _ := jsonv.Get(o, "nodeId").(string)
	x, y := ex.host.FindEmptySpace(num("width", 400), num("height", 300), dir, num("padding", 0), anchor)
	r := jsonv.NewObject()
	r.Set("x", jsonv.Float(x))
	r.Set("y", jsonv.Float(y))
	return string(jsonv.Marshal(r)), nil
}

func (ex *exec) generate(a []string) (string, error) {
	ty, target, prompt := arg(a, 0), arg(a, 1), arg(a, 2)
	d := ex.host.Doc()
	switch {
	case ex.preview:
		return "", nil
	case ty == "ai" || ty == "stock":
		// The fill points at a pending url right away (the canvas shows a shimmer skeleton) and the real image
		// replaces it when the download finishes.
		seed := fmt.Sprintf("%x", svg.FxHash(prompt))
		pending := fmt.Sprintf("nalarupa:pending-image-%s-%x", seed, ex.host.IDs().Next())
		fill := jsonv.NewObject()
		fill.Set("type", "image")
		fill.Set("url", pending)
		fill.Set("mode", "fill")
		data := jsonv.NewObject()
		data.Set("fill", fill)
		if err := d.Update(target, data); err != nil {
			return "", err
		}
		ex.host.Changed()
		// The download starts only once the fill is set: a bad target starts none.
		if err := ex.host.Generate(ty, target, prompt, pending); err != nil {
			return "", err
		}
		ex.notes = append(ex.notes, fmt.Sprintf("Generate(\"%s\") on %s: the image is loading in the background and appears when ready (placeholder photo source, no image-generation backend).", ty, target))
		return "", nil
	case ty == "svg":
		// SVG artwork is drawn into a frame (the AI Pen), streamed after the call commits.
		if n := d.Find(target); n == nil || !jsonv.StrIs(jsonv.Get(n, "type"), "frame") {
			return "", fmt.Errorf("Generate(\"svg\") draws into a frame: Insert a frame with placeholder: true first and pass its id (got '%s')", target)
		}
		data := jsonv.NewObject()
		data.Set("placeholder", true)
		if err := d.Update(target, data); err != nil {
			return "", err
		}
		ex.host.Changed()
		ex.pens = append(ex.pens, Pen{Frame: target, Prompt: prompt})
		ex.notes = append(ex.notes, fmt.Sprintf("Generate(\"svg\") on %s: the AI Pen draws the artwork into the frame in the background; its `placeholder` flag clears when it is done (usually within a few minutes). Keep designing; do not add children to that frame or generate it again.", target))
		return "", nil
	}
	return "", fmt.Errorf("Generate type '%s' is not available in Nalarupa; use fills, icons and paths instead", ty)
}

func (ex *exec) get(a []string) (string, error) {
	path := arg(a, 0)
	opts, err := jsonArg(a, 1)
	if err != nil {
		return "", err
	}
	ex.read = append(ex.read, path)
	v, err := ex.nodeJSON(path, getOptsFrom(opts))
	if err != nil {
		return "", err
	}
	return string(jsonv.Marshal(v)), nil
}
