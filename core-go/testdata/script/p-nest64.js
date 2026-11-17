/** @schema 2.19 */
let n = {type: "rectangle", width: 1, height: 1};
for (let i = 0; i < 63; i++) n = {type: "frame", layout: "none", children: [n]};
return [n];
