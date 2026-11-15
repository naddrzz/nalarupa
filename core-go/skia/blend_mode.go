package skia

// BlendMode is a blending mode (blend_mode.rs). The Go zero value is BlendModeClear; Rust's default,
// SourceOver, is what DefaultPaint and DefaultPixmapPaint set.
type BlendMode uint8

const (
	BlendModeClear BlendMode = iota
	BlendModeSource
	BlendModeDestination
	BlendModeSourceOver
	BlendModeDestinationOver
	BlendModeSourceIn
	BlendModeDestinationIn
	BlendModeSourceOut
	BlendModeDestinationOut
	BlendModeSourceAtop
	BlendModeDestinationAtop
	BlendModeXor
	BlendModePlus
	BlendModeModulate
	BlendModeScreen
	BlendModeOverlay
	BlendModeDarken
	BlendModeLighten
	BlendModeColorDodge
	BlendModeColorBurn
	BlendModeHardLight
	BlendModeSoftLight
	BlendModeDifference
	BlendModeExclusion
	BlendModeMultiply
	BlendModeHue
	BlendModeSaturation
	BlendModeColor
	BlendModeLuminosity
)

// shouldPreScaleCoverage: never pre-scale with coverage if the mode involves a source-alpha term, and
// always pre-scale Plus (a finer-grained SkBlendMode_SupportsCoverageAsAlpha).
func (m BlendMode) shouldPreScaleCoverage() bool {
	switch m {
	case BlendModeDestination, BlendModeDestinationOver, BlendModePlus, BlendModeDestinationOut,
		BlendModeSourceAtop, BlendModeSourceOver, BlendModeXor:
		return true
	}
	return false
}

// toStage returns the blending stage; Source has none (it is a no-op).
func (m BlendMode) toStage() (stage, bool) {
	switch m {
	case BlendModeClear:
		return stageClear, true
	case BlendModeSource:
		return 0, false
	case BlendModeDestination:
		return stageMoveDestinationToSource, true
	case BlendModeSourceOver:
		return stageSourceOver, true
	case BlendModeDestinationOver:
		return stageDestinationOver, true
	case BlendModeSourceIn:
		return stageSourceIn, true
	case BlendModeDestinationIn:
		return stageDestinationIn, true
	case BlendModeSourceOut:
		return stageSourceOut, true
	case BlendModeDestinationOut:
		return stageDestinationOut, true
	case BlendModeSourceAtop:
		return stageSourceAtop, true
	case BlendModeDestinationAtop:
		return stageDestinationAtop, true
	case BlendModeXor:
		return stageXor, true
	case BlendModePlus:
		return stagePlus, true
	case BlendModeModulate:
		return stageModulate, true
	case BlendModeScreen:
		return stageScreen, true
	case BlendModeOverlay:
		return stageOverlay, true
	case BlendModeDarken:
		return stageDarken, true
	case BlendModeLighten:
		return stageLighten, true
	case BlendModeColorDodge:
		return stageColorDodge, true
	case BlendModeColorBurn:
		return stageColorBurn, true
	case BlendModeHardLight:
		return stageHardLight, true
	case BlendModeSoftLight:
		return stageSoftLight, true
	case BlendModeDifference:
		return stageDifference, true
	case BlendModeExclusion:
		return stageExclusion, true
	case BlendModeMultiply:
		return stageMultiply, true
	case BlendModeHue:
		return stageHue, true
	case BlendModeSaturation:
		return stageSaturation, true
	case BlendModeColor:
		return stageColor, true
	case BlendModeLuminosity:
		return stageLuminosity, true
	}
	return 0, false
}
