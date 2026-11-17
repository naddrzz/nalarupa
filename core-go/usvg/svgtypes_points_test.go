// Port of svgtypes 0.16.1 src/points.rs tests

package usvg

import "testing"

func TestSTPointsParser(t *testing.T) {
	for _, text := range []string{"10 20 30 40", "10 20 30 40 50"} {
		p := stNewPointsParser(text)
		for _, want := range [][2]float64{{10, 20}, {30, 40}} {
			x, y, ok := p.Next()
			if !ok || x != want[0] || y != want[1] {
				t.Fatalf("%q: Next() = %v, %v, %v; want %v", text, x, y, ok, want)
			}
		}
		if _, _, ok := p.Next(); ok {
			t.Fatalf("%q: expected end", text)
		}
	}
}
