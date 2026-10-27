import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { apiError, json, mockApi, renderApp } from "../test/api";
import { dialable, type OriginateStatus } from "../api/originate";
import { REASON_TEXT } from "./Dial";

const ALICE = {
  "GET /api/v1/auth/me": () =>
    json({
      username: "alice",
      role: "user",
      extensions: [{ id: 10, number: "101", name: "Alice" }],
    }),
};

function status(
  state: OriginateStatus["state"],
  extra: Partial<OriginateStatus> = {},
): OriginateStatus {
  return {
    originateId: "orig_1",
    state,
    extension: "101",
    destination: "0501234567",
    createdAt: "2026-10-27T10:00:00Z",
    updatedAt: "2026-10-27T10:00:01Z",
    ...extra,
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("dialable", () => {
  it("reads what a tel: link may carry and refuses what is not a number", () => {
    expect(dialable("0501234567")).toBe("0501234567");
    expect(dialable("tel:+971-50-123 4567")).toBe("+971501234567");
    expect(dialable("tel:%2B97150;phone-context=x")).toBe("+97150");
    expect(dialable("+1 (555) 010-9999")).toBe("+15550109999");
    expect(dialable("*97")).toBe("*97");
    expect(dialable("sip:evil@host")).toBe("");
    expect(dialable("050abc")).toBe("");
    expect(dialable("")).toBe("");
    expect(dialable("1".repeat(65))).toBe("");
  });
});

describe("Dial", () => {
  it("shows the number of a tel link and the extension that rings, and places nothing until clicked", async () => {
    const calls = mockApi({ ...ALICE });
    renderApp("/dial?n=tel:0501234567");
    expect(await screen.findByDisplayValue("0501234567")).toBeInTheDocument();
    expect(screen.getByText("101")).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Call 0501234567/ }),
    ).toBeEnabled();
    await new Promise((r) => setTimeout(r, 50));
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("places the call on the click, follows it and hangs up", async () => {
    const calls = mockApi({
      ...ALICE,
      "POST /api/v1/calls/originate": () => json(status("queued"), 202),
      "GET /api/v1/calls/originate/orig_1": [
        () => json(status("ringing_agent")),
        () => json(status("connected")),
      ],
      "DELETE /api/v1/calls/originate/orig_1": () =>
        json(status("connected"), 202),
    });
    renderApp("/dial?n=0501234567");
    fireEvent.click(
      await screen.findByRole("button", { name: /Call 0501234567/ }),
    );
    expect(await screen.findByText("Getting a line…")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toMatchObject({
      destination: "0501234567",
      extension: "101",
    });
    expect(
      (post?.body as { idempotencyKey: string }).idempotencyKey,
    ).toBeTruthy();
    expect(
      await screen.findByText(/Your phone is ringing/, {}, { timeout: 4000 }),
    ).toBeInTheDocument();
    expect(
      await screen.findByText("Connected.", {}, { timeout: 4000 }),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Hang up" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE")).toBe(true),
    );
  });

  it("says why a call failed", async () => {
    mockApi({
      ...ALICE,
      "POST /api/v1/calls/originate": () =>
        json(status("failed", { reason: "agent_no_answer" }), 202),
    });
    renderApp("/dial?n=0501234567");
    fireEvent.click(
      await screen.findByRole("button", { name: /Call 0501234567/ }),
    );
    expect(
      await screen.findByText(REASON_TEXT.agent_no_answer),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /Dial another number/ }),
    ).toBeInTheDocument();
  });

  it("shows an API refusal and keeps the form", async () => {
    mockApi({
      ...ALICE,
      "POST /api/v1/calls/originate": () =>
        apiError(
          422,
          "emergency_not_allowed",
          "click-to-call does not place emergency calls",
        ),
    });
    renderApp("/dial?n=999");
    fireEvent.click(await screen.findByRole("button", { name: /Call 999/ }));
    expect(
      await screen.findByText(/does not place emergency calls/),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Call 999/ })).toBeEnabled();
  });

  it("refuses a link that is not a number", async () => {
    mockApi({ ...ALICE });
    renderApp("/dial?n=sip:evil@host");
    expect(
      await screen.findByText("That link is not a phone number"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Call" })).toBeDisabled();
  });

  it("tells a user without an extension there is no phone to ring", async () => {
    mockApi({
      "GET /api/v1/auth/me": () => json({ username: "lone", role: "user" }),
    });
    renderApp("/dial?n=0501234567");
    expect(await screen.findByText("No extension linked")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Call/ })).toBeNull();
  });

  it("lets an administrator without an extension name the one to ring", async () => {
    const calls = mockApi({
      "GET /api/v1/auth/me": () => json({ username: "root", role: "admin" }),
      "POST /api/v1/calls/originate": () => json(status("queued"), 202),
    });
    renderApp("/dial?n=0501234567");
    const field = await screen.findByLabelText("Ring this extension first");
    expect(
      screen.getByRole("button", { name: /Call 0501234567/ }),
    ).toBeDisabled();
    fireEvent.change(field, { target: { value: "102" } });
    fireEvent.click(screen.getByRole("button", { name: /Call 0501234567/ }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({
        extension: "102",
      }),
    );
  });

  it("registers the console as the tel: handler only when asked", async () => {
    mockApi({ ...ALICE });
    const register = vi.fn();
    vi.stubGlobal("navigator", {
      ...navigator,
      registerProtocolHandler: register,
    });
    renderApp("/dial");
    const button = await screen.findByRole("button", {
      name: "Use Hello for phone links",
    });
    expect(register).not.toHaveBeenCalled();
    fireEvent.click(button);
    expect(register).toHaveBeenCalledWith(
      "tel",
      `${window.location.origin}/dial?n=%s`,
    );
  });
});
