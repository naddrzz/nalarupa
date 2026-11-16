a = Insert(document, {type: "frame", name: "A", x: 0, y: 450, width: 100, height: 100, layout: "none", children: [{type: "rectangle", width: 10, height: 10}, {type: "ellipse", width: 10, height: 10}]});
Copy(a, document, {name: "B", x: 200, y: 450});
Insert(a, {type: "rectangle", name: "C", x: 50, y: 50, width: 10, height: 10});
