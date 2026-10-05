import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { apiError, json, ME, mockApi, noContent, renderApp } from "../test/api";

const CREATED = "2026-10-04T09:00:00Z";

const ANN = {
  id: 7,
  name: "closing",
  createdAt: CREATED,
  updatedAt: CREATED,
};

const REPLACED = {
  id: 9,
  name: "please-hold",
  createdAt: CREATED,
  updatedAt: "2026-10-05T12:00:00Z",
};

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/announcements": () => json({ items: [ANN, REPLACED] }),
    ...extra,
  });
}

/** Put a file into the WAV drop field. */
function chooseFile(file: File, scope: HTMLElement = document.body) {
  const input = within(scope).getByLabelText(
    "Drop a WAV file or browse",
  ) as HTMLInputElement;
  Object.defineProperty(input, "files", { value: [file], configurable: true });
  fireEvent.change(input);
}

const wav = (name = "a.wav") =>
  new File(["RIFF....WAVE"], name, { type: "audio/wav" });

beforeEach(() => {
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
});

describe("Announcements", () => {
  it("lists the announcements, with Updated only once replaced", async () => {
    setup();
    renderApp("/announcements");
    const row = await screen.findByRole("row", { name: /closing/ });
    expect(within(row).getByText("closing")).toBeVisible();
    const cells = within(row).getAllByRole("cell");
    expect(cells[3]).toHaveTextContent("—");
    const replaced = screen.getByRole("row", { name: /please-hold/ });
    expect(within(replaced).getAllByRole("cell")[3]).not.toHaveTextContent("—");
  });

  it("shows the empty state", async () => {
    setup({ "GET /api/v1/announcements": () => json({ items: [] }) });
    renderApp("/announcements");
    expect(await screen.findByText("No announcements yet")).toBeVisible();
  });

  it("uploads a WAV as multipart and lists it", async () => {
    const calls = setup({
      "POST /api/v1/announcements": () =>
        json(
          { id: 8, name: "holding", createdAt: CREATED, updatedAt: CREATED },
          201,
        ),
    });
    renderApp("/announcements");
    await screen.findByRole("row", { name: /closing/ });

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "holding" },
    });
    chooseFile(wav("holding.wav"));
    expect(screen.getByText("holding.wav")).toBeVisible();
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
    // The toast and the new row land in one render; the toast goes first
    // because it hides after 3 s.
    expect(
      await screen.findByText("Announcement holding uploaded."),
    ).toBeInTheDocument();
    expect(screen.getByRole("row", { name: /holding/ })).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveValue("");
  });

  it("rejects an oversized file before sending anything", async () => {
    const calls = setup();
    renderApp("/announcements");
    await screen.findByRole("row", { name: /closing/ });
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "toobig" },
    });
    chooseFile(
      new File(["x".repeat(10 * 1024 * 1024 + 1)], "big.wav", {
        type: "audio/wav",
      }),
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Upload announcement" }),
    );
    expect(await screen.findByText(/larger than 10 MB/)).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("rejects an invalid name before sending anything", async () => {
    const calls = setup();
    renderApp("/announcements");
    await screen.findByRole("row", { name: /closing/ });
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "not ok!" },
    });
    chooseFile(wav());
    fireEvent.click(
      screen.getByRole("button", { name: "Upload announcement" }),
    );
    expect(
      await screen.findByText("Enter a name of 1-64 of A-Z a-z 0-9 . _ -."),
    ).toBeVisible();
    expect(screen.getByLabelText("Name")).toHaveAttribute(
      "aria-invalid",
      "true",
    );
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
    chooseFile(wav());
    fireEvent.click(
      screen.getByRole("button", { name: "Upload announcement" }),
    );
    expect(await screen.findByText(/already exists/)).toBeVisible();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(1);
  });

  it("plays an announcement through its audio route", async () => {
    setup({
      "GET /api/v1/announcements/7/audio": () =>
        new Response(null, {
          status: 302,
          headers: { Location: "https://minio.example/ann/closing.wav" },
        }),
    });
    renderApp("/announcements");
    await screen.findByRole("row", { name: /closing/ });
    fireEvent.click(screen.getByRole("button", { name: "Play closing" }));
    const audio = await screen.findByLabelText("Playback of closing");
    expect(audio).toHaveAttribute("src", "/api/v1/announcements/7/audio");
    // No length is known until the audio loads: unknown reads "—".
    expect(screen.getByText("0:00 / —")).toBeVisible();
  });

  it("replaces an announcement's audio with a PUT", async () => {
    const calls = setup({
      "PUT /api/v1/announcements/7": () =>
        json({ ...ANN, updatedAt: "2026-10-05T15:00:00Z" }),
    });
    renderApp("/announcements");
    const row = await screen.findByRole("row", { name: /closing/ });
    fireEvent.click(
      within(row).getByRole("button", {
        name: "Replace the audio of closing",
      }),
    );
    const dialog = screen.getByRole("dialog", { name: "Replace closing" });

    // Nothing chosen: the field says so and nothing is sent.
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Replace audio" }),
    );
    expect(await within(dialog).findByText("Choose a WAV file.")).toBeVisible();
    expect(calls.some((c) => c.method === "PUT")).toBe(false);

    chooseFile(wav("new.wav"), dialog);
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Replace audio" }),
    );
    const put = await waitFor(() => {
      const found = calls.find(
        (c) => c.method === "PUT" && c.url === "/api/v1/announcements/7",
      );
      expect(found).toBeDefined();
      return found!;
    });
    const form = put.body as FormData;
    expect((form.get("file") as File).name).toBe("new.wav");
    expect(form.get("name")).toBeNull();
    expect(
      await screen.findByText("Audio of closing replaced."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).toBeNull();
    const updated = screen.getByRole("row", { name: /closing/ });
    expect(within(updated).getAllByRole("cell")[3]).not.toHaveTextContent("—");
  });

  it("shows the server's refusal of a replacement in the dialog", async () => {
    setup({
      "PUT /api/v1/announcements/7": () =>
        json(
          {
            error: {
              code: "invalid",
              message: "invalid request",
              fields: [
                { path: "file", message: "the audio must be a RIFF/WAVE file" },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/announcements");
    const row = await screen.findByRole("row", { name: /closing/ });
    fireEvent.click(
      within(row).getByRole("button", {
        name: "Replace the audio of closing",
      }),
    );
    const dialog = screen.getByRole("dialog", { name: "Replace closing" });
    chooseFile(wav(), dialog);
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Replace audio" }),
    );
    expect(
      await within(dialog).findByText("the audio must be a RIFF/WAVE file"),
    ).toBeVisible();
    expect(
      screen.getByRole("dialog", { name: "Replace closing" }),
    ).toBeVisible();
  });

  it("deletes an announcement after the confirmation", async () => {
    const calls = setup({
      "DELETE /api/v1/announcements/7": noContent,
    });
    renderApp("/announcements");
    const row = await screen.findByRole("row", { name: /closing/ });
    fireEvent.click(
      within(row).getByRole("button", { name: "Delete announcement closing" }),
    );
    const dialog = screen.getByRole("dialog", {
      name: "Delete announcement?",
    });
    expect(
      within(dialog).getByRole("button", { name: "Cancel" }),
    ).toHaveFocus();
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Delete announcement" }),
    );
    expect(
      await screen.findByText("Announcement closing deleted."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("row", { name: /closing/ })).toBeNull();
    expect(calls).toContainEqual({
      method: "DELETE",
      url: "/api/v1/announcements/7",
      body: undefined,
    });
  });
});
