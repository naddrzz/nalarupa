/** @schema 2.19 */
// Jobs never run for script nodes: only the synchronous output shows, and the rejection is not an error.
const out = [{type: "rectangle", x: 0, y: 0, width: 30, height: 30, fill: "#3377ff"}];
(async () => { await null; out.push({type: "rectangle", x: 40, y: 0, width: 30, height: 30, fill: "#ff3377"}); })();
Promise.resolve().then(() => out.push({type: "ellipse", width: 10, height: 10, fill: "#000000"}));
Promise.reject(new Error("never seen"));
return out;
