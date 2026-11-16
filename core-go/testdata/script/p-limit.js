/** @schema 2.19 */
Error.stackTraceLimit = 2;
function a() { b(); }
function b() { c(); }
function c() { throw new TypeError("deep"); }
a();
