#version 300 es
#extension GL_OES_standard_derivatives : enable
precision highp float;
precision highp sampler2D;

/* The block comment holds `uniform float hidden;` and a ; that must not count. */
layout(location = 0) out highp vec4 fragColor;
uniform sampler2D input;
uniform float output, active;
/** @resolution */ uniform vec2 iResolution;

#define TAU 6.28318530718
#if 1
float common(float smooth) { return smooth * 0.5; }
#endif

void main() {
  vec2 uv = gl_FragCoord.xy / iResolution.xy;
  float sample = common(uv.x) + output * active; // reserved words as names
  fragColor = texture(input, uv) * sample + vec4(dFdx(uv.x));
  fragColor.a = 1.0;
}
