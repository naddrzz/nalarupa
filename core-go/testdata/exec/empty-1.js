// The CLI's fallback when the document does not exist: the empty template.
page = Insert(document, {type: "frame", name: "Screen", width: 390, height: 844, layout: "vertical", gap: 12, padding: 16, fill: "#FFFFFF"});
Insert(page, {type: "rectangle", name: "Header", width: "fill_container", height: 64, fill: "#222222"});
Insert(page, {type: "rectangle", name: "Card", width: "fill_container", height: 120, fill: "#EEEEEE", x: 5});
Copy(page, document, {name: "Screen 2", x: 440});
Print(FindEmptySpace({width: 390, height: 844}), Get(n => n.name));
