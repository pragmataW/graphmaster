export function formatName(name: string): string {
  return capitalize(name.trim());
}

export const capitalize = (s: string): string =>
  s.length === 0 ? s : s[0].toUpperCase() + s.slice(1);
