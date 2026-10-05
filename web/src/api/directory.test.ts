import { describe, expect, it } from "vitest";
import { expiresIn, forwardingLabel } from "./directory";

const NOW = Date.parse("2026-10-01T10:00:00Z");

describe("directory views", () => {
  it.each([
    ["2026-10-01T10:53:20Z", "53 min"],
    ["2026-10-01T10:00:40Z", "40 s"],
    ["2026-10-01T09:59:00Z", "Expired"],
    ["not a time", "—"],
  ])("expiresIn(%s) is %s", (at, want) => {
    expect(expiresIn(at, NOW)).toBe(want);
  });

  it("labels forwarding, — when none", () => {
    const base = {
      id: 1,
      number: "100",
      name: "R",
      externalNumber: "",
      createdAt: "",
      updatedAt: "",
    };
    expect(forwardingLabel(base)).toBe("—");
    expect(forwardingLabel({ ...base, forwardAlways: "+971501234567" })).toBe(
      "Always → +971501234567",
    );
  });
});
