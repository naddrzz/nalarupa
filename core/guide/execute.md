# The `execute` tool

`execute({input})` runs a JavaScript snippet (ES2022) against the open
document. All operations in one call form a single transaction and a single
undo step: if anything throws, the whole call is rolled back.

## Failures

The error response contains an `editId`. Fix the snippet with
`execute({editId, edits: [{find, replace, all?}]})` — never resend the whole
snippet. `find` must match the stored snippet exactly once (or set `all: true`).
If the patched snippet fails again, keep patching with the same `editId`.

## Limits

A call may run for 30 seconds and use 64 MB of JavaScript memory; past
either limit it stops with an error and is rolled back. Deep recursion fails
with "Maximum call stack size exceeded".

## Persistence between calls

Each call has its own scope. `const`/`let` vanish after the call. Assign
without a keyword (`heroId = Insert(...)`) to keep a JSON-serializable value
for later calls. The response lists which globals carried over.

## Live streaming

The canvas runs your snippet while you are still writing it: every complete
top-level statement executes as soon as it is streamed, and the user watches
the design build up (your cursor flies to each node you touch). The final call
commits everything as one undo step, or rolls everything back on error.

- Write top-down: create the container, then its children, then details, so
  the build-up reads naturally.
- Keep one statement per line (or end statements with `;`).
- Mark top-level frames `placeholder: true` while you build them (they glow
  "Generating") and set it back to `false` in the call that finishes them.

## API

```ts
const document: "document"                      // the root
Insert(parent: string, node: Node): string      // returns the new id; node may include children
Copy(path: string, parent: string, data?: Node & { descendants?: {...} }): string
                                                // reusable source → creates a ref instance
Update(path: string, data: Partial<Node>): void // merge; null removes a property
Replace(path: string, node: Node): string       // swap a node (or an instance child) entirely
Move(path: string, parent?: string, index?: number): void
Delete(path: string): void                      // not for instance children: Update(..., {enabled:false})
SetVariables(vars: {[name]: {type, value}}, replace?: boolean): void
GetVariables(): { variables, themes }
Get(path, opts?): Node                          // one node with nested children
Get(path?, visit, opts?): T[]                   // visit a subtree (or the whole document)
FindEmptySpace({width, height, direction?: "right"|"bottom"|"left"|"top", padding?, nodeId?}): {x, y}
Print(...values): void                          // lines appear in the response
TakeScreenshot(ids: string[] | "document"): void   // PNGs attached to the response
Export(ids, "png" | "jpeg" | "webp" | "svg" | "html-css", outputPath, {scale?, quality?, includeLayerNames?, includeLayerIds?, includeHtmlScaffold?})
Export(ids | [], "pdf", outputPath)             // one page per id; [] = every top-level frame (the deck)
                                                // outputPath stays in the document's folder (relative = inside it); a .pdf / .html name; never the document file itself
Generate("stock" | "ai", nodeId, prompt)        // puts a placeholder photo fill on the node
Generate("svg", frameId, prompt)                // the AI Pen draws vector artwork into the frame
```

`Generate("svg")` is for artwork that is the point of the design (a logo mark, mascot,
illustration, custom diagram); icons, photos and decorative shapes have cheaper options.
Insert a frame with the target width/height and `placeholder: true`, then pass its id;
the artwork is fitted into it as path children. The generator sees nothing of the
document, so the prompt states subject, composition, style and colors. It is drawn in the
background after the call returns (a minute or more): keep designing, never add children
to that frame or generate it again, and check `Get(frameId, {depth: 0}).placeholder`
(false when done) in a later call before screenshotting it.

`path` is a node id, or `instanceId/childId` (any depth) for a child inside a
component instance. Always pass ids (strings), never node objects.

`GetOptions`: `depth` (0 = only the node; deeper children become `"..."`),
`resolveVariables` (values instead of `$refs`), `resolveInstances` (expand
component instances; their children get `instance/child` ids),
`includePathGeometry`.

Visitor: `(node, ctx) => value | undefined`. `ctx` has `node`, `parentCtx`,
`depth`, `index`, `bounds` `{x, y, width, height}` in the parent's coordinate
space, `problems` (`"partially clipped"` / `"fully clipped"`) and
`skipChildren()`. Nodes passed to visitors carry no `children` array.
Returning `undefined` collects nothing.

## Rules

- Set a `name` on every node you create. The response maps names to ids.
- Never set `id`; ids are generated.
- Write compact code: loops, helper functions, spreads, template strings.
  No comments.
- Style objects you spread must include `type`.
- Read back with `Get` instead of guessing sizes; layout is computed for you.
- End a call that completes a section with `TakeScreenshot([sectionId])`.
  Screenshots are expensive: one per finished section.

## Examples

```js
pos = FindEmptySpace({width: 1440, height: 900, padding: 120})
page = Insert(document, {type: "frame", name: "Landing", x: pos.x, y: pos.y, width: 1440, layout: "vertical",
  fill: "#FAFAF7", clip: true, placeholder: true})
nav = Insert(page, {type: "frame", name: "Nav", width: "fill_container", padding: [24, 64], justifyContent: "space_between", alignItems: "center"})
Insert(nav, {type: "text", name: "Logo", content: "Acme", fontFamily: "Inter", fontSize: 20, fontWeight: "700", fill: "#111111"})
links = Insert(nav, {type: "frame", name: "Links", gap: 32})
for (const l of ["Product", "Pricing", "Docs"]) Insert(links, {type: "text", name: l, content: l, fontFamily: "Inter", fontSize: 15, fill: "#444444"})
TakeScreenshot([nav])
```

```js
Get(page, (n, c) => c.problems && Print(n.name, c.problems))
Get(n => n.type === "text" && n.fontSize < 12 && Update(n.id, {fontSize: 12}))
Print(Get(page, {depth: 1}).children.map(c => c.name))
```
