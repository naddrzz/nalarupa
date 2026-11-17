// Port of svgtypes 0.16.1 src/transform.rs tests

package usvg

import (
	"strconv"
	"strings"
	"testing"
)

// stFmtF64 is Rust's f64 Display: the shortest round-trip decimal, never an exponent.
func stFmtF64(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }

func TestSTTransformParse(t *testing.T) {
	cases := []struct{ text, want string }{
		{"matrix(1 0 0 1 10 20)", "matrix(1 0 0 1 10 20)"},
		{"translate(10 20)", "matrix(1 0 0 1 10 20)"},
		{"scale(2 3)", "matrix(2 0 0 3 0 0)"},
		{"rotate(30)", "matrix(0.8660254037844387 0.49999999999999994 -0.49999999999999994 0.8660254037844387 0 0)"},
		{"rotate(30 10 20)", "matrix(0.8660254037844387 0.49999999999999994 -0.49999999999999994 0.8660254037844387 11.339745962155611 -2.3205080756887746)"},
		{"translate(10 15) translate(0 5)", "matrix(1 0 0 1 10 20)"},
		{"translate(10) scale(2)", "matrix(2 0 0 2 10 0)"},
		{"translate(25 215) scale(2) skewX(45)", "matrix(2 0 1.9999999999999998 2 25 215)"},
		{"skewX(45)", "matrix(1 0 0.9999999999999999 1 0 0)"},
	}
	for _, c := range cases {
		ts, err := stParseTransform(c.text)
		if err != nil {
			t.Errorf("stParseTransform(%q) error %v", c.text, err)
			continue
		}
		v := []string{stFmtF64(ts.A), stFmtF64(ts.B), stFmtF64(ts.C), stFmtF64(ts.D), stFmtF64(ts.E), stFmtF64(ts.F)}
		if got := "matrix(" + strings.Join(v, " ") + ")"; got != c.want {
			t.Errorf("stParseTransform(%q) = %s; want %s", c.text, got, c.want)
		}
	}
}

func TestSTTransformParseErr(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"text", "unexpected end of stream"},
		{"???G", "expected '(' not '?' at position 1"},
		{"rect()", "unexpected data at position 1"},
		{"scale(2) rect()", "unexpected data at position 10"},
	} {
		_, err := stParseTransform(c.text)
		if err == nil || err.Error() != c.want {
			t.Errorf("stParseTransform(%q) error = %v; want %q", c.text, err, c.want)
		}
	}

	p := stNewTransformListParser("scale(2) text") // parse_err_2
	p.Next()
	if _, ok, err := p.Next(); !ok || err == nil || err.Error() != "unexpected end of stream" {
		t.Errorf("parse_err_2: %v, %v", ok, err)
	}
	if _, ok, _ := stNewTransformListParser(" ").Next(); ok { // parse_err_4
		t.Error("parse_err_4: expected end")
	}
	if _, ok, err := stNewTransformListParser("\x01").Next(); !ok || err == nil { // parse_err_5
		t.Errorf("parse_err_5: %v, %v", ok, err)
	}
}

func TestSTTransformListParser(t *testing.T) {
	p := stNewTransformListParser("scale(2) translate(10, -20)")
	if v, ok, err := p.Next(); !ok || err != nil || v != (stTransformListToken{Kind: stTransformScale, Sx: 2, Sy: 2}) {
		t.Fatalf("first = %+v, %v, %v", v, ok, err)
	}
	if v, ok, err := p.Next(); !ok || err != nil || v != (stTransformListToken{Kind: stTransformTranslate, Tx: 10, Ty: -20}) {
		t.Fatalf("second = %+v, %v, %v", v, ok, err)
	}
	if _, ok, _ := p.Next(); ok {
		t.Fatal("expected end")
	}
}
