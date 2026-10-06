import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { createEvent, fireEvent as fe } from "@testing-library/react";
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

    fireEvent.click(await screen.findByRole("tab", { name: /^Inbound/ }));
    expect(screen.getByRole("tab", { name: /^Inbound/ })).toHaveAttribute(
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

  it("reloads the stored order after a refused reorder, so the next move works", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/trunks": () => json({ items: TRUNKS }),
      // Someone else added Gamma and reordered meanwhile.
      "GET /api/v1/routes/outbound": [
        () =>
          json({ items: [outbound(10, 1, "Alpha"), outbound(20, 2, "Beta")] }),
        () =>
          json({
            items: [
              outbound(20, 1, "Beta"),
              outbound(30, 2, "Gamma"),
              outbound(10, 3, "Alpha"),
            ],
          }),
      ],
      "PUT /api/v1/routes/outbound/order": [
        () =>
          json({ error: { code: "conflict", message: "order is stale" } }, 409),
        noContent,
      ],
    });
    renderApp("/routes");

    fireEvent.click(
      await screen.findByRole("button", { name: "Move Beta up" }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "order is stale",
    );
    await waitFor(() => expect(rowNames()).toEqual(["Beta", "Gamma", "Alpha"]));

    fireEvent.click(screen.getByRole("button", { name: "Move Alpha up" }));
    await waitFor(() => expect(rowNames()).toEqual(["Beta", "Alpha", "Gamma"]));
    expect(calls.filter((c) => c.method === "PUT").map((c) => c.body)).toEqual([
      { ids: [20, 10] },
      { ids: [20, 10, 30] },
    ]);
  });

  it.each([
    ["numberTransform.template", "numberTransform.template"],
    ["NumberTransform.Template", "matched case-insensitively"],
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

    // The trunk picker and the submit button are gated on the trunk list: on
    // a loaded runner the form is ready first, and interacting with the
    // disabled controls (or setting an option that does not exist yet) is
    // silently dropped. Wait for the trunks to be offered.
    fireEvent.click(
      (await screen.findAllByRole("button", { name: "New route" }))[0]!,
    );
    expect(
      screen.getByRole("dialog", { name: "New outbound route" }),
    ).toBeVisible();
    await screen.findByRole("option", { name: "carrier-primary" });
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

  it("shows a trunks[0] error at that trunk in the picker; other items' paths go to the alert", async () => {
    mockApi({
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
                  path: "trunks[0]",
                  message: "trunk carrier-primary is disabled",
                },
                {
                  path: "outbound[3].match",
                  message: "regex does not compile",
                },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/routes");

    // As above: open the form, then wait for the trunk list to land before
    // touching the gated picker and submit button.
    fireEvent.click(
      (await screen.findAllByRole("button", { name: "New route" }))[0]!,
    );
    expect(
      screen.getByRole("dialog", { name: "New outbound route" }),
    ).toBeVisible();
    await screen.findByRole("option", { name: "carrier-primary" });
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "UAE Mobile" },
    });
    fireEvent.change(screen.getByLabelText("Match prefix"), {
      target: { value: "05" },
    });
    for (const id of ["4", "5"]) {
      fireEvent.change(screen.getByLabelText("Add trunk"), {
        target: { value: id },
      });
      fireEvent.click(screen.getByRole("button", { name: "Add" }));
    }
    fireEvent.click(screen.getByRole("button", { name: "Create route" }));

    const picker = screen.getByRole("group", { name: "Trunks, in try order" });
    const message = await within(picker).findByText(
      "trunk carrier-primary is disabled",
    );
    const [first, second] = within(picker).getAllByRole("listitem");
    expect(first).toContainElement(message);
    expect(second).not.toHaveTextContent("disabled");
    expect(
      within(picker).getByRole("button", { name: "Remove carrier-primary" }),
    ).toHaveAccessibleDescription("trunk carrier-primary is disabled");
    // A path into another route matches no field here: it is listed, not
    // pinned on this form's Match field.
    expect(screen.getByRole("alert")).toHaveTextContent(
      "outbound[3].match: regex does not compile",
    );
    expect(screen.getByLabelText("Match prefix")).not.toHaveAttribute(
      "aria-invalid",
    );
  });

  it("keeps an inbound route's trunk while the trunk list is unavailable", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/trunks": [
        () =>
          json({ error: { code: "internal", message: "database down" } }, 500),
        () => json({ items: TRUNKS }),
      ],
      "GET /api/v1/routes/outbound": () => json({ items: [] }),
      "GET /api/v1/routes/inbound": () =>
        json({ items: [{ ...inbound(1, 1, "Main"), trunkId: 5 }] }),
      "PATCH /api/v1/routes/inbound/1": () =>
        json({ ...inbound(1, 1, "Main line"), trunkId: 5 }),
    });
    renderApp("/routes?tab=inbound");

    expect(
      await screen.findByText("Could not load the trunk list"),
    ).toBeVisible();
    fireEvent.click(await screen.findByRole("button", { name: "Edit Main" }));

    const select = screen.getByLabelText("From trunk");
    // Not "Any trunk": the stored trunk stays selected, and cannot be changed blind.
    expect(select).toHaveValue("5");
    expect(select).toBeDisabled();
    const save = screen.getByRole("button", { name: "Save route" });
    expect(save).toBeDisabled();
    expect(save).toHaveAccessibleDescription(
      "Saving is available once the trunk list has loaded.",
    );

    fireEvent.click(
      screen.getByRole("button", { name: "Retry loading trunks" }),
    );
    await waitFor(() => expect(save).toBeEnabled());
    expect(select).toHaveValue("5");
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Main line" },
    });
    fireEvent.click(save);

    await screen.findByRole("row", { name: /Main line/ });
    const patch = calls.find((c) => c.method === "PATCH");
    expect(patch?.body).toMatchObject({ trunkId: 5 });
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

    // The From-trunk select and the submit button are gated on the trunk
    // list; on a loaded runner the form can be ready first, and interacting
    // with the disabled controls is silently dropped. Open the form, then
    // wait for the trunks.
    fireEvent.click(
      (await screen.findAllByRole("button", { name: "New route" }))[0]!,
    );
    expect(
      screen.getByRole("dialog", { name: "New inbound route" }),
    ).toBeVisible();
    await screen.findByRole("option", { name: "carrier-backup" });
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

    // The toast says the create landed; the table then holds the route.
    expect(
      await screen.findByText("Route Office hours created."),
    ).toBeVisible();
    expect(screen.getByRole("row", { name: /Office hours/ })).toBeVisible();
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

  it("shows the design's columns: match, rewrite, trunk chain, schedule, emergency", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/trunks": () => json({ items: TRUNKS }),
      "GET /api/v1/routes/outbound": () =>
        json({
          items: [
            {
              ...outbound(1, 1, "UAE landline"),
              matchKind: "regex",
              match: "^0[2-9][0-9]{7}$",
              numberTransform: { strip: 1, prefix: "+971" },
              trunks: [5, 4],
              schedule: {
                timeZone: "Asia/Dubai",
                windows: [
                  { days: [0, 1, 2, 3, 4], start: "08:00", end: "18:00" },
                ],
              },
            },
            { ...outbound(2, 2, "Emergency"), emergency: true, match: "112" },
          ],
        }),
      "GET /api/v1/extensions": () =>
        json({ items: [{ id: 1, number: "101", name: "Reception" }] }),
      "GET /api/v1/routes/inbound": () =>
        json({ items: [inbound(3, 1, "Main number")] }),
    });
    renderApp("/routes");

    const row = await screen.findByRole("row", { name: /UAE landline/ });
    const cells = within(row).getAllByRole("cell");
    expect(cells.map((c) => c.textContent)).toEqual([
      "1",
      "regex^0[2-9][0-9]{7}$",
      "strip 1 · prefix +971",
      "carrier-backupcarrier-primary",
      "Sun–Thu 08:00–18:00 Asia/Dubai",
      "",
      "",
    ]);
    expect(
      within(screen.getByRole("row", { name: /Emergency/ })).getByText(
        "Emergency",
        { selector: ".az-badge" },
      ),
    ).toBeVisible();
    expect(screen.getByRole("tab", { name: /^Outbound/ })).toHaveTextContent(
      "Outbound2",
    );

    fireEvent.click(screen.getByRole("tab", { name: /^Inbound/ }));
    const main = await screen.findByRole("row", { name: /Main number/ });
    expect(within(main).getByText("Extension · Reception")).toBeVisible();
    expect(within(main).getByText("Always")).toBeVisible();
  });

  it("toggles Enabled in the list, and puts it back when the save fails", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/trunks": () => json({ items: TRUNKS }),
      "GET /api/v1/routes/outbound": () =>
        json({ items: [outbound(10, 1, "Alpha")] }),
      "PATCH /api/v1/routes/outbound/10": [
        () => json({ ...outbound(10, 1, "Alpha"), enabled: false }),
        () =>
          json({ error: { code: "internal", message: "database down" } }, 500),
      ],
    });
    renderApp("/routes");

    const toggle = await screen.findByRole("switch", { name: "Alpha enabled" });
    expect(toggle).toBeChecked();
    fireEvent.click(toggle);
    expect(await screen.findByText("Route Alpha disabled.")).toBeVisible();
    expect(toggle).not.toBeChecked();
    expect(calls.filter((c) => c.method === "PATCH")[0]?.body).toEqual({
      enabled: false,
    });

    fireEvent.click(toggle);
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not enable Alpha: database down",
    );
    expect(toggle).not.toBeChecked();
  });

  it("reorders by dragging a row onto another", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/trunks": () => json({ items: TRUNKS }),
      "GET /api/v1/routes/outbound": () =>
        json({
          items: [
            outbound(10, 1, "Alpha"),
            outbound(20, 2, "Beta"),
            outbound(30, 3, "Gamma"),
          ],
        }),
      "PUT /api/v1/routes/outbound/order": noContent,
    });
    renderApp("/routes");

    const gamma = await screen.findByRole("row", { name: /Gamma/ });
    const alpha = screen.getByRole("row", { name: /Alpha/ });
    const dataTransfer = { setData: () => {}, effectAllowed: "" };
    fe(gamma, createEvent.dragStart(gamma, { dataTransfer }));
    fe(alpha, createEvent.dragOver(alpha, { dataTransfer }));
    fe(alpha, createEvent.drop(alpha, { dataTransfer }));

    await waitFor(() => expect(rowNames()).toEqual(["Gamma", "Alpha", "Beta"]));
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
      ids: [30, 10, 20],
    });
  });

  it("deletes a route after the confirmation dialog", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/trunks": () => json({ items: TRUNKS }),
      "GET /api/v1/routes/outbound": () =>
        json({ items: [outbound(10, 1, "Alpha")] }),
      "DELETE /api/v1/routes/outbound/10": noContent,
    });
    renderApp("/routes");

    fireEvent.click(
      await screen.findByRole("button", { name: "Delete Alpha" }),
    );
    const dialog = screen.getByRole("dialog", { name: "Delete route Alpha?" });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Delete route" }),
    );
    expect(await screen.findByText("No outbound routes yet.")).toBeVisible();
    expect(calls.some((c) => c.method === "DELETE")).toBe(true);
  });
});
