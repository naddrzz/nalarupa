package history

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
)

var j = jsonv.MustParse

// roundTrip checks that Diff then Apply reaches the target, and the returned patch comes back, key order
// included.
func roundTrip(t *testing.T, a, b jsonv.Value) {
	t.Helper()
	p := Diff(a, b)
	if p == nil {
		t.Fatal("documents differ")
	}
	v := jsonv.Clone(a)
	inv := Apply(p, &v)
	if got, want := string(jsonv.Marshal(v)), string(jsonv.Marshal(b)); got != want {
		t.Fatalf("apply:\n%s\nwant\n%s", got, want)
	}
	Apply(inv, &v)
	if got, want := string(jsonv.Marshal(v)), string(jsonv.Marshal(a)); got != want {
		t.Fatalf("undo:\n%s\nwant\n%s", got, want)
	}
}

func node(i int) string {
	return fmt.Sprintf(`{"id": "n%d", "x": %d, "fill": "#f00"}`, i, i)
}

func docWith(nodes ...int) jsonv.Value {
	var s []string
	for _, i := range nodes {
		s = append(s, node(i))
	}
	return j(`{"version": 1, "children": [` + strings.Join(s, ",") + `]}`)
}

func kids(v jsonv.Value) *jsonv.Array { return jsonv.Arr(jsonv.Get(v, "children")) }

func TestPatchesRoundTrip(t *testing.T) {
	doc := docWith(0, 1, 2, 3)
	edited := jsonv.Clone(doc)
	jsonv.Obj(kids(edited).Items[1]).Set("x", jsonv.Int(50))
	roundTrip(t, doc, edited)
	roundTrip(t, doc, docWith(9, 0, 1, 2, 3))
	roundTrip(t, doc, docWith(0, 1, 3))
	roundTrip(t, doc, docWith(1, 2, 3, 0))
	keys := jsonv.Clone(doc)
	jsonv.Obj(kids(keys).Items[2]).Remove("fill")
	jsonv.Obj(kids(keys).Items[3]).Set("stroke", j(`{"width": 2}`))
	jsonv.Obj(keys).Set("name", "x")
	roundTrip(t, doc, keys)
	roundTrip(t, doc, j(`{"children": 5}`))
}

func TestRestoresKeyOrder(t *testing.T) {
	a := j(`{"id": "a", "x": 1, "y": 2, "w": 3}`)
	b := j(`{"id": "a", "w": 3, "z": 9}`)
	v := jsonv.Clone(a)
	inv := Apply(Diff(a, b), &v)
	if got := jsonv.Obj(v).Keys(); !slices.Equal(got, []string{"id", "w", "z"}) {
		t.Fatal(got)
	}
	Apply(inv, &v)
	if got := jsonv.Obj(v).Keys(); !slices.Equal(got, []string{"id", "x", "y", "w"}) {
		t.Fatal(got)
	}
}

func TestARewrittenNumberIsNoChange(t *testing.T) {
	if Diff(j(`{"x": 10}`), j(`{"x": 10.0}`)) != nil || Diff(j(`{"c": [{"x": 1}]}`), j(`{"c": [{"x": 1.0}]}`)) != nil {
		t.Fatal("10 and 10.0 differ")
	}
	if Diff(j(`{"x": 10}`), j(`{"x": 10.5}`)) == nil {
		t.Fatal("10 and 10.5 are the same")
	}
}

func TestOneEditInABigDocumentStoresLittle(t *testing.T) {
	var s []string
	for i := range 5000 {
		s = append(s, fmt.Sprintf(`{"id": "n%d", "name": "r%d", "x": %d, "width": 20, "fill": "#f00"}`, i, i, i))
	}
	a := j(`{"children": [{"id": "f", "children": [` + strings.Join(s, ",") + `]}]}`)
	b := jsonv.Clone(a)
	jsonv.Obj(kids(kids(b).Items[0]).Items[5]).Set("x", jsonv.Int(-1))
	p := Diff(b, a)
	stored := 0
	ValuesMut(p, func(v *jsonv.Value) { stored += len(jsonv.Marshal(*v)) })
	if stored == 0 || stored > 10 {
		t.Fatalf("stores %d bytes of values", stored)
	}
}
