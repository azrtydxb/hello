// Reconnect backoff with jitter (spec edge case "reconnect storms"): clients
// of a restarted edge must not all return in the same instant.

export interface BackoffOptions {
  baseMs?: number;
  maxMs?: number;
  /** Fraction of the delay that is random, 0 to 1. */
  jitter?: number;
}

/** The delay before attempt n (0-based). `rand` is injectable for tests. */
export function backoffDelay(
  attempt: number,
  opts: BackoffOptions = {},
  rand: () => number = Math.random,
): number {
  const base = opts.baseMs ?? 1000;
  const max = opts.maxMs ?? 30_000;
  const jitter = Math.min(1, Math.max(0, opts.jitter ?? 0.5));
  const exp = Math.min(max, base * 2 ** Math.min(attempt, 16));
  // Between (1 - jitter) and 1 of the exponential delay.
  return Math.round(exp * (1 - jitter * rand()));
}
