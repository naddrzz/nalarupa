// Edits on the mobile template: its iPhone frame is a vertical auto layout.
const bar = Insert("mob01", {type: "frame", name: "Nav bar", width: "fill_container", height: 56, layout: "horizontal", padding: [0, 16], alignItems: "center", fill: "#F7F7F7"});
Insert(bar, {type: "ellipse", name: "Avatar", width: 32, height: 32, fill: "#C4C4C4"});
Insert("mob01", {type: "rectangle", name: "Hero", width: "fill_container", height: 240, fill: "#3366FF", x: 10, y: 10});
Insert("mob01", {type: "rectangle", name: "Floating", width: 56, height: 56, fill: "#FF6A3D", layoutPosition: "absolute", x: 321, y: 780, cornerRadius: 28});
Update("mob01", {gap: 8, padding: [0, 0, 24, 0]});
const second = Copy("mob01", document, {name: "iPhone 16 (2)", x: 450});
Replace(Get(second, {depth: 1}).children[1].id, {type: "rectangle", name: "Hero 2", width: "fill_container", height: 200, fill: "#00AA55"});
Move("mob01", document, 1);
Print(Get("mob01", (n, c) => [n.name, c.depth, c.index, c.bounds, c.problems || null]));
Print(FindEmptySpace({width: 393, height: 852, padding: 40}));
