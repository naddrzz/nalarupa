page = Insert(document, {type: "frame", name: "Page", x: 0, y: 600, width: 300, height: 200, layout: "none"});
count = 3; label = "three"; nested = {list: [1, 2.5, {deep: true}], none: null};
helper = function () { return 1 };
nothing = undefined;
var declared = "var";
let scoped = "let";
when = new Date(0);
big = 2 ** 70; weird = NaN; cyclic = {}; cyclic.self = cyclic;
