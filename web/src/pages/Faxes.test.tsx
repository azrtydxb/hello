import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, noContent, renderApp } from "../test/api";

const WHEN = "2026-11-09T10:00:00Z";

const base = {
  localNumber: "+41441110000",
  destination: "",
  ownerExtension: "101",
  mode: "g711",
  bitRate: 14400,
  resultCode: 0,
  resultText: "OK",
  correlationId: "c1",
  createdBy: "",
  createdAt: WHEN,
  finishedAt: WHEN,
  attempts: 0,
  nextAttemptAt: null,
  mailTo: "",
  mailStatus: "sent",
};

const FAXES = [
  {
    ...base,
    id: 3,
    direction: "inbound",
    status: "received",
    peerNumber: "+41447654321",
    destination: "main",
    pages: 3,
    hasPdf: true,
  },
  {
    ...base,
    id: 2,
    direction: "inbound",
    status: "partial",
    peerNumber: "+4144999",
    pages: 1,
    hasPdf: true,
    resultCode: 22,
    resultText: "The call dropped prematurely",
    mailStatus: "failed",
  },
  {
    ...base,
    id: 1,
    direction: "outbound",
    status: "queued",
    peerNumber: "+3225551234",
    pages: 2,
    hasPdf: true,
    mode: "",
    resultCode: null,
    resultText: "busy",
    finishedAt: null,
    attempts: 1,
    nextAttemptAt: "2026-11-09T10:05:00Z",
    mailStatus: "none",
  },
];

const DESTS = [
  {
    id: 5,
    name: "main",
    extension: "101",
    email: "",
    createdAt: WHEN,
    updatedAt: WHEN,
  },
];

describe("Faxes", () => {
  it("lists received and sent faxes with their state and a download", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/faxes": () => json({ items: FAXES, next: "" }),
      "GET /api/v1/fax-destinations": () => json({ items: DESTS }),
    });
    renderApp("/faxes");
    const table = await screen.findByRole("table", { name: "Faxes" });
    expect(within(table).getByText("+41447654321")).toBeVisible();
    expect(within(table).getAllByText("Received").length).toBeGreaterThan(0);
    expect(within(table).getByText("Partial")).toBeVisible();
    expect(
      within(table).getByText(/The call dropped prematurely/),
    ).toBeVisible();
    expect(within(table).getByText("Queued")).toBeVisible();
    expect(within(table).getByText("Email failed")).toBeVisible();
    const download = within(table).getByRole("link", {
      name: "Download fax 3",
    });
    expect(download.getAttribute("href")).toBe("/api/v1/faxes/3/pdf");
    // The destination table.
    expect(
      await screen.findByRole("table", { name: "Fax destinations" }),
    ).toBeVisible();
    expect(screen.getByText("The owner's users")).toBeVisible();
  });

  it("filters by direction and status through the API", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/faxes": () => json({ items: FAXES, next: "" }),
      "GET /api/v1/faxes?direction=outbound": () =>
        json({ items: [FAXES[2]], next: "" }),
      "GET /api/v1/fax-destinations": () => json({ items: [] }),
    });
    renderApp("/faxes");
    await screen.findByRole("table", { name: "Faxes" });
    fireEvent.change(screen.getByLabelText("Direction"), {
      target: { value: "outbound" },
    });
    await waitFor(() =>
      expect(calls.some((c) => c.url.includes("direction=outbound"))).toBe(
        true,
      ),
    );
    await waitFor(() => expect(screen.queryByText("+41447654321")).toBeNull());
  });

  it("deletes a fax, and says so when there are none", async () => {
    let rows = FAXES;
    const calls = mockApi({
      ...ME,
      "GET /api/v1/faxes": () => json({ items: rows, next: "" }),
      "DELETE /api/v1/faxes/2": () => {
        rows = [FAXES[0]!, FAXES[2]!];
        return noContent();
      },
      "GET /api/v1/fax-destinations": () => json({ items: [] }),
    });
    renderApp("/faxes");
    await screen.findByRole("table", { name: "Faxes" });
    fireEvent.click(screen.getByRole("button", { name: "Delete fax 2" }));
    expect(await screen.findByText("Fax 2 deleted.")).toBeVisible();
    await waitFor(() =>
      expect(
        within(screen.getByRole("table", { name: "Faxes" })).queryByText(
          "Partial",
        ),
      ).toBeNull(),
    );
    expect(calls.some((c) => c.method === "DELETE")).toBe(true);
  });

  it("shows the empty state", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/faxes": () => json({ items: [], next: "" }),
      "GET /api/v1/fax-destinations": () => json({ items: [] }),
    });
    renderApp("/faxes");
    expect(await screen.findByText("No faxes")).toBeVisible();
    expect(screen.getByText("No fax destinations")).toBeVisible();
  });

  it("sends a PDF as multipart with the number, and refuses a bad number", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/faxes": () => json({ items: [], next: "" }),
      "GET /api/v1/fax-destinations": () => json({ items: [] }),
      "POST /api/v1/faxes": () => json({ ...FAXES[2], id: 9 }, 201),
    });
    renderApp("/faxes");
    const form = await screen.findByRole("form", { name: "Send a fax" });
    const button = within(form).getByRole("button", { name: "Send fax" });
    expect(button).toBeDisabled();
    const file = new File(["%PDF-1.4"], "doc.pdf", { type: "application/pdf" });
    fireEvent.change(within(form).getByLabelText("PDF"), {
      target: { files: [file] },
    });
    fireEvent.change(within(form).getByLabelText("To"), {
      target: { value: "0441234567" },
    });
    expect(within(form).getByText(/Use E\.164/)).toBeVisible();
    expect(button).toBeDisabled();
    fireEvent.change(within(form).getByLabelText("To"), {
      target: { value: "+3225551234" },
    });
    expect(button).toBeEnabled();
    fireEvent.click(button);
    expect(
      await within(form).findByText(/Fax 9 to \+3225551234 queued/),
    ).toBeVisible();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url).toBe("/api/v1/faxes");
  });

  it("adds and deletes a fax destination as an admin", async () => {
    let dests = DESTS;
    const calls = mockApi({
      ...ME,
      "GET /api/v1/faxes": () => json({ items: [], next: "" }),
      "GET /api/v1/fax-destinations": () => json({ items: dests }),
      "POST /api/v1/fax-destinations": () => {
        dests = [
          ...DESTS,
          { ...DESTS[0]!, id: 6, name: "sales", extension: "102" },
        ];
        return json(dests[1], 201);
      },
      "DELETE /api/v1/fax-destinations/5": () => noContent(),
    });
    renderApp("/faxes");
    const form = await screen.findByRole("form", {
      name: "Add a fax destination",
    });
    fireEvent.change(within(form).getByLabelText("Name"), {
      target: { value: "sales" },
    });
    fireEvent.change(within(form).getByLabelText("Owner extension"), {
      target: { value: "102" },
    });
    fireEvent.click(
      within(form).getByRole("button", { name: "Add destination" }),
    );
    await waitFor(() => expect(screen.getByText("sales")).toBeVisible());
    expect(
      calls.find(
        (c) => c.method === "POST" && c.url.includes("fax-destinations"),
      )?.body,
    ).toMatchObject({
      name: "sales",
      extension: "102",
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Delete fax destination main" }),
    );
    expect(
      await screen.findByText("Fax destination main deleted."),
    ).toBeVisible();
  });

  it("shows a user only their own faxes, with no send form or destinations", async () => {
    const calls = mockApi({
      "GET /api/v1/auth/me": () => json({ username: "pat", role: "user" }),
      "GET /api/v1/me/faxes": () => json({ items: [FAXES[0]], next: "" }),
    });
    renderApp("/me/faxes");
    const table = await screen.findByRole("table", { name: "Faxes" });
    const download = within(table).getByRole("link", {
      name: "Download fax 3",
    });
    expect(download.getAttribute("href")).toBe("/api/v1/me/faxes/3/pdf");
    expect(screen.getByRole("heading", { name: "My faxes" })).toBeVisible();
    expect(screen.queryByRole("form", { name: "Send a fax" })).toBeNull();
    expect(screen.queryByText("Fax destinations")).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete fax 3" })).toBeNull();
    // Nothing outside the self-service API is asked for.
    expect(
      calls
        .map((c) => c.url)
        .filter(
          (u) =>
            !u.startsWith("/api/v1/me/") &&
            !u.startsWith("/api/v1/auth/") &&
            u !== "/api/v1/version",
        ),
    ).toEqual([]);
  });
});
