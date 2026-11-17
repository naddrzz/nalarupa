package text

// UAX #14 line break opportunities exactly as unicode-linebreak 0.1.5 (Unicode 15.0) finds them, which is
// what the Rust core wraps with. go-text's segmenter follows Unicode 17 and breaks differently around
// quotes and word-initial hyphens, so the Rust crate's state machine and tables are ported instead.

const (
	lbAllowedBit   = 0x80
	lbMandatoryBit = 0x40
	lbZWJ          = 10
	lbXX           = 42
	lbEOT          = 43
	lbSOT          = 44
)

func breakProperty(cp rune) uint8 {
	const (
		bmpLimit  = 0x10000
		shift3    = 4
		shift2    = 5 + shift3
		shift1    = 5 + shift2
		bmpShift  = 6
		index2Len = 1 << (shift1 - shift2)
		index3Len = 1 << (shift2 - shift3)
		smallLen  = 1 << shift3
		bmpLen    = 1 << bmpShift
		bmpIndex  = bmpLimit >> bmpShift
		omitted   = bmpLimit >> shift1
	)
	c := uint32(cp)
	var pos uint16
	switch {
	case c < bmpLimit:
		pos = lbTrieIndex[c>>bmpShift] + uint16(c&(bmpLen-1))
	case c < lbHighStart:
		i2 := lbTrieIndex[(c>>shift1)+bmpIndex-omitted] + uint16((c>>shift2)&(index2Len-1))
		block := lbTrieIndex[i2]
		data := lbTrieIndex[block+uint16((c>>shift3)&(index3Len-1))]
		pos = data + uint16(c&(smallLen-1))
	default:
		return lbXX
	}
	return lbTrieData[pos]
}

// lineBreak is a break opportunity before the byte at `at` (len(s) for the end of the text).
type lineBreak struct {
	at        int
	mandatory bool
}

// lineBreaks lists the break opportunities of s, ending with the mandatory one at the end of the text.
func lineBreaks(s string) []lineBreak {
	var out []lineBreak
	state, zwj := uint8(lbSOT), false
	step := func(i int, cls uint8) {
		val := lbPairTable[state][cls]
		mandatory := val&lbMandatoryBit != 0
		if val&lbAllowedBit != 0 && (!zwj || mandatory) {
			out = append(out, lineBreak{i, mandatory})
		}
		state, zwj = val&^(lbAllowedBit|lbMandatoryBit), cls == lbZWJ
	}
	for i, r := range s {
		step(i, breakProperty(r))
	}
	step(len(s), lbEOT)
	return out
}
