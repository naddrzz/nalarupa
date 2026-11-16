// SetVariables with replace drops the old definitions; themes gain the new axis values.
SetVariables({only: {type: "number", value: [{value: 1}, {value: 2, theme: {density: "compact"}}]}}, true);
Print(GetVariables());
Insert(document, {type: "rectangle", name: "Sized", x: 0, y: 450, width: "$only", height: 10});
