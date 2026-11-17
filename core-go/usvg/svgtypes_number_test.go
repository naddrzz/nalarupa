// Port of svgtypes 0.16.1 src/number.rs tests

package usvg

import "testing"

func TestSTNumberParse(t *testing.T) {
	cases := []struct {
		text string
		want float64
	}{
		{"0", 0.0}, {"1", 1.0}, {"-1", -1.0}, {" -1 ", -1.0}, {"  1  ", 1.0}, {".4", 0.4}, {"-.4", -0.4},
		{"-.4text", -0.4}, {"-.01 text", -0.01}, {"-.01 4", -0.01}, {".0000000000008", 0.0000000000008},
		{"1000000000000", 1000000000000.0}, {"123456.123456", 123456.123456}, {"+10", 10.0},
		{"1e2", 100.0}, {"1e+2", 100.0}, {"1E2", 100.0}, {"1e-2", 0.01}, {"1ex", 1.0}, {"1em", 1.0},
		{"12345678901234567890", 12345678901234567000.0}, {"0.", 0.0}, {"1.3e-2", 0.013},
	}
	for _, c := range cases {
		s := stNewStream(c.text)
		got, err := s.parseNumber()
		if err != nil || got != c.want {
			t.Errorf("parseNumber(%q) = %v, %v; want %v", c.text, got, err, c.want)
		}
	}
}

func TestSTNumberParseErr(t *testing.T) {
	for _, text := range []string{"q", "", "-", "+", "-q", ".", "99999999e99999999", "-99999999e99999999"} {
		s := stNewStream(text)
		_, err := s.parseNumber()
		if err == nil || err.Error() != "invalid number at position 1" {
			t.Errorf("parseNumber(%q) error = %v", text, err)
		}
	}
}

func TestSTNumberListParser(t *testing.T) {
	p := stNewNumberListParser("10, 20 -50")
	for _, want := range []float64{10, 20, -50} {
		v, ok, err := p.Next()
		if !ok || err != nil || v != want {
			t.Fatalf("Next() = %v, %v, %v; want %v", v, ok, err, want)
		}
	}
	if _, ok, _ := p.Next(); ok {
		t.Fatal("expected end")
	}
}
