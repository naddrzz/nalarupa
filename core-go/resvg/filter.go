package resvg

import (
	"errors"

	"github.com/naddrzz/nalarupa/core-go/skia"
	"github.com/naddrzz/nalarupa/core-go/usvg"
)

// Port of filter/mod.rs. Rust's ImageRef/ImageRefMut views over `[RGBA8]` are imageRef here: the pixmap's
// bytes with its size, four bytes per pixel.

type imageRef struct {
	data          []byte
	width, height uint32
}

func asImageRef(p *skia.Pixmap) imageRef { return imageRef{p.DataMut(), p.Width(), p.Height()} }

func (r imageRef) alphaAt(x, y uint32) int16 { return int16(r.data[(r.width*y+x)*4+3]) }

func (r imageRef) at(x, y uint32) []byte {
	i := (r.width*y + x) * 4
	return r.data[i : i+4 : i+4]
}

var (
	errInvalidRegion = errors.New("filter has an invalid region")
	errNoResults     = errors.New("filter has no results")
)

func tryCreate(w, h uint32) (*skia.Pixmap, error) {
	p, ok := skia.NewPixmap(w, h)
	if !ok {
		return nil, errInvalidRegion
	}
	return p, nil
}

func copyRegion(p *skia.Pixmap, region skia.IntRect) (*skia.Pixmap, error) {
	rect, ok := skia.IntRectFromXYWH(region.X(), region.Y(), region.Width(), region.Height())
	if !ok {
		return nil, errInvalidRegion
	}
	c, ok := p.CloneRect(rect)
	if !ok {
		return nil, errInvalidRegion
	}
	return c, nil
}

func clearPixmap(p *skia.Pixmap) { p.Fill(skia.Color{}) }

func intoSRGB(p *skia.Pixmap) {
	demultiplyAlpha(p.DataMut())
	fromLinearRGB(p.DataMut())
	multiplyAlpha(p.DataMut())
}

func intoLinearRGB(p *skia.Pixmap) {
	demultiplyAlpha(p.DataMut())
	intoLinearRGBData(p.DataMut())
	multiplyAlpha(p.DataMut())
}

// multiplyAlpha multiplies the pixels' alpha.
func multiplyAlpha(data []byte) {
	for i := 0; i < len(data); i += 4 {
		a := float32(data[i+3]) / 255.0
		data[i+2] = asU8(float32(float32(data[i+2])*a) + 0.5)
		data[i+1] = asU8(float32(float32(data[i+1])*a) + 0.5)
		data[i] = asU8(float32(float32(data[i])*a) + 0.5)
	}
}

// demultiplyAlpha demultiplies the pixels' alpha.
func demultiplyAlpha(data []byte) {
	for i := 0; i < len(data); i += 4 {
		a := float32(data[i+3]) / 255.0
		data[i+2] = asU8(float32(data[i+2])/a + 0.5)
		data[i+1] = asU8(float32(data[i+1])/a + 0.5)
		data[i] = asU8(float32(data[i])/a + 0.5)
	}
}

// srgbToLinearRGBTable is the precomputed sRGB to LinearRGB table.
var srgbToLinearRGBTable = [256]uint8{
	0, 0, 0, 0, 0, 0, 0, 1, 1, 1, 1, 1, 1, 1, 1, 1,
	1, 1, 2, 2, 2, 2, 2, 2, 2, 2, 3, 3, 3, 3, 3, 3,
	4, 4, 4, 4, 4, 5, 5, 5, 5, 6, 6, 6, 6, 7, 7, 7,
	8, 8, 8, 8, 9, 9, 9, 10, 10, 10, 11, 11, 12, 12, 12, 13,
	13, 13, 14, 14, 15, 15, 16, 16, 17, 17, 17, 18, 18, 19, 19, 20,
	20, 21, 22, 22, 23, 23, 24, 24, 25, 25, 26, 27, 27, 28, 29, 29,
	30, 30, 31, 32, 32, 33, 34, 35, 35, 36, 37, 37, 38, 39, 40, 41,
	41, 42, 43, 44, 45, 45, 46, 47, 48, 49, 50, 51, 51, 52, 53, 54,
	55, 56, 57, 58, 59, 60, 61, 62, 63, 64, 65, 66, 67, 68, 69, 70,
	71, 72, 73, 74, 76, 77, 78, 79, 80, 81, 82, 84, 85, 86, 87, 88,
	90, 91, 92, 93, 95, 96, 97, 99, 100, 101, 103, 104, 105, 107, 108, 109,
	111, 112, 114, 115, 116, 118, 119, 121, 122, 124, 125, 127, 128, 130, 131, 133,
	134, 136, 138, 139, 141, 142, 144, 146, 147, 149, 151, 152, 154, 156, 157, 159,
	161, 163, 164, 166, 168, 170, 171, 173, 175, 177, 179, 181, 183, 184, 186, 188,
	190, 192, 194, 196, 198, 200, 202, 204, 206, 208, 210, 212, 214, 216, 218, 220,
	222, 224, 226, 229, 231, 233, 235, 237, 239, 242, 244, 246, 248, 250, 253, 255,
}

// linearRGBToSRGBTable is the precomputed LinearRGB to sRGB table.
var linearRGBToSRGBTable = [256]uint8{
	0, 13, 22, 28, 34, 38, 42, 46, 50, 53, 56, 59, 61, 64, 66, 69,
	71, 73, 75, 77, 79, 81, 83, 85, 86, 88, 90, 92, 93, 95, 96, 98,
	99, 101, 102, 104, 105, 106, 108, 109, 110, 112, 113, 114, 115, 117, 118, 119,
	120, 121, 122, 124, 125, 126, 127, 128, 129, 130, 131, 132, 133, 134, 135, 136,
	137, 138, 139, 140, 141, 142, 143, 144, 145, 146, 147, 148, 148, 149, 150, 151,
	152, 153, 154, 155, 155, 156, 157, 158, 159, 159, 160, 161, 162, 163, 163, 164,
	165, 166, 167, 167, 168, 169, 170, 170, 171, 172, 173, 173, 174, 175, 175, 176,
	177, 178, 178, 179, 180, 180, 181, 182, 182, 183, 184, 185, 185, 186, 187, 187,
	188, 189, 189, 190, 190, 191, 192, 192, 193, 194, 194, 195, 196, 196, 197, 197,
	198, 199, 199, 200, 200, 201, 202, 202, 203, 203, 204, 205, 205, 206, 206, 207,
	208, 208, 209, 209, 210, 210, 211, 212, 212, 213, 213, 214, 214, 215, 215, 216,
	216, 217, 218, 218, 219, 219, 220, 220, 221, 221, 222, 222, 223, 223, 224, 224,
	225, 226, 226, 227, 227, 228, 228, 229, 229, 230, 230, 231, 231, 232, 232, 233,
	233, 234, 234, 235, 235, 236, 236, 237, 237, 238, 238, 238, 239, 239, 240, 240,
	241, 241, 242, 242, 243, 243, 244, 244, 245, 245, 246, 246, 246, 247, 247, 248,
	248, 249, 249, 250, 250, 251, 251, 251, 252, 252, 253, 253, 254, 254, 255, 255,
}

// intoLinearRGBData converts unpremultiplied pixels from sRGB into LinearRGB.
func intoLinearRGBData(data []byte) {
	for i := 0; i < len(data); i += 4 {
		data[i] = srgbToLinearRGBTable[data[i]]
		data[i+1] = srgbToLinearRGBTable[data[i+1]]
		data[i+2] = srgbToLinearRGBTable[data[i+2]]
	}
}

// fromLinearRGB converts unpremultiplied pixels from LinearRGB into sRGB.
func fromLinearRGB(data []byte) {
	for i := 0; i < len(data); i += 4 {
		data[i] = linearRGBToSRGBTable[data[i]]
		data[i+1] = linearRGBToSRGBTable[data[i+1]]
		data[i+2] = linearRGBToSRGBTable[data[i+2]]
	}
}

// filterImage is a filter primitive result. All images have the size of the filter region; region (in
// global coordinates) is the part with actual data, used by feTile. owned is Rust's unique Rc: take hands
// over an owned pixmap and copies a shared one (a stored result), as Rc::try_unwrap falls back to a clone.
type filterImage struct {
	image      *skia.Pixmap
	region     skia.IntRect
	colorSpace usvg.ColorInterpolation
	owned      bool
}

func imageFrom(p *skia.Pixmap, cs usvg.ColorInterpolation) filterImage {
	r, _ := skia.IntRectFromXYWH(0, 0, p.Width(), p.Height())
	return filterImage{p, r, cs, true}
}

func (img filterImage) intoColorSpace(cs usvg.ColorInterpolation) filterImage {
	if cs == img.colorSpace {
		return img
	}
	p := img.take()
	switch cs {
	case usvg.ColorInterpolationSRGB:
		intoSRGB(p)
	case usvg.ColorInterpolationLinearRGB:
		intoLinearRGB(p)
	}
	return filterImage{p, img.region, cs, true}
}

func (img filterImage) take() *skia.Pixmap {
	if img.owned {
		return img.image
	}
	return clonePixmap(img.image)
}

func (img filterImage) width() uint32  { return img.image.Width() }
func (img filterImage) height() uint32 { return img.image.Height() }

type filterResult struct {
	name  string
	image filterImage
}

func applyFilter(filter *usvg.Filter, ts skia.Transform, source *skia.Pixmap) {
	result, err := applyFilterInner(filter, ts, source)
	if err == nil {
		err = applyToCanvas(result, source)
	}
	// Clear on error. Rust logs an invalid region.
	if err != nil {
		source.Fill(skia.Color{})
	}
}

func applyFilterInner(filter *usvg.Filter, ts skia.Transform, source *skia.Pixmap) (filterImage, error) {
	// Nalarupa: the region never reaches past `source` (the layer, clamped to its target): flood and
	// composite results are region-sized, so a huge node allocated gigabytes.
	r, ok := filter.Rect().Transform(ts)
	if !ok {
		return filterImage{}, errInvalidRegion
	}
	bounds, ok := skia.IntRectFromXYWH(0, 0, source.Width(), source.Height())
	if !ok {
		return filterImage{}, errInvalidRegion
	}
	region, ok := fitToRect(r.ToIntRect(), bounds)
	if !ok {
		return filterImage{}, errInvalidRegion
	}

	var results []filterResult
	for i := range filter.Primitives() {
		primitive := &filter.Primitives()[i]
		sr, ok := primitive.Rect().Transform(ts)
		if !ok {
			return filterImage{}, errInvalidRegion
		}
		subregion := sr.ToIntRect()

		// `feOffset` inherits its region from the input.
		offset, isOffset := primitive.Kind().(*usvg.FeOffset)
		if isOffset && offset.Input().Kind == usvg.FilterInputReference {
			if res, ok := findResult(results, offset.Input().Name); ok {
				subregion = res.image.region
			}
		}

		cs := primitive.ColorInterpolation()

		var result filterImage
		var err error
		input := func(in usvg.FilterInput) filterImage { return getInput(in, region, source, results) }
		switch fe := primitive.Kind().(type) {
		case *usvg.FeBlend:
			result, err = applyBlend(fe, cs, region, input(fe.Input1()), input(fe.Input2()))
		case *usvg.FeDropShadow:
			result, err = applyDropShadow(fe, cs, ts, input(fe.Input()))
		case *usvg.FeFlood:
			result, err = applyFlood(fe, region)
		case *usvg.FeGaussianBlur:
			result, err = applyBlur(fe, cs, ts, input(fe.Input()))
		case *usvg.FeOffset:
			result, err = applyOffset(fe, ts, input(fe.Input()))
		case *usvg.FeComposite:
			result, err = applyComposite(fe, cs, region, input(fe.Input1()), input(fe.Input2()))
		case *usvg.FeMerge:
			result, err = applyMerge(fe, cs, region, source, results)
		case *usvg.FeTile:
			result, err = applyTile(input(fe.Input()), region)
		case *usvg.FeImage:
			result, err = applyImage(fe, region, subregion, ts)
		case *usvg.FeComponentTransfer:
			result, err = applyComponentTransfer(fe, cs, input(fe.Input()))
		case *usvg.FeColorMatrix:
			result, err = applyColorMatrix(fe, cs, input(fe.Input()))
		case *usvg.FeConvolveMatrix:
			result, err = applyConvolveMatrix(fe, cs, input(fe.Input()))
		case *usvg.FeMorphology:
			result, err = applyMorphology(fe, cs, ts, input(fe.Input()))
		case *usvg.FeDisplacementMap:
			result, err = applyDisplacementMap(fe, region, cs, ts, input(fe.Input1()), input(fe.Input2()))
		case *usvg.FeTurbulence:
			result, err = applyTurbulence(fe, region, cs, ts)
		case *usvg.FeDiffuseLighting:
			result, err = applyDiffuseLighting(fe, region, cs, ts, input(fe.Input()))
		case *usvg.FeSpecularLighting:
			result, err = applySpecularLighting(fe, region, cs, ts, input(fe.Input()))
		}
		if err != nil {
			return filterImage{}, err
		}

		if region != subregion {
			// Clip the result.
			var subregion2 skia.IntRect
			if isOffset {
				// We do not support clipping on feOffset.
				subregion2, ok = region.TranslateTo(0, 0)
			} else {
				subregion2, ok = subregion.Translate(-region.X(), -region.Y())
			}
			if !ok {
				panic("resvg: filter subregion") // Rust unwraps
			}

			// This is cropping by clearing the pixels outside the region.
			paint := skia.DefaultPaint()
			paint.SetColor(skia.ColorFromRGBA8(0, 0, 0, 255))
			paint.BlendMode = skia.BlendModeClear

			pixmap := result.take()
			w := float32(pixmap.Width())
			h := float32(pixmap.Height())
			if rect, ok := skia.RectFromXYWH(0, 0, w, float32(subregion2.Y())); ok {
				pixmap.FillRect(rect, &paint, skia.Identity(), nil)
			}
			if rect, ok := skia.RectFromXYWH(0, 0, float32(subregion2.X()), h); ok {
				pixmap.FillRect(rect, &paint, skia.Identity(), nil)
			}
			if rect, ok := skia.RectFromXYWH(float32(subregion2.Right()), 0, w, h); ok {
				pixmap.FillRect(rect, &paint, skia.Identity(), nil)
			}
			if rect, ok := skia.RectFromXYWH(0, float32(subregion2.Bottom()), w, h); ok {
				pixmap.FillRect(rect, &paint, skia.Identity(), nil)
			}

			result = filterImage{pixmap, subregion, result.colorSpace, true}
		}

		result.owned = false // shared by the results from now on
		results = append(results, filterResult{primitive.Result(), result})
	}

	if len(results) == 0 {
		return filterImage{}, errNoResults
	}
	return results[len(results)-1].image, nil
}

func findResult(results []filterResult, name string) (filterResult, bool) {
	for i := len(results) - 1; i >= 0; i-- {
		if results[i].name == name {
			return results[i], true
		}
	}
	return filterResult{}, false
}

func getInput(input usvg.FilterInput, region skia.IntRect, source *skia.Pixmap, results []filterResult) filterImage {
	switch input.Kind {
	case usvg.FilterInputSourceGraphic:
		return filterImage{clonePixmap(source), region, usvg.ColorInterpolationSRGB, true}
	case usvg.FilterInputSourceAlpha:
		image := clonePixmap(source)
		// Set RGB to black. Keep alpha as is.
		data := image.DataMut()
		for i := 0; i < len(data); i += 4 {
			data[i], data[i+1], data[i+2] = 0, 0, 0
		}
		return filterImage{image, region, usvg.ColorInterpolationSRGB, true}
	}
	if res, ok := findResult(results, input.Name); ok {
		return res.image
	}
	// Technically unreachable. Rust logs an unknown reference.
	return getInput(usvg.FilterInput{Kind: usvg.FilterInputSourceGraphic}, region, source, results)
}

func drawPlain(dst *skia.Pixmap, x, y int32, src *skia.Pixmap) {
	paint := skia.DefaultPixmapPaint()
	dst.DrawPixmap(x, y, src.AsRef(), &paint, skia.Identity(), nil)
}

func drawBlended(dst *skia.Pixmap, src *skia.Pixmap, mode skia.BlendMode) {
	paint := skia.DefaultPixmapPaint()
	paint.BlendMode = mode
	dst.DrawPixmap(0, 0, src.AsRef(), &paint, skia.Identity(), nil)
}

func applyDropShadow(fe *usvg.FeDropShadow, cs usvg.ColorInterpolation, ts skia.Transform, input filterImage) (filterImage, error) {
	dx, dy := offsetCoordinates(fe.Dx(), fe.Dy(), ts)

	pixmap, err := tryCreate(input.width(), input.height())
	if err != nil {
		return filterImage{}, err
	}
	inputPixmap := input.intoColorSpace(cs).take()
	shadow := clonePixmap(inputPixmap)

	if sdx, sdy, useBox, ok := resolveStdDev(fe.StdDevX(), fe.StdDevY(), ts); ok {
		if useBox {
			boxBlur(sdx, sdy, asImageRef(shadow))
		} else {
			iirBlur(sdx, sdy, asImageRef(shadow))
		}
	}

	// flood
	c := fe.Color()
	color := skia.ColorFromRGBA8(c.Red, c.Green, c.Blue, opacityToU8(fe.Opacity()))
	data := shadow.DataMut()
	for i := 0; i < len(data); i += 4 {
		col := color
		col.ApplyOpacity(float32(data[i+3]) / 255.0)
		p := col.Premultiply().ToColorU8()
		copy(data[i:i+4], p[:])
	}

	switch cs {
	case usvg.ColorInterpolationSRGB:
		intoSRGB(shadow)
	case usvg.ColorInterpolationLinearRGB:
		intoLinearRGB(shadow)
	}

	drawPlain(pixmap, asI32(dx), asI32(dy), shadow)
	drawPlain(pixmap, 0, 0, inputPixmap)
	return imageFrom(pixmap, cs), nil
}

func applyBlur(fe *usvg.FeGaussianBlur, cs usvg.ColorInterpolation, ts skia.Transform, input filterImage) (filterImage, error) {
	sdx, sdy, useBox, ok := resolveStdDev(fe.StdDevX(), fe.StdDevY(), ts)
	if !ok {
		return input, nil
	}
	pixmap := input.intoColorSpace(cs).take()
	if useBox {
		boxBlur(sdx, sdy, asImageRef(pixmap))
	} else {
		iirBlur(sdx, sdy, asImageRef(pixmap))
	}
	return imageFrom(pixmap, cs), nil
}

func applyOffset(fe *usvg.FeOffset, ts skia.Transform, input filterImage) (filterImage, error) {
	dx, dy := offsetCoordinates(fe.Dx(), fe.Dy(), ts)
	if approxZeroUlps32(dx, 4) && approxZeroUlps32(dy, 4) {
		return input, nil
	}
	pixmap, err := tryCreate(input.width(), input.height())
	if err != nil {
		return filterImage{}, err
	}
	drawPlain(pixmap, asI32(dx), asI32(dy), input.image)
	return imageFrom(pixmap, input.colorSpace), nil
}

func applyBlend(fe *usvg.FeBlend, cs usvg.ColorInterpolation, region skia.IntRect, input1, input2 filterImage) (filterImage, error) {
	input1 = input1.intoColorSpace(cs)
	input2 = input2.intoColorSpace(cs)
	pixmap, err := tryCreate(region.Width(), region.Height())
	if err != nil {
		return filterImage{}, err
	}
	drawPlain(pixmap, 0, 0, input2.image)
	drawBlended(pixmap, input1.image, convertBlendMode(fe.Mode()))
	return imageFrom(pixmap, cs), nil
}

func applyComposite(fe *usvg.FeComposite, cs usvg.ColorInterpolation, region skia.IntRect, input1, input2 filterImage) (filterImage, error) {
	input1 = input1.intoColorSpace(cs)
	input2 = input2.intoColorSpace(cs)
	pixmap, err := tryCreate(region.Width(), region.Height())
	if err != nil {
		return filterImage{}, err
	}

	op := fe.Operator()
	if op.Op == usvg.CompositeArithmetic {
		compositeArithmetic(op.K1, op.K2, op.K3, op.K4, asImageRef(input1.take()), asImageRef(input2.take()), asImageRef(pixmap))
		return imageFrom(pixmap, cs), nil
	}

	drawPlain(pixmap, 0, 0, input2.image)
	mode := skia.BlendModeSourceOver
	switch op.Op {
	case usvg.CompositeIn:
		mode = skia.BlendModeSourceIn
	case usvg.CompositeOut:
		mode = skia.BlendModeSourceOut
	case usvg.CompositeAtop:
		mode = skia.BlendModeSourceAtop
	case usvg.CompositeXor:
		mode = skia.BlendModeXor
	}
	drawBlended(pixmap, input1.image, mode)
	return imageFrom(pixmap, cs), nil
}

func applyMerge(fe *usvg.FeMerge, cs usvg.ColorInterpolation, region skia.IntRect, source *skia.Pixmap, results []filterResult) (filterImage, error) {
	pixmap, err := tryCreate(region.Width(), region.Height())
	if err != nil {
		return filterImage{}, err
	}
	for _, in := range fe.Inputs() {
		input := getInput(in, region, source, results).intoColorSpace(cs)
		drawPlain(pixmap, 0, 0, input.image)
	}
	return imageFrom(pixmap, cs), nil
}

func applyFlood(fe *usvg.FeFlood, region skia.IntRect) (filterImage, error) {
	c := fe.Color()
	pixmap, err := tryCreate(region.Width(), region.Height())
	if err != nil {
		return filterImage{}, err
	}
	pixmap.Fill(skia.ColorFromRGBA8(c.Red, c.Green, c.Blue, opacityToU8(fe.Opacity())))
	return imageFrom(pixmap, usvg.ColorInterpolationSRGB), nil
}

func applyTile(input filterImage, region skia.IntRect) (filterImage, error) {
	subregion, ok := input.region.Translate(-region.X(), -region.Y())
	if !ok {
		panic("resvg: feTile subregion") // Rust unwraps
	}
	tile, err := copyRegion(input.image, subregion)
	if err != nil {
		return filterImage{}, err
	}
	paint := skia.DefaultPaint()
	paint.Shader = skia.NewPattern(tile.AsRef(), skia.SpreadModeRepeat, skia.FilterQualityBicubic, 1,
		skia.TransformFromTranslate(float32(subregion.X()), float32(subregion.Y())))

	pixmap, err := tryCreate(region.Width(), region.Height())
	if err != nil {
		return filterImage{}, err
	}
	rect, ok := skia.RectFromXYWH(0, 0, float32(region.Width()), float32(region.Height()))
	if !ok {
		panic("resvg: feTile rect") // Rust unwraps
	}
	pixmap.FillRect(rect, &paint, skia.Identity(), nil)
	return imageFrom(pixmap, usvg.ColorInterpolationSRGB), nil
}

func applyImage(fe *usvg.FeImage, region, subregion skia.IntRect, ts skia.Transform) (filterImage, error) {
	pixmap, err := tryCreate(region.Width(), region.Height())
	if err != nil {
		return filterImage{}, err
	}
	sx, sy := ts.GetScale()
	transform := skia.TransformFromRow(sx, 0, 0, sy, float32(subregion.X()), float32(subregion.Y()))
	renderNodes(fe.Root(), transform, pixmap.AsMut())
	return imageFrom(pixmap, usvg.ColorInterpolationSRGB), nil
}

func applyComponentTransfer(fe *usvg.FeComponentTransfer, cs usvg.ColorInterpolation, input filterImage) (filterImage, error) {
	pixmap := input.intoColorSpace(cs).take()
	demultiplyAlpha(pixmap.DataMut())
	componentTransfer(fe, asImageRef(pixmap))
	multiplyAlpha(pixmap.DataMut())
	return imageFrom(pixmap, cs), nil
}

func applyColorMatrix(fe *usvg.FeColorMatrix, cs usvg.ColorInterpolation, input filterImage) (filterImage, error) {
	pixmap := input.intoColorSpace(cs).take()
	demultiplyAlpha(pixmap.DataMut())
	colorMatrix(fe.Kind(), asImageRef(pixmap))
	multiplyAlpha(pixmap.DataMut())
	return imageFrom(pixmap, cs), nil
}

func applyConvolveMatrix(fe *usvg.FeConvolveMatrix, cs usvg.ColorInterpolation, input filterImage) (filterImage, error) {
	pixmap := input.intoColorSpace(cs).take()
	if fe.PreserveAlpha() {
		demultiplyAlpha(pixmap.DataMut())
	}
	convolveMatrix(fe, asImageRef(pixmap))
	return imageFrom(pixmap, cs), nil
}

func applyMorphology(fe *usvg.FeMorphology, cs usvg.ColorInterpolation, ts skia.Transform, input filterImage) (filterImage, error) {
	pixmap := input.intoColorSpace(cs).take()
	rx, ry := scaleCoordinates(fe.RadiusX(), fe.RadiusY(), ts)
	if !(rx > 0 && ry > 0) {
		clearPixmap(pixmap)
		return imageFrom(pixmap, cs), nil
	}
	morphology(fe.Operator(), rx, ry, asImageRef(pixmap))
	return imageFrom(pixmap, cs), nil
}

func applyDisplacementMap(fe *usvg.FeDisplacementMap, region skia.IntRect, cs usvg.ColorInterpolation, ts skia.Transform, input1, input2 filterImage) (filterImage, error) {
	pixmap1 := input1.intoColorSpace(cs).take()
	pixmap2 := input2.intoColorSpace(cs).take()
	pixmap, err := tryCreate(region.Width(), region.Height())
	if err != nil {
		return filterImage{}, err
	}
	sx, sy := scaleCoordinates(fe.Scale(), fe.Scale(), ts)
	displacementMap(fe, sx, sy, asImageRef(pixmap1), asImageRef(pixmap2), asImageRef(pixmap))
	return imageFrom(pixmap, cs), nil
}

func applyTurbulence(fe *usvg.FeTurbulence, region skia.IntRect, cs usvg.ColorInterpolation, ts skia.Transform) (filterImage, error) {
	pixmap, err := tryCreate(region.Width(), region.Height())
	if err != nil {
		return filterImage{}, err
	}
	sx, sy := ts.GetScale()
	if approxZeroUlps32(sx, 4) || approxZeroUlps32(sy, 4) {
		return imageFrom(pixmap, cs), nil
	}
	turbulence(
		float64(region.X())-float64(ts.TX),
		float64(region.Y())-float64(ts.TY),
		float64(sx), float64(sy),
		float64(fe.BaseFrequencyX()), float64(fe.BaseFrequencyY()),
		fe.NumOctaves(), fe.Seed(), fe.StitchTiles(),
		fe.Kind() == usvg.TurbulenceFractalNoise,
		asImageRef(pixmap),
	)
	multiplyAlpha(pixmap.DataMut())
	return imageFrom(pixmap, cs), nil
}

func applyDiffuseLighting(fe *usvg.FeDiffuseLighting, region skia.IntRect, cs usvg.ColorInterpolation, ts skia.Transform, input filterImage) (filterImage, error) {
	pixmap, err := tryCreate(region.Width(), region.Height())
	if err != nil {
		return filterImage{}, err
	}
	light := transformLightSource(fe.LightSource(), region, ts)
	diffuseLighting(fe, light, asImageRef(input.image), asImageRef(pixmap))
	return imageFrom(pixmap, cs), nil
}

func applySpecularLighting(fe *usvg.FeSpecularLighting, region skia.IntRect, cs usvg.ColorInterpolation, ts skia.Transform, input filterImage) (filterImage, error) {
	pixmap, err := tryCreate(region.Width(), region.Height())
	if err != nil {
		return filterImage{}, err
	}
	light := transformLightSource(fe.LightSource(), region, ts)
	specularLighting(fe, light, asImageRef(input.image), asImageRef(pixmap))
	return imageFrom(pixmap, cs), nil
}

// sqrt2 is std::f32::consts::SQRT_2.
const sqrt2 = float32(1.41421356237309504880168872420969808)

func transformLightSource(source usvg.LightSource, region skia.IntRect, ts skia.Transform) usvg.LightSource {
	switch light := source.(type) {
	case usvg.PointLight:
		p := skia.Pt(light.X, light.Y)
		ts.MapPoint(&p)
		light.X = p.X - float32(region.X())
		light.Y = p.Y - float32(region.Y())
		light.Z = float32(light.Z*sqrt32(float32(ts.SX*ts.SX)+float32(ts.SY*ts.SY))) / sqrt2
		return light
	case usvg.SpotLight:
		sz := sqrt32(float32(ts.SX*ts.SX)+float32(ts.SY*ts.SY)) / sqrt2

		p := skia.Pt(light.X, light.Y)
		ts.MapPoint(&p)
		light.X = p.X - float32(region.X())
		light.Y = p.Y - float32(region.X()) // sic: upstream subtracts x
		light.Z = float32(light.Z * sz)

		p = skia.Pt(light.PointsAtX, light.PointsAtY)
		ts.MapPoint(&p)
		light.PointsAtX = p.X - float32(region.X())
		light.PointsAtY = p.Y - float32(region.X()) // sic
		light.PointsAtZ = float32(light.PointsAtZ * sz)
		return light
	}
	return source
}

func applyToCanvas(input filterImage, pixmap *skia.Pixmap) error {
	input = input.intoColorSpace(usvg.ColorInterpolationSRGB)
	pixmap.Fill(skia.Color{})
	drawPlain(pixmap, 0, 0, input.image)
	return nil
}

// resolveStdDev calculates the Gaussian blur sigmas for the current world transform; useBox says a box
// blur should be used rather than IIR. ok is false when the blur is disabled.
func resolveStdDev(stdDx, stdDy float32, ts skia.Transform) (float64, float64, bool, bool) {
	stdDx, stdDy = scaleCoordinates(stdDx, stdDy, ts)

	// 'A negative value or a value of zero disables the effect of the given filter primitive (i.e., the
	// result is the filter input image).'
	if approxEqUlps32(stdDx, 0, 4) && approxEqUlps32(stdDy, 0, 4) {
		return 0, 0, false, false
	}

	// Ignore tiny sigmas. In case of IIR blur it can lead to a transparent image.
	if stdDx < 0.05 {
		stdDx = 0
	}
	if stdDy < 0.05 {
		stdDy = 0
	}

	const blurSigmaThreshold = 2.0
	// Check that the current feGaussianBlur filter can be applied using a box blur.
	box := stdDx >= blurSigmaThreshold || stdDy >= blurSigmaThreshold
	return float64(stdDx), float64(stdDy), box, true
}

// offsetCoordinates maps a filter offset onto the canvas.
// Nalarupa: an offset is a vector in the filtered node's own axes, so it follows the whole linear part of
// the transform (rotation, flips, skew), not just its scale. Upstream keeps it on canvas axes, which puts
// the shadow of a rotated or flipped node in the wrong place.
func offsetCoordinates(x, y float32, ts skia.Transform) (float32, float32) {
	return float32(x*ts.SX) + float32(y*ts.KX), float32(x*ts.KY) + float32(y*ts.SY)
}

func scaleCoordinates(x, y float32, ts skia.Transform) (float32, float32) {
	sx, sy := ts.GetScale()
	return float32(x * sx), float32(y * sy)
}
