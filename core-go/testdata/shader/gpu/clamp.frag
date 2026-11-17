// Colour above alpha: the readback clamps it to a valid premultiplied pixel.
/** @resolution */ uniform vec2 res;
void main() {
  vec2 uv = gl_FragCoord.xy / res;
  gl_FragColor = vec4(1.0, uv.x, 0.2, uv.y);
}
