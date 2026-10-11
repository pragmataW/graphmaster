export function formatTotal(value, currency = "EUR") {
  return `${pad(value.toFixed(2))} ${currency}`;
}

function pad(s) {
  return s.padStart(10, " ");
}
