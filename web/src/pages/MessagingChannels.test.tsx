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
// Built at run time: a literal looks like a real Twilio SID to secret scanners.
const ACCOUNT_SID = "AC" + "ab".repeat(16);

const channel = (over: Record<string, unknown> = {}) => ({
  id: 1,
  kind: "sms",
  provider: "sip_trunk",
  name: "Main",
  enabled: true,
  trunkId: 3,
  trunkName: "carrier",
  hasCredentials: false,
  accountHint: "",
  messagingService: false,
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
  deskPhoneDelivery: false,
  ownerExtensionId: null,
  sendsPerMinute: 60,
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
    "GET /api/v1/extensions": () =>
      json({ items: [{ id: 10, number: "101", name: "Reception" }] }),
    ...more,
  });
}

// Built at run time: a literal looks like a real key to secret scanners.
const API_KEY = "KEY" + "0123456789".repeat(3);
const PROFILE_ID = "4001932a-6b2d-4a0c-8f7e-9d5a1c2b3e4f";

const registration = (over: Record<string, unknown> = {}) => ({
  provider: "telnyx",
  supported: true,
  readAt: STAMP,
  brands: [
    {
      id: "B1",
      name: "Acme",
      type: "private_profit",
      state: "approved",
      detail: "",
    },
  ],
  campaigns: [
    {
      id: "C1",
      brandId: "B1",
      useCase: "customer_care",
      state: "approved",
      detail: "",
    },
  ],
  numbers: [
    {
      number: "+12125550100",
      kind: "10dlc",
      state: "approved",
      detail: "",
      added: true,
    },
    {
      number: "+18005550100",
      kind: "toll_free",
      state: "rejected",
      detail: "missing opt-in",
      added: false,
    },
  ],
  unavailable: [],
  checklist: [
    {
      id: "brand",
      title: "Register the brand (A2P 10DLC)",
      state: "done",
      detail: "A brand is approved.",
      link: "https://developers.telnyx.com/docs/messaging/10dlc/quickstart",
    },
    {
      id: "tollfree",
      title: "Verify toll-free numbers",
      state: "attention",
      detail: "A toll-free verification was rejected.",
      link: "https://developers.telnyx.com/docs/messaging",
    },
  ],
  ...over,
});

const telnyxChannel = channel({
  id: 7,
  name: "Telnyx",
  provider: "telnyx",
  trunkId: null,
  trunkName: "",
  hasCredentials: true,
  accountHint: "3e4f",
  messagingService: true,
});

const telnyxAddress = {
  ...address,
  id: 8,
  channelId: 7,
  channelName: "Telnyx",
  address: "+12125550100",
};

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

  it("adds a Twilio channel and never shows the token again", async () => {
    const calls = api({
      "POST /api/v1/messaging/channels": () =>
        json(
          channel({
            id: 3,
            name: "Twilio",
            provider: "twilio",
            trunkId: null,
            trunkName: "",
            hasCredentials: true,
            accountHint: "cdef",
          }),
          201,
        ),
    });
    renderApp("/messaging/channels");
    fireEvent.click(await screen.findByRole("button", { name: "Add channel" }));
    fireEvent.change(await screen.findByLabelText("Name"), {
      target: { value: "Twilio" },
    });
    fireEvent.change(screen.getByLabelText("Carrier"), {
      target: { value: "twilio" },
    });
    expect(screen.queryByLabelText("SIP trunk")).toBeNull();
    fireEvent.change(screen.getByLabelText("Account SID"), {
      target: { value: ACCOUNT_SID },
    });
    const token = screen.getByLabelText("Auth token");
    expect(token).toHaveAttribute("type", "password");
    fireEvent.change(token, { target: { value: "secret-token" } });
    fireEvent.click(
      screen.getAllByRole("button", { name: "Add channel" }).at(-1)!,
    );
    await waitFor(() =>
      expect(calls.find((c) => c.method === "POST")?.body).toEqual({
        name: "Twilio",
        enabled: true,
        kind: "sms",
        provider: "twilio",
        accountSid: ACCOUNT_SID,
        authToken: "secret-token",
        messagingServiceSid: "",
      }),
    );
    await waitFor(() =>
      expect(screen.queryByLabelText("Auth token")).toBeNull(),
    );
    expect(screen.queryByText("secret-token")).toBeNull();
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
        deskPhoneDelivery: false,
        ownerExtensionId: null,
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

  it("turns on desk phone delivery and names an owner extension", async () => {
    const calls = api({
      "PUT /api/v1/messaging/addresses/5": () =>
        json({ ...address, deskPhoneDelivery: true, ownerExtensionId: 10 }),
    });
    renderApp("/messaging/channels");
    fireEvent.click(
      await screen.findByRole("button", { name: "Edit +3270123456" }),
    );
    fireEvent.click(
      await screen.findByRole("switch", { name: /Deliver to desk phones/ }),
    );
    await screen.findByRole("option", { name: "101 Reception" });
    fireEvent.change(screen.getByLabelText("Owner extension"), {
      target: { value: "10" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save number" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toMatchObject({
        deskPhoneDelivery: true,
        ownerExtensionId: 10,
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

  it("adds a Telnyx channel and never shows the key again", async () => {
    const calls = api({
      "POST /api/v1/messaging/channels": () => json(telnyxChannel, 201),
    });
    renderApp("/messaging/channels");
    fireEvent.click(await screen.findByRole("button", { name: "Add channel" }));
    fireEvent.change(await screen.findByLabelText("Name"), {
      target: { value: "Telnyx" },
    });
    fireEvent.change(screen.getByLabelText("Carrier"), {
      target: { value: "telnyx" },
    });
    expect(screen.queryByLabelText("SIP trunk")).toBeNull();
    expect(screen.queryByLabelText("Account SID")).toBeNull();
    const key = screen.getByLabelText("API key");
    expect(key).toHaveAttribute("type", "password");
    fireEvent.change(key, { target: { value: API_KEY } });
    fireEvent.change(screen.getByLabelText(/Messaging profile ID/), {
      target: { value: PROFILE_ID },
    });
    fireEvent.click(
      screen.getAllByRole("button", { name: "Add channel" }).at(-1)!,
    );
    await waitFor(() =>
      expect(calls.find((c) => c.method === "POST")?.body).toEqual({
        name: "Telnyx",
        enabled: true,
        kind: "sms",
        provider: "telnyx",
        apiKey: API_KEY,
        messagingProfileId: PROFILE_ID,
      }),
    );
    await waitFor(() => expect(screen.queryByLabelText("API key")).toBeNull());
    expect(screen.queryByText(API_KEY)).toBeNull();
  });

  it("keeps the stored Telnyx key when only the name changes", async () => {
    const calls = api({
      "GET /api/v1/messaging/channels": () => json({ items: [telnyxChannel] }),
      "GET /api/v1/messaging/addresses": () => json({ items: [] }),
      "GET /api/v1/messaging/channels/7/registration": () =>
        json(registration()),
      "PUT /api/v1/messaging/channels/7": () => json(telnyxChannel),
    });
    renderApp("/messaging/channels");
    fireEvent.click(await screen.findByRole("button", { name: "Edit Telnyx" }));
    expect(screen.queryByLabelText("Carrier")).toBeNull();
    fireEvent.change(await screen.findByLabelText("Name"), {
      target: { value: "Telnyx two" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save channel" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
        name: "Telnyx two",
        enabled: true,
      }),
    );
  });

  it("shows each carrier number's registration state and a checklist", async () => {
    api({
      "GET /api/v1/messaging/channels": () =>
        json({ items: [channel(), telnyxChannel] }),
      "GET /api/v1/messaging/addresses": () =>
        json({ items: [address, telnyxAddress] }),
      "GET /api/v1/messaging/channels/7/registration": () =>
        json(registration()),
    });
    renderApp("/messaging/channels");
    const numbers = await screen.findByRole("table", { name: "Numbers" });
    expect(
      await within(numbers).findByText("10DLC approved"),
    ).toBeInTheDocument();
    // A SIP trunk number has no registration to show.
    const sipRow = within(numbers).getByText("+3270123456").closest("tr")!;
    expect(within(sipRow).queryByText(/approved|pending/i)).toBeNull();
    // Only a carrier channel has the panel.
    expect(
      screen.queryByRole("button", { name: "Registration of Main" }),
    ).toBeNull();
    fireEvent.click(
      screen.getByRole("button", { name: "Registration of Telnyx" }),
    );
    const checklist = await screen.findByRole("table", { name: "Checklist" });
    expect(
      within(checklist).getByText("Register the brand (A2P 10DLC)"),
    ).toBeInTheDocument();
    expect(within(checklist).getByText("Needs attention")).toBeInTheDocument();
    const link = within(checklist).getAllByRole("link")[0];
    expect(link).toHaveAttribute(
      "href",
      "https://developers.telnyx.com/docs/messaging/10dlc/quickstart",
    );
    expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));
    expect(screen.getByRole("table", { name: "Brands" })).toBeInTheDocument();
    expect(
      screen.getByRole("table", { name: "Campaigns" }),
    ).toBeInTheDocument();
    const carrierNumbers = screen.getByRole("table", {
      name: "Carrier numbers",
    });
    expect(
      within(carrierNumbers).getByText("missing opt-in"),
    ).toBeInTheDocument();
  });

  it("says when the carrier would not answer part of the registration", async () => {
    api({
      "GET /api/v1/messaging/channels": () => json({ items: [telnyxChannel] }),
      "GET /api/v1/messaging/addresses": () => json({ items: [telnyxAddress] }),
      "GET /api/v1/messaging/channels/7/registration": () =>
        json(
          registration({
            unavailable: ["brands", "campaigns"],
            numbers: [
              {
                number: "+12125550100",
                kind: "10dlc",
                state: "unknown",
                detail: "",
                added: true,
              },
            ],
          }),
        ),
    });
    renderApp("/messaging/channels");
    const numbers = await screen.findByRole("table", { name: "Numbers" });
    expect(
      await within(numbers).findByText("10DLC unknown"),
    ).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: "Registration of Telnyx" }),
    );
    expect(
      await screen.findByText("Part of the registration cannot be read"),
    ).toBeInTheDocument();
    expect(screen.getByText(/brands, campaigns/)).toBeInTheDocument();
  });

  it("leaves the numbers unknown and explains when the carrier is unreachable", async () => {
    api({
      "GET /api/v1/messaging/channels": () => json({ items: [telnyxChannel] }),
      "GET /api/v1/messaging/addresses": () => json({ items: [telnyxAddress] }),
      "GET /api/v1/messaging/channels/7/registration": () =>
        apiError(
          502,
          "provider_unreachable",
          "the carrier could not be reached or did not answer as expected",
        ),
    });
    renderApp("/messaging/channels");
    const numbers = await screen.findByRole("table", { name: "Numbers" });
    expect(await within(numbers).findByText("Unknown")).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: "Registration of Telnyx" }),
    );
    expect(
      await screen.findByText("Could not read the registration"),
    ).toBeInTheDocument();
    expect(
      screen.getAllByText(/the carrier could not be reached/).length,
    ).toBeGreaterThan(0);
  });
});
