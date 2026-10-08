import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, mockApi, renderApp } from "../test/api";

const me = (role: string) => ({
  "GET /api/v1/auth/me": () => json({ username: "pat", role }),
});
const ON = {
  "GET /api/v1/ai/status": json({ enabled: true, reason: null, agents: [] }),
};

const FINDING = {
  id: "f1",
  candidateId: "auth_bruteforce:10.0.0.9",
  type: "auth_bruteforce",
  subject: "10.0.0.9",
  severity: "critical",
  status: "open",
  title: "REGISTER flood from 10.0.0.9",
  evidence: { attempts: 400, window: "5m" },
  explanation: {
    summary: "Many failed registers <b>now</b>",
    likelyCause: "A scanner",
    nextStep: "Block the address",
    relatedIds: [],
  },
  explained: true,
  rank: 1,
  firstSeen: "2026-10-08T09:00:00Z",
  lastSeen: "2026-10-08T10:00:00Z",
  occurrences: 3,
  dismissReason: null,
  proposalId: null,
};
const UNEXPLAINED = {
  ...FINDING,
  id: "f2",
  title: "Quiet trunk",
  explanation: null,
  explained: false,
  severity: "info",
};

describe("AI findings", () => {
  it("lists evidence and explanations as plain text, and says 'Not explained'", async () => {
    mockApi({
      ...me("viewer"),
      ...ON,
      "GET /api/v1/ai/findings?status=open": json({
        items: [FINDING, UNEXPLAINED],
        healthScore: 72,
      }),
    });
    const { container } = renderApp("/ai/findings");
    expect(
      await screen.findByText("REGISTER flood from 10.0.0.9"),
    ).toBeVisible();
    const ev = screen.getAllByRole("table", { name: "Evidence" })[0]!;
    expect(within(ev).getByText("attempts")).toBeVisible();
    expect(within(ev).getByText("400")).toBeVisible();
    expect(screen.getByText("Many failed registers <b>now</b>")).toBeVisible();
    expect(container.querySelector("main .aix-plain b")).toBeNull();
    expect(screen.getByText("Not explained")).toBeVisible();
    expect(screen.getByText(/Health score 72/)).toBeVisible();
  });

  it("filters by status and severity through the query", async () => {
    const calls = mockApi({
      ...me("viewer"),
      ...ON,
      "GET /api/v1/ai/findings?status=open": json({
        items: [],
        healthScore: 100,
      }),
      "GET /api/v1/ai/findings?status=dismissed&severity=critical": json({
        items: [],
        healthScore: 100,
      }),
    });
    renderApp("/ai/findings");
    await screen.findByText("No findings");
    fireEvent.change(screen.getByLabelText("Status"), {
      target: { value: "dismissed" },
    });
    fireEvent.change(screen.getByLabelText("Severity"), {
      target: { value: "critical" },
    });
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.url === "/api/v1/ai/findings?status=dismissed&severity=critical",
        ),
      ).toBe(true),
    );
  });

  it("shows a viewer no acknowledge or dismiss", async () => {
    mockApi({
      ...me("viewer"),
      ...ON,
      "GET /api/v1/ai/findings?status=open": json({
        items: [FINDING],
        healthScore: 90,
      }),
    });
    renderApp("/ai/findings");
    await screen.findByText("REGISTER flood from 10.0.0.9");
    expect(screen.queryByRole("button", { name: "Acknowledge" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Dismiss" })).toBeNull();
  });

  it("lets an operator acknowledge, and dismiss only with a reason", async () => {
    const calls = mockApi({
      ...me("operator"),
      ...ON,
      "GET /api/v1/ai/findings?status=open": json({
        items: [FINDING],
        healthScore: 90,
      }),
      "POST /api/v1/ai/findings/f1/acknowledge": json({
        ...FINDING,
        status: "acknowledged",
      }),
      "POST /api/v1/ai/findings/f1/dismiss": json({
        ...FINDING,
        status: "dismissed",
      }),
    });
    renderApp("/ai/findings");
    fireEvent.click(await screen.findByRole("button", { name: "Acknowledge" }));
    await waitFor(() =>
      expect(calls.some((c) => c.url.endsWith("/f1/acknowledge"))).toBe(true),
    );

    fireEvent.click(await screen.findByRole("button", { name: "Dismiss" }));
    const confirm = screen.getByRole("button", { name: "Dismiss finding" });
    expect(confirm).toBeDisabled();
    fireEvent.click(confirm);
    expect(calls.some((c) => c.url.endsWith("/f1/dismiss"))).toBe(false);
    fireEvent.change(screen.getByLabelText("Reason"), {
      target: { value: "known scanner" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Dismiss finding" }));
    await waitFor(() =>
      expect(calls.find((c) => c.url.endsWith("/f1/dismiss"))?.body).toEqual({
        reason: "known scanner",
      }),
    );
  });
});
