const test = require("node:test");
const assert = require("node:assert");
const { Calculator } = require("./calc");

describe("Calculator", function () {
  it("sums", () => {
    const c = new Calculator(0);
    assert.strictEqual(c.sum(2, 3), 5);
  });
});

test("Calculator#product multiplies", () => {
  assert.strictEqual(new Calculator(0).product(2, 3), 6);
});
