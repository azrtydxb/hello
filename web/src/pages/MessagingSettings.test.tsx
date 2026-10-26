import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, mockApi, renderApp, type Routes } from "../test/api";

const SETTINGS = {
  retentionSmsDays: 365,
  retentionChatDays: 90,
  reopenDays: 7,
  emailNotify: true,
};

function api(more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "a", role: "admin" }),
    "GET /api/v1/messaging/settings": () => json(SETTINGS),
    ...more,
  });
}

describe("Messaging settings", () => {
  it("validates and saves the retention", async () => {
    const calls = api({
      "PUT /api/v1/messaging/settings": () =>
        json({ ...SETTINGS, retentionSmsDays: 30 }),
    });
    renderApp("/messaging/settings");
    const days = await screen.findByLabelText(
      "Keep text conversations for (days)",
    );
    expect(days).toHaveValue("365");
    fireEvent.change(days, { target: { value: "0" } });
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    expect(
      await screen.findByText("Enter a whole number from 1 to 3650."),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
    fireEvent.change(days, { target: { value: "30" } });
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
        ...SETTINGS,
        retentionSmsDays: 30,
      }),
    );
    expect(
      await screen.findByText("Messaging settings saved."),
    ).toBeInTheDocument();
  });

  it("erases a customer after a confirmation and reports counts", async () => {
    const calls = api({
      "POST /api/v1/inbox/erase": () =>
        json({
          conversations: 2,
          messages: 9,
          optOutsKept: 1,
          providers: [
            {
              provider: "sip_trunk",
              redacted: false,
              detail:
                "the carrier keeps its own logs; Hello cannot delete them",
            },
          ],
        }),
    });
    renderApp("/messaging/settings");
    fireEvent.change(await screen.findByLabelText("Customer's number"), {
      target: { value: "+32475123456" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Erase this customer…" }),
    );
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.click(
      await screen.findByRole("button", { name: "Erase customer" }),
    );
    expect(
      await screen.findByText(/Deleted 2 conversations and 9 messages/),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/the carrier keeps its own logs/),
    ).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      peer: "+32475123456",
    });
  });

  it("shows why an erase failed and keeps the dialog", async () => {
    api({
      "POST /api/v1/inbox/erase": () =>
        apiError(
          400,
          "bad_request",
          "invalid configuration: peer: must be a phone number",
        ),
    });
    renderApp("/messaging/settings");
    fireEvent.change(await screen.findByLabelText("Customer's number"), {
      target: { value: "nope" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Erase this customer…" }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Erase customer" }),
    );
    expect(
      await screen.findByText(/peer: must be a phone number/),
    ).toBeInTheDocument();
  });
});
