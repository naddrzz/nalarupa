// The issues of shapes: near-miss alignment, overflow, x/y in auto layout, fill_container without it, a collapsed
// frame, missing icons, a name with a line break.
const f = Insert(document, {type: "frame", name: "Free", x: 0, y: 700, width: 400, height: 300, layout: "none", fill: "#FFFFFF"});
Insert(f, {type: "rectangle", name: "A", x: 40, y: 20, width: 100, height: 40, fill: "#CCCCCC"});
Insert(f, {type: "rectangle", name: "Slipped", x: 41.5, y: 80, width: 100, height: 40, fill: "#CCCCCC"});
Insert(f, {type: "rectangle", name: "Three off", x: 43, y: 140, width: 100, height: 40, fill: "#CCCCCC"});
Insert(f, {type: "rectangle", name: "Overflow", x: 350, y: 250, width: 100, height: 100, fill: "#CCCCCC"});
const col = Insert(document, {type: "frame", name: "Col", x: 500, y: 700, layout: "vertical", gap: 4, width: 200, height: 200});
Insert(col, {type: "rectangle", name: "Placed", x: 10, y: 10, width: 50, height: 20});
Insert(col, {type: "rectangle", name: "Pinned", x: 10, y: 10, width: 50, height: 20, layoutPosition: "absolute"});
Insert(f, {type: "rectangle", name: "Stretch", x: 200, y: 20, width: "fill_container", height: 20});
const shrink = Insert(document, {type: "frame", name: "Shrink", x: 800, y: 700, layout: "vertical", width: "fit_content", height: "fit_content"});
Insert(shrink, {type: "rectangle", name: "Fills", width: "fill_container", height: "fill_container"});
Insert(f, {type: "icon", name: "Glyph", x: 300, y: 20, width: 24, height: 24, icon: "no-such-icon"});
Insert(f, {type: "icon", name: "Weighted", x: 330, y: 20, width: 24, height: 24, library: "material-symbols", icon: "nope", weight: 500});
Insert(f, {type: "rectangle", name: "Two\nlines", x: 100, y: 200, width: 10, height: 10, opacity: 0.5});
Insert(f, {type: "rectangle", name: "Two\nlines", x: 101, y: 230, width: 10, height: 10, rotation: 0});
Update("ok", {x: 702});
