import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, mockApi, renderApp } from "../test/api";

const me = (role: string) => ({
  "GET /api/v1/auth/me": () => json({ username: "pat", role }),
});
const ON = {
  "GET /api/v1/ai/status": json({ enabled: true, reason: null, agents: [] }),
};

const BASE = {
  source: "assistant",
  sessionId: null,
  findingId: null,
  rationale: "Because the route is shadowed.",
  configRevision: 7,
  createdAt: "2026-10-08T10:00:00Z",
  updatedAt: "2026-10-08T10:00:00Z",
  appliedAt: null,
  dismissedAt: null,
  dismissReason: null,
  dismissText: null,
  failure: null,
};
const UPDATE = {
  ...BASE,
  id: "p1",
  title: "Raise the ring timeout",
  status: "open",
  hasDelete: false,
  actions: [
    {
      operationId: "updateRingGroup",
      pathParams: { id: "3" },
      before: { name: "Sales", timeout: 20 },
      after: { name: "Sales", timeout: 40 },
      current: { name: "Sales", timeout: 25 },
    },
    {
      operationId: "updateTrunk",
      pathParams: { id: "9" },
      before: { maxCalls: 10 },
      after: { maxCalls: 20 },
      current: { maxCalls: 10 },
    },
  ],
};
const DELETE = {
  ...BASE,
  id: "p2",
  title: "Remove the shadowed route",
  status: "open",
  hasDelete: true,
  actions: [
    {
      operationId: "deleteOutboundRoute",
      pathParams: { id: "5" },
      before: { name: "old-route", pattern: "^9" },
      after: null,
      current: { name: "old-route", pattern: "^9" },
    },
  ],
};

describe("AI proposals inbox", () => {
  it("lists by status and marks a delete proposal", async () => {
    const calls = mockApi({
      ...me("viewer"),
      ...ON,
      "GET /api/v1/ai/proposals?status=open": json({ items: [UPDATE, DELETE] }),
      "GET /api/v1/ai/proposals?status=applied": json({ items: [] }),
    });
    renderApp("/ai/proposals");
    const del = await screen.findByRole("article", {
      name: "Proposal: Remove the shadowed route",
    });
    expect(within(del).getByText("Deletes")).toBeVisible();
    const upd = screen.getByRole("article", {
      name: "Proposal: Raise the ring timeout",
    });
    expect(within(upd).queryByText("Deletes")).toBeNull();
    fireEvent.change(screen.getByLabelText("Status"), {
      target: { value: "applied" },
    });
    await waitFor(() =>
      expect(calls.some((c) => c.url.endsWith("status=applied"))).toBe(true),
    );
  });
});

describe("AI proposal detail", () => {
  it("marks fields where current differs from before", async () => {
    mockApi({
      ...me("viewer"),
      ...ON,
      "GET /api/v1/ai/proposals/p1": json(UPDATE),
    });
    renderApp("/ai/proposals/p1");
    await screen.findByRole("article", { name: "Change 1" });
    const first = screen.getByRole("article", { name: "Change 1" });
    expect(within(first).getAllByText("changed since")).toHaveLength(1);
    const second = screen.getByRole("article", { name: "Change 2" });
    expect(within(second).queryByText("changed since")).toBeNull();
  });

  it("gives a viewer no apply or dismiss", async () => {
    mockApi({
      ...me("viewer"),
      ...ON,
      "GET /api/v1/ai/proposals/p1": json(UPDATE),
    });
    renderApp("/ai/proposals/p1");
    await screen.findByRole("article", { name: "Change 1" });
    expect(screen.queryByRole("button", { name: "Apply" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Dismiss" })).toBeNull();
  });

  it("names each operation before applying", async () => {
    const calls = mockApi({
      ...me("operator"),
      ...ON,
      "GET /api/v1/ai/proposals/p1": json(UPDATE),
      "POST /api/v1/ai/proposals/p1/apply": json({
        ...UPDATE,
        status: "applied",
      }),
    });
    renderApp("/ai/proposals/p1");
    fireEvent.click(await screen.findByRole("button", { name: "Apply" }));
    const dialog = screen.getByRole("dialog");
    expect(within(dialog).getByText(/updateRingGroup \(id 3\)/)).toBeVisible();
    expect(within(dialog).getByText(/updateTrunk \(id 9\)/)).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.click(within(dialog).getByRole("button", { name: "Apply" }));
    await waitFor(() =>
      expect(calls.some((c) => c.url.endsWith("/p1/apply"))).toBe(true),
    );
    expect(await screen.findByText("applied")).toBeVisible();
  });

  it("always sends a reason when dismissing", async () => {
    const calls = mockApi({
      ...me("operator"),
      ...ON,
      "GET /api/v1/ai/proposals/p1": json(UPDATE),
      "POST /api/v1/ai/proposals/p1/dismiss": json({
        ...UPDATE,
        status: "dismissed",
      }),
    });
    renderApp("/ai/proposals/p1");
    fireEvent.click(await screen.findByRole("button", { name: "Dismiss" }));
    fireEvent.click(screen.getByRole("button", { name: "Dismiss proposal" }));
    await waitFor(() =>
      expect(calls.find((c) => c.url.endsWith("/p1/dismiss"))?.body).toEqual({
        reason: "not_needed",
      }),
    );
  });

  it("names the applied actions of a failed proposal and offers no retry", async () => {
    mockApi({
      ...me("operator"),
      ...ON,
      "GET /api/v1/ai/proposals/p1": json({
        ...UPDATE,
        status: "failed",
        failure: {
          index: 1,
          status: 409,
          code: "conflict",
          message: "trunk changed",
          applied: [0],
        },
      }),
    });
    renderApp("/ai/proposals/p1");
    expect(await screen.findByText(/Change 2 failed/)).toBeVisible();
    expect(
      screen.getByText(
        /Already applied and not undone: updateRingGroup \(id 3\)/,
      ),
    ).toBeVisible();
    expect(screen.queryByRole("button", { name: /Apply|Retry/ })).toBeNull();
  });

  it("shows an apply error from the API", async () => {
    mockApi({
      ...me("operator"),
      ...ON,
      "GET /api/v1/ai/proposals/p1": json(UPDATE),
      "POST /api/v1/ai/proposals/p1/apply": apiError(
        409,
        "proposal_not_open",
        "proposal is not open",
      ),
    });
    renderApp("/ai/proposals/p1");
    fireEvent.click(await screen.findByRole("button", { name: "Apply" }));
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", { name: "Apply" }),
    );
    expect(await screen.findByText("proposal is not open")).toBeVisible();
  });
});

describe("TestProposalDeleteUI", () => {
  it("marks a delete visually and needs an explicit confirmation to apply", async () => {
    const calls = mockApi({
      ...me("operator"),
      ...ON,
      "GET /api/v1/ai/proposals/p2": json(DELETE),
      "POST /api/v1/ai/proposals/p2/apply": json({
        ...DELETE,
        status: "applied",
      }),
    });
    renderApp("/ai/proposals/p2");
    expect(
      await screen.findByText("This proposal deletes configuration"),
    ).toBeVisible();
    const change = screen.getByRole("article", { name: "Change 1" });
    expect(within(change).getByText("Delete")).toBeVisible();
    // What disappears is in the diff.
    expect(within(change).getAllByText("removed").length).toBeGreaterThan(0);
    expect(within(change).getAllByText("old-route").length).toBeGreaterThan(0);

    fireEvent.click(screen.getByRole("button", { name: "Apply" }));
    const dialog = screen.getByRole("dialog");
    expect(
      within(dialog).getByText(/Delete: deleteOutboundRoute \(id 5\)/),
    ).toBeVisible();
    const confirm = within(dialog).getByRole("button", {
      name: "Apply and delete",
    });
    expect(confirm).toBeDisabled();
    fireEvent.click(confirm);
    expect(calls.some((c) => c.method === "POST")).toBe(false);

    fireEvent.click(within(dialog).getByLabelText(/I understand this deletes/));
    expect(confirm).toBeEnabled();
    fireEvent.click(confirm);
    await waitFor(() =>
      expect(calls.some((c) => c.url.endsWith("/p2/apply"))).toBe(true),
    );
  });
});
