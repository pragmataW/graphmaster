"use strict";

const { add, multiply } = require("./math");

class Calculator {
  constructor() {
    this.history = [];
  }

  sum(a, b) {
    const r = add(a, b);
    this.record(r);
    return r;
  }

  product(a, b) {
    return multiply(a, b);
  }

  record(value) {
    this.history.push(value);
  }
}

class ScientificCalculator extends Calculator {
  square(x) {
    return this.product(x, x);
  }
}

module.exports = { Calculator, ScientificCalculator };
