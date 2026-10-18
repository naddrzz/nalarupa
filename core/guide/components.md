# Components

- A node with `reusable: true` is a component. Build components in their own
  top-level frames (above or left of the screens), then use instances.
- An instance is `{type: "ref", ref: componentId}`. Any root property can be
  overridden on the instance itself (`width`, `fill`, `name`...).
- Child overrides live in `descendants`, keyed by the child's id path inside
  the component: `{[labelId]: {content: "Buy"}}`. Nested instances use
  `innerInstanceId/childId`.
- An override that contains `type` replaces that child completely.
- Hide a child with `{enabled: false}` — instance children cannot be deleted.
- After creation, address instance children as `instanceId/childId`:
  `Update(btn + "/" + labelId, {content: "Save"})`.
- `Copy(componentId, parent, {...})` also creates an instance.
- Edits to the component propagate to every instance unless overridden.
- Frames with `slot: [componentIds]` mark places meant to be filled with
  instances of those components (menus, lists, toolbars).

```js
btn = Insert(document, {type: "frame", name: "Button", reusable: true, padding: [10, 16], gap: 8,
  cornerRadius: 8, fill: "$primary", alignItems: "center", x: 0, y: -200})
btnLabel = Insert(btn, {type: "text", name: "Label", content: "Button", fontFamily: "Inter", fontSize: 14, fontWeight: "600", fill: "#FFFFFF"})
row = Insert(screenId, {type: "frame", name: "Actions", gap: 12})
for (const t of ["Save", "Share"]) Insert(row, {type: "ref", ref: btn, name: t, descendants: {[btnLabel]: {content: t}}})
```
