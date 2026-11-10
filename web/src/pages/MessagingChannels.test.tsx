import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  apiError,
  json,
  mockApi,
  noContent,
  renderApp,
  type Routes,
} from "../test/api";

const STAMP = "2026-10-01T10:00:00Z";

const channel = (over: Record<string, unknown> = {}) => ({
  id: 1,
  kind: "sms",
  provider: "sip_trunk",
  name: "Main",
  enabled: true,
  trunkId: 3,
  trunkName: "carrier",
  hasCredentials: false,
  status: "ok",
  statusDetail: "",
  lastEventAt: null,
  addresses: 1,
  createdAt: STAMP,
  updatedAt: STAMP,
  ...over,
});

const address = {
  id: 5,
  channelId: 1,
  channelName: "Main",
  address: "+3270123456",
  label: "Support line",
  routeKind: "inbox",
  enabled: true,
  optOutKeywords: ["stop"],
  helpText: "",
  optOutText: "",
  conversations: 2,
  createdAt: STAMP,
};

const trunks = {
  items: [
    { id: 3, name: "carrier", messaging: true },
    { id: 4, name: "backup", messaging: false },
  ],
};

function api(more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "a", role: "admin" }),
    "GET /api/v1/messaging/channels": () => json({ items: [channel()] }),
    "GET /api/v1/messaging/addresses": () => json({ items: [address] }),
    "GET /api/v1/trunks": () => json(trunks),
    ...more,
  });
}

describe("Messaging channels", () => {
  it("lists channels and numbers, never a credential", async () => {
    api();
    renderApp("/messaging/channels");
    const channels = await screen.findByRole("table", { name: "Channels" });
    expect(within(channels).getByText("carrier")).toBeInTheDocument();
    const numbers = await screen.findByRole("table", { name: "Numbers" });
    expect(within(numbers).getByText("+3270123456")).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/password|secret|token/i);
  });

  it("adds a channel on a trunk and warns when SMS over SIP is off", async () => {
    const calls = api({
      "POST /api/v1/messaging/channels": () =>
        json(channel({ id: 2, name: "Backup" }), 201),
    });
    renderApp("/messaging/channels");
    fireEvent.click(await screen.findByRole("button", { name: "Add channel" }));
    fireEvent.change(await screen.findByLabelText("Name"), {
      target: { value: "Backup" },
    });
    fireEvent.change(screen.getByLabelText("SIP trunk"), {
      target: { value: "4" },
    });
    expect(
      await screen.findByText("SMS over SIP is off on this trunk"),
    ).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("SIP trunk"), {
      target: { value: "3" },
    });
    fireEvent.click(
      screen.getAllByRole("button", { name: "Add channel" }).at(-1)!,
    );
    await waitFor(() =>
      expect(calls.find((c) => c.method === "POST")?.body).toEqual({
        name: "Backup",
        enabled: true,
        trunkId: 3,
        kind: "sms",
        provider: "sip_trunk",
      }),
    );
  });

  it("shows the reason the server refuses a channel", async () => {
    api({
      "POST /api/v1/messaging/channels": () =>
        apiError(409, "conflict", "messaging channel: already exists"),
    });
    renderApp("/messaging/channels");
    fireEvent.click(await screen.findByRole("button", { name: "Add channel" }));
    fireEvent.change(await screen.findByLabelText("Name"), {
      target: { value: "Main" },
    });
    fireEvent.change(screen.getByLabelText("SIP trunk"), {
      target: { value: "3" },
    });
    fireEvent.click(
      screen.getAllByRole("button", { name: "Add channel" }).at(-1)!,
    );
    expect(
      await screen.findByText("messaging channel: already exists"),
    ).toBeInTheDocument();
  });

  it("tests a channel and lists each check", async () => {
    api({
      "POST /api/v1/messaging/channels/1/test": () =>
        json({
          ok: false,
          checks: [
            { name: "trunk", ok: true, detail: "trunk carrier is enabled" },
            {
              name: "sms over sip",
              ok: false,
              detail: "turn on SMS over SIP on the trunk",
            },
          ],
        }),
    });
    renderApp("/messaging/channels");
    fireEvent.click(await screen.findByRole("button", { name: "Test Main" }));
    expect(
      await screen.findByText("The channel cannot work yet"),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/Failed: sms over sip, turn on SMS over SIP/),
    ).toBeInTheDocument();
  });

  it("adds a number to a channel", async () => {
    const calls = api({
      "POST /api/v1/messaging/addresses": () =>
        json({ ...address, id: 6, address: "+3270999999" }, 201),
    });
    renderApp("/messaging/channels");
    fireEvent.click(await screen.findByRole("button", { name: "Add number" }));
    fireEvent.change(await screen.findByLabelText("Number"), {
      target: { value: "+3270999999" },
    });
    fireEvent.change(screen.getByLabelText("Opt-out words"), {
      target: { value: "stop, arret" },
    });
    fireEvent.click(
      screen.getAllByRole("button", { name: "Add number" }).at(-1)!,
    );
    await waitFor(() =>
      expect(calls.find((c) => c.method === "POST")?.body).toEqual({
        label: "",
        enabled: true,
        optOutKeywords: ["stop", "arret"],
        helpText: "",
        optOutText: "",
        channelId: 1,
        address: "+3270999999",
      }),
    );
  });

  it("refuses to delete a number that has conversations and says why", async () => {
    api({
      "DELETE /api/v1/messaging/addresses/5": () =>
        apiError(
          409,
          "conflict",
          "the number has 2 conversations; they are removed by retention or an erase request",
        ),
    });
    renderApp("/messaging/channels");
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete +3270123456" }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete number" }),
    );
    expect(
      await screen.findByText(/the number has 2 conversations/),
    ).toBeInTheDocument();
  });

  it("deletes an empty channel", async () => {
    const calls = api({
      "DELETE /api/v1/messaging/channels/1": () => noContent(),
    });
    renderApp("/messaging/channels");
    fireEvent.click(await screen.findByRole("button", { name: "Delete Main" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete channel" }),
    );
    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE")).toBe(true),
    );
  });
});
