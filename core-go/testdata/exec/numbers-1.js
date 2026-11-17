// Numbers as serde_json reads JavaScript's JSON and writes it again, in the document and in the globals.
nums = [2 ** 70, 0.1 + 0.2, 5e-324, 1e-7, 123456789.123456789, -0, 2 ** 53 + 2, 1e300 * 10, -1e-300, 1 / 3, 4.35,
  9007199254740993, 1e16, 12345678901234567890, -9223372036854775808, -9223372036854775809, 1e21, 2 ** 64, 0.000001,
  1.7976931348623157e308, 2.2250738585072014e-308, 98765.4321e10, 1e15 + 0.3];
Insert(document, {type: "frame", name: "Numbers", x: 0, y: 450, width: 10, height: 10, metadata: {nums}});
one = nums[0]; tiny = nums[2];
