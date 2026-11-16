/** @schema 2.19 */
let o = {};
for (let i = 0; i < 80; i++) o = {a: o};
return [{type: "rectangle", width: JSON.stringify(o).length, height: 1}];
