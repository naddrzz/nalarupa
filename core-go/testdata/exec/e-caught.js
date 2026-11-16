Insert("board", {type: "rectangle"});
try { Update("nope", {}) } catch (e) { Print(e.message, e.stack, Object.keys(e), __U.length, __U.name, typeof __U) }
function deep() { return Get("nope", {resolveVariables: true}) }
try { deep() } catch (e) { Print(e.stack) }
try { Insert("board", {type: "rectangle", fill: 1n}) } catch (e) { Print(e.name, e.message, e.stack) }
