import {
  act,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, mockApi, renderApp } from "../test/api";

const me = (role: string) => ({
  "GET /api/v1/auth/me": () => json({ username: "pat", role }),
});

const STATUS = {
  enabled: true,
  reason: null,
  provider: "openai",
  model: "qwen3",
  endpointHost: "fastllm.lab",
  endpointPrivate: true,
  structuredOutput: "json_schema",
  tokensToday: 1200,
  dailyTokenBudget: 2000000,
  backgroundTokenLimit: 1600000,
  concurrencyInUse: 1,
  maxConcurrency: 2,
  agents: [
    {
      name: "aiops",
      intervalSeconds: 60,
      lastRun: {
        startedAt: "2026-10-08T10:00:00Z",
        finishedAt: null,
        outcome: "ok",
        tokens: 5,
        error: null,
      },
      nextDueAt: "2026-10-08T10:01:00Z",
      runRequested: false,
    },
  ],
  recentErrors: [{ code: "provider_error", at: "2026-10-08T09:00:00Z" }],
  openFindings: { info: 1, warning: 2, critical: 3 },
  healthScore: 80,
  openProposals: 4,
};

describe("AI status page and navigation", () => {
  it("shows the endpoint host, budget and agents; an operator can run now; no URL path or key", async () => {
    const calls = mockApi({
      ...me("operator"),
      "GET /api/v1/ai/status": json(STATUS),
      "POST /api/v1/ai/agents/aiops/run": json(
        { ...STATUS.agents[0], runRequested: true },
        202,
      ),
    });
    renderApp("/ai/status");
    expect(await screen.findByText("fastllm.lab")).toBeVisible();
    expect(screen.getByText(/1,200 of 2,000,000/)).toBeVisible();
    expect(screen.getByText("provider_error")).toBeVisible();
    expect(document.body.textContent).not.toMatch(
      /https?:\/\/|api[-_ ]?key|\/v1/i,
    );
    fireEvent.click(screen.getByRole("button", { name: "Run now" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "POST" && c.url === "/api/v1/ai/agents/aiops/run",
        ),
      ).toBe(true),
    );
  });

  it("gives a viewer no Run now button", async () => {
    mockApi({ ...me("viewer"), "GET /api/v1/ai/status": json(STATUS) });
    renderApp("/ai/status");
    await screen.findByText("fastllm.lab");
    expect(screen.queryByRole("button", { name: "Run now" })).toBeNull();
  });

  it("shows the reason while AI is off and hides the other AI nav items", async () => {
    mockApi({
      ...me("operator"),
      "GET /api/v1/ai/status": json({
        ...STATUS,
        enabled: false,
        reason: "not_configured",
      }),
    });
    renderApp("/ai/status");
    expect(
      await screen.findByText(/No model endpoint is configured/),
    ).toBeVisible();
    const nav = await screen.findByRole("navigation", { name: "Primary" });
    expect(within(nav).queryByRole("link", { name: /Assistant/ })).toBeNull();
    expect(within(nav).queryByRole("link", { name: /Findings/ })).toBeNull();
    expect(within(nav).getByRole("link", { name: /AI status/ })).toBeVisible();
  });

  it("shows the AI group when enabled, without the assistant for a viewer", async () => {
    mockApi({ ...me("viewer"), "GET /api/v1/ai/status": json(STATUS) });
    renderApp("/ai/status");
    const nav = await screen.findByRole("navigation", { name: "Primary" });
    await waitFor(() =>
      expect(within(nav).getByRole("link", { name: /Findings/ })).toBeVisible(),
    );
    expect(within(nav).getByRole("link", { name: /Proposals/ })).toBeVisible();
    expect(within(nav).queryByRole("link", { name: /Assistant/ })).toBeNull();
    await act(async () => {});
  });
});
