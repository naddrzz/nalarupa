// Edits on the slides template (text: compared live only).
Update("sld01b", {content: "A new title,\nstill short."});
const copy = Copy("sld02", document, {name: "04 Copy", x: 6060});
Insert("sld03", {type: "text", name: "Footnote", content: "More at the end", fill: "#0F1115", fontSize: 24});
Delete("sld02b");
SetVariables({"slide-accent": {type: "color", value: "#3D7BFF"}});
Print(Get("sld01", (n, c) => [n.id, c.bounds]), Get(copy, {depth: 1, resolveVariables: true}).children.map(k => k.fill));
