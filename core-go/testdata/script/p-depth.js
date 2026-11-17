/** @schema 2.19 */
let d = 0;
function f(n) { d = n; return f(n + 1) + 1; }
try { f(0); } catch (e) { throw new Error(d + " " + e); }
