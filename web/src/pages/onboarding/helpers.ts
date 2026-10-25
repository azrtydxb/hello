/**
 * `n` consecutive extension numbers from `start`, keeping its width
 * ("0099" -> "0099", "0100"); null when `start` is not 2-10 digits or the
 * range outgrows 10 digits. The server applies the same rule.
 */
export function consecutiveNumbers(start: string, n: number): string[] | null {
  if (!/^[0-9]{2,10}$/.test(start)) return null;
  const first = Number(start);
  const out: string[] = [];
  for (let i = 0; i < n; i++) {
    const num = String(first + i).padStart(start.length, "0");
    if (num.length > 10) return null;
    out.push(num);
  }
  return out;
}
