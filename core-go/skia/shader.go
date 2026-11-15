package skia

// Shaders (shaders/mod.rs). Rust's Shader enum is an interface here, implemented by *SolidColor,
// *LinearGradient, *RadialGradient, *SweepGradient and *Pattern (shader_*.go).

// SpreadMode says how a shader fills the area outside its bounds.
type SpreadMode uint8

const (
	SpreadModePad     SpreadMode = iota // repeat the edge color, the default
	SpreadModeReflect                   // repeat the shader mirrored
	SpreadModeRepeat                    // repeat the shader
)

// Shader is a paint source.
type Shader interface {
	// IsOpaque reports whether the shader is known to be opaque everywhere.
	IsOpaque() bool
	// pushStages pushes the shader's stages; false means it cannot be drawn.
	pushStages(cs ColorSpace, p *rasterPipelineBuilder) bool
	// postTransform post-concatenates ts onto the shader's transform (Shader::transform).
	postTransform(ts Transform)
	// applyOpacity multiplies the shader's opacity, clamped to 0..=1.
	applyOpacity(opacity float32)
	// clone returns a deep copy, so a painter can transform it without changing the caller's.
	clone() Shader
}

// SolidColor is a single color shader (Shader::SolidColor).
type SolidColor struct{ Color Color }

// NewSolidColor creates a solid color shader.
func NewSolidColor(c Color) *SolidColor { return &SolidColor{c} }

func (s *SolidColor) IsOpaque() bool { return s.Color.IsOpaque() }

func (s *SolidColor) pushStages(cs ColorSpace, p *rasterPipelineBuilder) bool {
	p.pushUniformColor(cs.expandColor(s.Color).Premultiply())
	return true
}

func (s *SolidColor) postTransform(Transform)      {}
func (s *SolidColor) applyOpacity(opacity float32) { s.Color.ApplyOpacity(opacity) }
func (s *SolidColor) clone() Shader                { c := *s; return &c }
