import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  apiError,
  json,
  ME,
  mockApi,
  noContent,
  renderApp,
  type Routes,
} from "../test/api";
import { consecutiveNumbers } from "./onboarding/helpers";

const ago = (h: number) => new Date(Date.now() - h * 3600_000).toISOString();

function candidate(over: Record<string, unknown> = {}) {
  return {
    mac: "000b82000001",
    vendor: "grandstream",
    model: "WP822",
    firmware: "1.0.9.69",
    sourceIp: "192.0.2.21",
    requestedPath: "/p/boot/cfg000b82000001.xml",
    firstSeen: ago(30),
    lastSeen: ago(1),
    requestCount: 12,
    ...over,
  };
}

const EXT = (id: number, number: string, name: string) => ({
  id,
  number,
  name,
  externalNumber: "",
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
});
const EXTENSIONS = [EXT(7, "101", "Front desk"), EXT(8, "102", "Back office")];

const A = candidate();
const B = candidate({
  mac: "805ec0000002",
  vendor: "yealink",
  model: "T54W",
  firmware: "96.86.0.70",
  sourceIp: "192.0.2.22",
  requestCount: 3,
});

function api(candidates: unknown[] = [A, B], more: Routes = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/onboarding/candidates": () => json({ items: candidates }),
    "GET /api/v1/onboarding/ignored": () => json({ items: [] }),
    "GET /api/v1/extensions": () => json({ items: EXTENSIONS }),
    ...more,
  });
}

const queue = () =>
  screen.findByRole("table", { name: "Phones waiting to be adopted" });

afterEach(() => vi.useRealTimers());

describe("Onboarding page", () => {
  it("lists the waiting phones with model, MAC, address, firmware and counts", async () => {
    api();
    renderApp("/onboarding");
    const t = await queue();
    expect(
      screen.getByRole("heading", { level: 1, name: "Onboarding" }),
    ).toBeInTheDocument();
    expect(
      screen.getByText(/next retry or after a reboot/),
    ).toBeInTheDocument();
    const rows = within(t).getAllByRole("row");
    expect(rows).toHaveLength(3);
    const first = within(rows[1]!);
    expect(first.getByText("Grandstream WP822")).toBeInTheDocument();
    expect(first.getByText("00:0b:82:00:00:01")).toBeInTheDocument();
    expect(first.getByText("192.0.2.21")).toBeInTheDocument();
    expect(first.getByText("1.0.9.69")).toBeInTheDocument();
    expect(first.getByText("12")).toBeInTheDocument();
    expect(within(rows[2]!).getByText("Yealink T54W")).toBeInTheDocument();
  });

  it("says so when nobody is waiting", async () => {
    api([]);
    renderApp("/onboarding");
    expect(await screen.findByText("No phones waiting")).toBeInTheDocument();
  });

  it("shows the count as a badge in the Directory nav group", async () => {
    api();
    renderApp("/onboarding");
    const link = await screen.findByRole("link", {
      name: /Onboarding.*2 waiting/,
    });
    expect(link).toHaveAttribute("href", "/onboarding");
    expect(
      within(screen.getByRole("navigation", { name: "Primary" })).getAllByRole(
        "link",
      ).length,
    ).toBeGreaterThan(5);
  });

  it("is not offered to a viewer, and asks the queue nothing for it", async () => {
    const calls = api([A], {
      "GET /api/v1/auth/me": () => json({ username: "vera", role: "viewer" }),
    });
    renderApp("/phones");
    await screen.findByRole("heading", { level: 1, name: "Phones" });
    expect(screen.queryByRole("link", { name: /Onboarding/ })).toBeNull();
    expect(calls.some((c) => c.url.includes("/onboarding/"))).toBe(false);
  });

  it("reloads by itself", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let n = 0;
    mockApi({
      ...ME,
      "GET /api/v1/onboarding/candidates": () =>
        json({ items: n++ === 0 ? [A] : [A, B] }),
      "GET /api/v1/onboarding/ignored": () => json({ items: [] }),
      "GET /api/v1/extensions": () => json({ items: EXTENSIONS }),
    });
    renderApp("/onboarding");
    await screen.findByText("Grandstream WP822");
    expect(screen.queryByText("Yealink T54W")).toBeNull();
    await vi.advanceTimersByTimeAsync(10_500);
    expect(await screen.findByText("Yealink T54W")).toBeInTheDocument();
  });

  it("adopts a phone onto an extension with a label", async () => {
    const calls = api([A, B], {
      "POST /api/v1/onboarding/candidates/000b82000001/adopt": () =>
        json({ id: 9, mac: "000b82000001" }, 201),
    });
    renderApp("/onboarding");
    await queue();
    fireEvent.click(
      screen.getByRole("button", { name: "Adopt 00:0b:82:00:00:01" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "Adopt 00:0b:82:00:00:01",
    });
    expect(
      within(dialog).getByText(/next retry or after a reboot/),
    ).toBeInTheDocument();
    const submit = within(dialog).getByRole("button", { name: "Adopt phone" });
    expect(submit).toBeDisabled();
    // The model is known, so it is not asked for.
    expect(within(dialog).queryByLabelText("Model")).toBeNull();
    fireEvent.change(within(dialog).getByLabelText("Extension"), {
      target: { value: "7" },
    });
    fireEvent.change(within(dialog).getByLabelText("Label"), {
      target: { value: "Warehouse" },
    });
    fireEvent.click(submit);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url).toBe("/api/v1/onboarding/candidates/000b82000001/adopt");
    expect(post?.body).toEqual({ extensionId: "7", label: "Warehouse" });
    expect(
      await screen.findByText(/adopted\. The phone picks up/),
    ).toBeInTheDocument();
  });

  it("asks for the model of a phone that did not report one, and shows server errors", async () => {
    const calls = api([candidate({ model: "", vendor: "generic" })], {
      "POST /api/v1/onboarding/candidates/000b82000001/adopt": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid configuration: model: must be 1-64",
              fields: [{ path: "model", message: "must be 1-64 characters" }],
            },
          },
          400,
        ),
    });
    renderApp("/onboarding");
    await queue();
    expect(screen.getByText("Unknown model")).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: "Adopt 00:0b:82:00:00:01" }),
    );
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Extension"), {
      target: { value: "8" },
    });
    fireEvent.change(within(dialog).getByLabelText("Model"), {
      target: { value: "X1" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Adopt phone" }),
    );
    expect(
      await within(dialog).findByText("must be 1-64 characters"),
    ).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toEqual({ extensionId: "8", model: "X1" });
    expect(screen.getByRole("dialog")).toBeInTheDocument();
  });

  it("dismisses a phone, and can ignore its MAC", async () => {
    const calls = api([A, B], {
      "POST /api/v1/onboarding/candidates/805ec0000002/dismiss": () =>
        noContent(),
      "POST /api/v1/onboarding/candidates/000b82000001/dismiss": () =>
        noContent(),
    });
    renderApp("/onboarding");
    await queue();
    fireEvent.click(
      screen.getByRole("button", { name: "Dismiss 80:5e:c0:00:00:02" }),
    );
    let dialog = await screen.findByRole("dialog", {
      name: "Dismiss 80:5e:c0:00:00:02?",
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Dismiss" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());

    fireEvent.click(
      screen.getByRole("button", { name: "Dismiss 00:0b:82:00:00:01" }),
    );
    dialog = await screen.findByRole("dialog");
    fireEvent.click(
      within(dialog).getByRole("checkbox", { name: /Ignore this MAC/ }),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Dismiss" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const posts = calls.filter((c) => c.method === "POST");
    expect(posts.map((p) => p.body)).toEqual([
      { ignore: false },
      { ignore: true },
    ]);
  });

  it("lists ignored MACs and takes one off the list", async () => {
    const calls = api([], {
      "GET /api/v1/onboarding/ignored": () =>
        json({
          items: [
            {
              mac: "000b82000009",
              vendor: "grandstream",
              model: "WP822",
              createdBy: "user:olga",
              createdAt: ago(2),
            },
          ],
        }),
      "DELETE /api/v1/onboarding/ignored/000b82000009": () => noContent(),
    });
    renderApp("/onboarding");
    const t = await screen.findByRole("table", { name: "Ignored MACs" });
    expect(within(t).getByText("00:0b:82:00:00:09")).toBeInTheDocument();
    expect(within(t).getByText("user:olga")).toBeInTheDocument();
    fireEvent.click(
      screen.getByRole("button", { name: "Stop ignoring 00:0b:82:00:00:09" }),
    );
    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE")).toBe(true),
    );
  });
});

describe("Bulk adopt", () => {
  async function selectBoth() {
    await queue();
    expect(screen.getByRole("button", { name: "Bulk adopt" })).toBeDisabled();
    fireEvent.click(
      screen.getByRole("checkbox", { name: "Select 00:0b:82:00:00:01" }),
    );
    fireEvent.click(
      screen.getByRole("checkbox", { name: "Select 80:5e:c0:00:00:02" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Bulk adopt (2)" }));
    return screen.findByRole("dialog", { name: "Adopt 2 phones" });
  }

  it("assigns consecutive extensions from a starting number", async () => {
    const calls = api([A, B], {
      "POST /api/v1/onboarding/adopt": () =>
        json({ rows: [], ok: true, adopted: 2 }),
    });
    renderApp("/onboarding");
    const dialog = await selectBoth();
    const adopt = within(dialog).getByRole("button", { name: "Adopt phones" });
    expect(adopt).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText("Starting extension"), {
      target: { value: "101" },
    });
    // The preview names the extension each phone gets, and flags a missing one.
    const t = within(dialog).getByRole("table", { name: "Phones to adopt" });
    expect(within(t).getByText("Front desk")).toBeInTheDocument();
    expect(within(t).getByText("Back office")).toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText("Starting extension"), {
      target: { value: "102" },
    });
    expect(within(t).getByText("No such extension")).toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText("Starting extension"), {
      target: { value: "101" },
    });
    fireEvent.change(
      within(dialog).getByLabelText("Label for 00:0b:82:00:00:01"),
      { target: { value: "Dock" } },
    );
    fireEvent.click(adopt);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const post = calls.find((c) => c.method === "POST");
    expect(post?.url).toBe("/api/v1/onboarding/adopt");
    expect(post?.body).toEqual({
      items: [
        { mac: "000b82000001", extensionNumber: "101", label: "Dock" },
        { mac: "805ec0000002", extensionNumber: "102" },
      ],
    });
    expect(await screen.findByText(/2 phones adopted/)).toBeInTheDocument();
  });

  it("lets each phone pick its own extension", async () => {
    const calls = api([A, B], {
      "POST /api/v1/onboarding/adopt": () =>
        json({ rows: [], ok: true, adopted: 2 }),
    });
    renderApp("/onboarding");
    const dialog = await selectBoth();
    fireEvent.click(
      within(dialog).getByRole("radio", { name: "Pick per phone" }),
    );
    const adopt = within(dialog).getByRole("button", { name: "Adopt phones" });
    expect(adopt).toBeDisabled();
    fireEvent.change(
      within(dialog).getByLabelText("Extension for 00:0b:82:00:00:01"),
      { target: { value: "8" } },
    );
    fireEvent.change(
      within(dialog).getByLabelText("Extension for 80:5e:c0:00:00:02"),
      { target: { value: "7" } },
    );
    fireEvent.click(adopt);
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      items: [
        { mac: "000b82000001", extensionNumber: "102" },
        { mac: "805ec0000002", extensionNumber: "101" },
      ],
    });
  });

  it("shows which row stopped a refused batch and keeps the dialog", async () => {
    api([A, B], {
      "POST /api/v1/onboarding/adopt": () =>
        json(
          {
            ok: false,
            adopted: 0,
            rows: [
              { index: 0, mac: "000b82000001", errors: [] },
              {
                index: 1,
                mac: "805ec0000002",
                errors: ['extensionNumber no extension has number "102"'],
              },
            ],
          },
          400,
        ),
    });
    renderApp("/onboarding");
    const dialog = await selectBoth();
    fireEvent.change(within(dialog).getByLabelText("Starting extension"), {
      target: { value: "101" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Adopt phones" }),
    );
    expect(
      await within(dialog).findByText("Nothing was adopted"),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText(/no extension has number "102"/),
    ).toBeInTheDocument();
  });

  it("shows a request failure in the dialog", async () => {
    api([A, B], {
      "POST /api/v1/onboarding/adopt": () =>
        apiError(
          409,
          "conflict",
          "device 101-000001 belongs to another extension",
        ),
    });
    renderApp("/onboarding");
    const dialog = await selectBoth();
    fireEvent.change(within(dialog).getByLabelText("Starting extension"), {
      target: { value: "101" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Adopt phones" }),
    );
    expect(
      await within(dialog).findByText(/belongs to another extension/),
    ).toBeInTheDocument();
  });
});

describe("consecutiveNumbers", () => {
  it("counts up keeping the width, and refuses what the server refuses", () => {
    expect(consecutiveNumbers("101", 3)).toEqual(["101", "102", "103"]);
    expect(consecutiveNumbers("0099", 3)).toEqual(["0099", "0100", "0101"]);
    expect(consecutiveNumbers("99", 2)).toEqual(["99", "100"]);
    expect(consecutiveNumbers("9999999999", 2)).toBeNull();
    expect(consecutiveNumbers("1", 1)).toBeNull();
    expect(consecutiveNumbers("12a", 1)).toBeNull();
    expect(consecutiveNumbers("", 1)).toBeNull();
  });
});
