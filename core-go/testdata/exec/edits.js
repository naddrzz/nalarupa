// Every prelude call that edits: inserts with names, copies (a node, a component, an instance descendant, with
// overrides), updates (an instance descendant too), replace, move, delete and variables.
const card = Insert("board", {type: "frame", name: "Card", x: 40, y: 260, width: 160, height: 100, layout: "vertical", gap: 8, padding: 12, fill: "#FAFAFA", children: [
  {type: "rectangle", name: "Media", width: "fill_container", height: 40, fill: "#DDDDDD"},
  {type: "frame", name: "Row", layout: "horizontal", gap: 4, children: [{type: "ellipse", name: "Pip", width: 8, height: 8, fill: "#00AA55"}]}
]});
const copy = Copy(card, "board", {name: "Card copy", x: 240, y: 260, descendants: {Media: {fill: "#FFCC00"}}});
inst = Copy("button", document, {name: "Cancel button", x: 860, y: 100});
Copy("ok/btnicon", "board", {x: 500, y: 300});
Update("ok/btnicon", {fill: "#FFEE00"});
Update("hero", {cornerRadius: 16, opacity: 0.9, shadow: null});
const swapped = Replace("dot", {type: "rectangle", name: "Square", x: 260, y: 40, width: 40, height: 40, fill: "#8800FF"});
Replace("ok/btnbg", {type: "ellipse", width: 20, height: 20, fill: "#000000"});
Move("s2", "stack", 0);
Move("wave", document);
Move("hero", null, 99);
Delete("s1");
SetVariables({accent: {type: "color", value: "#FF0066"}, gap: {type: "number", value: [{value: 12}, {value: 20, theme: {mode: "dark"}}]}});
Update(swapped, {fill: "$accent"});
const vars = GetVariables();
Print(Object.keys(vars.variables), vars.themes);
Print(FindEmptySpace({width: 300, height: 200}), FindEmptySpace({width: 50, height: 50, direction: "bottom", padding: 20, nodeId: "board"}), FindEmptySpace({direction: "left", nodeId: "ok"}), FindEmptySpace({direction: "top", nodeId: "nope"}));
Print(Get(copy, {depth: 1}).children.length, Get(inst).ref, typeof copy);
Insert(document, {type: "frame", name: "Loose", x: 0, y: 500, width: 50, height: 50, x2: 1e21, y2: 0.1 + 0.2, big: 2 ** 64});
