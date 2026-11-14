// The ringtone: a two-tone cadence from the Web Audio API, started only once
// the user has interacted with the page (browser autoplay policy), so a tab
// that was never touched rings silently and relies on the notification.

export interface Ringer {
  start(): void;
  stop(): void;
}

type Ctx = { new (): AudioContext };

/** Whether the page may play sound yet (the user has interacted with it). */
export function mayPlaySound(): boolean {
  const ua = (
    globalThis.navigator as
      { userActivation?: { hasBeenActive: boolean } } | undefined
  )?.userActivation;
  return ua ? ua.hasBeenActive : true;
}

export function createRinger(
  ctor: Ctx | undefined = (globalThis as { AudioContext?: Ctx }).AudioContext,
): Ringer {
  let ctx: AudioContext | null = null;
  let timer: ReturnType<typeof setInterval> | null = null;
  const burst = () => {
    if (!ctx) return;
    const t = ctx.currentTime;
    for (const [i, f] of [440, 480].entries()) {
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      osc.frequency.value = f;
      gain.gain.setValueAtTime(0, t);
      gain.gain.linearRampToValueAtTime(0.12, t + 0.05);
      gain.gain.setValueAtTime(0.12, t + 0.9);
      gain.gain.linearRampToValueAtTime(0, t + 1.0);
      osc.connect(gain).connect(ctx.destination);
      osc.start(t + i * 0.0);
      osc.stop(t + 1.05);
    }
  };
  return {
    start() {
      if (timer || !ctor || !mayPlaySound()) return;
      try {
        ctx = new ctor();
        void ctx.resume();
        burst();
        timer = setInterval(burst, 3000);
      } catch {
        ctx = null;
      }
    },
    stop() {
      if (timer) clearInterval(timer);
      timer = null;
      if (ctx) void ctx.close().catch(() => undefined);
      ctx = null;
    },
  };
}
