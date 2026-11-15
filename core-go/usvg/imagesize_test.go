package usvg

import (
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
)

// imagesize 0.14.0 ships no tests in its crate; these inputs cover every detected format, truncated and
// odd data, and the expected strings come from running the real crate (image_type then blob_size, printed
// as `Type WxH` or `err(message)`).

func imgszBytes(parts ...any) []byte {
	var b []byte
	for _, p := range parts {
		switch v := p.(type) {
		case string:
			b = append(b, v...)
		case []byte:
			b = append(b, v...)
		case int:
			b = append(b, byte(v))
		default:
			panic(fmt.Sprintf("%T", p))
		}
	}
	return b
}

func u16le(v uint16) []byte { return binary.LittleEndian.AppendUint16(nil, v) }
func u16be(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func u32le(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }
func u32be(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func u64le(v uint64) []byte { return binary.LittleEndian.AppendUint64(nil, v) }
func zeros(n int) []byte    { return make([]byte, n) }

// imgszHeifBox is an ISO BMFF box.
func imgszHeifBox(tag string, body ...any) []byte {
	b := imgszBytes(body...)
	return imgszBytes(u32be(uint32(8+len(b))), tag, b)
}

var imgszCases = []struct {
	name string
	data []byte
	want string
}{
	{"png", imgszBytes("\x89PNG\r\n\x1a\n", u32be(13), "IHDR", u32be(123), u32be(321), 8, 6, 0, 0, 0), "Png 123x321"},
	{"png doc example", imgszBytes(0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0x0D, 0x49, 0x48, 0x44, 0x52, 0, 0, 0, 0x7B, 0, 0, 0x01, 0x41, 8, 6, 0, 0, 0, 0x9A, 0x38, 0xC4), "Png 123x321"},
	{"not png", imgszBytes(0x89, 0x89, 0x89, 0x89, 0x0D, 0x0A, 0x1A, 0x0A, 0, 0, 0, 0x0D, 0x49, 0x48, 0x44, 0x52, 0, 0, 0, 0x7B, 1, 0, 1, 0x41), "err(Could not decode image) err(Could not decode image)"},
	{"png truncated", imgszBytes("\x89PNG\r\n\x1a\n", u32be(13), "IHDR", u32be(123)), "Png err(failed to fill whole buffer)"},
	{"png 12 bytes", imgszBytes("\x89PNG\r\n\x1a\n", u32be(13)), "Png err(failed to fill whole buffer)"},
	{"short", imgszBytes("\x89PNG"), "err(failed to fill whole buffer) err(failed to fill whole buffer)"},
	{"one byte", imgszBytes(0), "err(failed to fill whole buffer) err(failed to fill whole buffer)"},
	{"jpeg", imgszBytes(0xFF, 0xD8, 0xFF, 0xE0, u16be(16), "JFIF", 0, 1, 1, 0, u16be(1), u16be(1), 0, 0, 0xFF, 0xC0, u16be(17), 8, u16be(480), u16be(640), 3), "Jpeg 640x480"},
	{"jpeg progressive", imgszBytes(0xFF, 0xD8, 0xFF, 0xC2, u16be(17), 8, u16be(7), u16be(9), 3, zeros(9)), "Jpeg 9x7"},
	{"jpeg thumbnail", imgszBytes(0xFF, 0xD8, 0xFF, 0xE1, u16be(2+16), 0xFF, 0xD8, 0xFF, 0xC0, u16be(11), 8, u16be(5), u16be(6), 1, 0, 0, 0xFF, 0xD9, 0xFF, 0xC0, u16be(11), 8, u16be(50), u16be(60), 1), "Jpeg 60x50"},
	{"jpeg skip C4", imgszBytes(0xFF, 0xD8, 0xFF, 0xC4, u16be(4), 0, 0, 0xFF, 0xC1, u16be(11), 8, u16be(2), u16be(3), 1), "Jpeg 3x2"},
	{"jpeg no marker", imgszBytes(0xFF, 0xD8, 0xFF, 0xE0, u16be(4), 0, 0, 0x12, 0x34, zeros(8)), "Jpeg err(Hit end of file before finding size)"},
	{"jpeg EOI first", imgszBytes(0xFF, 0xD8, 0xFF, 0xD9, u16be(2), zeros(8)), "Jpeg err(Hit end of file before finding size)"},
	{"jpeg zero length", imgszBytes(0xFF, 0xD8, 0xFF, 0xE0, u16be(0), zeros(10)), "Jpeg err(Hit end of file before finding size)"},
	{"jpeg truncated", imgszBytes(0xFF, 0xD8, 0xFF, 0xE0, u16be(200), zeros(10)), "Jpeg err(failed to fill whole buffer)"},
	{"jpeg sof truncated", imgszBytes(0xFF, 0xD8, 0xFF, 0xC0, u16be(17), 8, u16be(480), zeros(4)), "Jpeg 0x480"},
	{"gif", imgszBytes("GIF89a", u16le(300), u16le(200), zeros(4)), "Gif 300x200"},
	{"gif87", imgszBytes("GIF87a", u16le(1), u16le(65535), zeros(1)), "err(failed to fill whole buffer) err(failed to fill whole buffer)"},
	{"webp vp8", imgszBytes("RIFF", u32le(100), "WEBP", "VP8 ", u32le(50), zeros(6), u16le(640), u16le(480)), "Webp 640x480"},
	{"webp vp8 scaled", imgszBytes("RIFF", u32le(100), "WEBP", "VP8 ", u32le(50), zeros(6), u16le(0xC000|640), u16le(480)), "Webp 49792x480"},
	{"webp vp8l", imgszBytes("RIFF", u32le(100), "WEBP", "VP8L", u32le(50), 0x2F, u32le(99|(49<<14)|(3<<28))), "Webp 100x50"},
	{"webp vp8x", imgszBytes("RIFF", u32le(100), "WEBP", "VP8X", u32le(10), 0, 0, 0, 0, 0x3F, 0x01, 0, 0xFF, 0, 0), "Webp 320x256"},
	{"webp bad tag", imgszBytes("RIFF", u32le(100), "WEBP", "VP8Q", zeros(20)), "Webp err(Invalid VP8 Tag)"},
	{"webp truncated", imgszBytes("RIFF", u32le(100), "WEBP", "VP8X", u32le(10), 0, 0), "Webp err(failed to fill whole buffer)"},
	{"webp only header", imgszBytes("RIFF", u32le(100), "WEBP"), "Webp err(failed to fill whole buffer)"},
	{"bmp", imgszBytes("BM", zeros(16), u32le(31), u32le(0xFFFFFFF0)), "Bmp 31x4294967280"},
	{"tiff le", imgszBytes("II", u16le(42), u32le(8), u16le(2), u16le(0x100), u16le(3), u32le(1), u16le(640), u16le(0), u16le(0x101), u16le(4), u32le(1), u32le(480)), "Tiff 640x480"},
	{"tiff be", imgszBytes("MM", u16be(42), u32be(8), u16be(3), u16be(0x103), u16be(3), u32be(1), u16be(1), u16be(0), u16be(0x101), u16be(3), u32be(1), u16be(7), u16be(0), u16be(0x100), u16be(4), u32be(1), u32be(9)), "Tiff 9x7"},
	{"tiff rational width", imgszBytes("II", u16le(42), u32le(8), u16le(2), u16le(0x100), u16le(5), u32le(1), u32le(16), u16le(0x101), u16le(3), u32le(1), u16le(4), u16le(0)), "Tiff err(No dimensions in IFD tags)"},
	{"tiff bad type", imgszBytes("II", u16le(42), u32le(8), u16le(1), u16le(0x100), u16le(99), u32le(1), u32le(16)), "Tiff err(Invalid IFD type)"},
	{"tiff zero ifd", imgszBytes("II", u16le(42), u32le(0), zeros(8)), "Tiff err(Invalid IFD offset)"},
	{"tiff short value", imgszBytes("II", u16le(42), u32le(8), u16le(1), u16le(0x100), u16le(3), u32le(1), 5, 0), "Tiff err(Invalid IFD value length)"},
	{"tiff no dims", imgszBytes("II", u16le(42), u32le(8), u16le(0), zeros(4)), "Tiff err(No dimensions in IFD tags)"},
	{"bigtiff", imgszBytes("II", u16le(43), u16le(8), u16le(0), u64le(16), u64le(2), u16le(0x100), u16le(16), u64le(1), u64le(5), u16le(0x101), u16le(3), u64le(1), u16le(6), zeros(6)), "Tiff err(No dimensions in IFD tags)"},
	{"bigtiff bad offset size", imgszBytes("II", u16le(43), u16le(4), u16le(0), zeros(8)), "Tiff err(Unrecognised BigTiff offset size)"},
	{"heif heic", imgszBytes(imgszHeifBox("ftyp", "heic", u32be(0), "mif1"), imgszHeifBox("meta", u32be(0), imgszHeifBox("hdlr", zeros(4)), imgszHeifBox("iprp", imgszHeifBox("ipco", imgszHeifBox("colr", zeros(4)), imgszHeifBox("ispe", u32be(0), u32be(400), u32be(300)), imgszHeifBox("ispe", u32be(0), u32be(40), u32be(30)), imgszHeifBox("irot", 1))))), "Heif(Hevc) 300x400"},
	{"heif mif1 avif", imgszBytes(imgszHeifBox("ftyp", "mif1", u32be(0), "avif"), zeros(8)), "Heif(Av1) err(Invalid heif box size: 0)"},
	{"heif mif1 msf1 jpeg", imgszBytes(u32be(28), "ftyp", "mif1", u32be(0), "msf1", "jpeg", zeros(8)), "Heif(Jpeg) err(failed to fill whole buffer)"},
	{"heif mif1 short", imgszBytes(u32be(28), "ftyp", "mif1", u32be(0)), "Heif(Unknown) err(failed to fill whole buffer)"},
	{"heif unknown brand", imgszBytes(u32be(20), "ftyp", "isom", zeros(12)), "Heif(Unknown) err(failed to fill whole buffer)"},
	{"jxl raw", imgszBytes(0xFF, 0x0A, 0xFA, 0x7F, 0x01, 0x90, 0x08, zeros(9)), "Jxl 4096x4096"},
	{"jxl raw short", imgszBytes(0xFF, 0x0A, 0x00, 0x00, zeros(8)), "Jxl 1x1"},
	{"jxl container", imgszBytes(u32be(12), "JXL \r\n\x87\n", u32be(20), "ftyp", "jxl ", u32be(0), "jxl ", u32be(8+9), "jxlc", 0xFF, 0x0A, 0x01, 0x80, 0x15, 0x44, 0x22, 0x00, 0x00), "Jxl 8x8"},
	{"jxl jxlp", imgszBytes(u32be(12), "JXL \r\n\x87\n", u32be(12), "jxll", 0, 0, 0, 0, u32be(8+4+3), "jxlp", u32be(0), 0xFF, 0x0A, 0x01, u32be(8+4+5), "jxlp", u32be(0x80000001), 0x80, 0x15, 0x44, 0x22, 0x00), "Jxl 8x8"},
	{"ico", imgszBytes(0, 0, 1, 0, u16le(3), 16, 16, zeros(14), 0, 0, zeros(14), 32, 48, zeros(14)), "Ico 256x256"},
	{"ico equal area", imgszBytes(0, 0, 1, 0, u16le(2), 16, 32, zeros(14), 32, 16, zeros(14)), "Ico 32x16"},
	{"ico truncated", imgszBytes(0, 0, 1, 0, u16le(5), 16, 16, zeros(14), 64, 64, zeros(3), 255), "Ico 64x64"},
	{"ico empty", imgszBytes(0, 0, 1, 0, u16le(0), zeros(10)), "Ico err(Hit end of file before finding size)"},
	{"psd", imgszBytes("8BPS", u16be(1), zeros(6), u16be(3), u32be(200), u32be(100)), "Psd 100x200"},
	{"dds", imgszBytes("DDS ", u32le(124), u32le(0), u32le(64), u32le(128)), "Dds 128x64"},
	{"exr", imgszBytes(0x76, 0x2f, 0x31, 0x01, u32le(2), "channels\x00chlist\x00", u32le(2), 0, 0, "dataWindow\x00box2i\x00", u32le(16), u32le(0xFFFFFFFE), u32le(3), u32le(9), u32le(12), 0), "Exr 12x10"},
	{"exr no window", imgszBytes(0x76, 0x2f, 0x31, 0x01, u32le(2), "a\x00b\x00", u32le(1), 7, 0), "Exr err(Data window not found)"},
	{"exr long name", imgszBytes(0x76, 0x2f, 0x31, 0x01, u32le(2), strings.Repeat("x", 40), 0), "Exr err(Delimiter not found within 31 bytes)"},
	{"hdr", imgszBytes("#?RADIANCE\nFORMAT=32-bit_rle_rgbe\n\n-Y 512 +X 768\n"), "Hdr 768x512"},
	{"hdr rgbe rotated", imgszBytes("#?RGBE\nEXPOSURE=1\n+X 10 -Y 20\n"), "Hdr 20x10"},
	{"hdr bad dims", imgszBytes("#?RADIANCE\n\n-Y 512 +X\n"), "Hdr err(Invalid HDR dimensions line)"},
	{"hdr unparsable", imgszBytes("#?RADIANCE\n\n-Y a +X 3\n"), "Hdr err(HDR dimensions not found)"},
	{"hdr plus", imgszBytes("#?RADIANCE\n\n-Y +5 +X 007\n"), "Hdr 7x5"},
	{"ktx2", imgszBytes(0xAB, 0x4B, 0x54, 0x58, 0x20, 0x32, 0x30, 0xBB, 0x0D, 0x0A, 0x1A, 0x0A, u32le(0), u32le(256), u32le(128)), "Ktx2 256x128"},
	{"qoi", imgszBytes("qoif", u32be(800), u32be(600), 4, 0), "Qoi 800x600"},
	{"farbfeld", imgszBytes("farbfeld", u32be(5), u32be(6)), "Farbfeld 5x6"},
	{"pnm", imgszBytes("P6\n# comment\n 640  480\n255\n"), "Pnm 640x480"},
	{"pnm comment after token", imgszBytes("P2 #c\n3 4 15\n"), "Pnm err(failed to fill whole buffer)"},
	{"pnm bad", imgszBytes("P3\nx 4\n255\n"), "err(failed to fill whole buffer) err(failed to fill whole buffer)"},
	{"pnm short", imgszBytes("P1\n12 13 14 15"), "Pnm 12x13"},
	{"not pnm P7", imgszBytes("P7\n12 13 14 15"), "err(Could not decode image) err(Could not decode image)"},
	{"vtf", imgszBytes("VTF\x00", u32le(7), u32le(2), u32le(64), u16le(512), u16le(1024)), "Vtf 512x1024"},
	{"ilbm", imgszBytes("FORM", u32be(100), "ILBM", "ANNO", u32be(3), "abc", 0, "BMHD", u32be(20), u16be(320), u16be(200)), "Ilbm 320x200"},
	{"ilbm body first", imgszBytes("FORM", u32be(100), "PBM ", "BODY", u32be(4), zeros(4)), "Ilbm err(Hit end of file before finding size)"},
	{"aseprite", imgszBytes(u32le(1000), 0xE0, 0xA5, u16le(1), u16le(48), u16le(24)), "Aseprite 48x24"},
	{"tga footer", imgszBytes(0, 0, 2, zeros(9), u16le(33), u16le(44), 32, 8, "TRUEVISION-XFILE.\x00"), "Tga 33x44"},
	{"tga heuristic", imgszBytes(0, 0, 2, zeros(9), u16le(12), u16le(34), 24, 0, zeros(10)), "Tga 12x34"},
	{"tga bad pixel size", imgszBytes(0, 0, 2, zeros(9), u16le(12), u16le(34), 23, 0, zeros(10)), "err(Could not decode image) err(Could not decode image)"},
	{"tga too short", imgszBytes(0, 0, 2, zeros(9), u16le(12), u16le(34)), "err(Could not decode image) err(Could not decode image)"},
	{"tga colormap", imgszBytes(0, 1, 1, u16le(0), u16le(256), 24, zeros(4), u16le(8), u16le(9), 8, 0, zeros(4)), "Tga 8x9"},
	{"unknown", imgszBytes("hello world, nothing here"), "err(Could not decode image) err(Could not decode image)"},
}

// imgszFormat prints a result as the reference oracle does.
func imgszFormat(data []byte) string {
	names := map[imgszImageType]string{
		imgszTypeAseprite: "Aseprite", imgszTypeBmp: "Bmp", imgszTypeDds: "Dds", imgszTypeExr: "Exr",
		imgszTypeFarbfeld: "Farbfeld", imgszTypeGif: "Gif", imgszTypeHdr: "Hdr", imgszTypeHeifAv1: "Heif(Av1)",
		imgszTypeHeifHevc: "Heif(Hevc)", imgszTypeHeifJpeg: "Heif(Jpeg)", imgszTypeHeifUnknown: "Heif(Unknown)",
		imgszTypeIco: "Ico", imgszTypeIlbm: "Ilbm", imgszTypeJpeg: "Jpeg", imgszTypeJxl: "Jxl", imgszTypeKtx2: "Ktx2",
		imgszTypePng: "Png", imgszTypePnm: "Pnm", imgszTypePsd: "Psd", imgszTypeQoi: "Qoi", imgszTypeTga: "Tga",
		imgszTypeTiff: "Tiff", imgszTypeVtf: "Vtf", imgszTypeWebp: "Webp",
	}
	var t, s string
	if ty, err := imgszGetImageType(data); err != nil {
		t = "err(" + err.Error() + ")"
	} else {
		t = names[ty]
	}
	if sz, err := imgszBlobSize(data); err != nil {
		s = "err(" + err.Error() + ")"
	} else {
		s = fmt.Sprintf("%dx%d", sz.Width, sz.Height)
	}
	return t + " " + s
}

func TestImgszOracle(t *testing.T) {
	for _, c := range imgszCases {
		if got := imgszFormat(c.data); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestImgszHelpers(t *testing.T) {
	for _, c := range []struct {
		in   string
		want uint64
		ok   bool
	}{{"0", 0, true}, {"+7", 7, true}, {"+", 0, false}, {"", 0, false}, {"-1", 0, false}, {"1a", 0, false},
		{"18446744073709551615", 1<<64 - 1, true}, {"18446744073709551616", 0, false}} {
		if got, ok := imgszParseUsize(c.in); got != c.want || ok != c.ok {
			t.Errorf("parse %q: got %d %v", c.in, got, ok)
		}
	}
	r := &imgszCursor{data: []byte{1, 2, 3}}
	if r.seekCurrent(-1) == nil || r.pos != 0 {
		t.Error("negative seek must fail without moving")
	}
	r.seekStart(10)
	if n := r.read(make([]byte, 4)); n != 0 {
		t.Errorf("read past the end gave %d bytes", n)
	}
	if r.seekEnd(-3) != nil || r.pos != 0 || r.seekEnd(-4) == nil {
		t.Error("seek from end")
	}
}
