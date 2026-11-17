/** @schema 2.10 */
// 2.10 output: stroke objects (align, thickness) are migrated to the current schema.
return [
  {type: "rectangle", x: 10, y: 10, width: 60, height: 40, fill: "#ffffff", stroke: {align: "outside", thickness: 4, fill: "#cc0044"}},
  {type: "frame", x: 20, y: 60, width: 80, height: 50, fill: "#eeeeee", stroke: {align: "center", thickness: 2, fill: "#000000"}, children: [
    {type: "rectangle", x: 10, y: 10, width: 20, height: 20, fill: "#0044cc", stroke: {align: "inside", thickness: 3, fill: "#00cc44"}}
  ]}
];
