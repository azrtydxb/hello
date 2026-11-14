import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  apiError,
  json,
  mockApi,
  renderApp,
  type Routes,
} from "../../test/api";

const STAMP = "2026-10-01T10:00:00Z";

const channel = {
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
};

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

const routing = {
  addressId: 5,
  routeKind: "inbox",
  routeTarget: "",
  routeName: "",
  schedule: { zone: "", days: [] },
  afterHours: {
    action: "none",
    text: "",
    routeKind: "inbox",
    routeTarget: "",
    routeName: "",
  },
  firstContactText: "",
};

const queue = (id: number, name: string, textEnabled: boolean) => ({
  id,
  name,
  number: "",
  enabled: true,
  strategy: "ring_all",
  textEnabled,
  members: [],
});

function api(more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "a", role: "admin" }),
    "GET /api/v1/messaging/channels": () => json({ items: [channel] }),
    "GET /api/v1/messaging/addresses": () => json({ items: [address] }),
    "GET /api/v1/trunks": () => json({ items: [] }),
    "GET /api/v1/messaging/addresses/5/routing": () => json(routing),
    "GET /api/v1/queues": () =>
      json({
        items: [queue(7, "support", true), queue(8, "voiceonly", false)],
      }),
    "GET /api/v1/me/inbox/agents": () =>
      json({
        items: [{ id: 4, username: "agnes", displayName: "Agnes" }],
      }),
    "GET /api/v1/extensions": () => json({ items: [] }),
    "GET /api/v1/ring-groups": () => json({ items: [] }),
    ...more,
  });
}

async function openRouting() {
  renderApp("/messaging/channels");
  fireEvent.click(
    await screen.findByRole("button", { name: "Routing +3270123456" }),
  );
  return screen.findByLabelText("New conversations go to");
}

beforeEach(() => undefined);
afterEach(() => vi.unstubAllGlobals());

describe("Number routing", () => {
  it("shows where each number goes", async () => {
    api();
    renderApp("/messaging/channels");
    const numbers = await screen.findByRole("table", { name: "Numbers" });
    expect(
      within(numbers).getByText("The supervisors' pool"),
    ).toBeInTheDocument();
  });

  it("routes a number to a queue that takes texts, with hours and an after-hours reply", async () => {
    const calls = api({
      "PUT /api/v1/messaging/addresses/5/routing": () =>
        json({ ...routing, routeKind: "queue", routeTarget: "7" }),
    });
    const kind = await openRouting();
    fireEvent.change(kind, { target: { value: "queue" } });
    // Only a queue that takes texts is offered.
    const which = await screen.findByLabelText("Which");
    expect(within(which).getByText("support")).toBeInTheDocument();
    expect(within(which).queryByText("voiceonly")).toBeNull();
    fireEvent.change(which, { target: { value: "7" } });

    fireEvent.change(screen.getByLabelText("First-contact text"), {
      target: { value: "Thanks for texting" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add a window" }));
    fireEvent.change(screen.getByLabelText("Time zone"), {
      target: { value: "Europe/Brussels" },
    });
    fireEvent.change(screen.getByLabelText("A new conversation"), {
      target: { value: "reply" },
    });
    fireEvent.change(screen.getByLabelText("Reply"), {
      target: { value: "We open at nine" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save routing" }));

    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
        routeKind: "queue",
        routeTarget: "7",
        firstContactText: "Thanks for texting",
        schedule: {
          zone: "Europe/Brussels",
          days: [{ day: 1, from: "09:00", to: "17:00" }],
        },
        afterHours: {
          action: "reply",
          text: "We open at nine",
          routeKind: "inbox",
          routeTarget: "",
        },
      }),
    );
    expect(
      await screen.findByText("Routing of +3270123456 saved."),
    ).toBeVisible();
  });

  it("sends the pool with no target and no zone when there are no windows", async () => {
    const calls = api({
      "PUT /api/v1/messaging/addresses/5/routing": () => json(routing),
    });
    await openRouting();
    fireEvent.click(screen.getByRole("button", { name: "Save routing" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toMatchObject({
        routeKind: "inbox",
        routeTarget: "",
        schedule: { zone: "", days: [] },
      }),
    );
  });

  it("shows why the server refuses a route", async () => {
    api({
      "PUT /api/v1/messaging/addresses/5/routing": () =>
        apiError(400, "validation", "routeTarget: no such queue"),
    });
    const kind = await openRouting();
    fireEvent.change(kind, { target: { value: "user" } });
    fireEvent.change(await screen.findByLabelText("Which"), {
      target: { value: "4" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save routing" }));
    expect(await screen.findByText(/no such queue/)).toBeInTheDocument();
  });
});
