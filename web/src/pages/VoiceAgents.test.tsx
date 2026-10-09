import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../test/api";

const AGENT = {
  id: 5,
  name: "support",
  description: "Answers support questions",
  enabled: true,
  sipUser: "va-3f2a9c01",
  extension: "3000",
  revision: 3,
};

const STATUS = {
  revision: 7,
  lastSeenAt: new Date(Date.now() - 10_000).toISOString(),
  version: "talking-agent 0.4",
  healthy: true,
  agents: [{ name: "support", revision: 3, state: "loaded" }],
};

function setup(role: string, extra = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/auth/me": () => json({ username: "pat", role }),
    "GET /api/v1/voice/agents": () => json({ items: [AGENT] }),
    "GET /api/v1/voice/status": () => json(STATUS),
    ...extra,
  });
}

describe("VoiceAgents", () => {
  it("lists the agents with their runtime state (spec S-25)", async () => {
    setup("admin");
    renderApp("/voice/agents");

    const row = await screen.findByRole("row", { name: /support/ });
    expect(row).toHaveTextContent("va-3f2a9c01");
    expect(row).toHaveTextContent("3000");
    expect(row).toHaveTextContent("Live");
  });

  it("marks a disabled agent and an agent the runtime has not loaded", async () => {
    setup("viewer", {
      "GET /api/v1/voice/agents": () =>
        json({
          items: [
            AGENT,
            {
              ...AGENT,
              id: 6,
              name: "afterhours",
              description: "Takes the calls after dark",
              enabled: false,
              revision: 9,
            },
          ],
        }),
      "GET /api/v1/voice/status": () =>
        json({
          ...STATUS,
          agents: [{ name: "support", revision: 2, state: "loaded" }],
        }),
    });
    renderApp("/voice/agents");

    expect(
      await screen.findByRole("row", { name: /support/ }),
    ).toHaveTextContent("Stale");
    expect(screen.getByRole("row", { name: /afterhours/ })).toHaveTextContent(
      "Disabled",
    );
  });

  it("hides the edit controls from a viewer (spec S-31)", async () => {
    setup("viewer");
    renderApp("/voice/agents");

    await screen.findByRole("row", { name: /support/ });
    expect(screen.queryByText("New voice agent")).toBeNull();
    expect(
      screen.queryByRole("button", { name: /Delete voice agent/ }),
    ).toBeNull();
  });

  it("creates an agent from the wizard without JSON (spec S-25)", async () => {
    const calls = setup("admin", {
      "GET /api/v1/voice/agents": [
        () => json({ items: [] }),
        () => json({ items: [AGENT] }),
      ],
      "POST /api/v1/voice/agents": () => json(AGENT, 201),
    });
    renderApp("/voice/agents");

    fireEvent.click(
      await screen.findByRole("button", { name: "New voice agent" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "New voice agent",
    });
    fireEvent.change(within(dialog).getByLabelText("Name"), {
      target: { value: "support" },
    });
    fireEvent.change(within(dialog).getByLabelText("Extension (optional)"), {
      target: { value: "3000" },
    });
    fireEvent.change(within(dialog).getByLabelText("Greeting"), {
      target: { value: "Hello, this is an automated assistant." },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create agent" }),
    );

    await screen.findByRole("row", { name: /support/ });
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url).toBe("/api/v1/voice/agents");
    const body = post?.body as Record<string, unknown>;
    expect(body.name).toBe("support");
    expect(body.extension).toBe("3000");
    // The persona starts from the template; the operator never writes JSON.
    expect(String(body.prompt)).toContain("assistant");
    expect(body.greeting).toContain("automated assistant");
  });

  it("refuses an invalid name and a bad extension before calling the API", async () => {
    const calls = setup("admin");
    renderApp("/voice/agents");

    fireEvent.click(
      await screen.findByRole("button", { name: "New voice agent" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "New voice agent",
    });
    fireEvent.change(within(dialog).getByLabelText("Name"), {
      target: { value: "not valid!" },
    });
    fireEvent.change(within(dialog).getByLabelText("Extension (optional)"), {
      target: { value: "30a0" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create agent" }),
    );

    expect(await within(dialog).findByText(/Use 1-64 of A-Z/)).toBeVisible();
    expect(within(dialog).getByText(/Use 2 to 10 digits/)).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("deletes an agent after confirming", async () => {
    const calls = setup("admin", {
      "GET /api/v1/voice/agents": [
        () => json({ items: [AGENT] }),
        () => json({ items: [] }),
      ],
      "DELETE /api/v1/voice/agents/5": () =>
        new Response(null, { status: 204 }),
    });
    renderApp("/voice/agents");

    const row = await screen.findByRole("row", { name: /support/ });
    fireEvent.click(
      within(row).getByRole("button", { name: "Delete voice agent support" }),
    );
    const confirm = await screen.findByRole("dialog", {
      name: "Delete voice agent support?",
    });
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Delete agent" }),
    );
    await screen.findByText("No voice agents yet");
    expect(calls).toContainEqual({
      method: "DELETE",
      url: "/api/v1/voice/agents/5",
      body: undefined,
    });
  });

  it("links an agent to its editor", async () => {
    setup("viewer");
    renderApp("/voice/agents");

    const link = await screen.findByRole("link", { name: /support/ });
    expect(link).toHaveAttribute("href", "/voice/agents/5");
  });
});
