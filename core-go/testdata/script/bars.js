/**
 * A bar chart.
 * @schema 2.19
 * @input count: number(min=1, max=12) = 5
 * @input color: color = #336699
 * @input mode: enum("grouped", "stacked") = "grouped"
 * @input rounded: boolean
 * @input label: string = "Bars"
 */
const { width, height, input } = nalarupa;
const gap = 4;
const w = (width - gap * (input.count - 1)) / input.count;
const out = [];
for (let i = 0; i < input.count; i++) {
  const h = Math.round(height * (0.2 + 0.8 * Math.random()));
  const bar = {type: "rectangle", id: "dropped", x: i * (w + gap), y: height - h, width: w, height: h, fill: input.color};
  if (input.rounded) bar.cornerRadius = [3, 3, 0, 0];
  if (input.mode === "stacked") {
    out.push({type: "frame", layout: "vertical", x: bar.x, y: bar.y, width: w, height: h, children: [
      {type: "rectangle", width: "fill_container", height: h / 3, fill: "#222222"},
      {type: "rectangle", width: "fill_container", height: "fill_container", fill: input.color, opacity: 0.7}
    ]});
  } else {
    out.push(bar);
  }
}
out.push({type: "ref", ref: "comp", x: width - 20, y: 0});
out.push({type: "path", x: 0, y: 0, width: width, height: height, stroke: {fill: "#000000", thickness: 1}, geometry: "M0 " + height + " L" + width + " " + height});
return out;
