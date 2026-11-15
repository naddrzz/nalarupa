// Port of imagesize 0.14.0 (default features) src/lib.rs, src/util.rs, src/formats/*.rs and
// src/container/heif.rs: image type detection from the header and image dimensions from a byte blob,
// reading through a std::io::Cursor model so truncated and odd inputs fail exactly as in Rust.

package usvg

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf8"
)

// imgszImageType is imagesize::ImageType, in Rust's declaration order; Heif(Compression) is flattened
// into one constant per compression.
type imgszImageType uint8

const (
	imgszTypeAseprite imgszImageType = iota
	imgszTypeBmp
	imgszTypeDds
	imgszTypeExr
	imgszTypeFarbfeld
	imgszTypeGif
	imgszTypeHdr
	imgszTypeHeifAv1
	imgszTypeHeifHevc
	imgszTypeHeifJpeg
	imgszTypeHeifUnknown
	imgszTypeIco
	imgszTypeIlbm
	imgszTypeJpeg
	imgszTypeJxl
	imgszTypeKtx2
	imgszTypePng
	imgszTypePnm
	imgszTypePsd
	imgszTypeQoi
	imgszTypeTga
	imgszTypeTiff
	imgszTypeVtf
	imgszTypeWebp
)

// imgszSize is imagesize::ImageSize (usize fields).
type imgszSize struct{ Width, Height uint64 }

// imgszErrorKind lists imagesize::ImageError variants.
type imgszErrorKind uint8

const (
	imgszErrNotSupported   imgszErrorKind = iota // "Could not decode image"
	imgszErrCorruptedImage                       // "Hit end of file before finding size"
	imgszErrIO                                   // std::io::Error; Msg is its message
)

// imgszError is imagesize::ImageError.
type imgszError struct {
	Kind imgszErrorKind
	Msg  string
}

func (e *imgszError) Error() string {
	switch e.Kind {
	case imgszErrNotSupported:
		return "Could not decode image"
	case imgszErrCorruptedImage:
		return "Hit end of file before finding size"
	}
	return e.Msg
}

var (
	imgszErrEOF         = &imgszError{Kind: imgszErrIO, Msg: "failed to fill whole buffer"}
	imgszErrSeek        = &imgszError{Kind: imgszErrIO, Msg: "invalid seek to a negative or overflowing position"}
	imgszErrCorrupted   = &imgszError{Kind: imgszErrCorruptedImage}
	imgszErrUnsupported = &imgszError{Kind: imgszErrNotSupported}
)

func imgszInvalidData(msg string) *imgszError { return &imgszError{Kind: imgszErrIO, Msg: msg} }

// imgszGetImageType is imagesize::image_type: the format detected from the first 12 bytes (and, for HEIF
// and TGA, a few more reads).
func imgszGetImageType(header []byte) (imgszImageType, *imgszError) {
	return imgszFormatsImageType(&imgszCursor{data: header})
}

// imgszBlobSize is imagesize::blob_size: the dimensions of an image held in data.
func imgszBlobSize(data []byte) (imgszSize, *imgszError) {
	r := &imgszCursor{data: data}
	t, err := imgszFormatsImageType(r)
	if err != nil {
		return imgszSize{}, err
	}
	return imgszReaderSize(t, r)
}

// imgszReaderSize is ImageType::reader_size.
func imgszReaderSize(t imgszImageType, r *imgszCursor) (imgszSize, *imgszError) {
	switch t {
	case imgszTypeAseprite:
		return imgszFixedSize(r, 0x8, 2, false, false)
	case imgszTypeBmp:
		return imgszFixedSize(r, 0x12, 4, false, false)
	case imgszTypeDds:
		return imgszFixedSize(r, 12, 4, false, true)
	case imgszTypeExr:
		return imgszExrSize(r)
	case imgszTypeFarbfeld:
		return imgszFixedSize(r, 8, 4, true, false)
	case imgszTypeGif:
		return imgszFixedSize(r, 6, 2, false, false)
	case imgszTypeHdr:
		return imgszHdrSize(r)
	case imgszTypeIco:
		return imgszIcoSize(r)
	case imgszTypeIlbm:
		return imgszIlbmSize(r)
	case imgszTypeJpeg:
		return imgszJpegSize(r)
	case imgszTypeJxl:
		return imgszJxlSize(r)
	case imgszTypeKtx2:
		return imgszFixedSize(r, 16, 4, false, false)
	case imgszTypePng:
		return imgszFixedSize(r, 0x10, 4, true, false)
	case imgszTypePnm:
		return imgszPnmSize(r)
	case imgszTypePsd:
		return imgszFixedSize(r, 0x0E, 4, true, true)
	case imgszTypeQoi:
		return imgszFixedSize(r, 4, 4, true, false)
	case imgszTypeTga:
		return imgszFixedSize(r, 12, 2, false, false)
	case imgszTypeTiff:
		return imgszTiffSize(r)
	case imgszTypeVtf:
		return imgszFixedSize(r, 16, 2, false, false)
	case imgszTypeWebp:
		return imgszWebpSize(r)
	case imgszTypeHeifAv1, imgszTypeHeifHevc, imgszTypeHeifJpeg, imgszTypeHeifUnknown:
		return imgszHeifSize(r)
	}
	return imgszSize{}, imgszErrUnsupported
}

// imgszCursor models std::io::Cursor<&[u8]>: the position may pass the end, reads past it fail.
type imgszCursor struct {
	data []byte
	pos  uint64
}

func (c *imgszCursor) remaining() []byte {
	return c.data[min(c.pos, uint64(len(c.data))):]
}

// read is Cursor::read: copies what is available and returns the count.
func (c *imgszCursor) read(buf []byte) int {
	n := copy(buf, c.remaining())
	c.pos += uint64(n)
	return n
}

// readExact is Cursor::read_exact: on failure the cursor moves to the end.
func (c *imgszCursor) readExact(buf []byte) *imgszError {
	rem := c.remaining()
	if len(rem) < len(buf) {
		c.pos = uint64(len(c.data))
		return imgszErrEOF
	}
	copy(buf, rem)
	c.pos += uint64(len(buf))
	return nil
}

func (c *imgszCursor) seekStart(n uint64) { c.pos = n }

func (c *imgszCursor) seekCurrent(off int64) *imgszError {
	p, ok := imgszCheckedAddSigned(c.pos, off)
	if !ok {
		return imgszErrSeek
	}
	c.pos = p
	return nil
}

func (c *imgszCursor) seekEnd(off int64) *imgszError {
	p, ok := imgszCheckedAddSigned(uint64(len(c.data)), off)
	if !ok {
		return imgszErrSeek
	}
	c.pos = p
	return nil
}

// imgszCheckedAddSigned is u64::checked_add_signed.
func imgszCheckedAddSigned(a uint64, b int64) (uint64, bool) {
	if b >= 0 {
		s := a + uint64(b)
		return s, s >= a
	}
	d := uint64(-(b + 1)) + 1 // |b| without overflow for MinInt64
	return a - d, a >= d
}

// imgszTake models a std::io::Take over the cursor.
type imgszTake struct {
	c     *imgszCursor
	limit uint64
}

func (t *imgszTake) read(buf []byte) int {
	if t.limit == 0 {
		return 0
	}
	if uint64(len(buf)) > t.limit {
		buf = buf[:t.limit]
	}
	n := t.c.read(buf)
	t.limit -= uint64(n)
	return n
}

// readExact is the default Read::read_exact over Take: it reads until buf is full or a read returns 0.
func (t *imgszTake) readExact(buf []byte) *imgszError {
	for len(buf) > 0 {
		n := t.read(buf)
		if n == 0 {
			return imgszErrEOF
		}
		buf = buf[n:]
	}
	return nil
}

func imgszReadU8(r *imgszCursor) (uint8, *imgszError) {
	var b [1]byte
	if err := r.readExact(b[:]); err != nil {
		return 0, err
	}
	return b[0], nil
}

func imgszReadU16(r *imgszCursor, big bool) (uint16, *imgszError) {
	var b [2]byte
	if err := r.readExact(b[:]); err != nil {
		return 0, err
	}
	if big {
		return binary.BigEndian.Uint16(b[:]), nil
	}
	return binary.LittleEndian.Uint16(b[:]), nil
}

func imgszReadU24(r *imgszCursor, big bool) (uint32, *imgszError) {
	var b [3]byte
	if err := r.readExact(b[:]); err != nil {
		return 0, err
	}
	if big {
		return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2]), nil
	}
	return uint32(b[2])<<16 | uint32(b[1])<<8 | uint32(b[0]), nil
}

func imgszReadU32(r *imgszCursor, big bool) (uint32, *imgszError) {
	var b [4]byte
	if err := r.readExact(b[:]); err != nil {
		return 0, err
	}
	if big {
		return binary.BigEndian.Uint32(b[:]), nil
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

func imgszReadU64(r *imgszCursor, big bool) (uint64, *imgszError) {
	var b [8]byte
	if err := r.readExact(b[:]); err != nil {
		return 0, err
	}
	if big {
		return binary.BigEndian.Uint64(b[:]), nil
	}
	return binary.LittleEndian.Uint64(b[:]), nil
}

// imgszReadTag is util::read_tag: a big-endian u32 size followed by a 4-byte tag.
func imgszReadTag(r *imgszCursor) ([4]byte, uint64, *imgszError) {
	var tag [4]byte
	size, err := imgszReadU32(r, true)
	if err != nil {
		return tag, 0, err
	}
	if err := r.readExact(tag[:]); err != nil {
		return tag, 0, err
	}
	return tag, uint64(size), nil
}

// imgszReadUntilCapped is util::read_until_capped.
func imgszReadUntilCapped(r *imgszCursor, delimiter byte, maxSize int) ([]byte, *imgszError) {
	var out []byte
	amountRead := 0
	for amountRead < maxSize {
		b, err := imgszReadU8(r)
		if err != nil {
			return nil, err
		}
		if b == delimiter {
			break
		}
		out = append(out, b)
		amountRead++
	}
	if amountRead >= maxSize {
		return nil, imgszInvalidData(fmt.Sprintf("Delimiter not found within %d bytes", maxSize))
	}
	return out, nil
}

// imgszIsASCIIWhitespace is u8::is_ascii_whitespace (no vertical tab).
func imgszIsASCIIWhitespace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\x0C' || b == '\r'
}

// imgszReadUntilWhitespace is util::read_until_whitespace, including its count of the terminating byte.
func imgszReadUntilWhitespace(r *imgszCursor, maxSize int) (string, *imgszError) {
	var out []byte
	amountRead := 0
	seenNonWhitespace := false
	for amountRead < maxSize {
		amountRead++
		b, err := imgszReadU8(r)
		if err != nil {
			return "", err
		}
		if imgszIsASCIIWhitespace(b) {
			if seenNonWhitespace {
				break
			}
			continue
		}
		out = append(out, b)
		seenNonWhitespace = true
	}
	if amountRead >= maxSize {
		return "", imgszInvalidData(fmt.Sprintf("Delimiter not found within %d bytes", maxSize))
	}
	return imgszUTF8(out)
}

// imgszUTF8 is String::from_utf8 mapped to an InvalidData error.
func imgszUTF8(b []byte) (string, *imgszError) {
	if !utf8.Valid(b) {
		return "", imgszInvalidData("invalid utf-8")
	}
	return string(b), nil
}

// imgszParseUsize is usize::from_str: an optional '+', then ASCII digits, without overflow.
func imgszParseUsize(s string) (uint64, bool) {
	if s == "" {
		return 0, false
	}
	if s[0] == '+' {
		s = s[1:]
		if s == "" {
			return 0, false
		}
	}
	var v uint64
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		d := uint64(c - '0')
		if v > (^uint64(0)-d)/10 {
			return 0, false
		}
		v = v*10 + d
	}
	return v, true
}

// imgszFormatsImageType is formats::image_type, checking formats in the crate's order.
func imgszFormatsImageType(r *imgszCursor) (imgszImageType, *imgszError) {
	var header [12]byte
	if err := r.readExact(header[:]); err != nil {
		return 0, err
	}
	h := header[:]
	has := func(p string) bool { return bytes.HasPrefix(h, []byte(p)) }

	switch {
	case has("\xFF\xD8\xFF"):
		return imgszTypeJpeg, nil
	case has("\x89PNG"):
		return imgszTypePng, nil
	case has("GIF8"):
		return imgszTypeGif, nil
	case (has("II") && (h[2] == 0x2A || h[2] == 0x2B) && h[3] == 0) ||
		(has("MM\x00") && (h[3] == 0x2A || h[3] == 0x2B)):
		return imgszTypeTiff, nil
	case string(h[0:4]) == "RIFF" && string(h[8:12]) == "WEBP":
		return imgszTypeWebp, nil
	}
	if t, ok := imgszHeifMatches(h, r); ok {
		return t, nil
	}
	switch {
	case has("\xFF\x0A") || has("\x00\x00\x00\x0CJXL \x0D\x0A\x87\x0A"):
		return imgszTypeJxl, nil
	case has("\x42\x4D"):
		return imgszTypeBmp, nil
	case has("8BPS"):
		return imgszTypePsd, nil
	case has("\x00\x00\x01\x00"):
		return imgszTypeIco, nil
	case string(h[4:6]) == "\xE0\xA5":
		return imgszTypeAseprite, nil
	case has("\x76\x2f\x31\x01"):
		return imgszTypeExr, nil
	case has("#?RADIANCE\n") || has("#?RGBE\n"):
		return imgszTypeHdr, nil
	case has("DDS "):
		return imgszTypeDds, nil
	case has("\xAB\x4B\x54\x58\x20\x32\x30\xBB\x0D\x0A\x1A\x0A"):
		return imgszTypeKtx2, nil
	case has("qoif"):
		return imgszTypeQoi, nil
	case has("farbfeld"):
		return imgszTypeFarbfeld, nil
	case h[0] == 'P' && h[1] >= '1' && h[1] <= '6':
		return imgszTypePnm, nil
	case has("VTF\x00"):
		return imgszTypeVtf, nil
	case string(h[0:4]) == "FORM" && (string(h[8:12]) == "ILBM" || string(h[8:12]) == "PBM "):
		return imgszTypeIlbm, nil
	}
	// TGA is last: it has the highest probability of false positives.
	if imgszTgaMatches(h, r) {
		return imgszTypeTga, nil
	}
	return 0, imgszErrUnsupported
}

// imgszFixedSize reads two integers of n bytes (2 or 4) at offset, width first unless heightFirst.
func imgszFixedSize(r *imgszCursor, offset uint64, n int, big, heightFirst bool) (imgszSize, *imgszError) {
	r.seekStart(offset)
	read := func() (uint64, *imgszError) {
		if n == 2 {
			v, err := imgszReadU16(r, big)
			return uint64(v), err
		}
		v, err := imgszReadU32(r, big)
		return uint64(v), err
	}
	a, err := read()
	if err != nil {
		return imgszSize{}, err
	}
	b, err := read()
	if err != nil {
		return imgszSize{}, err
	}
	if heightFirst {
		return imgszSize{Width: b, Height: a}, nil
	}
	return imgszSize{Width: a, Height: b}, nil
}

// imgszJpegSize is formats/jpeg.rs size: the first SOFn marker outside embedded images.
func imgszJpegSize(r *imgszCursor) (imgszSize, *imgszError) {
	var marker [2]byte
	depth := int32(0)
	r.seekStart(2) // the first tag after FF D8

	for {
		if err := r.readExact(marker[:]); err != nil {
			return imgszSize{}, err
		}
		if marker[0] != 0xFF {
			return imgszSize{}, imgszErrCorrupted // not a marker
		}
		page := marker[1]

		// Valid SOFn markers; C4, C8 and CC are not dimension markers.
		if (page >= 0xC0 && page <= 0xC3) || (page >= 0xC5 && page <= 0xC7) ||
			(page >= 0xC9 && page <= 0xCB) || (page >= 0xCD && page <= 0xCF) {
			if depth == 0 {
				// Skip the length and precision to the height.
				if err := r.seekCurrent(3); err != nil {
					return imgszSize{}, err
				}
				break
			}
		} else if page == 0xD8 {
			depth++
		} else if page == 0xD9 {
			depth--
			if depth < 0 {
				return imgszSize{}, imgszErrCorrupted
			}
		}

		pageSize, err := imgszReadU16(r, true)
		if err != nil {
			return imgszSize{}, err
		}
		if err := r.seekCurrent(int64(pageSize) - 2); err != nil {
			return imgszSize{}, err
		}
	}

	height, err := imgszReadU16(r, true)
	if err != nil {
		return imgszSize{}, err
	}
	width, err := imgszReadU16(r, true)
	if err != nil {
		return imgszSize{}, err
	}
	return imgszSize{Width: uint64(width), Height: uint64(height)}, nil
}

// imgszWebpSize is formats/webp.rs size. It reads the chunk tag at the cursor's current position, which
// after detection is byte 12.
func imgszWebpSize(r *imgszCursor) (imgszSize, *imgszError) {
	var buffer [4]byte
	if err := r.readExact(buffer[:]); err != nil {
		return imgszSize{}, err
	}
	switch buffer[3] {
	case ' ': // VP8
		return imgszFixedSize(r, 0x1A, 2, false, false)
	case 'L': // VP8L
		r.seekStart(0x15)
		dims, err := imgszReadU32(r, false)
		if err != nil {
			return imgszSize{}, err
		}
		return imgszSize{Width: uint64(dims&0x3FFF) + 1, Height: uint64((dims>>14)&0x3FFF) + 1}, nil
	case 'X': // VP8X
		r.seekStart(0x18)
		w, err := imgszReadU24(r, false)
		if err != nil {
			return imgszSize{}, err
		}
		h, err := imgszReadU24(r, false)
		if err != nil {
			return imgszSize{}, err
		}
		return imgszSize{Width: uint64(w) + 1, Height: uint64(h) + 1}, nil
	}
	return imgszSize{}, imgszInvalidData("Invalid VP8 Tag")
}

// imgszIcoSize is formats/ico.rs size: the largest image by area (the last of equal areas, as
// Iterator::max).
func imgszIcoSize(r *imgszCursor) (imgszSize, *imgszError) {
	r.seekStart(4)
	count, err := imgszReadU16(r, false)
	if err != nil {
		return imgszSize{}, err
	}
	var best imgszSize
	found := false
	for range count {
		w, err := imgszReadU8(r)
		if err != nil {
			break // headers are truncated: use the largest found so far
		}
		h, err := imgszReadU8(r)
		if err != nil {
			break
		}
		// Dimensions are 1-256, with 0 meaning 256.
		size := imgszSize{Width: uint64(w-1) + 1, Height: uint64(h-1) + 1}
		if !found || size.Width*size.Height >= best.Width*best.Height {
			best, found = size, true
		}
		// Each ICONDIRENTRY is 16 bytes; skip the last 14.
		if err := r.seekCurrent(14); err != nil {
			return imgszSize{}, err
		}
	}
	if !found {
		return imgszSize{}, imgszErrCorrupted
	}
	return best, nil
}

// imgszIlbmSize is formats/ilbm.rs size: the BMHD chunk, which must precede BODY.
func imgszIlbmSize(r *imgszCursor) (imgszSize, *imgszError) {
	r.seekStart(12) // skip the IFF header
	var chunkID [4]byte
	for {
		if err := r.readExact(chunkID[:]); err != nil {
			return imgszSize{}, err
		}
		length, err := imgszReadU32(r, true)
		if err != nil {
			return imgszSize{}, err
		}
		switch string(chunkID[:]) {
		case "BMHD":
			return imgszFixedSize(r, r.pos, 2, true, false)
		case "BODY":
			return imgszSize{}, imgszErrCorrupted
		}
		// Chunks of odd length have a padding byte.
		chunkLength := int64(length)
		if err := r.seekCurrent(chunkLength + chunkLength%2); err != nil {
			return imgszSize{}, err
		}
	}
}

// imgszExrSize is formats/exr.rs size: the dataWindow header attribute.
func imgszExrSize(r *imgszCursor) (imgszSize, *imgszError) {
	r.seekStart(4)
	flags, err := imgszReadU32(r, false)
	if err != nil {
		return imgszSize{}, err
	}
	// With the long names flag, attribute and type names have up to 255 bytes, else 31.
	maxNameSize := 31
	if flags&0x400 != 0 {
		maxNameSize = 255
	}
	readName := func() (string, *imgszError) {
		b, err := imgszReadUntilCapped(r, 0, maxNameSize)
		if err != nil {
			return "", err
		}
		return imgszUTF8(b)
	}
	readI64 := func() (int64, *imgszError) {
		v, err := imgszReadU32(r, false)
		return int64(int32(v)), err
	}

	for {
		attrName, err := readName()
		if err != nil {
			return imgszSize{}, err
		}
		if attrName == "" {
			break // end of the header
		}
		attrType, err := readName()
		if err != nil {
			return imgszSize{}, err
		}
		attrSize, err := imgszReadU32(r, false)
		if err != nil {
			return imgszSize{}, err
		}

		if attrName == "dataWindow" && attrType == "box2i" {
			var v [4]int64 // x_min, y_min, x_max, y_max
			for i := range v {
				if v[i], err = readI64(); err != nil {
					return imgszSize{}, err
				}
			}
			if v[0] > v[2] || v[1] > v[3] {
				continue
			}
			return imgszSize{Width: uint64(v[2] - v[0] + 1), Height: uint64(v[3] - v[1] + 1)}, nil
		}
		// Skip the attribute value.
		if err := r.seekCurrent(int64(attrSize)); err != nil {
			return imgszSize{}, err
		}
	}
	return imgszSize{}, imgszInvalidData("Data window not found")
}

// imgszHdrSize is formats/hdr.rs size: the resolution line after the header.
func imgszHdrSize(r *imgszCursor) (imgszSize, *imgszError) {
	r.seekStart(0)
	readLine := func(maxSize int) (string, *imgszError) {
		b, err := imgszReadUntilCapped(r, '\n', maxSize)
		if err != nil {
			return "", err
		}
		return imgszUTF8(b)
	}

	// At most 11 bytes, the longest valid identifier.
	id, err := readLine(11)
	if err != nil {
		return imgszSize{}, err
	}
	if !strings.HasPrefix(id, "#?RADIANCE") && !strings.HasPrefix(id, "#?RGBE") {
		return imgszSize{}, imgszInvalidData("Invalid HDR format identifier")
	}

	for {
		line, err := readLine(256)
		if err != nil {
			return imgszSize{}, err
		}
		if strings.TrimSpace(line) == "" { // str::trim, Unicode whitespace
			continue
		}
		// In all 8 orientations the line holds the height first, then the width.
		if strings.HasPrefix(line, "-Y") || strings.HasPrefix(line, "+Y") ||
			strings.HasPrefix(line, "-X") || strings.HasPrefix(line, "+X") {
			dims := strings.Fields(line) // str::split_whitespace
			if len(dims) != 4 {
				return imgszSize{}, imgszInvalidData("Invalid HDR dimensions line")
			}
			height, okH := imgszParseUsize(dims[1])
			width, okW := imgszParseUsize(dims[3])
			if okW && okH {
				return imgszSize{Width: width, Height: height}, nil
			}
			break
		}
	}
	return imgszSize{}, imgszInvalidData("HDR dimensions not found")
}

// imgszPnmSize is formats/pnm.rs size: width and height after comments and blank tokens.
func imgszPnmSize(r *imgszCursor) (imgszSize, *imgszError) {
	r.seekStart(2)
	for {
		line, err := imgszReadUntilWhitespace(r, 1024)
		if err != nil {
			return imgszSize{}, err
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			// A comment: skip to the newline.
			if _, err := imgszReadUntilCapped(r, '\n', 1024); err != nil {
				return imgszSize{}, err
			}
			continue
		}
		if trimmed == "" {
			continue
		}

		rawWidth := line // untrimmed, as in Rust
		line, err = imgszReadUntilWhitespace(r, 1024)
		if err != nil {
			return imgszSize{}, err
		}
		width, okW := imgszParseUsize(rawWidth)
		height, okH := imgszParseUsize(strings.TrimSpace(line))
		if okW && okH {
			return imgszSize{Width: width, Height: height}, nil
		}
		break
	}
	return imgszSize{}, imgszInvalidData("PNM dimensions not found")
}

// imgszTiffSize is formats/tiff.rs size: tags 0x100 (width) and 0x101 (height) of the first IFD.
func imgszTiffSize(r *imgszCursor) (imgszSize, *imgszError) {
	r.seekStart(0)
	var marker [2]byte
	if err := r.readExact(marker[:]); err != nil {
		return imgszSize{}, err
	}
	var big bool
	switch string(marker[:]) {
	case "II":
		big = false
	case "MM":
		big = true
	default:
		return imgszSize{}, imgszInvalidData("Invalid TIFF header")
	}
	typeMarker, err := imgszReadU16(r, big)
	if err != nil {
		return imgszSize{}, err
	}
	var bigTiff bool
	switch typeMarker {
	case 42:
	case 43:
		bigTiff = true
	default:
		return imgszSize{}, imgszInvalidData("Invalid TIFF header")
	}

	if bigTiff {
		// BigTIFF header additions are the constants 8 and 0.
		offsetBytesize, err := imgszReadU16(r, big)
		if err != nil {
			return imgszSize{}, err
		}
		if offsetBytesize != 8 {
			return imgszSize{}, imgszInvalidData("Unrecognised BigTiff offset size")
		}
		extra, err := imgszReadU16(r, big)
		if err != nil {
			return imgszSize{}, err
		}
		if extra != 0 {
			return imgszSize{}, imgszInvalidData("Invalid BigTiff header")
		}
	}

	// readWord reads a u32 for TIFF, a u64 for BigTIFF.
	readWord := func() (uint64, *imgszError) {
		if bigTiff {
			return imgszReadU64(r, big)
		}
		v, err := imgszReadU32(r, big)
		return uint64(v), err
	}

	ifdOffset, err := readWord()
	if err != nil {
		return imgszSize{}, err
	}
	if ifdOffset == 0 {
		return imgszSize{}, imgszInvalidData("Invalid IFD offset")
	}
	r.seekStart(ifdOffset)

	var ifdCount uint64
	if bigTiff {
		ifdCount, err = imgszReadU64(r, big)
	} else {
		var c uint16
		c, err = imgszReadU16(r, big)
		ifdCount = uint64(c)
	}
	if err != nil {
		return imgszSize{}, err
	}

	var width, height uint32
	var hasWidth, hasHeight bool
	for range ifdCount {
		tag, err := imgszReadU16(r, big)
		if err != nil {
			return imgszSize{}, err
		}
		kind, err := imgszReadU16(r, big)
		if err != nil {
			return imgszSize{}, err
		}
		if _, err := readWord(); err != nil { // count
			return imgszSize{}, err
		}

		var valueBytes int
		switch kind {
		case 1, 2, 6, 7: // BYTE, ASCII, SBYTE, UNDEFINED
			valueBytes = 1
		case 3, 8: // SHORT, SSHORT
			valueBytes = 2
		case 4, 9, 11, 13: // LONG, SLONG, FLOAT, IFD
			valueBytes = 4
		case 5, 10: // RATIONAL, SRATIONAL
			valueBytes = 8
		case 12: // DOUBLE
			valueBytes = 8
		case 16, 17, 18: // BigTIFF only: LONG8, SLONG8, IFD8
			if !bigTiff {
				return imgszSize{}, imgszInvalidData("Invalid IFD type for standard TIFF")
			}
			valueBytes = 8
		default:
			return imgszSize{}, imgszInvalidData("Invalid IFD type")
		}

		var valueBuffer [8]byte
		ifdValueLength := uint64(4)
		if bigTiff {
			ifdValueLength = 8
		}
		handle := imgszTake{c: r, limit: ifdValueLength}
		if uint64(handle.read(valueBuffer[:])) != ifdValueLength {
			return imgszSize{}, imgszInvalidData("Invalid IFD value length")
		}

		value, hasValue := uint32(0), false
		switch valueBytes {
		case 2:
			if big {
				value = uint32(binary.BigEndian.Uint16(valueBuffer[:]))
			} else {
				value = uint32(binary.LittleEndian.Uint16(valueBuffer[:]))
			}
			hasValue = true
		case 4:
			if big {
				value = binary.BigEndian.Uint32(valueBuffer[:])
			} else {
				value = binary.LittleEndian.Uint32(valueBuffer[:])
			}
			hasValue = true
		}

		switch tag {
		case 0x100:
			width, hasWidth = value, hasValue
		case 0x101:
			height, hasHeight = value, hasValue
		}
		if hasWidth && hasHeight {
			return imgszSize{Width: uint64(width), Height: uint64(height)}, nil
		}
	}
	return imgszSize{}, imgszInvalidData("No dimensions in IFD tags")
}

// imgszJxlSize is formats/jxl.rs size: the SizeHeader of a raw codestream or of one in jxlc/jxlp boxes.
func imgszJxlSize(r *imgszCursor) (imgszSize, *imgszError) {
	var fileHeader [16]byte // variable size, at most 16 bytes
	headerSize := 0

	r.seekStart(0)
	if err := r.readExact(fileHeader[:2]); err != nil {
		return imgszSize{}, err
	}

	if string(fileHeader[:2]) == "\xFF\x0A" {
		// Raw codestream: read the header directly.
		headerSize = r.read(fileHeader[2:]) + 2
	} else {
		// Container: a single jxlc box or several jxlp boxes.
		r.seekStart(12)
		for {
			boxType, boxSize32, err := imgszReadTag(r)
			if err != nil {
				return imgszSize{}, err
			}
			boxStart := r.pos - 8

			// Size 1 means the real size is in the first 8 bytes of content; 0 means the box ends at EOF.
			boxSize := boxSize32
			if boxSize32 == 1 {
				if boxSize, err = imgszReadU64(r, true); err != nil {
					return imgszSize{}, err
				}
			}

			boxEnd := boxStart + boxSize
			if boxEnd < boxStart {
				return imgszSize{}, imgszErrCorrupted
			}
			boxHeaderSize := r.pos - boxStart

			if boxSize != 0 && boxSize < boxHeaderSize {
				return imgszSize{}, imgszInvalidData(fmt.Sprintf("Invalid size for %s box: %d",
					strings.ToValidUTF8(string(boxType[:]), "�"), boxSize))
			}

			boxReader := imgszTake{c: r, limit: uint64(len(fileHeader))}
			if boxSize != 0 {
				boxReader.limit = boxSize - boxHeaderSize
			}

			// A jxlc box holds the whole codestream.
			if string(boxType[:]) == "jxlc" {
				headerSize = boxReader.read(fileHeader[:])
				break
			}

			// Or it is split over jxlp boxes.
			if string(boxType[:]) == "jxlp" {
				var jxlpIndex [4]byte
				if err := boxReader.readExact(jxlpIndex[:]); err != nil {
					return imgszSize{}, err
				}
				headerSize += boxReader.read(fileHeader[headerSize:])

				// The high bit of the index marks the final jxlp box.
				if headerSize == len(fileHeader) || jxlpIndex[0]&0x80 != 0 {
					break
				}
			}

			if boxSize == 0 {
				break
			}
			r.seekStart(boxEnd)
		}
	}

	if headerSize < 2 {
		return imgszSize{}, imgszErrCorrupted
	}
	if string(fileHeader[:2]) != "\xFF\x0A" {
		return imgszSize{}, imgszInvalidData("Invalid JXL signature")
	}

	// The header as a little-endian u128, read bit by bit.
	bits := func(numBits, offset, size uint64) (uint64, *imgszError) {
		if offset+numBits >= size {
			return 0, imgszErrCorrupted
		}
		var v uint64
		for i := uint64(0); i < numBits; i++ {
			p := offset + i
			v |= uint64(fileHeader[p/8]>>(p%8)&1) << i
		}
		return v, nil
	}
	size := 8 * uint64(headerSize)

	isSmallBit, err := bits(1, 16, size)
	if err != nil {
		return imgszSize{}, err
	}
	isSmall := isSmallBit != 0

	// Small images store the height in the next 5 bits; others use two bits to pick the bit count.
	heightSelector, err := bits(2, 17, size)
	if err != nil {
		return imgszSize{}, err
	}
	sizeBits := [4]uint64{9, 13, 18, 30}
	var heightBits, heightOffset, heightShift uint64
	if isSmall {
		heightBits, heightOffset, heightShift = 5, 17, 3
	} else {
		heightBits, heightOffset = sizeBits[heightSelector], 19
	}
	hv, err := bits(heightBits, heightOffset, size)
	if err != nil {
		return imgszSize{}, err
	}
	height := (hv + 1) << heightShift

	// A ratio of 0 reads the width like the height; others derive it from a fixed aspect ratio.
	ratio, err := bits(3, heightBits+heightOffset, size)
	if err != nil {
		return imgszSize{}, err
	}
	widthSelector, err := bits(2, heightBits+heightOffset+3, 128)
	if err != nil {
		return imgszSize{}, err
	}
	var widthBits, widthOffset, widthShift uint64
	if isSmall {
		widthBits, widthOffset, widthShift = 5, 25, 3
	} else {
		widthBits, widthOffset = sizeBits[widthSelector], heightBits+heightOffset+5
	}

	var width uint64
	switch ratio {
	case 1:
		width = height // 1:1
	case 2:
		width = (height / 10) * 12 // 12:10
	case 3:
		width = (height / 3) * 4 // 4:3
	case 4:
		width = (height / 2) * 3 // 3:2
	case 5:
		width = (height / 9) * 16 // 16:9
	case 6:
		width = (height / 4) * 5 // 5:4
	case 7:
		width = height * 2 // 2:1
	default:
		wv, err := bits(widthBits, widthOffset, size)
		if err != nil {
			return imgszSize{}, err
		}
		width = (wv + 1) << widthShift
	}

	// The orientation overrides the EXIF one.
	metadataOffset := heightBits + heightOffset + 3
	if ratio == 0 {
		metadataOffset = widthBits + widthOffset
	}
	allDefault, err := bits(1, metadataOffset, size)
	if err != nil {
		return imgszSize{}, err
	}
	orientation := uint64(0)
	if allDefault == 0 {
		extraFields, err := bits(1, metadataOffset+1, size)
		if err != nil {
			return imgszSize{}, err
		}
		if extraFields != 0 {
			if orientation, err = bits(3, metadataOffset+2, size); err != nil {
				return imgszSize{}, err
			}
		}
	}

	if orientation < 4 {
		return imgszSize{Width: width, Height: height}, nil
	}
	return imgszSize{Width: height, Height: width}, nil
}

// imgszHeifCompression is heif::inner_matches, giving the HEIF type for a brand.
func imgszHeifCompression(brand []byte) (imgszImageType, bool) {
	switch string(brand) {
	case "heic", "heix", "heis", "hevs", "heim", "hevm", "hevc", "hevx":
		return imgszTypeHeifHevc, true
	case "avif", "avio", "avis", "MA1A", "MA1B":
		return imgszTypeHeifAv1, true
	case "jpeg", "jpgs":
		return imgszTypeHeifJpeg, true
	}
	return 0, false
}

func imgszIsHeifBrand(b []byte) bool {
	switch string(b) {
	case "mif1", "msf1", "mif2", "miaf":
		return true
	}
	return false
}

// imgszHeifMatches is container/heif.rs matches; it may read 12 bytes past the header.
func imgszHeifMatches(header []byte, r *imgszCursor) (imgszImageType, bool) {
	if len(header) < 12 || string(header[4:8]) != "ftyp" {
		return 0, false
	}
	brand := header[8:12]
	if t, ok := imgszHeifCompression(brand); ok {
		return t, true // { heic, ... }
	}
	if imgszIsHeifBrand(brand) {
		var buf [12]byte
		if r.readExact(buf[:]) != nil {
			return imgszTypeHeifUnknown, true
		}
		brand2 := buf[4:8]
		if t, ok := imgszHeifCompression(brand2); ok {
			return t, true // { msf1, version, heic, msf1, ... }
		}
		if imgszIsHeifBrand(brand2) {
			if t, ok := imgszHeifCompression(buf[8:12]); ok {
				return t, true // { msf1, version, msf1, heic, ... }
			}
		}
	}
	return imgszTypeHeifUnknown, true
}

// imgszHeifSkipToTag is heif::skip_to_tag: the size of the first box with the tag.
func imgszHeifSkipToTag(r *imgszCursor, tag string) (uint32, *imgszError) {
	var tagBuf [4]byte
	for {
		size, err := imgszReadU32(r, true)
		if err != nil {
			return 0, err
		}
		if err := r.readExact(tagBuf[:]); err != nil {
			return 0, err
		}
		if string(tagBuf[:]) == tag {
			return size, nil
		}
		if size < 8 {
			return 0, imgszInvalidData(fmt.Sprintf("Invalid heif box size: %d", size))
		}
		if err := r.seekCurrent(int64(size) - 8); err != nil {
			return 0, err
		}
	}
}

// imgszHeifSize is container/heif.rs size: the largest ispe property, swapped by a 90/270 irot.
func imgszHeifSize(r *imgszCursor) (imgszSize, *imgszError) {
	r.seekStart(0)
	ftypSize, err := imgszReadU32(r, true)
	if err != nil {
		return imgszSize{}, err
	}
	r.seekStart(uint64(ftypSize)) // the first box after ftyp

	if _, err := imgszHeifSkipToTag(r, "meta"); err != nil {
		return imgszSize{}, err
	}
	if _, err := imgszReadU32(r, true); err != nil { // meta has a junk value after it
		return imgszSize{}, err
	}
	if _, err := imgszHeifSkipToTag(r, "iprp"); err != nil {
		return imgszSize{}, err
	}
	ipco, err := imgszHeifSkipToTag(r, "ipco")
	if err != nil {
		return imgszSize{}, err
	}
	ipcoSize := uint64(ipco)

	var maxWidth, maxHeight uint64
	foundIspe := false
	rotation := uint8(0)

	for {
		tag, size, err := imgszReadTag(r)
		if err != nil {
			break
		}
		if size < 8 {
			return imgszSize{}, imgszErrCorrupted
		}
		switch {
		case string(tag[:]) == "ispe":
			// A junk value, then width and height.
			foundIspe = true
			if _, err := imgszReadU32(r, true); err != nil {
				return imgszSize{}, err
			}
			w, err := imgszReadU32(r, true)
			if err != nil {
				return imgszSize{}, err
			}
			h, err := imgszReadU32(r, true)
			if err != nil {
				return imgszSize{}, err
			}
			if uint64(w)*uint64(h) > maxWidth*maxHeight {
				maxWidth, maxHeight = uint64(w), uint64(h)
			}
			continue
		case string(tag[:]) == "irot":
			if rotation, err = imgszReadU8(r); err != nil {
				return imgszSize{}, err
			}
			continue
		case size >= ipcoSize:
			// Past the ipco boundary.
		default:
			// Still inside ipco: skip the rest of this box.
			ipcoSize -= size
			if err := r.seekCurrent(int64(size) - 8); err != nil {
				return imgszSize{}, err
			}
			continue
		}
		break
	}

	if !foundIspe {
		return imgszSize{}, imgszInvalidData("Not enough data")
	}
	// Rotations 1 and 3 are 90 and 270 degrees.
	if rotation == 1 || rotation == 3 {
		maxWidth, maxHeight = maxHeight, maxWidth
	}
	return imgszSize{Width: maxWidth, Height: maxHeight}, nil
}

// imgszTgaMatches is formats/tga.rs matches: a footer signature or a plausible header.
func imgszTgaMatches(header []byte, r *imgszCursor) bool {
	colormapType := header[1]
	imageType := header[2]

	// Uncompressed or RLE compressed types only.
	switch imageType {
	case 1, 2, 3, 9, 10, 11:
	default:
		return false
	}
	// No colormap (0) or a colormap (1).
	if colormapType >= 2 {
		return false
	}
	ok, err := imgszIsTga(r, imageType, colormapType)
	return err == nil && ok
}

func imgszIsTga(r *imgszCursor, imageType, colormapType uint8) (bool, *imgszError) {
	// The footer; this doubles as a check for at least 18 bytes.
	if err := r.seekEnd(-18); err != nil {
		return false, err
	}
	var signature [18]byte
	if err := r.readExact(signature[:]); err != nil {
		return false, err
	}
	if string(signature[:]) == "TRUEVISION-XFILE.\x00" {
		return true, nil
	}

	// Without a footer, check that the header fields make a valid configuration.
	if (imageType == 1 || imageType == 9) && colormapType != 1 {
		return false, nil
	}

	r.seekStart(3)
	colormapOffset, err := imgszReadU32(r, false)
	if err != nil {
		return false, err
	}
	colormapSize, err := imgszReadU8(r)
	if err != nil {
		return false, err
	}
	if colormapType == 0 && (colormapOffset != 0 || colormapSize != 0) {
		return false, nil
	}
	if colormapType == 1 && colormapSize != 0 && colormapSize != 8 && colormapSize != 16 &&
		colormapSize != 24 && colormapSize != 32 {
		return false, nil
	}

	r.seekStart(16)
	pixelSize, err := imgszReadU8(r)
	if err != nil {
		return false, err
	}
	descriptor, err := imgszReadU8(r)
	if err != nil {
		return false, err
	}
	alphaBits := descriptor & 0x0F

	if descriptor&0x10 != 0 { // reserved bit
		return false, nil
	}
	if pixelSize != 8 && pixelSize != 16 && pixelSize != 24 && pixelSize != 32 {
		return false, nil
	}
	if (pixelSize == 8 || pixelSize == 24) && alphaBits != 0 {
		return false, nil
	}
	if pixelSize == 16 && alphaBits >= 2 {
		return false, nil
	}
	if pixelSize == 32 && alphaBits != 8 && alphaBits != 0 {
		return false, nil
	}
	return true, nil
}
