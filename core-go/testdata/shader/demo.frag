
precision mediump float;
/** @resolution */
uniform vec2 u_res;
/** @time */
uniform float u_time;
/**
 * Tint used on the left half.
 * @label Tint @color
 * @default #ff0000
 */
uniform vec3 u_tint;
/** @default 0.25 @range 0, 1 */
uniform float u_mix;
/** @default true */
uniform bool u_on;
/** @sdf */
uniform sampler2D u_shape;
vec3 shade(vec3 c) { return c * 0.5; }
vec2 shade(vec2 c) { return c; }
void main() {
  vec2 uv = gl_FragCoord.xy / u_res;
  float d = texture2D(u_shape, uv).r;
  vec2 size = vec2(textureSize(u_shape, 0));
  vec3 c = uv.x < 0.5 ? u_tint : vec3(0.0, uv.y, u_mix);
  if (!u_on) { c = shade(c); }
  gl_FragColor = vec4(c, d > 0.0 && size.x > 0.0 ? 1.0 : 0.0);
}
