// Port of svgtypes 0.16.1 src/directional_position.rs tests

package usvg

import "testing"

func TestSTDirectionalPositionParse(t *testing.T) {
	cases := []struct {
		text string
		want stDirectionalPosition
	}{
		{"left", stDirectionalPositionLeft}, {"right", stDirectionalPositionRight},
		{"center", stDirectionalPositionCenter}, {"top", stDirectionalPositionTop},
		{"bottom", stDirectionalPositionBottom},
	}
	for _, c := range cases {
		got, err := stParseDirectionalPosition(c.text)
		if err != nil || got != c.want {
			t.Errorf("stParseDirectionalPosition(%q) = %v, %v", c.text, got, err)
		}
	}
	for _, text := range []string{"left,", "left ,", "left center"} { // parse_6, parse_7, parse_16
		s := stNewStream(text)
		if got, err := s.parseDirectionalPosition(); err != nil || got != stDirectionalPositionLeft {
			t.Errorf("parseDirectionalPosition(%q) = %v, %v", text, got, err)
		}
	}
}

func TestSTDirectionalPositionErr(t *testing.T) {
	s := stNewStream("something")
	_, err := s.parseDirectionalPosition()
	want := "expected 'left', 'right', 'top', 'bottom', 'center' not 'something' at position 1"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v", err)
	}
}
