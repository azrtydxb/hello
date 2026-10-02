import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, noContent, renderApp } from "../test/api";

const TRUNKS = [
  { id: 4, name: "carrier-primary" },
  { id: 5, name: "carrier-backup" },
];

function outbound(id: number, position: number, name: string) {
  return {
    id,
    position,
    name,
    matchKind: "prefix",
    match: `0${id}`,
    sourceExtensions: [],
    schedule: null,
    numberTransform: {},
    callerIdTransform: {},
    trunks: [4],
    failoverCodes: [503],
    emergency: false,
    enabled: true,
  };
}

function inbound(id: number, position: number, name: string) {
  return {
    id,
    position,
    name,
    didKind: "exact",
    did: `97142000${id}`,
    trunkId: null,
    sipDomain: "",
    headerName: "",
    headerRegex: "",
    schedule: null,
    callerIdTransform: {},
    destinationKind: "extension",
    destination: "101",
    enabled: true,
  };
}

const rowNames = () =>
  screen
    .getAllByRole("row")
    .slice(1)
    .map((r) => within(r).getByRole("rowheader").textContent);

describe("Routes", () => {
  it("moving a route sends the full new order and applies it", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/trunks": () => json({ items: TRUNKS }),
      // Deliberately unsorted: the list is shown by position.
      "GET /api/v1/routes/outbound": () =>
        json({
          items: [
            outbound(30, 3, "Gamma"),
            outbound(10, 1, "Alpha"),
            outbound(20, 2, "Beta"),
          ],
        }),
      "PUT /api/v1/routes/outbound/order": noContent,
    });
    renderApp("/routes");

    await screen.findByRole("row", { name: /Gamma/ });
    expect(rowNames()).toEqual(["Alpha", "Beta", "Gamma"]);
    expect(
      screen.getByRole("button", { name: "Move Alpha up" }),
    ).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Move Gamma up" }));

    await waitFor(() => expect(rowNames()).toEqual(["Alpha", "Gamma", "Beta"]));
    expect(calls.filter((c) => c.method === "PUT")).toEqual([
      {
        method: "PUT",
        url: "/api/v1/routes/outbound/order",
        body: { ids: [10, 30, 20] },
      },
    ]);
    expect(rowNames()).toEqual(["Alpha", "Gamma", "Beta"]);
    // Focus stays with the moved route.
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Move Gamma up" }),
      ).toHaveFocus(),
    );
  });

  it("reorders inbound routes on the Inbound tab", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/trunks": () => json({ items: TRUNKS }),
      "GET /api/v1/routes/outbound": () => json({ items: [] }),
      "GET /api/v1/routes/inbound": () =>
        json({ items: [inbound(1, 1, "Main"), inbound(2, 2, "Sales")] }),
      "PUT /api/v1/routes/inbound/order": noContent,
    });
    renderApp("/routes");

    fireEvent.click(await screen.findByRole("tab", { name: "Inbound" }));
    expect(screen.getByRole("tab", { name: "Inbound" })).toHaveAttribute(
      "aria-selected",
      "true",
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Move Main down" }),
    );
    await waitFor(() => expect(rowNames()).toEqual(["Sales", "Main"]));
    expect(calls).toContainEqual({
      method: "PUT",
      url: "/api/v1/routes/inbound/order",
      body: { ids: [2, 1] },
    });
    expect(rowNames()).toEqual(["Sales", "Main"]);
  });

  it("keeps the old order when the server refuses the reorder", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/trunks": () => json({ items: TRUNKS }),
      "GET /api/v1/routes/outbound": () =>
        json({ items: [outbound(10, 1, "Alpha"), outbound(20, 2, "Beta")] }),
      "PUT /api/v1/routes/outbound/order": () =>
        json({ error: { code: "conflict", message: "order is stale" } }, 409),
    });
    renderApp("/routes");

    fireEvent.click(
      await screen.findByRole("button", { name: "Move Beta up" }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "order is stale",
    );
    expect(rowNames()).toEqual(["Alpha", "Beta"]);
  });

  it.each([
    ["numberTransform.template", "numberTransform.template"],
    ["outbound[0].number.template", "the engine's path"],
  ])("shows a server field error (%s) on the matching field", async (path) => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/trunks": () => json({ items: TRUNKS }),
      "GET /api/v1/routes/outbound": () => json({ items: [] }),
      "POST /api/v1/routes/outbound": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid route",
              fields: [
                {
                  path,
                  message: "template references group 2, which does not exist",
                },
                { path: "somethingElse", message: "unmapped problem" },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/routes");

    fireEvent.click(
      await screen.findByRole("button", { name: "New outbound route" }),
    );
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "UAE Mobile" },
    });
    fireEvent.change(screen.getByLabelText("Match prefix"), {
      target: { value: "05" },
    });
    fireEvent.change(screen.getByLabelText("Add trunk"), {
      target: { value: "4" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    const numberRewrite = screen.getByRole("group", { name: "Number rewrite" });
    fireEvent.change(within(numberRewrite).getByLabelText("Regex"), {
      target: { value: "^0(5[0-9]{8})$" },
    });
    fireEvent.change(within(numberRewrite).getByLabelText("Template"), {
      target: { value: "+971${2}" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create route" }));

    const template = within(numberRewrite).getByLabelText("Template");
    expect(
      await within(numberRewrite).findByText(
        "template references group 2, which does not exist",
      ),
    ).toBeVisible();
    expect(template).toHaveAttribute("aria-invalid", "true");
    expect(template).toHaveAccessibleDescription(
      "template references group 2, which does not exist",
    );
    // The caller-ID template is a different field and stays clean.
    const callerId = screen.getByRole("group", { name: "Caller ID rewrite" });
    expect(within(callerId).getByLabelText("Template")).not.toHaveAttribute(
      "aria-invalid",
    );
    // An error with no field on screen is still reported.
    expect(screen.getByRole("alert")).toHaveTextContent("unmapped problem");

    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({
      name: "UAE Mobile",
      matchKind: "prefix",
      match: "05",
      trunks: [4],
      numberTransform: { regex: "^0(5[0-9]{8})$", template: "+971${2}" },
      callerIdTransform: {},
      failoverCodes: [408, 480, 500, 502, 503, 504],
      schedule: null,
    });
  });

  it("creates an inbound route with a schedule", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/trunks": () => json({ items: TRUNKS }),
      "GET /api/v1/routes/outbound": () => json({ items: [] }),
      "GET /api/v1/routes/inbound": () => json({ items: [] }),
      "POST /api/v1/routes/inbound": () =>
        json(inbound(9, 1, "Office hours"), 201),
    });
    renderApp("/routes?tab=inbound");

    fireEvent.click(
      await screen.findByRole("button", { name: "New inbound route" }),
    );
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Office hours" },
    });
    fireEvent.change(screen.getByLabelText("DID"), {
      target: { value: "97142000101" },
    });
    fireEvent.change(screen.getByLabelText("From trunk"), {
      target: { value: "5" },
    });
    fireEvent.change(screen.getByLabelText("Extension number"), {
      target: { value: "101" },
    });
    fireEvent.click(screen.getByLabelText("Only during these times"));
    fireEvent.change(screen.getByLabelText("Time zone"), {
      target: { value: "Asia/Dubai" },
    });
    const window1 = screen.getByRole("group", { name: "Window 1" });
    fireEvent.click(within(window1).getByLabelText("Saturday"));
    fireEvent.change(within(window1).getByLabelText("End 1"), {
      target: { value: "18:30" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create route" }));

    expect(
      await screen.findByRole("row", { name: /Office hours/ }),
    ).toBeVisible();
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({
      name: "Office hours",
      didKind: "exact",
      did: "97142000101",
      trunkId: 5,
      destinationKind: "extension",
      destination: "101",
      schedule: {
        timeZone: "Asia/Dubai",
        windows: [{ days: [1, 2, 3, 4, 5, 6], start: "09:00", end: "18:30" }],
      },
    });
  });
});
