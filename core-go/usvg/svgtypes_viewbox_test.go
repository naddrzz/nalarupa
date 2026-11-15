// Port of svgtypes 0.16.1 src/viewbox.rs tests

package usvg

import "testing"

func TestSTViewBoxParse(t *testing.T) {
	got, err := stParseViewBox("-20 30 100 500")
	if err != nil || got != stNewViewBox(-20, 30, 100, 500) {
		t.Errorf("stParseViewBox = %v, %v", got, err)
	}
}

func TestSTViewBoxParseErr(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"qwe", "viewBox contains an invalid number"},
		{"10 20 30 0", "viewBox has a negative or zero size"},
		{"10 20 0 40", "viewBox has a negative or zero size"},
		{"10 20 0 0", "viewBox has a negative or zero size"},
		{"10 20 -30 0", "viewBox has a negative or zero size"},
		{"10 20 30 -40", "viewBox has a negative or zero size"},
		{"10 20 -30 -40", "viewBox has a negative or zero size"},
	} {
		_, err := stParseViewBox(c.text)
		if err == nil || err.Error() != c.want {
			t.Errorf("stParseViewBox(%q) error = %v; want %q", c.text, err, c.want)
		}
	}
}
