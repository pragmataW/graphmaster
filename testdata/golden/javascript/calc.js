"use strict";

const path = require("path");
const { add, multiply: mul, ...rest } = require("./math");
require("./polyfill");

function Legacy(name) {
  this.name = name;
}

Legacy.prototype.greet = function (other) {
  return "hi " + other.name + path.sep;
};

class Calculator extends Base {
  #total = 0;
  static precision = 2;
  history = [];

  constructor(start) {
    super();
    this.#total = start;
  }

  sum(a, b) {
    const r = add(a, b);
    this.record(r);
    return r;
  }

  product(a, b) {
    return mul(a, b);
  }

  record(value) {
    this.history.push(value);
  }

  get total() {
    return this.#total;
  }
}

class Scientific extends mixins.Precise {
  square(x) {
    return this.product(x, x);
  }
}

const helpers = {
  double(x) {
    return x * 2;
  },
  triple: (x) => x * 3,
};

const VERSION = "1.0";
let instances = 0;
var legacyFlag = true;

exports.create = function create() {
  instances++;
  return new Calculator(0);
};

helpers.reset = function reset() {
  instances = 0;
  return create();
};

module.exports.makeScientific = () => new Scientific();
module.exports = { Calculator, Scientific, helpers, VERSION, legacyFlag, rest };
