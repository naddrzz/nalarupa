// Port of svgtypes 0.16.1 src/paint.rs tests

package usvg

import "testing"

func TestSTPaintParse(t *testing.T) {
	iri := func(link string, fb *stPaintFallback) stPaint {
		p := stPaint{Kind: stPaintFuncIRI, Link: link}
		if fb != nil {
			p.HasFallback, p.Fallback = true, *fb
		}
		return p
	}
	cases := []struct {
		text string
		want stPaint
	}{
		{"none", stPaint{Kind: stPaintNone}},
		{"  none   ", stPaint{Kind: stPaintNone}},
		{" inherit ", stPaint{Kind: stPaintInherit}},
		{" currentColor ", stPaint{Kind: stPaintCurrentColor}},
		{" red ", stPaint{Kind: stPaintColor, Color: stColorRed()}},
		{" url(#qwe) ", iri("qwe", nil)},
		{" url(#qwe) none ", iri("qwe", &stPaintFallback{Kind: stPaintFallbackNone})},
		{" url(#qwe) currentColor ", iri("qwe", &stPaintFallback{Kind: stPaintFallbackCurrentColor})},
		{" url(#qwe) red ", iri("qwe", &stPaintFallback{Kind: stPaintFallbackColor, Color: stColorRed()})},
	}
	for _, c := range cases {
		got, err := stParsePaint(c.text)
		if err != nil || got != c.want {
			t.Errorf("stParsePaint(%q) = %+v, %v; want %+v", c.text, got, err, c.want)
		}
	}
}

func TestSTPaintParseErr(t *testing.T) {
	for _, text := range []string{"qwe", "red icc-color(acmecmyk, 0.11, 0.48, 0.83, 0.00)"} {
		_, err := stParsePaint(text)
		if err == nil || err.Error() != "invalid value" {
			t.Errorf("stParsePaint(%q) error = %v", text, err)
		}
	}
}
