# Nalarupa design guide

Nalarupa is an infinite-canvas design editor. A document is a tree of nodes
(frames, text, shapes, icons, component instances) stored as JSON in the
`.pen` format (also saved as `.nala`). You edit it by sending JavaScript to the
`execute` tool. Every change shows up on the user's canvas immediately.

## Topics (`read_guide` with `topic`)

| topic | read it when |
| --- | --- |
| `designing` | before any new design — the process and the design direction (the built-in agent has it already) |
| `schema` | always, before the first `execute` — node types and properties |
| `execute` | always, before the first `execute` — the JavaScript API |
| `components` | the design repeats UI (buttons, cards, rows, nav items) |
| `slides` | the user wants a presentation / deck |
| `design` | you need taste guidance: type scale, spacing, color, layout |
| `code` | turning a design into HTML/CSS or a framework component |

`get_style` (no arguments) lists ready-made visual styles you can adopt when
the user gives no brand direction.

## Working rules

1. Know the document first: `get_editor_state`; the main built-in agent gets it
   in its prompt. It lists the selection, top-level frames, components and
   variables. Reuse what exists before creating anything.
2. Do exactly what was asked. Technical requests (rename, move, delete, recolor)
   need no redesign.
3. The user may edit while you work. If a node you remember is gone or changed,
   read it again with `Get` instead of recreating it, and never revert their edits.
4. Build in visible steps: one `execute` per section (header, hero, list...).
   Users want to see progress quickly.
5. After each section, check it: use `Get` with a visitor to look for
   `ctx.problems` (clipped content), then run the critique pass (`designing`,
   step 6) on its screenshot.
6. Fix problems in place with `Update`. Never delete a finished screen to
   start over.

## Canvas rules

- Coordinates are relative to the parent's top-left; x grows right, y grows down.
- Only `frame` and `group` hold children.
- The document root holds only screens, slides, component frames and other
  large containers. Never put loose text, icons or buttons at the root.
- New screens are top-level frames with `clip: true`. Place them with
  `FindEmptySpace` so they never overlap existing work. Put components above
  or to the left of screens.
- While you work on a top-level frame set `placeholder: true`; set it back to
  `false` the moment that frame is finished.
- Properties never inherit. A text node is invisible until it has a `fill`.
- There is no scrolling: everything must fit inside its frame. Grow the frame
  (or use `fit_content`) when you add content.
- Not CSS: no margins, no percentages, no `calc`, no wrapping flex rows.
  Grids are rows of frames.

## Assets

- Any Google Font works (`fontFamily`). Icons come from lucide (default),
  feather, Material Symbols (Outlined/Rounded/Sharp) and phosphor.
- Don't hand-draw logos or illustrations from paths. Use icons, shapes,
  gradient fills and image fills.
