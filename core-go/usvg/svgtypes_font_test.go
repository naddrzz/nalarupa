// Port of svgtypes 0.16.1 src/font.rs tests

package usvg

import (
	"slices"
	"testing"
)

func TestSTFontFamilies(t *testing.T) {
	named := func(s string) stFontFamily { return stFontFamily{Kind: stFontFamilyNamed, Name: s} }
	serif := stFontFamily{Kind: stFontFamilySerif}
	sans := stFontFamily{Kind: stFontFamilySansSerif}
	fantasy := stFontFamily{Kind: stFontFamilyFantasy}
	mono := stFontFamily{Kind: stFontFamilyMonospace}
	cursive := stFontFamily{Kind: stFontFamilyCursive}
	cases := []struct {
		text string
		want []stFontFamily
	}{
		{"Times New Roman", []stFontFamily{named("Times New Roman")}},
		{"serif", []stFontFamily{serif}},
		{"sans-serif", []stFontFamily{sans}},
		{"cursive", []stFontFamily{cursive}},
		{"fantasy", []stFontFamily{fantasy}},
		{"monospace", []stFontFamily{mono}},
		{"'Times New Roman'", []stFontFamily{named("Times New Roman")}},
		{"'Times New Roman', sans-serif", []stFontFamily{named("Times New Roman"), sans}},
		{"'Times New Roman', sans-serif", []stFontFamily{named("Times New Roman"), sans}},
		{"Arial, sans-serif, 'fantasy'", []stFontFamily{named("Arial"), sans, named("fantasy")}},
		{"    Arial  , monospace  , 'fantasy'", []stFontFamily{named("Arial"), mono, named("fantasy")}},
		{"Times    New Roman", []stFontFamily{named("Times New Roman")}},
		{"\"Times New Roman\", sans-serif, sans-serif, \"Arial\"", []stFontFamily{named("Times New Roman"), sans, sans, named("Arial")}},
		{"Times New Roman,,,Arial", []stFontFamily{named("Times New Roman"), named("Arial")}},
		{"简体中文,sans-serif  , ,\"日本語フォント\",Arial", []stFontFamily{named("简体中文"), sans, named("日本語フォント"), named("Arial")}},
		{"", nil},
	}
	for _, c := range cases {
		got, err := stParseFontFamilies(c.text)
		if err != nil || !slices.Equal(got, c.want) {
			t.Errorf("stParseFontFamilies(%q) = %v, %v; want %v", c.text, got, err, c.want)
		}
	}
}

func TestSTFontFamiliesErr(t *testing.T) {
	for _, c := range []struct{ text, want string }{
		{"Red/Black, sans-serif", "invalid ident"},
		{"\"Lucida\" Grande, sans-serif", "unexpected data at position 10"},
		{"Ahem!, sans-serif", "invalid ident"},
		{"test@foo, sans-serif", "invalid ident"},
		{"#POUND, sans-serif", "invalid ident"},
		{"Hawaii 5-0, sans-serif", "invalid ident"},
	} {
		_, err := stParseFontFamilies(c.text)
		if err == nil || err.Error() != c.want {
			t.Errorf("stParseFontFamilies(%q) error = %v; want %q", c.text, err, c.want)
		}
	}
}

func TestSTFontShorthand(t *testing.T) {
	fs := func(style, variant, weight, stretch, size, family string) stFontShorthand {
		return stFontShorthand{style, variant, weight, stretch, size, family}
	}
	cases := []struct {
		text string
		want stFontShorthand
	}{
		{"12pt/14pt sans-serif", fs("", "", "", "", "12pt", "sans-serif")},
		{"80% sans-serif", fs("", "", "", "", "80%", "sans-serif")},
		{"bold italic large Palatino, serif", fs("italic", "", "bold", "", "large", "Palatino, serif")},
		{"x-large/110% \"new century schoolbook\", serif", fs("", "", "", "", "x-large", "\"new century schoolbook\", serif")},
		{"normal small-caps 120%/120% fantasy", fs("", "small-caps", "", "", "120%", "fantasy")},
		{"condensed oblique 12pt \"Helvetica Neue\", serif", fs("oblique", "", "", "condensed", "12pt", "\"Helvetica Neue\", serif")},
		{"italic 500 2em sans-serif, 'Noto Sans'", fs("italic", "", "500", "", "2em", "sans-serif, 'Noto Sans'")},
		{"xx-large 'Noto Sans'", fs("", "", "", "", "xx-large", "'Noto Sans'")},
		{"small-caps normal normal italic xx-small Times", fs("italic", "small-caps", "", "", "xx-small", "Times")},
	}
	for _, c := range cases {
		got, err := stParseFontShorthand(c.text)
		if err != nil || got != c.want {
			t.Errorf("stParseFontShorthand(%q) = %+v, %v; want %+v", c.text, got, err, c.want)
		}
	}
}

func TestSTFontShorthandErr(t *testing.T) {
	cases := []struct {
		text string
		want stError
	}{
		{"", stError{Kind: stErrUnexpectedEndOfStream}},
		{"Noto Sans", stError{Kind: stErrUnexpectedData, Pos: 0}},
		{"12pt  ", stError{Kind: stErrUnexpectedEndOfStream}},
		{"something 12pt 'Noto Sans'", stError{Kind: stErrUnexpectedData, Pos: 0}},
		{"'Noto Sans' 13pt", stError{Kind: stErrUnexpectedData, Pos: 0}},
		{"small-caps normal normal normal italic xx-large Times", stError{Kind: stErrUnexpectedData, Pos: 32}},
	}
	for _, c := range cases {
		_, err := stParseFontShorthand(c.text)
		e, ok := err.(*stError)
		if !ok || e.Kind != c.want.Kind || e.Pos != c.want.Pos {
			t.Errorf("stParseFontShorthand(%q) error = %v; want %v", c.text, err, c.want.Error())
		}
	}
}
