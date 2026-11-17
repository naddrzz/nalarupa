// Port of unicode-bidi 0.3.18 (lib.rs, explicit.rs, prepare.rs, implicit.rs, level.rs, char_data): the
// Unicode Bidirectional Algorithm as usvg's shape_text_with_font runs it, BidiInfo::new(text, Some(LTR))
// then visual_runs over the first paragraph. Like the crate, it works on byte indices: the classes and
// levels of a multi-byte character repeat for each of its bytes, and the weak and neutral rules visit
// every byte.

package usvg

import (
	"sort"
	"unicode/utf8"
)

// bidiClass is the Bidi_Class property (BidiClass), in the crate's order.
type bidiClass uint8

const (
	bidiAL bidiClass = iota
	bidiAN
	bidiB
	bidiBN
	bidiCS
	bidiEN
	bidiES
	bidiET
	bidiFSI
	bidiL
	bidiLRE
	bidiLRI
	bidiLRO
	bidiNSM
	bidiON
	bidiPDF
	bidiPDI
	bidiR
	bidiRLE
	bidiRLI
	bidiRLO
	bidiS
	bidiWS
)

// bidiClassOf is bidi_class: code points not in the table are L.
func bidiClassOf(c rune) bidiClass {
	i := sort.Search(len(bidiClassTable), func(i int) bool { return bidiClassTable[i].hi >= c })
	if i < len(bidiClassTable) && bidiClassTable[i].lo <= c {
		return bidiClassTable[i].class
	}
	return bidiL
}

// bidiMatchedOpeningBracket is bidi_matched_opening_bracket: the normalized opening bracket of a
// bracket pair c belongs to, and whether c opens it.
func bidiMatchedOpeningBracket(c rune) (opening rune, isOpen, ok bool) {
	for _, p := range bidiPairsTable {
		if p[0] == c || p[1] == c {
			opening = p[0]
			if p[2] != 0 {
				opening = p[2]
			}
			return opening, p[0] == c, true
		}
	}
	return 0, false, false
}

// Embedding levels (level.rs).
const (
	bidiMaxExplicitDepth = 125
	bidiMaxImplicitDepth = 126
)

func levelIsRTL(l uint8) bool { return l&1 == 1 }

func levelClass(l uint8) bidiClass {
	if levelIsRTL(l) {
		return bidiR
	}
	return bidiL
}

func removedByX9(c bidiClass) bool {
	switch c {
	case bidiRLE, bidiLRE, bidiRLO, bidiLRO, bidiPDF, bidiBN:
		return true
	}
	return false
}

// bidiRun is a LevelRun, a byte range.
type bidiRun struct{ start, end int }

// bidiCharAt is TextSource::char_at for str: the character starting at byte i, or false inside one.
func bidiCharAt(text string, i int) (rune, int, bool) {
	if i < 0 || i >= len(text) || !utf8.RuneStart(text[i]) {
		return 0, 0, false
	}
	r, n := utf8.DecodeRuneInString(text[i:])
	return r, n, true
}

// bidiVisualRuns gives text's first paragraph as level runs in visual order, with each byte's
// reordered level (BidiInfo::new with an LTR default, then visual_runs over paragraphs[0].range). It
// returns no runs for empty text.
func bidiVisualRuns(text string) ([]uint8, []bidiRun) {
	// compute_initial_info with a fixed LTR paragraph level: classes, the first paragraph, its flags.
	classes := make([]bidiClass, 0, len(text))
	paraEnd := len(text)
	pureLTR, hasIsolates := true, false
	var isolates []int
	for i, c := range text {
		class := bidiClassOf(c)
		n := utf8.RuneLen(c)
		if n < 0 {
			n = 1
		}
		for j := 0; j < n; j++ {
			classes = append(classes, class)
		}
		switch class {
		case bidiB:
			paraEnd = i + n
		case bidiL, bidiR, bidiAL:
			if class != bidiL {
				pureLTR = false
			}
			if k := len(isolates); k > 0 {
				if start := isolates[k-1]; classes[start] == bidiFSI {
					v := bidiLRI
					if class != bidiL {
						v = bidiRLI
					}
					for j := 0; j < 3; j++ { // FSI is 3 bytes
						classes[start+j] = v
					}
				}
			}
		case bidiAN, bidiLRE, bidiRLE, bidiLRO, bidiRLO:
			pureLTR = false
		case bidiRLI, bidiLRI, bidiFSI:
			pureLTR, hasIsolates = false, true
			isolates = append(isolates, i)
		case bidiPDI:
			if k := len(isolates); k > 0 {
				isolates = isolates[:k-1]
			}
		}
		if class == bidiB {
			break
		}
	}
	if len(text) == 0 {
		return nil, nil
	}
	// The classes of the first paragraph only: later paragraphs do not affect it.
	text = text[:paraEnd]
	classes = classes[:paraEnd]

	levels := make([]uint8, len(text))
	if !pureLTR {
		processing := append([]bidiClass(nil), classes...)
		runs := bidiExplicit(text, 0, classes, levels, processing)
		for _, seq := range bidiIsolatingRunSequences(0, classes, levels, runs, hasIsolates) {
			bidiResolveWeak(text, seq, processing)
			bidiResolveNeutral(text, seq, levels, classes, processing)
		}
		bidiResolveLevels(processing, levels)
		for i := range levels {
			if removedByX9(classes[i]) {
				if i > 0 {
					levels[i] = levels[i-1]
				} else {
					levels[i] = 0
				}
			}
		}
	}
	bidiReorderLevels(classes, levels, text, 0)
	return levels, bidiVisualRunsForLine(levels, 0, len(text))
}

type bidiStatus struct {
	level  uint8
	status uint8 // 0 neutral, 1 RTL, 2 LTR, 3 isolate
}

// bidiExplicit is explicit::compute (X1-X8) and returns the level runs (BD7).
func bidiExplicit(text string, paraLevel uint8, classes []bidiClass, levels []uint8, processing []bidiClass) []bidiRun {
	stack := []bidiStatus{{paraLevel, 0}}
	var overflowIsolate, overflowEmbedding, validIsolate uint32
	var runs []bidiRun
	var runLevel uint8
	runStart := 0
	override := func(i int, s uint8) {
		switch s {
		case 1:
			processing[i] = bidiR
		case 2:
			processing[i] = bidiL
		}
	}
	for i, c := range text {
		n := utf8.RuneLen(c)
		if n < 0 {
			n = 1
		}
		last := stack[len(stack)-1]
		switch classes[i] {
		case bidiRLE, bidiLRE, bidiRLO, bidiLRO, bidiRLI, bidiLRI, bidiFSI:
			levels[i] = last.level
			isIsolate := classes[i] == bidiRLI || classes[i] == bidiLRI || classes[i] == bidiFSI
			if isIsolate {
				override(i, last.status)
			}
			var newLevel uint8
			if classes[i] == bidiRLE || classes[i] == bidiRLO || classes[i] == bidiRLI {
				newLevel = (last.level + 1) | 1
			} else {
				newLevel = (last.level + 2) &^ 1
			}
			if newLevel <= bidiMaxExplicitDepth && overflowIsolate == 0 && overflowEmbedding == 0 {
				var st uint8
				switch classes[i] {
				case bidiRLO:
					st = 1
				case bidiLRO:
					st = 2
				case bidiRLI, bidiLRI, bidiFSI:
					st = 3
				}
				stack = append(stack, bidiStatus{newLevel, st})
				if isIsolate {
					validIsolate++
				} else {
					levels[i] = newLevel
				}
			} else if isIsolate {
				overflowIsolate++
			} else if overflowIsolate == 0 {
				overflowEmbedding++
			}
			if !isIsolate {
				processing[i] = bidiBN
			}
		case bidiPDI:
			if overflowIsolate > 0 {
				overflowIsolate--
			} else if validIsolate > 0 {
				overflowEmbedding = 0
				for len(stack) > 0 {
					top := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					if top.status == 3 {
						break
					}
				}
				validIsolate--
			}
			last := stack[len(stack)-1]
			levels[i] = last.level
			override(i, last.status)
		case bidiPDF:
			if overflowIsolate > 0 {
			} else if overflowEmbedding > 0 {
				overflowEmbedding--
			} else if last.status != 3 && len(stack) >= 2 {
				stack = stack[:len(stack)-1]
			}
			levels[i] = stack[len(stack)-1].level
			processing[i] = bidiBN
		case bidiB:
		default:
			levels[i] = last.level
			if classes[i] != bidiBN {
				override(i, last.status)
			}
		}
		for j := 1; j < n; j++ {
			levels[i+j] = levels[i]
			processing[i+j] = processing[i]
		}
		if i == 0 {
			runLevel = levels[i]
		} else if !removedByX9(classes[i]) && levels[i] != runLevel {
			runs = append(runs, bidiRun{runStart, i})
			runLevel = levels[i]
			runStart = i
		}
	}
	if len(levels) > runStart {
		runs = append(runs, bidiRun{runStart, len(levels)})
	}
	return runs
}

// bidiSequence is an IsolatingRunSequence.
type bidiSequence struct {
	runs     []bidiRun
	sos, eos bidiClass
}

// forwardsFrom lists the byte indices of the sequence from pos (in run runIndex) on.
func (s *bidiSequence) forwardsFrom(pos, runIndex int) []int {
	var out []int
	for i := pos; i < s.runs[runIndex].end; i++ {
		out = append(out, i)
	}
	for _, r := range s.runs[runIndex+1:] {
		for i := r.start; i < r.end; i++ {
			out = append(out, i)
		}
	}
	return out
}

// backwardsFrom lists the byte indices of the sequence before pos (in run runIndex), nearest first.
func (s *bidiSequence) backwardsFrom(pos, runIndex int) []int {
	var out []int
	for i := pos - 1; i >= s.runs[runIndex].start; i-- {
		out = append(out, i)
	}
	for k := runIndex - 1; k >= 0; k-- {
		r := s.runs[k]
		for i := r.end - 1; i >= r.start; i-- {
			out = append(out, i)
		}
	}
	return out
}

func (s *bidiSequence) all() []int {
	var out []int
	for _, r := range s.runs {
		for i := r.start; i < r.end; i++ {
			out = append(out, i)
		}
	}
	return out
}

func maxU8(a, b uint8) uint8 {
	if a > b {
		return a
	}
	return b
}

// bidiIsolatingRunSequences is prepare::isolating_run_sequences (BD13, X10).
func bidiIsolatingRunSequences(paraLevel uint8, classes []bidiClass, levels []uint8, runs []bidiRun, hasIsolates bool) []bidiSequence {
	predLevel := func(start int) uint8 {
		for i := start - 1; i >= 0; i-- {
			if !removedByX9(classes[i]) {
				return levels[i]
			}
		}
		return paraLevel
	}
	succLevel := func(end int) uint8 {
		for i := end; i < len(classes); i++ {
			if !removedByX9(classes[i]) {
				return levels[i]
			}
		}
		return paraLevel
	}
	var out []bidiSequence
	if !hasIsolates {
		for _, run := range runs {
			first, last := 0, run.end-run.start-1
			for i := run.start; i < run.end; i++ {
				if !removedByX9(classes[i]) {
					first = i - run.start
					break
				}
			}
			for i := run.end - 1; i >= run.start; i-- {
				if !removedByX9(classes[i]) {
					last = i - run.start
					break
				}
			}
			seqLevel, endLevel := levels[run.start+first], levels[run.start+last]
			out = append(out, bidiSequence{
				runs: []bidiRun{run},
				sos:  levelClass(maxU8(seqLevel, predLevel(run.start))),
				eos:  levelClass(maxU8(endLevel, succLevel(run.end))),
			})
		}
		return out
	}

	var sequences [][]bidiRun
	stack := [][]bidiRun{nil}
	for _, run := range runs {
		startClass := classes[run.start]
		endClass := startClass
		for i := run.end - 1; i >= run.start; i-- {
			if !removedByX9(classes[i]) {
				endClass = classes[i]
				break
			}
		}
		var seq []bidiRun
		if startClass == bidiPDI && len(stack) > 1 {
			seq = stack[len(stack)-1]
			stack = stack[:len(stack)-1]
		}
		seq = append(seq, run)
		if endClass == bidiRLI || endClass == bidiLRI || endClass == bidiFSI {
			stack = append(stack, seq)
		} else {
			sequences = append(sequences, seq)
		}
	}
	for k := len(stack) - 1; k >= 0; k-- {
		if len(stack[k]) > 0 {
			sequences = append(sequences, stack[k])
		}
	}
	for _, runs := range sequences {
		startOfSeq := runs[0].start
		endOfSeq := runs[len(runs)-1].end
		seq := bidiSequence{runs: runs, sos: bidiL, eos: bidiL}
		seqIdx := startOfSeq
		for _, i := range seq.forwardsFrom(startOfSeq, 0) {
			if !removedByX9(classes[i]) {
				seqIdx = i
				break
			}
		}
		endIdx := endOfSeq - 1
		for _, i := range seq.backwardsFrom(endOfSeq, len(runs)-1) {
			if !removedByX9(classes[i]) {
				endIdx = i
				break
			}
		}
		lastNonRemoved := bidiBN
		for i := endOfSeq - 1; i >= 0; i-- {
			if !removedByX9(classes[i]) {
				lastNonRemoved = classes[i]
				break
			}
		}
		succ := paraLevel
		if lastNonRemoved != bidiRLI && lastNonRemoved != bidiLRI && lastNonRemoved != bidiFSI {
			succ = succLevel(endOfSeq)
		}
		seq.sos = levelClass(maxU8(levels[seqIdx], predLevel(startOfSeq)))
		seq.eos = levelClass(maxU8(levels[endIdx], succ))
		out = append(out, seq)
	}
	return out
}

// bidiResolveWeak is implicit::resolve_weak (W1-W7).
func bidiResolveWeak(text string, seq bidiSequence, pc []bidiClass) {
	prevW4, prevW5, prevW1 := seq.sos, seq.sos, seq.sos
	lastStrongIsAL := false
	var etRun, bnRun []int
	for runIndex, run := range seq.runs {
		for i := run.start; i < run.end; i++ {
			if pc[i] == bidiBN {
				bnRun = append(bnRun, i)
				continue
			}
			w2 := pc[i]
			if pc[i] == bidiNSM {
				switch prevW1 {
				case bidiRLI, bidiLRI, bidiFSI, bidiPDI:
					pc[i] = bidiON
				default:
					pc[i] = prevW1
				}
				w2 = pc[i]
			}
			prevW1 = pc[i]
			switch pc[i] {
			case bidiEN:
				if lastStrongIsAL {
					pc[i] = bidiAN
				}
			case bidiAL:
				pc[i] = bidiR
			}
			switch w2 {
			case bidiL, bidiR:
				lastStrongIsAL = false
			case bidiAL:
				lastStrongIsAL = true
			}
			classBeforeW456 := pc[i]
			switch pc[i] {
			case bidiEN:
				for _, j := range etRun {
					pc[j] = bidiEN
				}
				etRun = etRun[:0]
			case bidiES, bidiCS:
				if _, n, ok := bidiCharAt(text, i); ok {
					next := seq.eos
					for _, j := range seq.forwardsFrom(i+n, runIndex) {
						if !removedByX9(pc[j]) {
							next = pc[j]
							break
						}
					}
					if next == bidiEN && lastStrongIsAL {
						next = bidiAN
					}
					switch {
					case prevW4 == bidiEN && (pc[i] == bidiES || pc[i] == bidiCS) && next == bidiEN:
						pc[i] = bidiEN
					case prevW4 == bidiAN && pc[i] == bidiCS && next == bidiAN:
						pc[i] = bidiAN
					default:
						pc[i] = bidiON
					}
					if pc[i] == bidiON {
						for _, j := range seq.backwardsFrom(i, runIndex) {
							if pc[j] != bidiBN {
								break
							}
							pc[j] = bidiON
						}
						for _, j := range seq.forwardsFrom(i+n, runIndex) {
							if pc[j] != bidiBN {
								break
							}
							pc[j] = bidiON
						}
					}
				} else {
					pc[i] = pc[i-1]
				}
			case bidiET:
				if prevW5 == bidiEN {
					pc[i] = bidiEN
				} else {
					etRun = append(etRun, bnRun...)
					etRun = append(etRun, i)
				}
			}
			bnRun = bnRun[:0]
			prevW5 = pc[i]
			if prevW5 != bidiET {
				for _, j := range etRun {
					pc[j] = bidiON
				}
				etRun = etRun[:0]
			}
			prevW4 = classBeforeW456
		}
	}
	for _, j := range etRun {
		pc[j] = bidiON
	}
	lastStrongIsL := seq.sos == bidiL
	for _, i := range seq.all() {
		switch pc[i] {
		case bidiEN:
			if lastStrongIsL {
				pc[i] = bidiL
			}
		case bidiL:
			lastStrongIsL = true
		case bidiR, bidiAL:
			lastStrongIsL = false
		}
	}
}

type bidiBracketPair struct{ start, end, startRun, endRun int }

func isNI(c bidiClass) bool {
	switch c {
	case bidiB, bidiS, bidiWS, bidiON, bidiFSI, bidiLRI, bidiRLI, bidiPDI:
		return true
	}
	return false
}

// bidiResolveNeutral is implicit::resolve_neutral (N0-N2).
func bidiResolveNeutral(text string, seq bidiSequence, levels []uint8, classes, pc []bidiClass) {
	e := levelClass(levels[seq.runs[0].start])
	notE := bidiL
	if e == bidiL {
		notE = bidiR
	}
	for _, pair := range bidiBracketPairs(text, seq, pc) {
		foundE, foundNotE := false, false
		var classToSet bidiClass
		set := false
		_, startLen, _ := bidiCharAt(text, pair.start)
		for _, i := range seq.forwardsFrom(pair.start+startLen, pair.startRun) {
			if i >= pair.end {
				break
			}
			class := pc[i]
			if class == e {
				foundE = true
			} else if class == notE {
				foundNotE = true
			} else if class == bidiEN || class == bidiAN {
				if e == bidiL {
					foundNotE = true
				} else {
					foundE = true
				}
			}
			if foundE {
				break
			}
		}
		if foundE {
			classToSet, set = e, true
		} else if foundNotE {
			prev := seq.sos
			for _, i := range seq.backwardsFrom(pair.start, pair.startRun) {
				if c := pc[i]; c == bidiL || c == bidiR || c == bidiEN || c == bidiAN {
					prev = c
					break
				}
			}
			if prev == bidiEN || prev == bidiAN {
				prev = bidiR
			}
			classToSet, set = prev, true
		}
		if !set {
			continue
		}
		_, endLen, _ := bidiCharAt(text, pair.end)
		for i := pair.start; i < pair.start+startLen; i++ {
			pc[i] = classToSet
		}
		for i := pair.end; i < pair.end+endLen; i++ {
			pc[i] = classToSet
		}
		for _, i := range seq.backwardsFrom(pair.start, pair.startRun) {
			if pc[i] != bidiBN {
				break
			}
			pc[i] = classToSet
		}
		for _, i := range seq.forwardsFrom(pair.start+startLen, pair.startRun) {
			if classes[i] == bidiNSM || pc[i] == bidiBN {
				pc[i] = classToSet
			} else {
				break
			}
		}
		for _, i := range seq.forwardsFrom(pair.end+endLen, pair.endRun) {
			if classes[i] == bidiNSM || pc[i] == bidiBN {
				pc[i] = classToSet
			} else {
				break
			}
		}
	}

	indices := seq.all()
	prev := seq.sos
	for k := 0; k < len(indices); k++ {
		i := indices[k]
		if isNI(pc[i]) || pc[i] == bidiBN {
			niRun := []int{i}
			var next bidiClass
			for {
				k++
				if k >= len(indices) {
					next = seq.eos
					break
				}
				i = indices[k]
				next = pc[i]
				if isNI(next) || next == bidiBN {
					niRun = append(niRun, i)
				} else {
					break
				}
			}
			var newClass bidiClass
			switch {
			case prev == bidiL && next == bidiL:
				newClass = bidiL
			case (prev == bidiR || prev == bidiAN || prev == bidiEN) && (next == bidiR || next == bidiAN || next == bidiEN):
				newClass = bidiR
			default:
				newClass = e
			}
			for _, j := range niRun {
				pc[j] = newClass
			}
		}
		prev = pc[i]
	}
}

// bidiBracketPairs is identify_bracket_pairs (BD16), sorted by the opening position.
func bidiBracketPairs(text string, seq bidiSequence, pc []bidiClass) []bidiBracketPair {
	type entry struct {
		opening       rune
		index, runIdx int
	}
	var stack []entry
	var pairs []bidiBracketPair
	for runIndex, run := range seq.runs {
		for off, ch := range text[run.start:run.end] {
			at := run.start + off
			if pc[at] != bidiON {
				continue
			}
			opening, isOpen, ok := bidiMatchedOpeningBracket(ch)
			if !ok {
				continue
			}
			if isOpen {
				if len(stack) >= 63 {
					break
				}
				stack = append(stack, entry{opening, at, runIndex})
				continue
			}
			for k := len(stack) - 1; k >= 0; k-- {
				if stack[k].opening == opening {
					pairs = append(pairs, bidiBracketPair{stack[k].index, at, stack[k].runIdx, runIndex})
					stack = stack[:k]
					break
				}
			}
		}
	}
	sort.SliceStable(pairs, func(a, b int) bool { return pairs[a].start < pairs[b].start })
	return pairs
}

// bidiResolveLevels is implicit::resolve_levels (I1, I2).
func bidiResolveLevels(pc []bidiClass, levels []uint8) {
	for i := range levels {
		rtl := levelIsRTL(levels[i])
		switch {
		case !rtl && (pc[i] == bidiAN || pc[i] == bidiEN):
			if levels[i]+2 <= bidiMaxImplicitDepth {
				levels[i] += 2
			} else {
				panic("usvg: bidi level number error") // Rust expects
			}
		case (!rtl && pc[i] == bidiR) || (rtl && (pc[i] == bidiL || pc[i] == bidiEN || pc[i] == bidiAN)):
			if levels[i]+1 <= bidiMaxImplicitDepth {
				levels[i]++
			} else {
				panic("usvg: bidi level number error")
			}
		}
	}
}

// bidiReorderLevels is reorder_levels (L1) over a line that starts at byte 0.
func bidiReorderLevels(classes []bidiClass, levels []uint8, text string, paraLevel uint8) {
	resetFrom, hasFrom := 0, true
	resetTo, hasTo := 0, false
	prev := paraLevel
	for i, c := range text {
		n := utf8.RuneLen(c)
		if n < 0 {
			n = 1
		}
		switch classes[i] {
		case bidiB, bidiS:
			resetTo, hasTo = i+n, true
			if !hasFrom {
				resetFrom, hasFrom = i, true
			}
		case bidiWS, bidiFSI, bidiLRI, bidiRLI, bidiPDI:
			if !hasFrom {
				resetFrom, hasFrom = i, true
			}
		case bidiRLE, bidiLRE, bidiRLO, bidiLRO, bidiPDF, bidiBN:
			if !hasFrom {
				resetFrom, hasFrom = i, true
			}
			for j := i; j < i+n; j++ {
				levels[j] = prev
			}
		default:
			hasFrom = false
		}
		if hasFrom && hasTo {
			for j := resetFrom; j < resetTo; j++ {
				levels[j] = paraLevel
			}
			hasFrom, hasTo = false, false
		}
		prev = levels[i]
	}
	if hasFrom {
		for j := resetFrom; j < len(levels); j++ {
			levels[j] = paraLevel
		}
	}
}

// bidiVisualRunsForLine is visual_runs_for_line (L2): the level runs of [start, end) in visual order.
func bidiVisualRunsForLine(levels []uint8, start, end int) []bidiRun {
	var runs []bidiRun
	runLevel := levels[start]
	minLevel, maxLevel := runLevel, runLevel
	for i := start + 1; i < end; i++ {
		if levels[i] != runLevel {
			runs = append(runs, bidiRun{start, i})
			start = i
			runLevel = levels[i]
			minLevel = min(runLevel, minLevel)
			maxLevel = max(runLevel, maxLevel)
		}
	}
	runs = append(runs, bidiRun{start, end})
	minLevel |= 1
	for maxLevel >= minLevel {
		for seqStart := 0; seqStart < len(runs); {
			if levels[runs[seqStart].start] < maxLevel {
				seqStart++
				continue
			}
			seqEnd := seqStart + 1
			for seqEnd < len(runs) && levels[runs[seqEnd].start] >= maxLevel {
				seqEnd++
			}
			for a, b := seqStart, seqEnd-1; a < b; a, b = a+1, b-1 {
				runs[a], runs[b] = runs[b], runs[a]
			}
			seqStart = seqEnd
		}
		maxLevel--
	}
	return runs
}
