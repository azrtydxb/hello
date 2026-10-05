/** A timestamp in the viewer's locale; empty input renders as an em dash. */
export function formatTime(value: string | undefined | null): string {
  if (!value) return "—";
  const d = new Date(value);
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString();
}

/** Milliseconds as h:mm:ss or m:ss. */
export function formatDuration(ms: number): string {
  const total = Math.max(0, Math.round(ms / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = String(total % 60).padStart(2, "0");
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
}

/** Trace steps in step order (by `n`, not by array position). */
export function sortedSteps<T extends { n: number }>(trace: readonly T[]): T[] {
  return [...trace].sort((a, b) => a.n - b.n);
}
