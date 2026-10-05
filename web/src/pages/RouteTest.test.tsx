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

    expect(await screen.findByText("No test yet")).toBeVisible();
    await screen.findByRole("option", { name: "101 · Desk" });
    fireEvent.change(screen.getByLabelText("Extension"), {
      target: { value: "101" },
    });
    fireEvent.change(screen.getByLabelText("Dialled number"), {
      target: { value: "0501234567" },
    });
    fireEvent.change(screen.getByLabelText("At"), {
      target: { value: "2026-10-04T09:30" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Test" }));

    expect(
      await screen.findByRole("heading", {
        name: "Outbound via carrier-primary",
      }),
    ).toBeVisible();
    const body = calls.find((c) => c.method === "POST")?.body as Record<
      string,
      unknown
    >;
    expect(body).toMatchObject({ from: "101", number: "0501234567" });
    expect(body.at).toBe(new Date("2026-10-04T09:30").toISOString());
    expect(body).not.toHaveProperty("callerId");

    const fact = (label: string) =>
      screen.getByText(label, { selector: "dt" }).nextElementSibling;
    expect(fact("Outcome")).toHaveTextContent("Outbound via trunks");
    expect(fact("Route")).toHaveTextContent("UAE Mobile");
    expect(fact("Number sent")).toHaveTextContent("+971501234567");
    expect(fact("Caller ID")).toHaveTextContent("+97142000101");
    const tryOrder = screen.getByRole("list", { name: "Trunks, in try order" });
    expect(
      within(tryOrder)
        .getAllByRole("listitem")
        .map((li) => li.textContent),
    ).toEqual(["carrier-primary", "carrier-backup"]);
    const trace = screen.getByRole("list", { name: "Trace" });
    expect(
      within(trace)
        .getAllByRole("listitem")
        .map((li) => li.lastElementChild?.textContent),
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

    fireEvent.click(
      await screen.findByRole("radio", { name: "Trunk (inbound)" }),
    );
    await screen.findByRole("option", { name: "carrier-primary" });
    fireEvent.change(screen.getByRole("combobox", { name: "Trunk" }), {
      target: { value: "4" },
    });
    fireEvent.change(screen.getByLabelText("Called number (DID)"), {
      target: { value: "97142009999" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Test" }));

    expect(
      await screen.findByRole("heading", { name: "Rejected · 404 Not Found" }),
    ).toBeVisible();
    const fact = (label: string) =>
      screen.getByText(label, { selector: "dt" }).nextElementSibling;
    expect(fact("Outcome")).toHaveTextContent("Rejected");
    expect(fact("Rejected with")).toHaveTextContent("404");
    expect(fact("Reason")).toHaveTextContent("no inbound route matched");
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      from: "trunk:4",
      number: "97142009999",
    });
  });

  it("tests at once from a link, and marks an emergency route", async () => {
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
            number: "112",
            callerId: "",
            route: "Emergency",
            trunks: ["carrier-primary"],
            emergency: true,
          },
          trace: [
            { n: 1, text: 'Route "Emergency" matched (prefix 112; emergency)' },
          ],
        }),
    });
    renderApp("/routes/test?from=101&number=112");

    expect(
      await screen.findByRole("heading", {
        name: "Emergency call via carrier-primary",
      }),
    ).toBeVisible();
    expect(screen.getByLabelText("Dialled number")).toHaveValue("112");
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      from: "101",
      number: "112",
    });
    // No caller ID is unknown, not blank.
    expect(
      screen.getByText("Caller ID", { selector: "dt" }).nextElementSibling,
    ).toHaveTextContent("—");
  });

  it("prefills a trunk from the trunk card's link", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/extensions": () => json({ items: [] }),
      "GET /api/v1/trunks": () =>
        json({ items: [{ id: 4, name: "carrier-primary" }] }),
    });
    renderApp("/routes/test?from=trunk%3A4");

    expect(
      await screen.findByRole("radio", { name: "Trunk (inbound)" }),
    ).toHaveAttribute("aria-checked", "true");
    await screen.findByRole("option", { name: "carrier-primary" });
    expect(screen.getByRole("combobox", { name: "Trunk" })).toHaveValue("4");
    expect(screen.getByLabelText("Called number (DID)")).toHaveValue("");
  });

  it("starts a fresh form from the top bar's Test a number", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/extensions": () => json({ items: [] }),
      "GET /api/v1/trunks": () =>
        json({ items: [{ id: 4, name: "carrier-primary" }] }),
    });
    renderApp("/routes/test?from=trunk%3A4");
    expect(
      await screen.findByRole("radio", { name: "Trunk (inbound)" }),
    ).toHaveAttribute("aria-checked", "true");

    fireEvent.click(screen.getByRole("link", { name: "Test a number" }));
    expect(
      await screen.findByRole("radio", { name: "Extension" }),
    ).toHaveAttribute("aria-checked", "true");
    expect(screen.getByTestId("location")).toHaveTextContent(
      /^\/routes\/test$/,
    );
  });
});
