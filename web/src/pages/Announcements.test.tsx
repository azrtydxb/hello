import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, ME, mockApi, noContent, renderApp } from "../test/api";

const CREATED = "2026-10-04T09:00:00Z";

const ANN = {
  id: 7,
  name: "closing",
  createdAt: CREATED,
  updatedAt: CREATED,
};

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/announcements": () => json({ items: [ANN] }),
    ...extra,
  });
}

describe("Announcements", () => {
  it("lists the uploaded announcements", async () => {
    setup();
    renderApp("/announcements");
    const row = await screen.findByRole("row", { name: /closing/ });
    expect(within(row).getByText("closing")).toBeVisible();
  });

  it("uploads a WAV as multipart and lists it", async () => {
    const calls = setup({
      "POST /api/v1/announcements": () =>
        json({ id: 8, name: "holding", createdAt: CREATED }, 201),
    });
    renderApp("/announcements");
    await screen.findByRole("row", { name: /closing/ });

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "holding" },
    });
    const input = screen.getByLabelText("Audio (WAV)") as HTMLInputElement;
    const file = new File(["RIFF....WAVE"], "holding.wav", {
      type: "audio/wav",
    });
    Object.defineProperty(input, "files", { value: [file] });
    fireEvent.change(input);
    fireEvent.click(
      screen.getByRole("button", { name: "Upload announcement" }),
    );

    const post = await waitFor(() => {
      const found = calls.find(
        (c) => c.method === "POST" && c.url === "/api/v1/announcements",
      );
      expect(found).toBeDefined();
      return found!;
    });
    expect(post.body).toBeInstanceOf(FormData);
    const form = post.body as FormData;
    expect(form.get("name")).toBe("holding");
    expect(form.get("file")).toBeInstanceOf(File);

    await screen.findByRole("row", { name: /holding/ });
  });

  it("rejects an oversized file before sending anything", async () => {
    const calls = setup();
    renderApp("/announcements");
    await screen.findByRole("row", { name: /closing/ });

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "toobig" },
    });
    const input = screen.getByLabelText("Audio (WAV)") as HTMLInputElement;
    const big = new File(["x".repeat(10 * 1024 * 1024 + 1)], "big.wav", {
      type: "audio/wav",
    });
    Object.defineProperty(input, "files", { value: [big] });
    fireEvent.change(input);
    fireEvent.click(
      screen.getByRole("button", { name: "Upload announcement" }),
    );

    expect(await screen.findByText(/larger than 10 MiB/)).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("rejects an invalid name before sending anything", async () => {
    const calls = setup();
    renderApp("/announcements");
    await screen.findByRole("row", { name: /closing/ });

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "not ok!" },
    });
    const input = screen.getByLabelText("Audio (WAV)") as HTMLInputElement;
    const file = new File(["RIFF....WAVE"], "a.wav", { type: "audio/wav" });
    Object.defineProperty(input, "files", { value: [file] });
    fireEvent.change(input);
    fireEvent.click(
      screen.getByRole("button", { name: "Upload announcement" }),
    );

    expect(
      await screen.findByText("Enter a name of 1-64 of A-Z a-z 0-9 . _ -."),
    ).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("surfaces the server's conflict for a taken name", async () => {
    const calls = setup({
      "POST /api/v1/announcements": () =>
        apiError(409, "conflict", "announcement: already exists"),
    });
    renderApp("/announcements");
    await screen.findByRole("row", { name: /closing/ });

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "closing" },
    });
    const input = screen.getByLabelText("Audio (WAV)") as HTMLInputElement;
    const file = new File(["RIFF....WAVE"], "a.wav", { type: "audio/wav" });
    Object.defineProperty(input, "files", { value: [file] });
    fireEvent.change(input);
    fireEvent.click(
      screen.getByRole("button", { name: "Upload announcement" }),
    );

    expect(await screen.findByText(/already exists/)).toBeVisible();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(1);
  });

  it("deletes an announcement after the in-page confirmation", async () => {
    const calls = setup({
      "DELETE /api/v1/announcements/7": noContent,
    });
    renderApp("/announcements");
    const row = await screen.findByRole("row", { name: /closing/ });

    fireEvent.click(
      within(row).getByRole("button", { name: "Delete announcement closing" }),
    );
    const confirm = screen.getByRole("group", {
      name: "Delete the announcement closing?",
    });
    expect(
      within(confirm).getByRole("button", { name: "Cancel" }),
    ).toHaveFocus();
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Delete announcement" }),
    );
    await screen.findByText("No announcements yet.");
    expect(calls).toContainEqual({
      method: "DELETE",
      url: "/api/v1/announcements/7",
      body: undefined,
    });
  });
});
