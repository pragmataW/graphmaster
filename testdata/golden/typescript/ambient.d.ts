declare module "legacy-lib" {
  export function legacy(x: number): string;
}

declare function globalHelper(input: string): number;

declare global {
  interface Window {
    appVersion: string;
  }
}

export declare class Widget {
  render(target: HTMLElement): void;
}
