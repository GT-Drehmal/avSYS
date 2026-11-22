import { expect, test } from "bun:test";
import { quantity } from "../src/lib/format";

test("memory shows in the largest unit it fills", () => {
  expect(quantity("3584Mi")).toBe("3.5 GB");
  expect(quantity("4096Mi")).toBe("4 GB");
  expect(quantity("12288Mi")).toBe("12 GB");
  expect(quantity("5632Mi")).toBe("5.5 GB");
  expect(quantity("512Mi")).toBe("512 MB");
  expect(quantity("4Gi")).toBe("4 GB");
  expect(quantity("10Gi")).toBe("10 GB");
  expect(quantity("2048Gi")).toBe("2 TB");
  expect(quantity("1500M")).toBe("1.5 GB");
  expect(quantity("")).toBe("");
  expect(quantity(undefined)).toBe("");
  expect(quantity("weird")).toBe("weird");
});
