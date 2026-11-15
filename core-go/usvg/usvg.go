// Port of usvg 0.47 src/parser/mod.rs: the public parsing entry points.

package usvg

import (
	"bytes"
	"compress/gzip"
	"io"
	"math"
	"unicode/utf8"
)

// FromData parses a Tree from SVG data, plain or gzip-compressed (Tree::from_data).
func FromData(data []byte, opt *Options) (*Tree, error) {
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		decoded, err := DecompressSVGZ(data)
		if err != nil {
			return nil, err
		}
		data = decoded
	}
	if !utf8.Valid(data) {
		return nil, &Error{Kind: ErrNotAnUtf8Str}
	}
	return FromStr(string(data), opt)
}

// FromDataNested is FromData for an SVG loaded by an `image` element: images linking to external files
// are ignored, as the SVG spec requires (Tree::from_data_nested). Only the DPI, font size, languages,
// rendering modes, default size and the data resolver carry over; the rest is default.
func FromDataNested(data []byte, opt *Options) (*Tree, error) {
	nested := DefaultOptions()
	nested.DPI = opt.DPI
	nested.FontSize = opt.FontSize
	nested.Languages = append([]string(nil), opt.Languages...)
	nested.ShapeRendering = opt.ShapeRendering
	nested.TextRendering = opt.TextRendering
	nested.ImageRendering = opt.ImageRendering
	nested.DefaultSize = opt.DefaultSize
	resolveData := opt.ImageHrefResolver.ResolveData
	nested.ImageHrefResolver = ImageHrefResolver{
		ResolveData: func(mime string, data []byte, o *Options) (ImageKind, bool) {
			if resolveData == nil {
				return nil, false
			}
			return resolveData(mime, data, o)
		},
		// External images are ignored.
		ResolveString: func(string, *Options) (ImageKind, bool) { return nil, false },
	}
	return FromData(data, &nested)
}

// FromStr parses a Tree from an SVG string (Tree::from_str), with DTDs allowed.
func FromStr(text string, opt *Options) (*Tree, error) {
	xml, xerr := xmlParseDoc(text, xmlParsingOptions{AllowDTD: true, NodesLimit: math.MaxUint32})
	if xerr != nil {
		return nil, &Error{Kind: ErrParsingFailed, XML: xerr}
	}
	return fromXMLTree(xml, opt)
}

// fromXMLTree is Tree::from_xmltree.
func fromXMLTree(xml *xmlDocument, opt *Options) (*Tree, error) {
	doc, err := svgParseTree(xml, opt.StyleSheet, opt.HasStyleSheet)
	if err != nil {
		return nil, err
	}
	return convertDoc(doc, opt)
}

// DecompressSVGZ decompresses SVGZ data; any gzip failure is ErrMalformedGZip.
func DecompressSVGZ(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, &Error{Kind: ErrMalformedGZip}
	}
	r.Multistream(false) // flate2's GzDecoder reads only the first member
	decoded, err := io.ReadAll(r)
	if err != nil {
		return nil, &Error{Kind: ErrMalformedGZip}
	}
	return decoded, nil
}
