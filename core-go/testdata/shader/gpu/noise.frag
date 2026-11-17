// Value noise from the common sin-dot hash: the same lattice point must hash the same from every cell.
/** @resolution */ uniform vec2 res;
float h(vec2 p) { return fract(sin(dot(p, vec2(127.1, 311.7))) * 43758.5453); }
float noise(vec2 p) {
  vec2 i = floor(p), f = fract(p);
  vec2 u = f * f * (3.0 - 2.0 * f);
  return mix(mix(h(i), h(i + vec2(1.0, 0.0)), u.x), mix(h(i + vec2(0.0, 1.0)), h(i + vec2(1.0, 1.0)), u.x), u.y);
}
void main() {
  float n = noise(gl_FragCoord.xy / 6.0);
  gl_FragColor = vec4(vec3(n), 1.0);
}
