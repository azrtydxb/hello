import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, mockApi, noContent, renderApp } from "../test/api";
import type { MyConnector } from "../api/crm";

const me = {
  "GET /api/v1/auth/me": () => json({ username: "anna", role: "user" }),
};

function connector(extra: Partial<MyConnector> = {}): MyConnector {
  return {
    id: 4,
    kind: "outlook",
    name: "Outlook",
    features: ["contacts", "calendar"],
    capabilities: {
      lookup: true,
      logCalls: false,
      recordings: false,
      clickToCall: false,
      screenPop: false,
      ticket: false,
      userScope: true,
      calendar: true,
    },
    connection: null,
    ...extra,
  };
}

const connected: MyConnector["connection"] = {
  status: "connected",
  externalAccount: "anna@contoso.com",
  grantedScopes: ["Contacts.Read", "Calendars.Read"],
  updatedAt: "2026-11-12T10:00:00Z",
  calendarDnd: false,
  calendarActive: false,
};

afterEach(() => vi.unstubAllGlobals());

describe("My connected accounts (spec integrations P18)", () => {
  it("is linked from the sidebar for every user", async () => {
    mockApi({ ...me, "GET /api/v1/me/connections": () => json({ items: [] }) });
    renderApp("/me/connections");
    expect(
      await screen.findByRole("link", { name: "Connected accounts" }),
    ).toHaveAttribute("href", "/me/connections");
    expect(await screen.findByText("Nothing to connect")).toBeVisible();
  });

  it("starts the user's own sign-in and sends the browser to the provider", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    mockApi({
      ...me,
      "GET /api/v1/me/connections": () => json({ items: [connector()] }),
      "POST /api/v1/me/connections/4/connect": () =>
        json({ authorizeUrl: "https://login.microsoftonline.com/x?state=s" }),
    });
    renderApp("/me/connections");
    const panel = await screen.findByRole("article", {
      name: "Outlook account",
    });
    expect(within(panel).getByText("Not connected")).toBeVisible();
    expect(
      within(panel).getByText(
        /your contacts name callers, your calendar can set/,
      ),
    ).toBeVisible();
    fireEvent.click(within(panel).getByRole("button", { name: "Connect" }));
    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith(
        "https://login.microsoftonline.com/x?state=s",
      ),
    );
  });

  it("turns calendar Do Not Disturb on and shows when it is holding it", async () => {
    const calls = mockApi({
      ...me,
      "GET /api/v1/me/connections": [
        () => json({ items: [connector({ connection: connected })] }),
        () =>
          json({
            items: [
              connector({
                connection: {
                  ...connected,
                  calendarDnd: true,
                  calendarActive: true,
                  calendarBusyUntil: "2026-11-12T11:00:00Z",
                },
              }),
            ],
          }),
      ],
      "PUT /api/v1/me/connections/4/calendar-dnd": () =>
        json({ ...connected, calendarDnd: true }),
    });
    renderApp("/me/connections");
    const panel = await screen.findByRole("article", {
      name: "Outlook account",
    });
    expect(within(panel).getByText("anna@contoso.com")).toBeVisible();
    fireEvent.click(
      within(panel).getByRole("switch", {
        name: /Do Not Disturb while my calendar/,
      }),
    );
    await waitFor(() =>
      expect(
        calls.find(
          (c) => c.method === "PUT" && c.url.endsWith("/calendar-dnd"),
        ),
      ).toBeTruthy(),
    );
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
      enabled: true,
    });
    expect(await screen.findByText(/holding Do Not/)).toBeVisible();
  });

  it("hides the calendar switch when the connection has no calendar access, and asks to connect again", async () => {
    mockApi({
      ...me,
      "GET /api/v1/me/connections": () =>
        json({
          items: [
            connector({
              connection: { ...connected, grantedScopes: ["Contacts.Read"] },
            }),
          ],
        }),
    });
    renderApp("/me/connections");
    const panel = await screen.findByRole("article", {
      name: "Outlook account",
    });
    expect(within(panel).queryByRole("switch")).toBeNull();
    expect(within(panel).getByText("Calendar access was added")).toBeVisible();
  });

  it("asks to sign in again when the provider refused the stored sign-in", async () => {
    mockApi({
      ...me,
      "GET /api/v1/me/connections": () =>
        json({
          items: [
            connector({
              connection: {
                ...connected,
                status: "needs_reauth",
                lastError: "the CRM refused the refresh token",
              },
            }),
          ],
        }),
    });
    renderApp("/me/connections");
    expect(
      await screen.findByText("Sign in again", {
        selector: "[class*=alert] *, h3, div",
      }),
    ).toBeVisible();
    expect(screen.getByRole("button", { name: "Connect again" })).toBeEnabled();
  });

  it("disconnects after a confirmation and shows why a sign-in failed", async () => {
    const calls = mockApi({
      ...me,
      "GET /api/v1/me/connections": () =>
        json({ items: [connector({ connection: connected })] }),
      "DELETE /api/v1/me/connections/4": () => noContent(),
    });
    renderApp("/me/connections?error=denied");
    expect(await screen.findByText("The sign-in was declined.")).toBeVisible();
    fireEvent.click(await screen.findByRole("button", { name: "Disconnect" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Disconnect" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "DELETE" && c.url === "/api/v1/me/connections/4",
        ),
      ).toBe(true),
    );
  });
});
