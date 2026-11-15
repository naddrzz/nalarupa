// Port of svgtypes 0.16.1 src/paint_order.rs tests

package usvg

import "testing"

func TestSTPaintOrderParse(t *testing.T) {
	F, S, M := stPaintOrderFill, stPaintOrderStroke, stPaintOrderMarkers
	def := stDefaultPaintOrder()
	cases := []struct {
		text string
		want stPaintOrder
	}{
		{"normal", def},
		{"qwe", def},
		{"", def},
		{"stroke qwe", def},
		{"stroke stroke", def},
		{"stroke", stPaintOrder{[3]stPaintOrderKind{S, F, M}}},
		{"stroke markers", stPaintOrder{[3]stPaintOrderKind{S, M, F}}},
		{"stroke markers fill", stPaintOrder{[3]stPaintOrderKind{S, M, F}}},
		{"markers", stPaintOrder{[3]stPaintOrderKind{M, F, S}}},
		{"  stroke\n", stPaintOrder{[3]stPaintOrderKind{S, F, M}}},
		{"stroke stroke stroke stroke", def},
	}
	for _, c := range cases {
		if got := stParsePaintOrder(c.text); got != c.want {
			t.Errorf("stParsePaintOrder(%q) = %v; want %v", c.text, got, c.want)
		}
	}
}
