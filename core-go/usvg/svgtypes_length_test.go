// Port of svgtypes 0.16.1 src/length.rs tests

package usvg

import "testing"

func TestSTLengthParse(t *testing.T) {
	cases := []struct {
		text string
		want stLength
	}{
		{"1", stNewLength(1, stLengthUnitNone)},
		{"1em", stNewLength(1, stLengthUnitEm)},
		{"1ex", stNewLength(1, stLengthUnitEx)},
		{"1px", stNewLength(1, stLengthUnitPx)},
		{"1in", stNewLength(1, stLengthUnitIn)},
		{"1cm", stNewLength(1, stLengthUnitCm)},
		{"1mm", stNewLength(1, stLengthUnitMm)},
		{"1pt", stNewLength(1, stLengthUnitPt)},
		{"1pc", stNewLength(1, stLengthUnitPc)},
		{"1%", stNewLength(1, stLengthUnitPercent)},
		{"1e0", stNewLength(1, stLengthUnitNone)},
		{"1.0e0", stNewLength(1, stLengthUnitNone)},
		{"1.0e0em", stNewLength(1, stLengthUnitEm)},
	}
	for _, c := range cases {
		got, err := stParseLength(c.text)
		if err != nil || got != c.want {
			t.Errorf("stParseLength(%q) = %v, %v; want %v", c.text, got, err, c.want)
		}
	}
}

func TestSTLengthStreamPrefix(t *testing.T) {
	for _, text := range []string{"1,", "1 ,", "1 1"} { // parse_14..16
		s := stNewStream(text)
		got, err := s.parseLength()
		if err != nil || got != stNewLength(1, stLengthUnitNone) {
			t.Errorf("parseLength(%q) = %v, %v", text, got, err)
		}
	}
}

func TestSTLengthErr(t *testing.T) {
	s := stNewStream("1q")
	if got, err := s.parseLength(); err != nil || got != stNewLength(1, stLengthUnitNone) {
		t.Fatalf("first parseLength = %v, %v", got, err)
	}
	if _, err := s.parseLength(); err == nil || err.Error() != "invalid number at position 2" {
		t.Fatalf("second parseLength error = %v", err)
	}
	if _, err := stParseLength("1mmx"); err == nil || err.Error() != "unexpected data at position 4" {
		t.Fatalf("stParseLength(1mmx) error = %v", err)
	}
}

func TestSTLengthListParser(t *testing.T) {
	p := stNewLengthListParser("10px 20% 50mm")
	for _, want := range []stLength{
		stNewLength(10, stLengthUnitPx), stNewLength(20, stLengthUnitPercent), stNewLength(50, stLengthUnitMm),
	} {
		v, ok, err := p.Next()
		if !ok || err != nil || v != want {
			t.Fatalf("Next() = %v, %v, %v; want %v", v, ok, err, want)
		}
	}
	if _, ok, _ := p.Next(); ok {
		t.Fatal("expected end")
	}
}
