import { describe, expect, it, beforeEach } from "vitest";
import { Circle, square } from "./shapes";

describe("Circle", () => {
  let c: Circle;

  beforeEach(() => {
    c = new Circle(1);
  });

  it("computes area", () => {
    expect(c.area()).toBeCloseTo(Math.PI);
  });

  it.each([1, 2])("handles %d", (n) => {
    expect(square(n)).toBe(n * n);
  });

  describe("Circle.describe", () => {
    it.skip("mentions the name", async () => {
      expect(c.describe(c)).toContain("circle");
    });
  });
});

test("square", () => {
  expect(square(3)).toBe(9);
});
