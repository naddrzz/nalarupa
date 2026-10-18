# Document schema (.pen compatible)

```ts
type Variable = string            // "$name" binds a property to a variable
type Num = number | Variable
type Color = string | Variable    // only "#RGB" | "#RRGGBB" | "#RRGGBBAA"; anything else (names, "#RGBA") is transparent
type Size = Num | "fit_content" | "fill_container" | "fit_content(120)" | "fill_container(80)"
// fit_content: hug the in-flow children that are not fill_container. Only frames with a layout
//   hug; otherwise (layout "none", text, shapes, icons, or nothing to hug) it is the value in (), default 0
// fill_container: share of the parent's free space, max(1, (inner - other children - gaps) / fill count);
//   the value in () is used when the parent has no layout. Fill children never size a fit_content parent

interface Document {
  version: "2.19"
  variables?: { [name: string]: { type: "color" | "number" | "string" | "boolean",
                                  value: any | { value: any, theme?: { [axis: string]: string } }[] } }
  themes?: { [axis: string]: string[] }     // first value of each axis is the default
  imports?: { [alias: string]: string }     // other .pen files; their nodes are "alias:id"
  children: Node[]
}

interface Base {
  id: string                 // generated for you; never set it when creating
  type: string
  name?: string              // always set a readable name
  x?: number; y?: number     // ignored inside a flex layout parent
  rotation?: Num             // degrees counter-clockwise around the top-left corner
  opacity?: Num
  enabled?: boolean          // false hides the node (use it to hide instance children)
  flipX?: boolean; flipY?: boolean   // mirror about the top-left corner (x=10, width 100, flipX covers -90..10)
  // in a flex layout, rotated/flipped nodes are placed by their bounding box
  layoutPosition?: "auto" | "absolute"   // absolute = ignore the parent's layout, use x/y
  reusable?: boolean         // makes the node a component
  theme?: { [axis: string]: string }     // switch theme for this subtree
  metadata?: object
}

type Fill =
  | Color
  | { type: "color", color: Color, enabled?: boolean, blendMode?: string }
  | { type: "gradient", gradientType?: "linear" | "radial" | "angular",
      rotation?: Num,        // degrees, 0 = pointing up, counter-clockwise
      center?: { x: number, y: number },   // 0..1 of the box, default 0.5/0.5
      size?: { width?: number, height?: number },  // 0..1; linear uses height as length
      colors: { color: Color, position: Num }[], opacity?: Num }
  | { type: "image", url: string, mode?: "stretch" | "fill" | "fit", opacity?: Num }
                                         // default stretch; url is relative to the document file
  | { type: "mesh_gradient", columns: number, rows: number, colors: Color[], opacity?: Num,
      points: ([x, y] | { position: [x, y], leftHandle?: [x, y], rightHandle?, topHandle?, bottomHandle? })[] }
                                         // bicubic patch mesh: rows*columns points row-major, one color each, in 0..1 of the box; handles offset from the point
  | { type: "shader", url: string,       // WebGL 1.0 fragment shader file (.glsl), relative to the document
      uniforms?: { [name: string]: number | boolean | Color | number[] | string }, opacity?: Num }
                                         // values for the shader's uniforms (see "Scripts and shaders"); a broken shader shows red stripes
// every fill may set blendMode ("multiply", "screen", "overlay", ...); gradients and images span the node box
interface Graphics {
  fill?: Fill | Fill[]                   // painted bottom to top
  stroke?: Fill | Fill[]
  strokeWidth?: Num | { top?: Num, right?: Num, bottom?: Num, left?: Num }   // required: no width, no stroke
  strokeAlignment?: "inner" | "center" | "outer"   // default center; open paths and lines are always center
  strokeLinecap?: "butt" | "round" | "square"; strokeLinejoin?: "miter" | "bevel" | "round"
  effect?: Effect | Effect[]
}
type Effect =
  | { type: "shadow", shadowType?: "outer" | "inner", offset?: { x: Num, y: Num }, blur?: Num, color?: Color }
      // defaults: offset 0, blur 0, color "#000000" (opaque). Cast by the node's own outline (not its
      // children), hidden under the node; shapes without a visible fill cast none
  | { type: "blur", radius?: Num }       // default 0; blurs the node, its children and its shadows
  | { type: "background_blur", radius?: Num }
      // blurs everything painted beneath the node (other top-level frames too), seen through its
      // outline; inside a translucent or blurred ancestor, only that ancestor's content.
      // A node with opacity < 1 shows none. Exports only blur what the same top-level frame painted

interface Layout {             // frames only
  layout?: "horizontal" | "vertical" | "none"   // default horizontal; "none" + fit_content = fallback size (0)
  gap?: Num
  padding?: Num | [Num, Num] | [Num, Num, Num, Num]   // all | [vertical, horizontal] | [t, r, b, l]
  justifyContent?: "start" | "center" | "end" | "space_between" | "space_around"   // space_* ignore gap
  alignItems?: "start" | "center" | "end"
}

interface Frame extends Base, Graphics, Layout {
  type: "frame"; width?: Size; height?: Size     // default fit_content
  cornerRadius?: Num | [Num, Num, Num, Num]      // tl, tr, br, bl
  clip?: boolean             // clips children; the frame's stroke is then drawn above them
  placeholder?: boolean
  slot?: string[]            // ids of components recommended for this slot
  children?: Node[]
}
interface Group extends Base { type: "group"; children?: Node[]; effect?: Effect | Effect[] }
                             // its box is the union of its children (may start away from x/y)
interface Rectangle extends Base, Graphics { type: "rectangle"; width: Size; height: Size; cornerRadius?: Num | Num[] }
                             // radii that overflow a side shrink together (CSS rule)
interface Ellipse extends Base, Graphics { type: "ellipse"; width: Size; height: Size
  innerRadius?: Num          // 0 solid … 1 hollow ring (donuts!)
  startAngle?: Num; sweepAngle?: Num }    // degrees, counter-clockwise from 3 o'clock
interface Polygon extends Base, Graphics { type: "polygon"; width: Size; height: Size
  polygonCount: Num          // required (>= 3), first vertex top-center
  cornerRadius?: Num }       // rounds every corner
interface Line extends Base, Graphics { type: "line"; width: Size; height: Size }   // stroke from top-left to bottom-right
interface Path extends Base, Graphics { type: "path"; width: Size; height: Size
  geometry: string           // SVG path data
  viewBox?: [x, y, w, h]     // stretched onto the node box; default = the geometry's tight bounds
                             // width/height are required (default 0)
  fillRule?: "nonzero" | "evenodd" }
interface Text extends Base, Graphics {
  type: "text"; content: string
  textGrowth?: "auto" | "fixed-width" | "fixed-width-height"
  //   auto: one line per \n, size from content (width/height and fill_container ignored, never wraps)
  //   fixed-width: set width (number or fill_container), wraps, height grows; no width = 0 = one word per line
  //   fixed-width-height: both set, wraps, may overflow
  width?: Size; height?: Size
  fontFamily?: string; fontSize?: Num
  fontWeight?: string        // "normal", "bold" or "100".."900"; other names ("semibold") mean 400
  fontStyle?: "normal" | "italic"; letterSpacing?: Num   // px, added after every character
  lineHeight?: Num           // multiple of fontSize, e.g. 1.4 (rounded to whole px)
  textAlign?: "left" | "center" | "right" | "justify"
  textAlignVertical?: "top" | "middle" | "bottom"   // needs a fixed textGrowth
  underline?: boolean; strikethrough?: boolean     // Nalarupa extension
  // missing characters fall back to Noto fonts (emoji are monochrome, in the text color)
}
interface Icon extends Base { type: "icon"; width: Size; height: Size   // required (default 0)
  fill: Fill                 // required: an icon without fill is invisible
  weight?: Num               // 100..700, Material Symbols only, default 200
  library?: "lucide" | "feather" | "Material Symbols Outlined" | "Material Symbols Rounded" | "Material Symbols Sharp" | "phosphor"
  icon: string }             // e.g. "arrow-right" (lucide), "home" (material)
interface Note extends Base { type: "note" | "prompt" | "context"; content?: string; width?: Size; height?: Size }
interface Script extends Base { type: "script"; width: Size; height: Size
  scriptUri?: string                     // .js file relative to the document; its output becomes the children
  inputs?: { [name: string]: string | number | boolean }   // values for the script's @input declarations
  clip?: boolean }                       // default false; the children are generated, never stored or edited
interface Ref extends Base {             // component instance
  type: "ref"; ref: string               // id of the reusable node
  descendants?: { [idPath: string]: object }   // overrides; with `type` = full replacement
  [prop: string]: any }                  // any root property can be overridden directly
```

## Text sizing

- Labels, buttons, tags: leave `textGrowth` unset (auto). Never guess widths.
- Paragraphs and titles that should wrap: `textGrowth: "fixed-width"` with
  `width: "fill_container"` inside a frame that has a layout and a known width.
- A `fit_content` parent whose children are all `fill_container` has no size
  to give them: give the parent a number, or one child a real size.

## Layout recipes

- Center children: `justifyContent: "center", alignItems: "center"` on the parent.
- Push items apart: `justifyContent: "space_between"`.
- Equal columns: children with `width: "fill_container"` and a `gap` on the row.
- Offset one child: wrap it in a frame with padding.
- Overlays (badges, decorations): `layoutPosition: "absolute"` plus x/y.
- Charts: build bars as frames sized by data inside a horizontal frame with
  `alignItems: "end"`; donut charts are ellipses with `innerRadius` and
  `startAngle`/`sweepAngle`.

## Scripts and shaders (.pen-compatible files next to the document)

- Script file: starts with `/** @schema 2.19 */` (required; older versions are migrated), declares
  `@input name: type [= default]` lines (`number(min=, max=)`, `string`, `boolean`, `color`, `ref`,
  `enum("a", "b")`), reads `nalarupa.width`, `nalarupa.height`, `nalarupa.input.<name>` and must
  `return [...nodes]` (schema nodes in the script node's local space, no ids, at most 1000, no
  components). `Math.random()` is seeded the same on every run. Limits: 2 s, 16 MB. It re-runs when
  its file, inputs or size change; errors are drawn in the node's box.
- Shader file: WebGL 1.0 fragment shader writing `gl_FragColor` (or `#version 300 es` with one
  `out vec4`); `gl_FragCoord` is in node units with the origin bottom-left. Annotate uniforms in a
  `/** … */` comment right above them: `@resolution` (vec2 = node size), `@time` (float, 0 in a
  still image), `@mouse` (vec2, node centre), `@sdf` (sampler2D: r = signed distance to the shape
  in node units, positive inside; gb = its gradient), `@backdrop` (sampler2D: what is painted
  behind the node in the same frame), `@color` (vec3/vec4 from "#RRGGBB"), `@default v`,
  `@range a, b`, `@label text`. Other `sampler2D` uniforms take an image url in `uniforms`.
  Avoid huge arguments to `sin` in hashes when exact noise matters: GPUs differ there.
