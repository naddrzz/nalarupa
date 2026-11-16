// Package jsonv is an ordered JSON value, the Go counterpart of serde_json's Value with the
// `preserve_order` feature: object keys keep their order through load, edit and save, and numbers keep
// their source text. A Go map would lose the key order.
//
// A Value is one of nil (null), bool, Number, string, *Array or *Object. Containers are pointers, so an
// edit through a Value reaches the document it was taken from; Clone makes an independent copy.
package jsonv

import (
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Value is nil, bool, Number, string, *Array or *Object.
type Value = any

// Number is a JSON number as written in its source.
type Number string

// Array is a JSON array; Items is edited in place.
type Array struct {
	Items []Value
}

// Object is a JSON object that keeps its key order (an IndexMap, as serde_json's `preserve_order`).
type Object struct {
	keys []string
	vals []Value
	idx  map[string]int
}

// NewObject returns an empty object.
func NewObject() *Object {
	return &Object{idx: map[string]int{}}
}

// NewArray returns an array holding items.
func NewArray(items ...Value) *Array {
	return &Array{Items: items}
}

// Len is the number of keys.
func (o *Object) Len() int { return len(o.keys) }

// Keys returns the keys in order (a copy).
func (o *Object) Keys() []string { return append([]string(nil), o.keys...) }

// Get returns the value of k and whether k is present.
func (o *Object) Get(k string) (Value, bool) {
	i, ok := o.idx[k]
	if !ok {
		return nil, false
	}
	return o.vals[i], true
}

// Has reports whether k is present.
func (o *Object) Has(k string) bool {
	_, ok := o.idx[k]
	return ok
}

// Ref returns a pointer to the value of k, or nil when k is absent. It stays valid until a key is
// added or removed.
func (o *Object) Ref(k string) *Value {
	i, ok := o.idx[k]
	if !ok {
		return nil
	}
	return &o.vals[i]
}

// Set inserts k at the end, or replaces its value where it already sits.
func (o *Object) Set(k string, v Value) {
	if i, ok := o.idx[k]; ok {
		o.vals[i] = v
		return
	}
	o.idx[k] = len(o.keys)
	o.keys = append(o.keys, k)
	o.vals = append(o.vals, v)
}

// Remove deletes k as serde_json's `Map::remove` does under `preserve_order`: the last entry takes
// its place (swap_remove), so the key order of the rest can change.
func (o *Object) Remove(k string) (Value, bool) {
	i, ok := o.idx[k]
	if !ok {
		return nil, false
	}
	v := o.vals[i]
	last := len(o.keys) - 1
	if i != last {
		o.keys[i], o.vals[i] = o.keys[last], o.vals[last]
		o.idx[o.keys[i]] = i
	}
	o.keys, o.vals = o.keys[:last], o.vals[:last]
	delete(o.idx, k)
	return v, true
}

// Each calls f for every entry in order.
func (o *Object) Each(f func(k string, v Value)) {
	for i, k := range o.keys {
		f(k, o.vals[i])
	}
}

// Clone returns a deep copy of v.
func Clone(v Value) Value {
	switch x := v.(type) {
	case *Array:
		out := make([]Value, len(x.Items))
		for i, it := range x.Items {
			out[i] = Clone(it)
		}
		return &Array{Items: out}
	case *Object:
		out := &Object{keys: append([]string(nil), x.keys...), vals: make([]Value, len(x.vals)), idx: make(map[string]int, len(x.keys))}
		for i, it := range x.vals {
			out.vals[i] = Clone(it)
			out.idx[x.keys[i]] = i
		}
		return out
	}
	return v
}

// ── accessors (serde's `v["k"]`, `as_str`, `as_f64`, ...) ───────────────────

// Get returns v[k], or nil when v is not an object or lacks k (serde's `v["k"]`).
func Get(v Value, k string) Value {
	if o, ok := v.(*Object); ok && o != nil {
		x, _ := o.Get(k)
		return x
	}
	return nil
}

// Str returns v as a string.
func Str(v Value) (string, bool) {
	s, ok := v.(string)
	return s, ok
}

// StrOr returns v as a string, or "".
func StrOr(v Value) string {
	s, _ := v.(string)
	return s
}

// Obj returns v as an object, or nil.
func Obj(v Value) *Object {
	o, _ := v.(*Object)
	return o
}

// Arr returns v as an array, or nil.
func Arr(v Value) *Array {
	a, _ := v.(*Array)
	return a
}

// Items returns the items of an array, or nil when v is not one.
func Items(v Value) []Value {
	if a, ok := v.(*Array); ok && a != nil {
		return a.Items
	}
	return nil
}

// F64 is serde's `as_f64`: any number as a float.
func F64(v Value) (float64, bool) {
	if n, ok := v.(Number); ok {
		return n.Float64()
	}
	return 0, false
}

// IsTrue reports whether v is the boolean true (serde's `v == true`).
func IsTrue(v Value) bool {
	b, ok := v.(bool)
	return ok && b
}

// IsFalse reports whether v is the boolean false.
func IsFalse(v Value) bool {
	b, ok := v.(bool)
	return ok && !b
}

// StrIs reports whether v is the string s (serde's `v == "s"`).
func StrIs(v Value, s string) bool {
	x, ok := v.(string)
	return ok && x == s
}

// ── numbers ──────────────────────────────────────────────────────────────────

type numKind uint8

const (
	posInt numKind = iota
	negInt
	float
)

// kind classifies the number as serde_json parses it: integer text that fits u64 (or i64 when
// negative, never -0) is an integer, anything else a float.
func (n Number) kind() (numKind, uint64) {
	s := string(n)
	if strings.ContainsAny(s, ".eE") {
		return float, 0
	}
	neg := strings.HasPrefix(s, "-")
	u, err := strconv.ParseUint(strings.TrimPrefix(s, "-"), 10, 64)
	switch {
	case err != nil:
		return float, 0
	case !neg:
		return posInt, u
	case u == 0 || u > 1<<63:
		return float, 0
	}
	return negInt, u
}

// Float64 is serde's `as_f64`.
func (n Number) Float64() (float64, bool) {
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil && !isRange(err) {
		return 0, false
	}
	return f, true
}

func isRange(err error) bool {
	e, ok := err.(*strconv.NumError)
	return ok && e.Err == strconv.ErrRange
}

// Int64 is serde's `as_i64`.
func (n Number) Int64() (int64, bool) {
	switch k, u := n.kind(); k {
	case posInt:
		if u <= math.MaxInt64 {
			return int64(u), true
		}
	case negInt:
		return -int64(u-1) - 1, true
	}
	return 0, false
}

// Uint64 is serde's `as_u64`.
func (n Number) Uint64() (uint64, bool) {
	if k, u := n.kind(); k == posInt {
		return u, true
	}
	return 0, false
}

// IsFloat is serde's `is_f64`.
func (n Number) IsFloat() bool {
	k, _ := n.kind()
	return k == float
}

// Int is an integer number.
func Int(i int64) Number { return Number(strconv.FormatInt(i, 10)) }

// Float is a float number written as serde_json writes an f64; nil (null) when f is not finite.
func Float(f float64) Value {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil
	}
	return Number(FormatFloat(f))
}

// FormatFloat writes a finite f64 as serde_json 1.0.154 does (zmij): the shortest round-trip digits,
// with ".0" on whole numbers and a signed exponent outside 1e-5 ≤ |f| < 1e16 (`1e+16`, `1e-6`).
func FormatFloat(f float64) string {
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // [-]d[.ddd]e±XX
	sign := ""
	if e[0] == '-' {
		sign, e = "-", e[1:]
	}
	mant, exp, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	x, _ := strconv.Atoi(exp)
	n := len(digits)
	kk := x + 1 // digits before the decimal point
	k := kk - n
	var out string
	switch {
	case k >= 0 && kk <= 16:
		out = digits + strings.Repeat("0", k) + ".0"
	case kk > 0 && kk <= 16:
		out = digits[:kk] + "." + digits[kk:]
	case kk > -5 && kk <= 0:
		out = "0." + strings.Repeat("0", -kk) + digits
	case n == 1:
		out = digits + exponent(kk-1)
	default:
		out = digits[:1] + "." + digits[1:] + exponent(kk-1)
	}
	return sign + out
}

// exponent is how serde_json's float writer (zmij) writes a decimal exponent: `e+21`, `e-7`.
func exponent(e int) string {
	if e >= 0 {
		return "e+" + strconv.Itoa(e)
	}
	return "e" + strconv.Itoa(e)
}

// Equal is serde_json's `==`: objects compare without regard to key order, and numbers by kind
// (10 and 10.0 differ).
func Equal(a, b Value) bool {
	switch x := a.(type) {
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case Number:
		y, ok := b.(Number)
		if !ok {
			return false
		}
		kx, ux := x.kind()
		ky, uy := y.kind()
		if kx != ky {
			return false
		}
		if kx == float {
			fx, _ := x.Float64()
			fy, _ := y.Float64()
			return fx == fy
		}
		return ux == uy
	case *Array:
		y, ok := b.(*Array)
		if !ok || len(x.Items) != len(y.Items) {
			return false
		}
		for i := range x.Items {
			if !Equal(x.Items[i], y.Items[i]) {
				return false
			}
		}
		return true
	case *Object:
		y, ok := b.(*Object)
		if !ok || x.Len() != y.Len() {
			return false
		}
		for i, k := range x.keys {
			w, ok := y.Get(k)
			if !ok || !Equal(x.vals[i], w) {
				return false
			}
		}
		return true
	}
	return false
}

// SameNumber is equality by value: integers exactly, an integer and a float only when the float
// holds that very integer (engine.rs `same_number`).
func SameNumber(x, y Number) bool {
	kx, ux := x.kind()
	ky, uy := y.kind()
	switch {
	case kx != float && ky != float:
		return kx == ky && ux == uy
	case kx == float && ky == float:
		fx, _ := x.Float64()
		fy, _ := y.Float64()
		return fx == fy
	}
	f, _ := x.Float64()
	k, u := ky, uy
	if kx != float {
		f, _ = y.Float64()
		k, u = kx, ux
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
		return false
	}
	i := new(big.Int).SetUint64(u)
	if k == negInt {
		i.Neg(i)
	}
	fi, _ := big.NewFloat(f).Int(nil)
	return fi.Cmp(i) == 0
}
