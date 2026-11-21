// Which names a list page still says could not be checked, after another
// request. Run with `bun test unit`.
import { expect, test } from "bun:test";
import { stillUnchecked } from "../src/lib/names";

test("names not checked stay until a request checks them", () => {
  // Another group added: the earlier ones stay.
  expect(stillUnchecked(["late_a", "Grumm"], ["Steve"], [])).toEqual(["late_a", "Grumm"]);
  // Some of them sent again and checked; one still not, and a new one not either.
  expect(stillUnchecked(["late_a", "Grumm"], ["LATE_A", "grumm", "Alex"], ["grumm", "Alex"])).toEqual(["grumm", "Alex"]);
  // A gamertag sent behind Floodgate's prefix is the same one.
  expect(stillUnchecked(["Tim203"], [".tim203"], [])).toEqual([]);
  // Not said twice.
  expect(stillUnchecked(["Notch"], [], ["notch"])).toEqual(["Notch"]);
});
