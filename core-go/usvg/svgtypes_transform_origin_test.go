// Port of svgtypes 0.16.1 src/transform_origin.rs tests

package usvg

import "testing"

func TestSTTransformOriginParse(t *testing.T) {
	pc := func(n float64) stLength { return stNewLength(n, stLengthUnitPercent) }
	px := func(n float64) stLength { return stNewLength(n, stLengthUnitPx) }
	cases := []struct {
		text string
		want stTransformOrigin
	}{
		{"center", stNewTransformOrigin(pc(50), pc(50), px(0))},
		{"left", stNewTransformOrigin(pc(0), pc(50), px(0))},
		{"right", stNewTransformOrigin(pc(100), pc(50), px(0))},
		{"top", stNewTransformOrigin(pc(50), pc(0), px(0))},
		{"bottom", stNewTransformOrigin(pc(50), pc(100), px(0))},
		{"30px", stNewTransformOrigin(px(30), pc(50), px(0))},
		{"center left", stNewTransformOrigin(pc(0), pc(50), px(0))},
		{"left center", stNewTransformOrigin(pc(0), pc(50), px(0))},
		{"center bottom", stNewTransformOrigin(pc(50), pc(100), px(0))},
		{"bottom center", stNewTransformOrigin(pc(50), pc(100), px(0))},
		{"30%, center", stNewTransformOrigin(pc(30), pc(50), px(0))},
		{" center, 30%", stNewTransformOrigin(pc(50), pc(30), px(0))},
		{"left top", stNewTransformOrigin(pc(0), pc(0), px(0))},
		{"center right 3px", stNewTransformOrigin(pc(100), pc(50), px(3))},
	}
	for _, c := range cases {
		got, err := stParseTransformOrigin(c.text)
		if err != nil || got != c.want {
			t.Errorf("stParseTransformOrigin(%q) = %v, %v; want %v", c.text, got, err, c.want)
		}
	}
}

func TestSTTransformOriginParseErr(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"", "transform origin doesn't have enough parameters"},
		{"some", "transform origin has invalid parameters"},
		{"center some", "transform origin has invalid parameters"},
		{"left right", "transform origin has invalid parameters"},
		{"left top 3%", "z-index cannot be a percentage"},
	} {
		_, err := stParseTransformOrigin(c.text)
		if err == nil || err.Error() != c.want {
			t.Errorf("stParseTransformOrigin(%q) error = %v; want %q", c.text, err, c.want)
		}
	}
}

// Not a Rust test: Rust panics (unreachable!) when the first part fails but the input is consumed.
func TestSTTransformOriginPanicsLikeRust(t *testing.T) {
	for _, text := range []string{" ", "-", "-left"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("stParseTransformOrigin(%q) did not panic", text)
				}
			}()
			stParseTransformOrigin(text)
		}()
	}
}
