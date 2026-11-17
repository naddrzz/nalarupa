// The @sdf texture as colour: distance in red, its gradient in green and blue.
/** @resolution */ uniform vec2 res;
/** @sdf */ uniform sampler2D shape;
void main() {
  vec4 s = texture2D(shape, gl_FragCoord.xy / res);
  gl_FragColor = vec4(clamp(s.r / 40.0 + 0.5, 0.0, 1.0), clamp(0.5 + s.g * 2.0, 0.0, 1.0), clamp(0.5 + s.b * 2.0, 0.0, 1.0), 1.0);
}
