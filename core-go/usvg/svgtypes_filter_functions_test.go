// Port of svgtypes 0.16.1 src/filter_functions.rs tests

package usvg

import "testing"

// stFilterItem is one expected parser item: a value, or an error text when err != "".
type stFilterItem struct {
	v   stFilterValue
	err string
}

func TestSTFilterValueListParser(t *testing.T) {
	num := stNewLengthNumber
	ds := func(hasColor bool, c stColor, dx, dy, sd float64) stFilterValue {
		return stFilterValue{Kind: stFilterDropShadow, HasColor: hasColor, Color: c, Dx: num(dx), Dy: num(dy), StdDev: num(sd)}
	}
	red := stNewColorRGB(255, 0, 0)
	ok := func(v stFilterValue) stFilterItem { return stFilterItem{v: v} }
	bad := func(e string) stFilterItem { return stFilterItem{err: e} }
	cases := []struct {
		name, text string
		items      []stFilterItem
	}{
		{"empty", "", nil},
		{"none", "none", nil},
		{"blur_default", "blur()", []stFilterItem{ok(stFilterValue{Kind: stFilterBlur, Length: stLengthZero()})}},
		{"blur_2", "blur(2)", []stFilterItem{ok(stFilterValue{Kind: stFilterBlur, Length: stNewLength(2, stLengthUnitNone)})}},
		{"blur_2mm", "blur(2mm)", []stFilterItem{ok(stFilterValue{Kind: stFilterBlur, Length: stNewLength(2, stLengthUnitMm)})}},
		{"blur_2percent", "blur(2%)", []stFilterItem{bad("a percentage value detected at position 6")}},
		{"blur_negative", "blur(-1)", []stFilterItem{bad("a negative value detected at position 6")}},
		{"blur_two_values", "blur(1 2)", []stFilterItem{bad("expected ')' not '2' at position 8")}},
		{"brightness_default", "brightness()", []stFilterItem{ok(stFilterValue{Kind: stFilterBrightness, Amount: 1})}},
		{"brightness_2", "brightness(2)", []stFilterItem{ok(stFilterValue{Kind: stFilterBrightness, Amount: 2})}},
		{"brightness_50percent", "brightness(50%)", []stFilterItem{ok(stFilterValue{Kind: stFilterBrightness, Amount: 0.5})}},
		{"brightness_negative", "brightness(-1)", []stFilterItem{bad("a negative value detected at position 12")}},
		{"brightness_2mm", "brightness(2mm)", []stFilterItem{bad("expected ')' not 'm' at position 13")}},
		{"drop_shadow_no_values", "drop-shadow()", []stFilterItem{bad("drop-shadow offset values are expected at position 13")}},
		{"drop_shadow_default", "drop-shadow(2 3)", []stFilterItem{ok(ds(false, stColor{}, 2, 3, 0))}},
		{"drop_shadow_color_first", "drop-shadow(red 2 3)", []stFilterItem{ok(ds(true, red, 2, 3, 0))}},
		{"drop_shadow_color_after", "drop-shadow(2 3 red)", []stFilterItem{ok(ds(true, red, 2, 3, 0))}},
		{"drop_shadow_curr_color_first", "drop-shadow(currentColor 2 3)", []stFilterItem{ok(ds(false, stColor{}, 2, 3, 0))}},
		{"drop_shadow_curr_color_after", "drop-shadow(2 3 currentColor)", []stFilterItem{ok(ds(false, stColor{}, 2, 3, 0))}},
		{"drop_shadow_with_dev", "drop-shadow(red 2 3 4)", []stFilterItem{ok(ds(true, red, 2, 3, 4))}},
		{"drop_shadow_color_twice", "drop-shadow(red 2 3 4 red)", []stFilterItem{bad("expected ')' not 'r' at position 23")}},
		{"drop_shadow_curr_color_twice", "drop-shadow(currentColor 2 3 4 currentColor)", []stFilterItem{bad("expected ')' not 'c' at position 32")}},
		{"drop_shadow_percent", "drop-shadow(2% 3% 4%)", []stFilterItem{bad("a percentage value detected at position 13")}},
		{"drop_shadow_negative_offset", "drop-shadow(-1 -2 3)", []stFilterItem{ok(ds(false, stColor{}, -1, -2, 3))}},
		{"hue_rotate_no_units", "hue-rotate(45)", []stFilterItem{bad("an invalid angle at position 12")}},
		{"url", "url(#qwe)", []stFilterItem{ok(stFilterValue{Kind: stFilterURL, URL: "qwe"})}},
		{"multiple_1", "blur() blur()", []stFilterItem{ok(stFilterValue{Kind: stFilterBlur}), ok(stFilterValue{Kind: stFilterBlur})}},
		{"multiple_2", "blur() contrast(1)", []stFilterItem{ok(stFilterValue{Kind: stFilterBlur}), ok(stFilterValue{Kind: stFilterContrast, Amount: 1})}},
	}
	for _, c := range cases {
		p := stNewFilterValueListParser(c.text)
		for i, want := range c.items {
			v, ok, err := p.Next()
			switch {
			case !ok:
				t.Errorf("%s: item %d: early end", c.name, i)
			case want.err != "":
				if err == nil || err.Error() != want.err {
					t.Errorf("%s: item %d: error = %v; want %q", c.name, i, err, want.err)
				}
			case err != nil || v != want.v:
				t.Errorf("%s: item %d = %+v, %v; want %+v", c.name, i, v, err, want.v)
			}
		}
		if _, ok, _ := p.Next(); ok {
			t.Errorf("%s: expected end", c.name)
		}
	}
}
