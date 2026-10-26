import { describe, expect, it } from "vitest";
import { hasPttKey } from "./PttOverride";

describe("hasPttKey", () => {
  it("covers every Grandstream WP family the server gives PTT", () => {
    for (const model of [
      "WP816",
      "WP826",
      "WP836",
      "wp 836",
      "WP856",
      "WP820",
      "WP810",
      "WP822",
      "WP825",
    ]) {
      expect(hasPttKey({ vendor: "grandstream", model })).toBe(true);
    }
    for (const model of ["GXP2170", "GRP2614", "WP800"]) {
      expect(hasPttKey({ vendor: "grandstream", model })).toBe(false);
    }
    expect(hasPttKey({ vendor: "yealink", model: "WP836" })).toBe(false);
  });
});
