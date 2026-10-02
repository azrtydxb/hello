import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../test/api";

describe("RouteTest", () => {
  it("tests a number from an extension and shows the decision and trace in order", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/extensions": () =>
        json({
          items: [{ id: 1, number: "101", name: "Desk", externalNumber: "" }],
        }),
      "GET /api/v1/trunks": () =>
        json({ items: [{ id: 4, name: "carrier-primary" }] }),
      "POST /api/v1/routing/test": () =>
        json({
          decision: {
            kind: "outbound",
            number: "+971501234567",
            callerId: "+97142000101",
            route: "UAE Mobile",
            trunks: ["carrier-primary", "carrier-backup"],
          },
          trace: [
            { n: 2, text: 'Route "UAE Mobile" matched (regex ^05[0-9]{8}$)' },
            {
              n: 1,
              text: 'Internal extension lookup "0501234567" -> no match',
            },
            { n: 3, text: "Rewrite 0501234567 -> +971501234567" },
          ],
        }),
    });
    renderApp("/routes/test");

    fireEvent.change(await screen.findByLabelText("Extension"), {
      target: { value: "101" },
    });
    fireEvent.change(screen.getByLabelText("Dialled number"), {
      target: { value: "0501234567" },
    });
    fireEvent.change(screen.getByLabelText("At"), {
      target: { value: "2026-10-04T09:30" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Test" }));

    expect(await screen.findByText("Outbound via trunks")).toBeVisible();
    const body = calls.find((c) => c.method === "POST")?.body as Record<
      string,
      unknown
    >;
    expect(body).toMatchObject({ from: "101", number: "0501234567" });
    expect(body.at).toBe(new Date("2026-10-04T09:30").toISOString());
    expect(body).not.toHaveProperty("callerId");

    expect(screen.getByText("UAE Mobile")).toBeVisible();
    expect(screen.getByText("+971501234567")).toBeVisible();
    const tryOrder = screen.getByText("carrier-primary").closest("ol");
    expect(
      within(tryOrder as HTMLElement)
        .getAllByRole("listitem")
        .map((li) => li.textContent),
    ).toEqual(["carrier-primary", "carrier-backup"]);
    const trace = screen.getByRole("list", { name: "Trace" });
    expect(
      within(trace)
        .getAllByRole("listitem")
        .map((li) => li.textContent),
    ).toEqual([
      'Internal extension lookup "0501234567" -> no match',
      'Route "UAE Mobile" matched (regex ^05[0-9]{8}$)',
      "Rewrite 0501234567 -> +971501234567",
    ]);
  });

  it("tests an inbound call from a trunk and shows a rejection", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/extensions": () => json({ items: [] }),
      "GET /api/v1/trunks": () =>
        json({ items: [{ id: 4, name: "carrier-primary" }] }),
      "POST /api/v1/routing/test": () =>
        json({
          decision: {
            kind: "reject",
            rejectCode: 404,
            reason: "no inbound route matched",
          },
          trace: [{ n: 1, text: "No inbound route matched 97142009999" }],
        }),
    });
    renderApp("/routes/test");

    fireEvent.change(await screen.findByLabelText("From"), {
      target: { value: "trunk" },
    });
    fireEvent.change(await screen.findByRole("combobox", { name: "Trunk" }), {
      target: { value: "4" },
    });
    fireEvent.change(screen.getByLabelText("Called number (DID)"), {
      target: { value: "97142009999" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Test" }));

    expect(await screen.findByText("Rejected")).toBeVisible();
    expect(screen.getByText("404")).toBeVisible();
    expect(screen.getByText("no inbound route matched")).toBeVisible();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      from: "trunk:4",
      number: "97142009999",
    });
  });
});
