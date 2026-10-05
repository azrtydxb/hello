import "@testing-library/jest-dom/vitest";
import { cleanup, configure } from "@testing-library/react";
import { afterEach } from "vitest";

// findBy*/waitFor give up after 1s by default, which a loaded CI runner
// (or a busy laptop) can exceed for a full render + mocked fetch round trip.
configure({ asyncUtilTimeout: 5000 });

afterEach(() => {
  cleanup();
  // The theme lives on <html> and in localStorage, both outside the render.
  window.localStorage.clear();
  delete document.documentElement.dataset.theme;
});
