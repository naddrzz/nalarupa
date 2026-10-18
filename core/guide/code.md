# From design to code

- Quick path: `Export([screenId], "html-css", "./out/landing.html")` writes a
  standalone HTML file. Frames become flexbox `div`s (`layout` → `flex-direction`,
  `gap`, `padding`, `justifyContent`/`alignItems`), `fill_container` becomes
  `flex: 1` on the main axis or `align-self: stretch` on the cross axis,
  text becomes `<p>`, icons are inlined SVG, Google Fonts are linked.
- For a real codebase, read the design and write components by hand in the
  project's framework and styling system:
  1. `Print(GetVariables())` → design tokens. Put them in the project's token
     system (CSS custom properties, Tailwind theme, etc.).
  2. List components: `Get(n => n.reusable ? [n.id, n.name] : undefined)`.
  3. Read one component at a time: `Print(Get(id, {depth: 6, includePathGeometry: true}))`.
  4. Instances (`ref`) map to component usages; `descendants` overrides map
     to props. A child that some instance hides (`enabled: false`) becomes an
     optional prop.
  5. Screens last, composed from the components.
- Keep exact values: sizes, radii, colors, font weights and line heights.
- Mapping cheat sheet: `fit_content` → `width: fit-content` / natural size;
  `fill_container` → `flex: 1` or `width: 100%`; `layoutPosition: absolute` →
  `position: absolute` inside a `position: relative` parent; `clip` →
  `overflow: hidden`; strokes with `strokeAlignment: inner` → inset box-shadow
  or `border` with `box-sizing: border-box`.
