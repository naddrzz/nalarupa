// Every directive, the block's std140 packing and the renames.
precision highp float;
precision mediump int;

/** @color @default 1, 0.5, 0, 0.25 */
uniform vec4 tint;
/** @color @default #00ff00 */
uniform lowp vec3 edge;
/**
 * Speed of the wave.
 * @label(Wave speed) @min 0 @max(10)
 * @default 2.5
 */
uniform float speed;
/** @range(0, 1) @default 0.5 */ uniform float filter;
/** @default 3.7 */ uniform int steps;
/** @default -2.5 */ uniform int down;
/** @default 1, 2, 3 */ uniform vec3 offset;
/** @default 4, 5 */ uniform ivec2 grid;
/** @default 1,2,3,4 */ uniform ivec4 quad;
uniform ivec3 cell;
/** @default false */ uniform bool flip;
uniform bool sample;
/** @default true */ uniform bool on;
/** @resolution */ uniform ivec2 res;
/** @mouse */ uniform ivec2 mouse;
/** @mouse */ uniform vec2 mousef;
/** @time */ uniform float time;
uniform mat3 rot;
uniform mat4 proj;
uniform float a, b;
uniform vec2 pair;
/** @sdf */ uniform sampler2D shape;
/** @backdrop */ uniform sampler2D behind;
uniform sampler2D photo;

float wave(float x) { return sin(x * speed + time) * 0.5 + 0.5; }

void main() {
  vec2 uv = gl_FragCoord.xy / vec2(res);
  vec4 c = texture2D(photo, uv) * tint + texture2D(behind, uv) * float(steps + down + grid.x + quad.w + cell.y);
  c.rgb += edge * wave(uv.x) * filter + offset * a * b + vec3(pair, 0.0);
  c.rgb = rot * c.rgb + (proj * c).xyz;
  if (flip || sample || !on) { c.a = texture2D(shape, uv).r; }
  c.xy += vec2(mouse) + mousef;
  gl_FragColor = c;
}
