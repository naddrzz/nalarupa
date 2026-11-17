// A plain sampler2D: the image at the uniform's url, repeated and filtered.
/** @resolution */ uniform vec2 res;
uniform sampler2D img;
uniform sampler2D missing;
void main() {
  vec2 uv = gl_FragCoord.xy / res * 2.5;
  gl_FragColor = texture2D(img, uv) + texture2D(missing, uv);
}
