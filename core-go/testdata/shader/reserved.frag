precision mediump float;
uniform sampler2D buffer;
uniform vec3 patch;
uniform vec4 c;
struct Light { vec3 color; float input; };
float filter(Light l) { return l.input + c.b + patch.b; }
void main() {
  Light l = Light(patch, 0.5);
  vec4 t = texture2D(buffer, gl_FragCoord.xy / 100.0) + texture2DLodEXT(buffer, vec2(0.5), 0.0);
  gl_FragColor = vec4(l.color * filter(l), 1.0) + t;
}
