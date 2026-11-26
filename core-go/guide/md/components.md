# Components

- A node with `reusable: true` is a component. Build components in their own
  top-level frames placed cleanly above the screens with `FindEmptySpace({width: w, height: h, direction: "top", padding: 80})`
  so multiple components never stack on top of each other or overlap screen headers.
- An instance is `{type: "ref", ref: componentId}`. Any root property can be
  overridden on the instance itself (`width`, `fill`, `name`...).
- Child overrides live in `descendants`, keyed by the child's id path inside
  the component: `{[labelId]: {content: "Buy"}}`. Nested instances use
  `innerInstanceId/childId`.
- An override that contains `type` replaces that child completely.
- Hide a child with `{enabled: false}` — instance children cannot be deleted.
- After creation, address instance children as `instanceId/childId`:
  `Update(instanceId + "/" + labelId, {content: "Save"})`, with the id
  Insert or Copy returned for the instance (not the component's).
- `Copy(componentId, parent, {...})` also creates an instance.
- Edits to the component propagate to every instance unless overridden.
- Frames with `slot: [componentIds]` mark places meant to be filled with
  instances of those components (menus, lists, toolbars).
- For multi-page or multi-screen designs, build shared navigation bars,
  footers, cards, and recurring surface/divider motifs as `reusable: true`
  components first, then place `{type: "ref", ref: id, width: "fill_container"}`
  instances on every screen so sibling pages never drift apart.

```js
spot = FindEmptySpace({width: 200, height: 64, direction: "top", padding: 80})
btn = Insert(document, {type: "frame", name: "Button", reusable: true, padding: [10, 16], gap: 8,
  cornerRadius: 8, fill: "#2563EB", alignItems: "center", x: spot.x, y: spot.y})
btnLabel = Insert(btn, {type: "text", name: "Label", content: "Button", fontFamily: "Manrope", fontSize: 14, fontWeight: "600", fill: "#FFFFFF"})
// screenId: the screen frame the buttons go on.
row = Insert(screenId, {type: "frame", name: "Actions", gap: 12})
for (const t of ["Save", "Share"]) Insert(row, {type: "ref", ref: btn, name: t, descendants: {[btnLabel]: {content: t}}})
```
