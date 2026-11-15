// Port of usvg 0.47 src/parser/options.rs.

package usvg

import (
	"path/filepath"
	"strings"

	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/text"
)

// Options are the processing options (usvg::Options).
type Options struct {
	// ResourcesDir is the directory relative paths are resolved against; empty is None. Expected to be
	// the directory that holds the SVG file. Default: None.
	ResourcesDir string
	// DPI is the target DPI; it affects unit conversion. Default: 96.
	DPI float32
	// FontFamily is the font family used when no `font-family` is set. Default: Times New Roman.
	FontFamily string
	// FontSize is the font size used when no `font-size` is set. Default: 12.
	FontSize float32
	// Languages resolve the `systemLanguage` conditional attribute, as `en` or `en-US`. Default: [en].
	Languages []string
	// ShapeRendering is used when `shape-rendering` is `auto`. Default: GeometricPrecision.
	ShapeRendering ShapeRendering
	// TextRendering is used when `text-rendering` is `auto`. Default: OptimizeLegibility.
	TextRendering TextRendering
	// ImageRendering is used when `image-rendering` is `auto`. Default: OptimizeQuality.
	ImageRendering ImageRendering
	// DefaultSize is the viewport assumed with no `viewBox` and relative `width` or `height`.
	// Default: 100x100.
	DefaultSize skia.Size
	// ImageHrefResolver handles `xlink:href` in `image` elements. Default: DefaultImageHrefResolver.
	ImageHrefResolver ImageHrefResolver
	// StyleSheet is a CSS style sheet injected into the SVG, used when HasStyleSheet is set (Rust's
	// Option<String>). Default: None.
	StyleSheet    string
	HasStyleSheet bool
	// FontDB is the font database text resolves against; nil is an empty one. Default: empty.
	FontDB *text.Database
	// FontResolver selects fonts and fallback fonts for text. Default: DefaultFontResolver.
	FontResolver FontResolver
}

// DefaultOptions is Options::default().
func DefaultOptions() Options {
	size, _ := skia.SizeFromWH(100, 100)
	return Options{
		DPI: 96,
		// The default font is user-agent dependent, so any will do.
		FontFamily:        "Times New Roman",
		FontSize:          12,
		Languages:         []string{"en"},
		ShapeRendering:    DefaultShapeRendering,
		TextRendering:     DefaultTextRendering,
		ImageRendering:    ImageRenderingOptimizeQuality,
		DefaultSize:       size,
		ImageHrefResolver: DefaultImageHrefResolver(),
		FontDB:            text.NewDatabase(),
		FontResolver:      DefaultFontResolver(),
	}
}

// GetAbsPath converts a relative path into one relative to ResourcesDir (Rust's PathBuf::join); with
// no ResourcesDir it returns rel.
func (o *Options) GetAbsPath(rel string) string {
	if o.ResourcesDir == "" {
		return rel
	}
	return optionsJoinPath(o.ResourcesDir, rel)
}

// optionsJoinPath is PathBuf::join: an absolute rel replaces base, a rooted rel without a volume keeps
// base's volume (Windows), otherwise a separator goes between unless base ends with one. No cleaning.
func optionsJoinPath(base, rel string) string {
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return rel
	}
	if strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, string(filepath.Separator)) {
		return filepath.VolumeName(base) + rel
	}
	if base == "" || strings.HasSuffix(base, "/") || strings.HasSuffix(base, string(filepath.Separator)) {
		return base + rel
	}
	return base + string(filepath.Separator) + rel
}
