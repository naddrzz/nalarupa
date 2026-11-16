/** @resolution */ uniform vec2 res;
void main() { gl_FragColor = vec4(gl_FragCoord.xy / res, 0.5, 1.0); }
