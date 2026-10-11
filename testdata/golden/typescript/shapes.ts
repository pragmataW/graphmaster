import { EventEmitter } from "node:events";
import Logger, { type Level, format as fmt } from "./logger";
import * as geo from "./geo";
import config = require("./config");
import "./polyfills";

export * from "./units";
export * as colors from "./colors";
export { Scale, Ratio as Proportion } from "./scale";

/** Anything that can report its area. */
export interface Shape extends Named, geo.Bounded {
  readonly name: string;
  area(): number;
}

export type ShapeKind = "circle" | "square";

export enum Unit {
  Px = "px",
  Em,
}

export abstract class Base<T extends Shape> implements Shape {
  static count = 0;
  #secret = 1;
  protected readonly log: Logger = new Logger(config.level);
  private onChange = (next: T): void => {
    this.log.info(fmt(next.name));
  };

  constructor(
    public name: string,
    private unit: Unit,
  ) {
    super();
    Base.count++;
  }

  abstract area(): number;

  get label(): string {
    return `${this.name} (${this.unit})`;
  }

  describe(other: T): string {
    const box: geo.Box = geo.boundsOf(other);
    return this.label + box.width + other.area();
  }
}

export class Circle extends Base<Circle> {
  constructor(private radius: number) {
    super("circle", Unit.Px);
  }

  area(): number {
    return Math.PI * square(this.radius);
  }
}

export namespace Registry {
  export function register(s: Shape): void {
    registry.set(s.name, s);
  }
}

const registry = new Map<string, Shape>();
export let current: Shape | undefined;
export const DEFAULT_UNIT = Unit.Px;

export function square(n: number): number {
  return n * n;
}

export const makeCircle = (r: number): Circle => new Circle(r);

function* ids(): Generator<number> {
  yield 1;
}

export default function main(): void {
  const c = makeCircle(2);
  const emitter = new EventEmitter();
  emitter.emit("shape", c.describe(c));
  Registry.register(c);
  for (const id of ids()) {
    console.log(id);
  }
}
