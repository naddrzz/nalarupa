// Port of svgtypes 0.16.1 src/angle.rs tests

package usvg

import "testing"

func TestSTAngleParse(t *testing.T) {
	cases := []struct {
		text string
		want stAngle
	}{
		{"1", stNewAngle(1, stAngleUnitDegrees)},
		{"1deg", stNewAngle(1, stAngleUnitDegrees)},
		{"1grad", stNewAngle(1, stAngleUnitGradians)},
		{"1rad", stNewAngle(1, stAngleUnitRadians)},
		{"1turn", stNewAngle(1, stAngleUnitTurns)},
	}
	for _, c := range cases {
		got, err := stParseAngle(c.text)
		if err != nil || got != c.want {
			t.Errorf("stParseAngle(%q) = %v, %v; want %v", c.text, got, err, c.want)
		}
	}
}

func TestSTAngleErr(t *testing.T) {
	s := stNewStream("1q")
	if got, err := s.parseAngle(); err != nil || got != stNewAngle(1, stAngleUnitDegrees) {
		t.Fatalf("first parseAngle = %v, %v", got, err)
	}
	if _, err := s.parseAngle(); err == nil || err.Error() != "invalid number at position 2" {
		t.Fatalf("second parseAngle error = %v", err)
	}
	if _, err := stParseAngle("1degq"); err == nil || err.Error() != "unexpected data at position 5" {
		t.Fatalf("stParseAngle(1degq) error = %v", err)
	}
}
