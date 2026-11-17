// Port of svgtypes 0.16.1 src/aspect_ratio.rs tests

package usvg

import "testing"

func TestSTAspectRatioParse(t *testing.T) {
	cases := []struct {
		text string
		want stAspectRatio
	}{
		{"none", stAspectRatio{Defer: false, Align: stAlignNone, Slice: false}},
		{"defer none", stAspectRatio{Defer: true, Align: stAlignNone, Slice: false}},
		{"xMinYMid", stAspectRatio{Defer: false, Align: stAlignXMinYMid, Slice: false}},
		{"xMinYMid slice", stAspectRatio{Defer: false, Align: stAlignXMinYMid, Slice: true}},
		{"xMinYMid meet", stAspectRatio{Defer: false, Align: stAlignXMinYMid, Slice: false}},
	}
	for _, c := range cases {
		got, err := stParseAspectRatio(c.text)
		if err != nil || got != c.want {
			t.Errorf("stParseAspectRatio(%q) = %v, %v; want %v", c.text, got, err, c.want)
		}
	}
}
