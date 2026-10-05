import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../test/api";

const BASE = {
  id: 77,
  correlationId: "corr-77",
  sipCallId: "abc@host",
  source: "101",
  destination: "0501234567",
  startTime: "2026-10-01T10:00:00Z",
  endTime: "2026-10-01T10:00:05Z",
  durationMs: 5000,
  billableMs: 0,
  sipNode: "hello-sip-1",
  mediaMode: "direct",
  finalStatus: 503,
  terminationSide: "system",
  failureReason: "all trunks failed",
  direction: "outbound",
  originalDestination: "0501234567",
  rewrittenDestination: "+971501234567",
  route: "UAE Mobile",
  trunk: "carrier-backup",
};

const STEPS = [
  'Internal extension lookup "0501234567" -> no match',
  'Route "UAE Mobile" matched (regex ^05[0-9]{8}$)',
  "Rewrite 0501234567 -> +971501234567",
  "carrier-primary (10.0.0.5:5060) -> 503 Service Unavailable",
  "Failover permitted for 503",
  "carrier-backup -> 503 Service Unavailable",
];

describe("CallDetail", () => {
  it("renders the trace steps in step order and explains a failed call", async () => {
    // Steps arrive shuffled; the page must order them by n.
    const trace = STEPS.map((text, i) => ({ n: i + 1, text }));
    const shuffled = [
      trace[3],
      trace[0],
      trace[5],
      trace[1],
      trace[4],
      trace[2],
    ];
    mockApi({
      ...ME,
      "GET /api/v1/cdrs/77": () =>
        json({
          ...BASE,
          trace: shuffled,
          explanation: "carrier-backup -> 503 Service Unavailable",
        }),
    });
    renderApp("/history/77");

    const list = await screen.findByRole("list", { name: "Routing trace" });
    const items = within(list).getAllByRole("listitem");
    // Each step reads its number, then its sentence.
    expect(items.map((li) => li.textContent)).toEqual(
      STEPS.map((text, i) => `${i + 1}${text}`),
    );
    // The last step of a failed call is marked as the failure; a skipped
    // step and a failover are marked as such.
    expect(items[5]!.querySelector(".icon-x")).not.toBeNull();
    expect(items[0]!.querySelector(".icon-minus")).not.toBeNull();
    expect(items[3]!.querySelector(".icon-triangle-alert")).not.toBeNull();
    expect(items[1]!.querySelector(".icon-check")).not.toBeNull();

    const why = screen.getByRole("alert");
    expect(why).toHaveTextContent("Why it failed");
    expect(why).toHaveTextContent("carrier-backup -> 503 Service Unavailable");
    expect(screen.getByText("0501234567 → +971501234567")).toBeVisible();
    expect(screen.getByText("UAE Mobile")).toBeVisible();
    expect(screen.getByText("carrier-backup")).toBeVisible();
    expect(screen.getByText(/^Outbound ·/)).toBeVisible();
    expect(screen.getByText("503 (failed)")).toBeVisible();

    // Timeline: never rang or answered, so both are unknown.
    const timeline = screen.getByLabelText("Timeline");
    const values = within(timeline)
      .getAllByRole("definition")
      .map((d) => d.textContent);
    expect(values[1]).toBe("—");
    expect(values[2]).toBe("—");

    // The actions open the SIP trace and the route test for this call.
    expect(screen.getByRole("link", { name: "SIP trace" })).toHaveAttribute(
      "href",
      "/diagnostics?call=corr-77",
    );
    expect(
      screen.getByRole("link", { name: "Re-test this number" }),
    ).toHaveAttribute("href", "/routes/test?from=101&number=0501234567");
  });

  it("shows the failover note on an answered call", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/cdrs/79": () =>
        json({
          ...BASE,
          id: 79,
          finalStatus: 200,
          answerTime: "2026-10-01T10:00:02Z",
          failureReason: "",
          trace: [],
          explanation: "",
          note: "carrier-primary (10.0.0.5:5060) -> 503 Service Unavailable; failed over to carrier-backup.",
        }),
    });
    renderApp("/history/79");

    const note = (await screen.findByText("Note")).closest(".az-alert");
    expect(note).toHaveAttribute("role", "status");
    expect(note).toHaveTextContent("failed over to carrier-backup.");
    expect(screen.queryByText("Why it failed")).toBeNull();
    expect(screen.getByText("No trace was recorded.")).toBeVisible();
    expect(screen.getByText("after 0:05")).toBeVisible();
  });

  it("explains why a call could not load", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/cdrs/404": () =>
        json({ error: { code: "not_found", message: "cdr: not found" } }, 404),
    });
    renderApp("/history/404");

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not load this call");
    expect(alert).toHaveTextContent("cdr: not found");
  });

  it("shows no failure explanation for an answered call", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/cdrs/78": () =>
        json({
          ...BASE,
          id: 78,
          finalStatus: 200,
          answerTime: "2026-10-01T10:00:02Z",
          failureReason: "",
          trace: [
            { n: 1, text: "carrier-primary -> 200 OK" },
            { n: 2, text: "Call established" },
          ],
        }),
    });
    renderApp("/history/78");

    expect(await screen.findByText("Call established")).toBeVisible();
    expect(screen.queryByRole("alert")).toBeNull();
    expect(screen.queryByText(/Why it failed/)).toBeNull();
    expect(screen.queryByText("Note")).toBeNull();
  });

  it("is reached from a Call History row", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/cdrs?limit=50": () => json({ items: [BASE], next: "" }),
      "GET /api/v1/cdrs/77": () =>
        json({ ...BASE, trace: [], explanation: "" }),
    });
    renderApp("/history");

    const link = await screen.findByRole("link", { name: /details$/ });
    expect(link).toHaveAttribute("href", "/history/77");
    link.click();
    expect(
      await screen.findByRole("heading", { level: 1, name: "Call 77" }),
    ).toBeVisible();
  });
});
