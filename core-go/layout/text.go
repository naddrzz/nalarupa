package layout

import (
	"math"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/jsonv"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

// TextStyle is what text measurement needs from a node (text.rs `TextStyle`).
type TextStyle struct {
	Family        string
	Size          float64
	Weight        uint16
	Italic        bool
	LetterSpacing float64
	// LineHeight is a multiple of Size; 0 means the font's natural line height.
	LineHeight float64
}

// StyleOf reads the text style of a node.
func StyleOf(n *resolve.RNode) TextStyle {
	st := TextStyle{Family: "Inter", Size: 14}
	if s, ok := n.S("fontFamily"); ok {
		st.Family = s
	}
	if f, ok := n.F("fontSize"); ok {
		st.Size = f
	}
	st.Size = fmax(st.Size, 1)
	w, _ := n.P.Get("fontWeight")
	st.Weight = WeightNum(w)
	if s, ok := n.S("fontStyle"); ok && strings.EqualFold(s, "italic") {
		st.Italic = true
	}
	st.LetterSpacing, _ = n.F("letterSpacing")
	if f, ok := n.F("lineHeight"); ok && f > 0 {
		st.LineHeight = f
	}
	return st
}

// WeightNum is "normal", "bold" or a number (1..1000, rounded); any other name ("semibold", "medium") is 400.
func WeightNum(v jsonv.Value) uint16 {
	var w float64
	ok := false
	switch x := v.(type) {
	case jsonv.Number:
		w, ok = x.Float64()
	case string:
		switch s := strings.TrimSpace(x); s {
		case "normal":
			w, ok = 400, true
		case "bold":
			w, ok = 700, true
		default:
			w, ok = resolve.ParseFloat(s)
		}
	}
	if !ok || !(w >= 1 && w <= 1000) {
		return 400
	}
	return uint16(math.Round(w))
}

// Metrics are the vertical metrics of a text style.
type Metrics struct {
	Ascent, Descent float64
	// LineH is whole pixels, like SkParagraph.
	LineH float64
	// Baseline is the baseline offset from the top of a line.
	Baseline float64
}

// TextMeasurer measures and wraps text. Layout takes it from outside so the document model needs no fonts.
type TextMeasurer interface {
	Metrics(st TextStyle) Metrics
	// Width is the advance of text (trailing spaces included) plus letter spacing after every cluster.
	Width(text string, st TextStyle) float64
	// Wrap breaks text into lines no wider than maxW: `\n` and the other UAX #14 mandatory breaks (CR, U+2028,
	// …) always break, trailing spaces hang, and a maxW of 0 puts one word per line. An infinite maxW only
	// breaks at the mandatory breaks.
	Wrap(text string, maxW float64, st TextStyle) []resolve.Line
}
