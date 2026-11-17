/** @schema 2.19 */
const seen = [];
try { undefinedName; } catch (e) { seen.push(String(e)); }
function f() { return f() + 1; }
try { f(); } catch (e) { seen.push(String(e)); }
throw new Error(seen.join(" | "));
