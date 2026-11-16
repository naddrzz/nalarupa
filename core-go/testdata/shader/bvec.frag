// bvecN uniforms become ivecN `_nrb` block members read through bvecN(...).
precision mediump float;
uniform bvec2 flags;
uniform lowp bvec3 mask;
/** @label Channels */ uniform bvec4 channels;
uniform bool on;
uniform float level;
void main() {
  vec4 c = vec4(flags.x ? 1.0 : 0.0, flags.y ? level : 0.0, any(mask) ? 1.0 : 0.0, 1.0);
  if (all(channels) && on) { c.rgb = vec3(mask); }
  gl_FragColor = c;
}
