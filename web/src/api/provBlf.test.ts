import { describe, expect, it } from "vitest";
import { supportsBlf } from "./prov";

describe("supportsBlf", () => {
  it("refuses BLF keys on Grandstream WP models", () => {
    for (const m of ["WP822", "wp 856", "WP836", "WP810", "WP-816"]) {
      expect(supportsBlf("grandstream", m)).toBe(false);
    }
  });
  it("keeps them for other phones", () => {
    expect(supportsBlf("grandstream", "GRP2614")).toBe(true);
    expect(supportsBlf("yealink", "T54W")).toBe(true);
    expect(supportsBlf("poly", "WP822")).toBe(true);
  });
});
