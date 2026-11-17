package export

import (
	"encoding/binary"
	"errors"
	"math/bits"
	"sort"
)

// Port of the lossless encoder of image-webp 0.2.4 (src/encoder.rs): one VP8L frame in the simple container,
// with the subtract-green and predictor transforms, no color cache and one set of Huffman codes. Only
// ColorType::Rgba8 without metadata is ported: what render.rs `encode_tree` uses.

var errInvalidDimensions = errors.New("Invalid dimensions")

type bitWriter struct {
	out    []byte
	buffer uint64
	nbits  uint8
}

func (w *bitWriter) writeBits(v uint64, nbits uint8) {
	w.buffer |= v << w.nbits
	w.nbits += nbits
	if w.nbits >= 64 {
		w.out = binary.LittleEndian.AppendUint64(w.out, w.buffer)
		w.nbits -= 64
		w.buffer = v >> (nbits - w.nbits) // a shift of 64 gives 0, as checked_shr's None
	}
}

func (w *bitWriter) flush() {
	if w.nbits%8 != 0 {
		w.writeBits(0, 8-w.nbits%8)
	}
	if w.nbits > 0 {
		var b [8]byte
		binary.LittleEndian.PutUint64(b[:], w.buffer)
		w.out = append(w.out, b[:w.nbits/8]...)
		w.buffer = 0
		w.nbits = 0
	}
}

func writeSingleEntryHuffmanTree(w *bitWriter, symbol uint8) {
	w.writeBits(1, 2)
	if symbol <= 1 {
		w.writeBits(0, 1)
		w.writeBits(uint64(symbol), 1)
	} else {
		w.writeBits(1, 1)
		w.writeBits(uint64(symbol), 8)
	}
}

// heapItem is encoder.rs `Item`: ordered by frequency, reversed, so the max-heap pops the rarest first.
type heapItem struct {
	freq  uint32
	index uint16
}

// less is `a < b` under Item's Ord.
func (a heapItem) less(b heapItem) bool { return b.freq < a.freq }

// maxHeap mirrors std's BinaryHeap (hole-based sift_down_range, sift_down_to_bottom and sift_up), so equal
// frequencies leave the heap in the same order as in Rust.
type maxHeap []heapItem

func (h maxHeap) siftDownRange(pos, end int) {
	elem := h[pos]
	child := 2*pos + 1
	for end >= 2 && child <= end-2 {
		if !h[child+1].less(h[child]) { // h[child] <= h[child+1]
			child++
		}
		if !elem.less(h[child]) { // elem >= h[child]
			h[pos] = elem
			return
		}
		h[pos] = h[child]
		pos = child
		child = 2*pos + 1
	}
	if child == end-1 && elem.less(h[child]) {
		h[pos] = h[child]
		pos = child
	}
	h[pos] = elem
}

func (h maxHeap) siftUp(start, pos int) {
	elem := h[pos]
	for pos > start {
		parent := (pos - 1) / 2
		if !h[parent].less(elem) { // elem <= h[parent]
			break
		}
		h[pos] = h[parent]
		pos = parent
	}
	h[pos] = elem
}

func (h maxHeap) siftDownToBottom(pos int) {
	end := len(h)
	start := pos
	elem := h[pos]
	child := 2*pos + 1
	for end >= 2 && child <= end-2 {
		if !h[child+1].less(h[child]) {
			child++
		}
		h[pos] = h[child]
		pos = child
		child = 2*pos + 1
	}
	if child == end-1 {
		h[pos] = h[child]
		pos = child
	}
	h[pos] = elem
	h.siftUp(start, pos)
}

func heapFrom(items []heapItem) maxHeap {
	h := maxHeap(items)
	for n := len(h) / 2; n > 0; {
		n--
		h.siftDownRange(n, len(h))
	}
	return h
}

func (h *maxHeap) pop() heapItem {
	old := *h
	item := old[len(old)-1]
	*h = old[:len(old)-1]
	if len(*h) > 0 {
		item, (*h)[0] = (*h)[0], item
		h.siftDownToBottom(0)
	}
	return item
}

func buildHuffmanTree(frequencies []uint32, lengths []uint8, codes []uint16, lengthLimit uint8) bool {
	nonzero := 0
	for _, f := range frequencies {
		if f > 0 {
			nonzero++
		}
	}
	if nonzero <= 1 {
		clear(lengths)
		clear(codes)
		return false
	}

	// Build a Huffman tree.
	type pair struct{ left, right uint16 }
	var internal []pair
	var items []heapItem
	for i, f := range frequencies {
		if f > 0 {
			items = append(items, heapItem{f, uint16(i)})
		}
	}
	nodes := heapFrom(items)
	for len(nodes) > 1 {
		first := nodes.pop()
		root := &nodes[0] // peek_mut: sifted down when dropped
		internal = append(internal, pair{first.index, root.index})
		*root = heapItem{first.freq + root.freq, uint16(len(internal)) + uint16(len(frequencies)) - 1}
		nodes.siftDownRange(0, len(nodes))
	}

	// Walk the tree to assign code lengths.
	clear(lengths)
	type entry struct {
		node  uint16
		depth uint8
	}
	stack := []entry{{nodes.pop().index, 0}}
	for len(stack) > 0 {
		e := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		node := int(e.node)
		if node < len(frequencies) {
			lengths[node] = e.depth
		} else {
			p := internal[node-len(frequencies)]
			stack = append(stack, entry{p.left, e.depth + 1}, entry{p.right, e.depth + 1})
		}
	}

	// Limit the codes to lengthLimit.
	maxLength := uint8(0)
	for _, l := range lengths {
		maxLength = max(maxLength, l)
	}
	if maxLength > lengthLimit {
		var counts [16]uint32
		for _, l := range lengths {
			counts[min(l, lengthLimit)]++
		}
		total := uint32(0)
		for i := 1; i <= int(lengthLimit); i++ {
			total += counts[i] << (int(lengthLimit) - i)
		}
		for total > 1<<lengthLimit {
			i := int(lengthLimit) - 1
			for counts[i] == 0 {
				i--
			}
			counts[i]--
			counts[lengthLimit]--
			counts[i+1] += 2
			total--
		}

		// Assign new lengths. Rust sorts unstably; equal frequencies may get their lengths in another order
		// here, which changes the file bytes but not the decoded pixels.
		indexes := make([]int, len(frequencies))
		for i := range indexes {
			indexes[i] = i
		}
		sort.SliceStable(indexes, func(a, b int) bool { return frequencies[indexes[a]] < frequencies[indexes[b]] })
		l := lengthLimit
		for _, i := range indexes {
			if frequencies[i] > 0 {
				for counts[l] == 0 {
					l--
				}
				lengths[i] = l
				counts[l]--
			}
		}
	}

	// Assign codes.
	clear(codes)
	code := uint32(0)
	for l := uint8(1); l <= lengthLimit; l++ {
		for i, length := range lengths {
			if length == l {
				codes[i] = bits.Reverse16(uint16(code)) >> (16 - l)
				code++
			}
		}
		code <<= 1
	}
	if code != 2<<lengthLimit {
		panic("webp: huffman code space") // Rust asserts
	}
	return true
}

var codeLengthOrder = [19]int{17, 18, 0, 1, 2, 3, 4, 5, 16, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}

func writeHuffmanTree(w *bitWriter, frequencies []uint32, lengths []uint8, codes []uint16) {
	if !buildHuffmanTree(frequencies, lengths, codes, 15) {
		symbol := 0
		for i, f := range frequencies {
			if f > 0 {
				symbol = i
				break
			}
		}
		writeSingleEntryHuffmanTree(w, uint8(symbol))
		return
	}

	var clLengths [16]uint8
	var clCodes [16]uint16
	var clFrequencies [16]uint32
	for _, l := range lengths {
		clFrequencies[l]++
	}
	single := !buildHuffmanTree(clFrequencies[:], clLengths[:], clCodes[:], 7)

	w.writeBits(0, 1)    // normal huffman tree
	w.writeBits(19-4, 4) // num_code_lengths - 4
	for _, i := range codeLengthOrder {
		switch {
		case i > 15 || clFrequencies[i] == 0:
			w.writeBits(0, 3)
		case single:
			w.writeBits(1, 3)
		default:
			w.writeBits(uint64(clLengths[i]), 3)
		}
	}

	switch len(lengths) {
	case 256:
		w.writeBits(1, 1)   // max_symbol is stored
		w.writeBits(3, 3)   // max_symbol_nbits / 2 - 2
		w.writeBits(254, 8) // max_symbol - 2
	case 280:
		w.writeBits(0, 1)
	default:
		panic("webp: alphabet size") // unreachable in Rust
	}

	if !single {
		for _, l := range lengths {
			w.writeBits(uint64(clCodes[l]), clLengths[l])
		}
	}
}

func lengthToSymbol(n uint16) (uint16, uint8) {
	n--
	highest := uint16(bits.Len16(n) - 1)
	second := (n >> (highest - 1)) & 1
	return 2*highest + second, uint8(highest - 1)
}

// runLength counts the pixels from pixels[at:] that repeat pixel, up to 4096.
func runLength(pixels []byte, at int, pixel []byte) int {
	n := 0
	for n < 4096 && at+4 <= len(pixels) && string(pixels[at:at+4]) == string(pixel) {
		n++
		at += 4
	}
	return n
}

func countRun(n int, frequencies1 *[280]uint32) {
	if n == 0 {
		return
	}
	if n <= 4 {
		frequencies1[256+n-1]++
	} else {
		symbol, _ := lengthToSymbol(uint16(n))
		frequencies1[256+int(symbol)]++
	}
}

func writeRun(w *bitWriter, n int, codes1 *[280]uint16, lengths1 *[280]uint8) {
	if n == 0 {
		return
	}
	if n <= 4 {
		s := 256 + n - 1
		w.writeBits(uint64(codes1[s]), lengths1[s])
	} else {
		symbol, extra := lengthToSymbol(uint16(n))
		s := 256 + int(symbol)
		w.writeBits(uint64(codes1[s]), lengths1[s])
		w.writeBits(uint64(n-1)&(1<<extra-1), extra)
	}
}

// encodeWebP encodes straight (not premultiplied) RGBA pixels as a lossless WebP file.
func encodeWebP(data []byte, width, height uint32) ([]byte, error) {
	if uint64(width)*uint64(height)*4 != uint64(len(data)) {
		panic("webp: data size") // Rust asserts
	}
	if width == 0 || width > 16384 || height == 0 || height > 16384 {
		return nil, errInvalidDimensions
	}
	w := &bitWriter{}

	w.writeBits(0x2f, 8) // signature
	w.writeBits(uint64(width)-1, 14)
	w.writeBits(uint64(height)-1, 14)
	w.writeBits(1, 1) // alpha used
	w.writeBits(0, 3) // version

	// subtract green transform
	w.writeBits(0b101, 3)

	// predictor transform
	w.writeBits(0b111001, 6)
	w.writeBits(0, 1) // no color cache
	writeSingleEntryHuffmanTree(w, 2)
	for range 4 {
		writeSingleEntryHuffmanTree(w, 0)
	}

	w.writeBits(0, 1) // transforms done
	w.writeBits(0, 1) // color cache
	w.writeBits(0, 1) // meta-huffman codes

	pixels := append([]byte(nil), data...)

	// subtract green
	for i := 0; i < len(pixels); i += 4 {
		pixels[i] -= pixels[i+1]
		pixels[i+2] -= pixels[i+1]
	}

	// predictor: each row minus the one above, the first row minus the pixel to its left
	rowBytes := int(width) * 4
	for y := int(height) - 1; y >= 1; y-- {
		cur := pixels[y*rowBytes : (y+1)*rowBytes]
		prev := pixels[(y-1)*rowBytes : y*rowBytes]
		for i := range cur {
			cur[i] -= prev[i]
		}
	}
	for i := rowBytes - 1; i >= 4; i-- {
		pixels[i] -= pixels[i-4]
	}
	pixels[3] -= 255

	// frequencies
	var f0, f2, f3 [256]uint32
	var f1 [280]uint32
	for at := 0; at < len(pixels); {
		pixel := pixels[at : at+4]
		at += 4
		f0[pixel[0]]++
		f1[pixel[1]]++
		f2[pixel[2]]++
		f3[pixel[3]]++
		n := runLength(pixels, at, pixel)
		at += n * 4
		countRun(n, &f1)
	}

	var l0, l2, l3 [256]uint8
	var l1 [280]uint8
	var c0, c2, c3 [256]uint16
	var c1 [280]uint16
	writeHuffmanTree(w, f1[:], l1[:], c1[:])
	writeHuffmanTree(w, f0[:], l0[:], c0[:])
	writeHuffmanTree(w, f2[:], l2[:], c2[:])
	writeHuffmanTree(w, f3[:], l3[:], c3[:])
	writeSingleEntryHuffmanTree(w, 1)

	// image data
	for at := 0; at < len(pixels); {
		pixel := pixels[at : at+4]
		at += 4
		len1, len0, len2, len3 := l1[pixel[1]], l0[pixel[0]], l2[pixel[2]], l3[pixel[3]]
		code := uint64(c1[pixel[1]]) |
			uint64(c0[pixel[0]])<<len1 |
			uint64(c2[pixel[2]])<<(len1+len0) |
			uint64(c3[pixel[3]])<<(len1+len0+len2)
		w.writeBits(code, len1+len0+len2+len3)
		n := runLength(pixels, at, pixel)
		at += n * 4
		writeRun(w, n, &c1, &l1)
	}
	w.flush()

	frame := w.out
	size := uint32(len(frame)) + 8
	if len(frame)%2 == 1 {
		size++
	}
	out := make([]byte, 0, 20+len(frame)+1)
	out = append(out, "RIFF"...)
	out = binary.LittleEndian.AppendUint32(out, size+4)
	out = append(out, "WEBPVP8L"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(frame)))
	out = append(out, frame...)
	if len(frame)%2 == 1 {
		out = append(out, 0)
	}
	return out, nil
}
