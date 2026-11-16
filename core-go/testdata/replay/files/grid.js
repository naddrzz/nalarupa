/** @schema 2.19 */
/**
 * @input cols: number(min=1) = 3
 * @input gap: number = 6
 * @input cols: number(min=2, max=8) = 4
 */
// The second `cols` replaces the first; a non-numeric gap falls back to 0.
const { cols, gap } = nalarupa.input;
const size = (nalarupa.width - gap * (cols - 1)) / cols;
const cells = [];
for (let r = 0; r < 3; r++) {
  for (let c = 0; c < cols; c++) {
    const hue = (r * cols + c) * 37 % 360;
    cells.push({type: "frame", x: c * (size + gap), y: r * (size + gap), width: size, height: size,
      fill: `hsl(${hue}, 70%, 55%)`, cornerRadius: 4, layout: "horizontal", padding: 4, gap: 2, children: [
        {type: "ellipse", width: 8, height: 8, fill: "#ffffff"},
        {type: "rectangle", width: "fill_container", height: 8, fill: "#000000", opacity: 0.3}
      ]});
  }
}
return cells;
