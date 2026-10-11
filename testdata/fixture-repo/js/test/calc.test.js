const test = require("node:test");
const assert = require("node:assert");
const { Calculator, ScientificCalculator } = require("../lib/calc");

test("sum adds and records", () => {
  const c = new Calculator();
  assert.strictEqual(c.sum(2, 3), 5);
  assert.deepStrictEqual(c.history, [5]);
});

test("square multiplies", () => {
  assert.strictEqual(new ScientificCalculator().square(4), 16);
});
