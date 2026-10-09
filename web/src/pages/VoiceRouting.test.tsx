import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../test/api";

const TRUNK = {
  id: 2,
  name: "lab-trunk",
  mode: "ip",
  enabled: true,
  maxCalls: 5,
};

const AGENTS = {
  items: [
    { id: 5, name: "support", enabled: true, sipUser: "va-3f2a9c01" },
    { id: 6, name: "afterhours", enabled: false, sipUser: "va-0000ffff" },
  ],
};

function setup(extra = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/auth/me": () => json({ username: "pat", role: "operator" }),
    "GET /api/v1/voice/agents": () => json(AGENTS),
    ...extra,
  });
}

describe("VoiceRouting", () => {
  it("offers Voice agent as an inbound route destination with disabled agents greyed (S-10, S-28)", async () => {
    setup({
      "GET /api/v1/trunks": () => json({ items: [TRUNK] }),
      "GET /api/v1/routes/outbound": () => json({ items: [] }),
      "GET /api/v1/routes/inbound": () => json({ items: [] }),
      "GET /api/v1/extensions": () => json({ items: [] }),
      "POST /api/v1/routes/inbound": () =>
        json(
          {
            id: 1,
            position: 1,
            name: "AI line",
            didKind: "exact",
            did: "97142000",
            trunkId: null,
            sipDomain: "",
            headerName: "",
            headerRegex: "",
            schedule: null,
            callerIdTransform: {},
            destinationKind: "voice_agent",
            destination: "support",
            enabled: true,
          },
          201,
        ),
    });
    renderApp("/routes?tab=inbound");

    fireEvent.click(await screen.findByRole("button", { name: "New route" }));
    const dialog = await screen.findByRole("dialog", {
      name: "New inbound route",
    });
    fireEvent.change(within(dialog).getByLabelText("DID"), {
      target: { value: "97142000" },
    });
    const kind = within(dialog).getByLabelText("Send to");
    const option = within(kind).getByRole("option", {
      name: "Voice agent",
    }) as HTMLOptionElement;
    expect(option).toBeDefined();

    fireEvent.change(kind, { target: { value: "voice_agent" } });
    const agent = within(dialog).getByLabelText("Voice agent", {
      selector: "select",
    });
    const support = within(agent).getByRole("option", {
      name: "support",
    }) as HTMLOptionElement;
    expect(support.disabled).toBe(false);
    const off = within(agent).getByRole("option", {
      name: "afterhours (disabled)",
    }) as HTMLOptionElement;
    expect(off.disabled).toBe(true);

    fireEvent.change(agent, { target: { value: "support" } });
    fireEvent.change(within(dialog).getByLabelText("Name"), {
      target: { value: "AI line" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create route" }),
    );

    await waitFor(() =>
      expect(screen.getByRole("row", { name: /AI line/ })).toBeInTheDocument(),
    );
  });

  it("sends the chosen agent name as the route destination", async () => {
    const calls = setup({
      "GET /api/v1/trunks": () => json({ items: [TRUNK] }),
      "GET /api/v1/routes/outbound": () => json({ items: [] }),
      "GET /api/v1/routes/inbound": () => json({ items: [] }),
      "GET /api/v1/extensions": () => json({ items: [] }),
      "POST /api/v1/routes/inbound": () =>
        json(
          {
            id: 1,
            position: 1,
            name: "AI line",
            didKind: "exact",
            did: "97142000",
            trunkId: null,
            sipDomain: "",
            headerName: "",
            headerRegex: "",
            schedule: null,
            callerIdTransform: {},
            destinationKind: "voice_agent",
            destination: "support",
            enabled: true,
          },
          201,
        ),
    });
    renderApp("/routes?tab=inbound");

    fireEvent.click(await screen.findByRole("button", { name: "New route" }));
    const dialog = await screen.findByRole("dialog", {
      name: "New inbound route",
    });
    fireEvent.change(within(dialog).getByLabelText("Name"), {
      target: { value: "AI line" },
    });
    fireEvent.change(within(dialog).getByLabelText("DID"), {
      target: { value: "97142000" },
    });
    fireEvent.change(within(dialog).getByLabelText("Send to"), {
      target: { value: "voice_agent" },
    });
    fireEvent.change(
      within(dialog).getByLabelText("Voice agent", { selector: "select" }),
      { target: { value: "support" } },
    );
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create route" }),
    );

    const post = await waitFor(() => {
      const p = calls.find(
        (c) => c.method === "POST" && c.url === "/api/v1/routes/inbound",
      );
      expect(p).toBeDefined();
      return p;
    });
    expect(post?.body).toMatchObject({
      destinationKind: "voice_agent",
      destination: "support",
    });
  });

  it("offers a voice agent as a sequential ring group member, refused elsewhere (S-12)", async () => {
    setup({
      "GET /api/v1/ring-groups": () => json({ items: [] }),
      "GET /api/v1/extensions": () =>
        json({ items: [{ id: 1, number: "101", name: "Alice" }] }),
    });
    renderApp("/ring-groups");

    const [create] = await screen.findAllByRole("button", {
      name: "New ring group",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    const dialog = await screen.findByRole("dialog", {
      name: "New ring group",
    });
    // ring-all is the default strategy: the agent option is greyed with the reason.
    const pick = within(dialog).getByLabelText("Add member");
    const agentOption = within(pick).getByRole("option", {
      name: "Voice agent support (sequential only)",
    }) as HTMLOptionElement;
    expect(agentOption.disabled).toBe(true);

    fireEvent.change(within(dialog).getByLabelText("Strategy"), {
      target: { value: "sequential" },
    });
    const enabledOption = within(pick).getByRole("option", {
      name: "Voice agent support",
    }) as HTMLOptionElement;
    expect(enabledOption.disabled).toBe(false);

    fireEvent.change(pick, { target: { value: "agent:5" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }));
    expect(await within(dialog).findByText("support")).toBeInTheDocument();
  });

  it("offers a voice agent as the failure target (S-12)", async () => {
    const calls = setup({
      "GET /api/v1/ring-groups": () => json({ items: [] }),
      "GET /api/v1/extensions": () =>
        json({ items: [{ id: 1, number: "101", name: "Alice" }] }),
      "POST /api/v1/ring-groups": () =>
        json(
          {
            id: 3,
            name: "Front desk",
            strategy: "ring-all",
            hunt: false,
            ringTimeout: 30,
            memberDelay: 0,
            ignoreDnd: false,
            failureKind: "voice_agent",
            failureTarget: "support",
            members: [{ extensionId: 1, position: 1, weight: 1, delay: 0 }],
          },
          201,
        ),
    });
    renderApp("/ring-groups");

    const [create] = await screen.findAllByRole("button", {
      name: "New ring group",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    const dialog = await screen.findByRole("dialog", {
      name: "New ring group",
    });
    // Migration 00010 dropped the announcement failure kind: the picker no
    // longer offers it, so an announcement fallback cannot be configured.
    const kind = within(dialog).getByLabelText("When no one answers", {
      selector: "select",
    }) as HTMLSelectElement;
    expect([...kind.options].map((o) => o.value)).toEqual([
      "none",
      "voicemail",
      "external",
      "voice_agent",
    ]);
    fireEvent.change(within(dialog).getByLabelText("Name"), {
      target: { value: "Front desk" },
    });
    fireEvent.change(within(dialog).getByLabelText("Add member"), {
      target: { value: "ext:1" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add" }));
    fireEvent.change(within(dialog).getByLabelText("When no one answers"), {
      target: { value: "voice_agent" },
    });
    fireEvent.change(
      within(dialog).getByLabelText("Voice agent", { selector: "select" }),
      { target: { value: "support" } },
    );
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create group" }),
    );

    const post = await waitFor(() => {
      const p = calls.find(
        (c) => c.method === "POST" && c.url === "/api/v1/ring-groups",
      );
      expect(p).toBeDefined();
      return p;
    });
    expect(post?.body).toMatchObject({
      failureKind: "voice_agent",
      failureTarget: "support",
    });
  });

  it("names the voice agent in a routing test trace (S-10, S-28)", async () => {
    setup({
      "GET /api/v1/extensions": () =>
        json({ items: [{ id: 1, number: "101", name: "Alice" }] }),
      "GET /api/v1/trunks": () => json({ items: [] }),
      "POST /api/v1/routing/test": () =>
        json({
          decision: {
            kind: "internal",
            extension: "3000",
            sipUri: "sip:va-3f2a9c01@192.168.10.142:5060",
            number: "3000",
            callerId: "101",
            route: "",
            trunks: [],
            emergency: false,
            rejectCode: 0,
            reason: "",
          },
          trace: [
            { n: 1, text: "Extension 3000 resolves to voice agent support" },
            {
              n: 2,
              text: 'Destination: voice agent "support" (sip:va-3f2a9c01@192.168.10.142:5060)',
            },
          ],
        }),
    });
    renderApp("/routes/test?from=101&number=3000");

    expect(
      await screen.findByText(/Destination: voice agent "support"/),
    ).toBeInTheDocument();
  });
});
