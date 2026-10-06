import "@testing-library/jest-dom/vitest";
import { cleanup, configure } from "@testing-library/react";
import { afterEach, vi } from "vitest";

// findBy*/waitFor give up after 1s by default, which a loaded CI runner
// (or a busy laptop) can exceed for a full render + mocked fetch round trip.
configure({ asyncUtilTimeout: 5000 });

// Testing Library drives fake timers only when it sees Jest's. Point it at
// Vitest's, so findBy*/waitFor keep working in a test that fakes setTimeout
// to step a poll on demand (vi.useFakeTimers + vi.advanceTimersByTimeAsync).
Object.assign(globalThis, {
  jest: { advanceTimersByTime: (ms: number) => vi.advanceTimersByTime(ms) },
});

afterEach(() => {
  cleanup();
  // The theme lives on <html> and in localStorage, both outside the render.
  window.localStorage.clear();
  delete document.documentElement.dataset.theme;
});
