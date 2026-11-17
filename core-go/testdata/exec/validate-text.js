// The issues of text (laid out with the installed fonts, so compared live only): contrast against the page and
// against a sibling, small body text, no fill, an unknown font, overflow.
const page = Insert(document, {type: "frame", name: "Page", x: 0, y: 700, layout: "vertical", width: 600, height: 500, fill: "#FFFFFF", gap: 4});
const text = (name, fill, size, weight) => Insert(page, {type: "text", name, content: "Quarterly revenue", fill, fontSize: size, fontWeight: weight, fontFamily: "Arial"});
text("Faint", "#999999", 16, "400");
text("Body", "#767676", 16, "400");
text("Large", "#949494", 32, "400");
text("Bold", "#949494", 19, "700");
Insert(page, {type: "text", name: "Fine print", content: "Pre-orders close on Thursday at six; collect them on Saturday morning.", fill: "#111111", fontSize: 11, fontFamily: "Arial"});
Insert(page, {type: "text", name: "Badge", content: "NEW", fill: "#111111", fontSize: 10, fontFamily: "Arial"});
Insert(page, {type: "text", name: "Ghost", content: "no fill", fontFamily: "Arial", x: 4});
Insert(page, {type: "text", name: "Unknown font", content: "Who?", fill: "#111111", fontFamily: "Nope Sans Pro"});
const card = Insert(document, {type: "frame", name: "Card", x: 700, y: 700, layout: "none", width: 300, height: 200, fill: "#FFFFFF"});
Insert(card, {type: "rectangle", name: "Shade", x: 0, y: 0, width: 300, height: 120, fill: {color: "#1F2A1C"}});
Insert(card, {type: "text", name: "On shade", x: 20, y: 20, content: "Over a sibling", fill: "#FFFFFF", fontSize: 16, fontFamily: "Arial"});
Insert(card, {type: "text", name: "Lost on shade", x: 20, y: 60, content: "Dark on dark", fill: "#333333", fontSize: 16, fontFamily: "Arial"});
Insert(card, {type: "text", name: "Half on shade", x: 20, y: 110, content: "Across the edge", fill: "#333333", fontSize: 16, fontFamily: "Arial"});
Insert(card, {type: "text", name: "Runs off", x: 200, y: 150, content: "Overflowing text that runs past the card edge", fill: "#111111", fontSize: 16, fontFamily: "Arial"});
Print(Get(page, (n, c) => n.type === "text" ? [n.name, c.bounds] : undefined));
