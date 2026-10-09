import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, ME, mockApi, renderApp } from "../test/api";

const DETAIL = {
  id: 5,
  name: "support",
  description: "Answers support questions",
  enabled: true,
  sipUser: "va-3f2a9c01",
  extension: "3000",
  revision: 3,
  prompt: "You are the phone assistant.",
  greeting: "",
  language: "en-US",
  maxCallSeconds: 600,
  maxConcurrent: 4,
  maxToolCalls: 20,
  idleTimeoutSeconds: 20,
  recordTranscript: false,
  transcriptRetentionDays: 30,
  callerVerification: "none",
  callerAllowlist: [],
  tools: {
    servers: [
      {
        serverId: 9,
        enabled: true,
        tools: [],
      },
    ],
  },
};

const SERVER = {
  id: 9,
  name: "crm",
  url: "https://crm.internal/mcp",
  auth: "bearer",
  timeoutMs: 10000,
  enabled: true,
  credentialSet: true,
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
    "GET /api/v1/voice/agents/5": () => json(DETAIL),
    "GET /api/v1/voice/agents/5/versions": () => json({ items: [] }),
    "GET /api/v1/voice/agents/5/calls": () => json({ items: [] }),
    "GET /api/v1/voice/mcp-servers": () => json({ items: [SERVER] }),
    "POST /api/v1/voice/mcp-servers/9/discover": () =>
      json({
        tools: [
          { name: "lookup_customer", description: "Finds a customer record" },
          {
            name: "create_ticket",
            description: "Opens a ticket",
          },
        ],
      }),
    "GET /api/v1/voice/status": () => json(STATUS),
    ...extra,
  });
}

afterEach(() => {
  vi.useRealTimers();
});

describe("VoiceAgentEdit", () => {
  it("shows the persona with a character counter and warns about an empty greeting (S-24, S-26)", async () => {
    setup("operator");
    renderApp("/voice/agents/5");

    await screen.findByRole("tab", { name: "Persona" });
    const prompt = await screen.findByLabelText("System prompt");
    expect(prompt).toHaveValue("You are the phone assistant.");
    expect(screen.getByText(/of 8000 characters/)).toBeInTheDocument();
    expect(screen.getByText("No greeting")).toBeInTheDocument();
    expect(
      screen.getByText(/only if the greeting says so/),
    ).toBeInTheDocument();
  });

  it("saves the persona with a PUT that carries every field (S-26)", async () => {
    const calls = setup("operator", {
      "PUT /api/v1/voice/agents/5": () => json(DETAIL),
    });
    renderApp("/voice/agents/5");

    const prompt = await screen.findByLabelText("System prompt");
    fireEvent.change(prompt, {
      target: { value: "You are the front desk. Be brief." },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save persona" }));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.url).toBe("/api/v1/voice/agents/5");
    const body = put?.body as Record<string, unknown>;
    expect(body.prompt).toBe("You are the front desk. Be brief.");
    expect(body.maxCallSeconds).toBe(600);
    expect(body.callerVerification).toBe("none");
  });

  it("shows a viewer the persona read-only (S-26, S-31)", async () => {
    setup("viewer");
    renderApp("/voice/agents/5");

    await screen.findByLabelText("System prompt");
    expect(screen.queryByRole("button", { name: "Save persona" })).toBeNull();
  });

  it("warns when an attached server has an empty allowlist and saves ticked tools (S-6)", async () => {
    const calls = setup("operator", {
      "PUT /api/v1/voice/agents/5/tools": () => json({ servers: [] }),
    });
    renderApp("/voice/agents/5");

    fireEvent.click(await screen.findByRole("tab", { name: /Tools/ }));
    expect(await screen.findByText("No tool ticked")).toBeInTheDocument();
    expect(
      screen.getByText(/nothing is available until you tick it/),
    ).toBeInTheDocument();

    const list = screen.getByRole("list", { name: /Tools of crm/ });
    fireEvent.click(
      await within(list).findByRole("checkbox", { name: "lookup_customer" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Save tools" }));

    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "PUT" && c.url === "/api/v1/voice/agents/5/tools",
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url === "/api/v1/voice/agents/5/tools",
    );
    const servers = (put?.body as { servers: { tools: unknown[] }[] }).servers;
    expect(servers[0]?.tools).toEqual([
      { name: "lookup_customer", confirm: true, write: true },
    ]);
  });

  it("warns when a write tool is ticked without caller verification (S-36)", async () => {
    setup("operator", {
      "GET /api/v1/voice/agents/5": () =>
        json({
          ...DETAIL,
          tools: {
            servers: [
              {
                serverId: 9,
                enabled: true,
                tools: [{ name: "create_ticket", confirm: true, write: true }],
              },
            ],
          },
        }),
    });
    renderApp("/voice/agents/5");

    fireEvent.click(await screen.findByRole("tab", { name: /Limits/ }));
    expect(
      await screen.findByText("A write tool is ticked"),
    ).toBeInTheDocument();
    expect(screen.getByText(/voice_verification_required/)).toBeInTheDocument();
  });

  it("lists the versions and restores one as a new revision (S-4)", async () => {
    const calls = setup("operator", {
      "GET /api/v1/voice/agents/5/versions": () =>
        json({
          items: [
            {
              revision: 1,
              actor: "pat",
              createdAt: "2026-10-01T09:00:00Z",
              persona: { prompt: "First version." },
            },
          ],
        }),
      "POST /api/v1/voice/agents/5/versions/1/restore": () => json(DETAIL),
    });
    renderApp("/voice/agents/5");

    fireEvent.click(await screen.findByRole("tab", { name: "History" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Restore revision 1" }),
    );
    const confirm = await screen.findByRole("dialog", {
      name: "Restore revision 1?",
    });
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.click(within(confirm).getByRole("button", { name: "Restore" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" &&
            c.url === "/api/v1/voice/agents/5/versions/1/restore",
        ),
      ).toBe(true),
    );
  });

  it("shows the test extension, the runtime state, and follows the next call (S-29)", async () => {
    vi.useFakeTimers();
    const CALL = {
      correlationId: "abc-123",
      agentName: "support",
      startTime: new Date().toISOString(),
      finalStatus: 200,
      outcome: "completed",
      summary: "Caller asked about opening hours; the agent answered.",
      toolCalls: 1,
      tokensIn: 100,
      tokensOut: 40,
    };
    setup("viewer", {
      "GET /api/v1/voice/agents/5/calls": [
        () => json({ items: [] }),
        () => json({ items: [CALL] }),
      ],
    });
    renderApp("/voice/agents/5");

    fireEvent.click(await screen.findByRole("tab", { name: "Test" }));
    expect(await screen.findByLabelText("Test extension")).toHaveValue("3000");
    expect(await screen.findByText(/running revision 7/)).toBeInTheDocument();
    expect(screen.getByText(/Waiting for the next call/)).toBeInTheDocument();

    await vi.advanceTimersByTimeAsync(5000);
    expect(
      await screen.findByText(/Caller asked about opening hours/),
    ).toBeInTheDocument();
    expect(screen.getByText("completed")).toBeInTheDocument();
  });
});
