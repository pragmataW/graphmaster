import fs from "node:fs";
import { formatTotal, pad as leftPad } from "./format.mjs";
import * as util from "./util.js";

export async function run(file) {
  const data = await fs.promises.readFile(file, "utf8");
  const lazy = await import("./lazy.js");
  lazy.init();
  return formatTotal(util.parse(data)) + leftPad("x");
}

export default class App {
  start() {
    return run("input.txt");
  }
}

export { run as main };
