// Frosted glass: the backdrop tinted by the outline's distance.
precision mediump float;
/** @resolution */ uniform vec2 res;
/** @backdrop */ uniform sampler2D behind;
/** @sdf */ uniform sampler2D shape;
void main() {
  vec2 uv = gl_FragCoord.xy / res;
  float d = clamp(texture2D(shape, uv).r / 8.0, 0.0, 1.0);
  gl_FragColor = vec4(texture2D(behind, uv).rgb * (0.6 + 0.4 * d), 1.0);
}
