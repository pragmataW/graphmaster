import { createRequire } from "node:module";
import { formatTotal } from "./format.mjs";

const require = createRequire(import.meta.url);
const { ScientificCalculator } = require("../lib/calc.js");

export function run() {
  const calc = new ScientificCalculator();
  const total = calc.sum(calc.square(3), 1);
  return formatTotal(total);
}

console.log(run());
