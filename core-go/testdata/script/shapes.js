/** @schema 2.19 */
const { width: w, height: h } = pencil;
return [
  {type: "ellipse", x: 0, y: 0, width: h, height: h, fill: "#ffcc00", stroke: {fill: "#663300", thickness: 3, align: "inside"}},
  {type: "polygon", x: h + 4, y: 0, width: h, height: h, polygonCount: 6, fill: "#22aa66", rotation: 15},
  {type: "rectangle", x: w - 16, y: 4, width: 12, height: h - 8, fill: {type: "gradient", gradientType: "linear", rotation: 90, colors: [{color: "#ff0000", position: 0}, {color: "#0000ff", position: 1}]}},
  {type: "line", x: 0, y: h - 2, width: w, height: 0, stroke: {fill: "#000000", thickness: 2}}
];
