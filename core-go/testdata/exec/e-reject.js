Insert("board", {type: "rectangle", name: "Temp"});
(async () => {
  await null;
  throw new RangeError("late");
})();
