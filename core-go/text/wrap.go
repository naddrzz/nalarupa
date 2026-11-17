package text

import (
	"sort"
	"strings"
	"unicode"

	"github.com/naddrzz/nalarupa/core-go/layout"
	"github.com/naddrzz/nalarupa/core-go/resolve"
)

// fitEps is the width slack when fitting text into a line (float noise between equal measurements).
const fitEps = 0.01

// Wrap is greedy UAX #14 line breaking (CJK breaks between ideographs): `\n` always breaks, trailing spaces
// hang, and words wider than maxW break between clusters unless maxW is 0 (no width at all: one word per
// line). No hyphenation.
func (f *Fonts) Wrap(text string, maxW float64, st layout.TextStyle) []resolve.Line {
	var out []resolve.Line
	for _, para := range strings.Split(text, "\n") {
		para = strings.TrimSuffix(para, "\r")
		cl := f.clusters(para, st)
		// prefix[i] is the advance of the clusters before cl[i].
		prefix := make([]float64, len(cl)+1)
		for i, c := range cl {
			prefix[i+1] = prefix[i] + c.adv*st.Size + st.LetterSpacing
		}
		first := func(b int) int { return sort.Search(len(cl), func(i int) bool { return cl[i].at >= b }) }
		w := func(a, b int) float64 { return prefix[first(b)] - prefix[first(a)] }
		trimmed := func(a, b int) int { return a + len(strings.TrimRightFunc(para[a:b], unicode.IsSpace)) }
		type span struct {
			a, b int
			soft bool
		}
		var lines []span
		start, end, lineW := 0, 0, 0.0
		for _, br := range lineBreaks(para) {
			te := trimmed(end, br.at)
			// At zero width a whitespace-only prefix is not a line of its own: the word joins it.
			blank := maxW == 0 && trimmed(start, end) == start
			if end > start && !blank && lineW+w(end, te) > maxW+fitEps {
				lines = append(lines, span{start, end, true})
				start, lineW = end, 0
			}
			if end == start && maxW > 0 && w(end, te) > maxW+fitEps {
				for i := first(end); i < first(te); i++ {
					b, a := cl[i].at, cl[i].adv*st.Size+st.LetterSpacing
					if b > start && lineW+a > maxW+fitEps {
						lines = append(lines, span{start, b, true})
						start, lineW = b, 0
					}
					lineW += a
				}
				lineW += w(te, br.at)
			} else {
				lineW += w(end, br.at)
			}
			end = br.at
			if br.mandatory && br.at < len(para) {
				lines = append(lines, span{start, end, false})
				start, lineW = end, 0
			}
		}
		lines = append(lines, span{start, len(para), false})
		for _, l := range lines {
			t := strings.TrimRightFunc(para[l.a:l.b], unicode.IsSpace)
			out = append(out, resolve.Line{Text: t, Width: w(l.a, l.a+len(t)), Soft: l.soft})
		}
	}
	return out
}
