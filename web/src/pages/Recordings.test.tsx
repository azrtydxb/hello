import { fireEvent, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { apiError, json, ME, mockApi, noContent, renderApp } from "../test/api";

const CREATED = "2026-10-04T09:00:00Z";

const EXT = {
  id: 1,
  number: "100",
  name: "Reception",
  createdAt: CREATED,
  updatedAt: CREATED,
};

const REC = {
  id: 31,
  correlationId: "corr-31",
  initiatedBy: "dtmf",
  durationMs: 21000,
  createdAt: CREATED,
  cdrId: 501,
  source: "100",
  destination: "+442071838750",
};

/** A recording whose CDR is not written yet: no parties, no link. */
const OTHER = {
  id: 32,
  correlationId: "corr-32",
  initiatedBy: "default",
  durationMs: 5000,
  createdAt: CREATED,
  source: "",
  destination: "",
};

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/extensions": () => json({ items: [EXT] }),
    "GET /api/v1/recordings?limit=50": () =>
      json({ items: [REC, OTHER], next: "" }),
    ...extra,
  });
}

beforeEach(() => {
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
});

describe("Recordings", () => {
  it("shows the call, the CDR link, the origin and a download link", async () => {
    setup();
    renderApp("/recordings");
    const row = await screen.findByRole("row", { name: /corr-31/ });
    expect(row).toHaveTextContent("100 → +442071838750");
    expect(within(row).getByText("dtmf (*1)")).toBeVisible();
    expect(within(row).getByText("0:21")).toBeVisible();
    expect(within(row).getByRole("link", { name: "corr-31" })).toHaveAttribute(
      "href",
      "/history/501",
    );
    expect(
      within(row).getByRole("link", {
        name: "Download the recording corr-31",
      }),
    ).toHaveAttribute("href", "/api/v1/recordings/31/audio?download=1");

    const other = screen.getByRole("row", { name: /corr-32/ });
    expect(within(other).queryByRole("link", { name: "corr-32" })).toBeNull();
    expect(within(other).getByText("—")).toBeVisible();
  });

  it("plays only recordings whose audio answers the gate", async () => {
    const calls = setup({
      "GET /api/v1/recordings/31/audio": () =>
        new Response(null, {
          status: 302,
          headers: { Location: "https://minio.example/rec/a.wav" },
        }),
      "GET /api/v1/recordings/32/audio": () =>
        apiError(404, "not_found", "no audio"),
    });
    renderApp("/recordings");
    await screen.findByRole("row", { name: /corr-31/ });

    fireEvent.click(
      screen.getByRole("button", { name: "Play the recording corr-32" }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not play the recording: the audio is not available (HTTP 404).",
    );
    expect(document.querySelector("audio")).toBeNull();
    expect(calls.filter((c) => c.url.endsWith("/audio"))).toHaveLength(1);

    fireEvent.click(
      screen.getByRole("button", { name: "Play the recording corr-31" }),
    );
    const audio = await screen.findByLabelText(
      "Playback of the recording corr-31",
    );
    expect(audio).toHaveAttribute("src", "/api/v1/recordings/31/audio");
    expect(screen.getByText("0:00 / 0:21")).toBeVisible();
  });

  it("deletes a recording after the confirmation", async () => {
    const calls = setup({
      "DELETE /api/v1/recordings/31": noContent,
    });
    renderApp("/recordings");
    const row = await screen.findByRole("row", { name: /corr-31/ });

    fireEvent.click(
      within(row).getByRole("button", { name: "Delete the recording corr-31" }),
    );
    const dialog = screen.getByRole("dialog", { name: "Delete recording?" });
    expect(
      within(dialog).getByRole("button", { name: "Cancel" }),
    ).toHaveFocus();
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Delete recording" }),
    );
    expect(
      await screen.findByText("Recording corr-31 deleted"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("row", { name: /corr-31/ })).toBeNull();
    expect(calls).toContainEqual({
      method: "DELETE",
      url: "/api/v1/recordings/31",
      body: undefined,
    });
  });

  it("re-requests with the extension filter", async () => {
    const calls = setup({
      "GET /api/v1/recordings?extension=100&limit=50": () =>
        json({ items: [], next: "" }),
    });
    renderApp("/recordings");
    await screen.findByRole("row", { name: /corr-31/ });
    await screen.findByRole("option", { name: "100 Reception" });

    fireEvent.change(screen.getByLabelText("Filter by extension"), {
      target: { value: "100" },
    });
    expect(
      await screen.findByText("No recordings for this extension"),
    ).toBeVisible();
    expect(
      calls.some(
        (c) =>
          c.method === "GET" &&
          c.url === "/api/v1/recordings?extension=100&limit=50",
      ),
    ).toBe(true);
  });

  it("pages to older recordings and back", async () => {
    setup({
      "GET /api/v1/recordings?limit=50": () =>
        json({ items: [REC], next: "31" }),
      "GET /api/v1/recordings?before=31&limit=50": () =>
        json({ items: [OTHER], next: "" }),
    });
    renderApp("/recordings");
    await screen.findByRole("row", { name: /corr-31/ });
    expect(screen.queryByRole("button", { name: "Newer" })).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Older" }));
    await screen.findByRole("row", { name: /corr-32/ });
    expect(screen.getByRole("button", { name: "Older" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Newer" }));
    expect(
      await screen.findByRole("row", { name: /corr-31/ }),
    ).toBeInTheDocument();
  });

  it("reports a list that fails to load", async () => {
    setup({
      "GET /api/v1/recordings?limit=50": () =>
        apiError(500, "internal", "database down"),
    });
    renderApp("/recordings");
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not load recordings",
    );
  });
});
