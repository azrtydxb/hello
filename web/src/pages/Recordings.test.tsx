import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
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
};

const OTHER = {
  ...REC,
  id: 32,
  correlationId: "corr-32",
  initiatedBy: "default",
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

describe("Recordings", () => {
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
  });

  it("deletes a recording after the in-page confirmation", async () => {
    const calls = setup({
      "DELETE /api/v1/recordings/31": noContent,
    });
    renderApp("/recordings");
    const row = await screen.findByRole("row", { name: /corr-31/ });

    fireEvent.click(
      within(row).getByRole("button", { name: "Delete the recording corr-31" }),
    );
    const confirm = screen.getByRole("group", {
      name: "Delete the recording corr-31?",
    });
    expect(
      within(confirm).getByRole("button", { name: "Cancel" }),
    ).toHaveFocus();
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Delete recording" }),
    );
    await screen.findByRole("row", { name: /corr-32/ });
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
        json({ items: [REC], next: "" }),
    });
    renderApp("/recordings");
    await screen.findByRole("row", { name: /corr-31/ });

    fireEvent.change(screen.getByLabelText("Filter by extension"), {
      target: { value: "100" },
    });
    expect(
      await screen.findByRole("row", { name: /corr-31/ }),
    ).toBeInTheDocument();
    expect(
      calls.some(
        (c) =>
          c.method === "GET" &&
          c.url === "/api/v1/recordings?extension=100&limit=50",
      ),
    ).toBe(true);
  });
});
