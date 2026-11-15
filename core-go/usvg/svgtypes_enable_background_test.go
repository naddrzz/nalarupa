// Port of svgtypes 0.16.1 src/enable_background.rs tests

package usvg

import "testing"

func TestSTEnableBackgroundParse(t *testing.T) {
	cases := []struct {
		text string
		want stEnableBackground
	}{
		{"accumulate", stEnableBackground{Kind: stEnableBackgroundAccumulate}},
		{"  accumulate  ", stEnableBackground{Kind: stEnableBackgroundAccumulate}},
		{"new", stEnableBackground{Kind: stEnableBackgroundNew}},
		{"  new  ", stEnableBackground{Kind: stEnableBackgroundNew}},
		{"new 1 2 3 4", stEnableBackground{stEnableBackgroundNewWithRegion, 1, 2, 3, 4}},
	}
	for _, c := range cases {
		got, err := stParseEnableBackground(c.text)
		if err != nil || got != c.want {
			t.Errorf("stParseEnableBackground(%q) = %v, %v; want %v", c.text, got, err, c.want)
		}
	}
}

func TestSTEnableBackgroundErr(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{" accumulate b ", "unexpected data at position 13"},
		{" new b ", "invalid number at position 6"},
		{"new 1 2 3", "unexpected end of stream"},
		{"new 1 2 3 4 5", "unexpected data at position 13"},
		{"new 0 0 0 0", "invalid value"},
	} {
		_, err := stParseEnableBackground(c.text)
		if err == nil || err.Error() != c.want {
			t.Errorf("stParseEnableBackground(%q) error = %v; want %q", c.text, err, c.want)
		}
	}
}
