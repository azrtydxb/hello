import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { PhonePush } from "../../api/prov";
import { PUSH_LABEL, PushLine } from "./push";

describe("push results", () => {
  it("tells the operator a rejected push needs one manual reboot", () => {
    expect(PUSH_LABEL.auth_failed).toMatch(/rejected/i);
    expect(PUSH_LABEL.auth_failed).toMatch(/manual reboot/i);
    expect(PUSH_LABEL.auth_failed).not.toMatch(/authentication failed/i);
    const push = {
      id: 1,
      phoneId: 1,
      kind: "resync",
      status: "auth_failed",
      source: "manual",
      contacts: 1,
      detail: "Phone rejected the request; it needs one manual reboot (401)",
      requestedAt: new Date().toISOString(),
    } as unknown as PhonePush;
    render(<PushLine push={push} />);
    expect(screen.getByText(/Resync: Phone rejected/)).toBeTruthy();
  });
});
