// Port of svgtypes 0.16.1 src/color.rs tests

package usvg

import "testing"

func TestSTColorParse(t *testing.T) {
	rgb, rgba := stNewColorRGB, stNewColorRGBA
	cases := []struct {
		name, text string
		want       stColor
	}{
		{"rrggbb", "#ff0000", rgb(255, 0, 0)},
		{"rrggbb_upper", "#FF0000", rgb(255, 0, 0)},
		{"rgb_hex", "#f00", rgb(255, 0, 0)},
		{"rrggbbaa", "#ff0000ff", rgba(255, 0, 0, 255)},
		{"rrggbbaa_upper", "#FF0000FF", rgba(255, 0, 0, 255)},
		{"rgba_hex", "#f00f", rgba(255, 0, 0, 255)},
		{"rrggbb_spaced", "  #ff0000  ", rgb(255, 0, 0)},
		{"rgb_numeric", "rgb(254, 203, 231)", rgb(254, 203, 231)},
		{"rgb_numeric_spaced", " rgb( 77 , 77 , 77 ) ", rgb(77, 77, 77)},
		{"rgb_percentage", "rgb(50%, 50%, 50%)", rgb(128, 128, 128)},
		{"rgb_percentage_overflow", "rgb(140%, -10%, 130%)", rgb(255, 0, 255)},
		{"rgb_percentage_float", "rgb(33.333%,46.666%,93.333%)", rgb(85, 119, 238)},
		{"rgb_numeric_upper_case", "RGB(254, 203, 231)", rgb(254, 203, 231)},
		{"rgb_numeric_mixed_case", "RgB(254, 203, 231)", rgb(254, 203, 231)},
		{"rgb_numeric_red_float", "rgb(3.141592653, 110, 201)", rgb(3, 110, 201)},
		{"rgb_numeric_green_float", "rgb(254, 150.829521289232389, 210)", rgb(254, 151, 210)},
		{"rgb_numeric_blue_float", "rgb(96, 255, 0.2)", rgb(96, 255, 0)},
		{"rgb_numeric_all_float", "rgb(0.0, 129.82, 231.092)", rgb(0, 130, 231)},
		{"rgb_numeric_all_float_with_alpha", "rgb(0.0, 129.82, 231.092, 0.5)", rgba(0, 130, 231, 128)},
		{"rgb_numeric_all_float_overflow", "rgb(290.2, 255.9, 300.0)", rgb(255, 255, 255)},
		{"name_red", "red", rgb(255, 0, 0)},
		{"name_red_spaced", " red ", rgb(255, 0, 0)},
		{"name_red_upper_case", "RED", rgb(255, 0, 0)},
		{"name_red_mixed_case", "ReD", rgb(255, 0, 0)},
		{"name_cornflowerblue", "cornflowerblue", rgb(100, 149, 237)},
		{"transparent", "transparent", rgba(0, 0, 0, 0)},
		{"rgba_half", "rgba(10, 20, 30, 0.5)", rgba(10, 20, 30, 128)},
		{"rgba_numeric_red_float", "rgba(3.141592653, 110, 201, 1.0)", rgba(3, 110, 201, 255)},
		{"rgba_numeric_all_float", "rgba(0.0, 129.82, 231.092, 1.5)", rgba(0, 130, 231, 255)},
		{"rgba_negative", "rgba(10, 20, 30, -2)", rgba(10, 20, 30, 0)},
		{"rgba_large_alpha", "rgba(10, 20, 30, 2)", rgba(10, 20, 30, 255)},
		{"rgb_with_alpha", "rgb(10, 20, 30, 0.5)", rgba(10, 20, 30, 128)},
		{"hsl_green", "hsl(120, 100%, 75%)", rgba(128, 255, 128, 255)},
		{"hsl_yellow", "hsl(60, 100%, 50%)", rgba(255, 255, 0, 255)},
		{"hsl_hue_360", "hsl(360, 100%, 100%)", rgba(255, 255, 255, 255)},
		{"hsl_out_of_bounds", "hsl(800, 150%, -50%)", rgba(0, 0, 0, 255)},
		{"hsla_green", "hsla(120, 100%, 75%, 0.5)", rgba(128, 255, 128, 128)},
		{"hsl_with_alpha", "hsl(120, 100%, 75%, 0.5)", rgba(128, 255, 128, 128)},
		{"hsl_to_rgb_red_round_up", "hsl(230, 57%, 54%)", rgba(71, 93, 205, 255)},
		{"hsl_with_hue_float", "hsl(120.152, 100%, 75%)", rgba(128, 255, 128, 255)},
		{"hsla_with_hue_float", "hsla(120.152, 100%, 75%, 0.5)", rgba(128, 255, 128, 128)},
	}
	for _, c := range cases {
		got, err := stParseColor(c.text)
		if err != nil || got != c.want {
			t.Errorf("%s: stParseColor(%q) = %v, %v; want %v", c.name, c.text, got, err, c.want)
		}
	}
}

func TestSTColorParseErr(t *testing.T) {
	cases := []struct{ name, text, want string }{
		{"not_a_color_1", "text", "invalid value"},
		{"icc_color_not_supported_1", "#CD853F icc-color(acmecmyk, 0.11, 0.48, 0.83, 0.00)", "unexpected data at position 9"},
		{"icc_color_not_supported_2", "red icc-color(acmecmyk, 0.11, 0.48, 0.83, 0.00)", "unexpected data at position 5"},
		{"invalid_input_1", "rgb(-0\x0d", "unexpected end of stream"},
		{"invalid_input_2", "#9ߞpx! ;", "invalid value"},
		{"rgba_with_percent_alpha", "rgba(10, 20, 30, 5%)", "expected ')' not '%' at position 19"},
		{"rgb_mixed_units", "rgb(140%, -10mm, 130pt)", "invalid number at position 14"},
	}
	for _, c := range cases {
		_, err := stParseColor(c.text)
		if err == nil || err.Error() != c.want {
			t.Errorf("%s: stParseColor(%q) error = %v; want %q", c.name, c.text, err, c.want)
		}
	}
}
