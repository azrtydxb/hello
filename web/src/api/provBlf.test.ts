import { describe, expect, it } from "vitest";
import { sideKeyChoices, speedDialSlots, supportsBlf } from "./prov";

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

describe("speedDialSlots", () => {
  it("gives a Grandstream WP speed dials in place of BLF keys", () => {
    expect(speedDialSlots("grandstream", "WP836")).toBe(9);
    expect(speedDialSlots("grandstream", "wp 822")).toBe(9);
    expect(speedDialSlots("grandstream", "WP820")).toBe(9);
    expect(speedDialSlots("grandstream", "WP856")).toBe(30);
  });
  it("gives every other phone none", () => {
    expect(speedDialSlots("grandstream", "GRP2614")).toBe(0);
    expect(speedDialSlots("yealink", "T54W")).toBe(0);
  });
});

describe("sideKeyChoices", () => {
  it("offers the alarm call on the WP8x6 only", () => {
    expect(sideKeyChoices("grandstream", "WP826")).toEqual([
      "ptt",
      "alarm",
      "none",
    ]);
    expect(sideKeyChoices("grandstream", "WP856")).toEqual(["ptt", "none"]);
    expect(sideKeyChoices("grandstream", "WP820")).toEqual(["ptt", "none"]);
    expect(sideKeyChoices("grandstream", "WP822")).toEqual([]);
    expect(sideKeyChoices("snom", "D785")).toEqual([]);
  });
});
