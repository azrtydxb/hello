import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { apiError, json, ME, mockApi, noContent, renderApp } from "../test/api";

const CREATED = "2026-10-01T10:00:00Z";

const BOX_100 = {
  id: 11,
  extensionId: 1,
  number: "100",
  name: "Reception",
  unheard: 1,
  total: 2,
};

const BOX_101 = {
  id: 12,
  extensionId: 2,
  number: "101",
  name: "Sales",
  unheard: 0,
  total: 0,
};

const DETAIL = {
  id: 11,
  extensionId: 1,
  email: "reception@hello.lab",
  hasPassword: true,
  greetingObject: "box/11/greeting.wav",
  unreachableObject: "",
  createdAt: CREATED,
  updatedAt: CREATED,
  emailDelivery: true,
};

const UNHEARD = {
  id: 21,
  boxId: 11,
  caller: "201",
  durationMs: 15000,
  heard: false,
  emailStatus: "failed",
  createdAt: "2026-10-03T09:00:00Z",
};

const HEARD = {
  id: 22,
  boxId: 11,
  caller: "+15551230001",
  durationMs: 8000,
  heard: true,
  emailStatus: "sent",
  createdAt: "2026-10-03T10:30:00Z",
};

/** Render /voicemail with two boxes; box 100 holds two messages. */
function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/voicemail/boxes": () => json({ items: [BOX_100, BOX_101] }),
    "GET /api/v1/extensions/1/voicemail": () => json(DETAIL),
    "GET /api/v1/extensions/2/voicemail": () =>
      json({
        ...DETAIL,
        id: 12,
        extensionId: 2,
        email: "",
        hasPassword: false,
        greetingObject: "",
      }),
    "GET /api/v1/voicemail/messages?box=11": () =>
      json({ items: [UNHEARD, HEARD] }),
    "GET /api/v1/voicemail/messages?box=12": () => json({ items: [] }),
    ...extra,
  });
}

beforeEach(() => {
  // jsdom has no media playback; the player only needs play() to resolve.
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
});

describe("Voicemail", () => {
  it("lists the boxes with unheard counts and summarises the selected box", async () => {
    setup();
    renderApp("/voicemail");
    const boxes = await screen.findByRole("list", { name: "Voicemail boxes" });
    const first = within(boxes).getByRole("button", { name: /100/ });
    expect(first).toHaveAttribute("aria-current", "true");
    expect(first).toHaveTextContent("1 unheard");
    expect(
      within(boxes).getByRole("button", { name: /101/ }),
    ).not.toHaveTextContent("unheard");

    const summary = screen.getByRole("list", { name: "Box settings summary" });
    await within(summary).findByText("reception@hello.lab");
    expect(within(summary).getByText("PIN set")).toBeVisible();
    expect(within(summary).getByText("Greeting uploaded")).toBeVisible();

    const row = await screen.findByRole("row", { name: /201/ });
    expect(within(row).getByText("New")).toBeVisible();
    expect(within(row).getByText("failed")).toBeVisible();
    expect(
      within(screen.getByRole("row", { name: /\+15551230001/ })).getByText(
        "Heard",
      ),
    ).toBeVisible();
  });

  it("switches boxes and shows the empty state", async () => {
    const calls = setup();
    renderApp("/voicemail");
    await screen.findByRole("row", { name: /201/ });
    fireEvent.click(screen.getByRole("button", { name: /101/ }));
    expect(await screen.findByText("No messages")).toBeVisible();
    expect(await screen.findByText("No email")).toBeVisible();
    expect(screen.getByText("PIN not set")).toBeVisible();
    expect(screen.getByText("Default greeting")).toBeVisible();
    expect(calls.map((c) => c.url)).toContain(
      "/api/v1/voicemail/messages?box=12",
    );
  });

  // Catches the console showing "pending" on every message (and the box's
  // address as if mail went out) on a deployment without SMTP.
  it("says email is not configured when the server cannot send it", async () => {
    setup({
      "GET /api/v1/extensions/1/voicemail": () =>
        json({ ...DETAIL, emailDelivery: false }),
      "GET /api/v1/voicemail/messages?box=11": () =>
        json({ items: [{ ...UNHEARD, emailStatus: "pending" }] }),
    });
    renderApp("/voicemail");
    const summary = await screen.findByRole("list", {
      name: "Box settings summary",
    });
    expect(
      await within(summary).findByText("Email not configured"),
    ).toBeVisible();
    const row = await screen.findByRole("row", { name: /201/ });
    expect(within(row).queryByText("pending")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Box settings" }));
    expect(
      await screen.findByText(/Email delivery is not configured/),
    ).toBeVisible();
  });

  it("shows an empty state when there are no boxes", async () => {
    setup({ "GET /api/v1/voicemail/boxes": () => json({ items: [] }) });
    renderApp("/voicemail");
    expect(await screen.findByText("No voicemail boxes")).toBeVisible();
  });

  it("reports a box list that fails to load", async () => {
    setup({
      "GET /api/v1/voicemail/boxes": () =>
        apiError(500, "internal", "database down"),
    });
    renderApp("/voicemail");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not load the voicemail boxes");
    expect(alert).toHaveTextContent("database down");
  });

  it("plays only messages whose audio answers the gate", async () => {
    const calls = setup({
      "GET /api/v1/voicemail/messages/21/audio": () =>
        new Response("R", {
          status: 206,
          headers: { "Content-Type": "audio/wav" },
        }),
      "GET /api/v1/voicemail/messages/22/audio": () =>
        apiError(404, "not_found", "no audio"),
    });
    renderApp("/voicemail");
    await screen.findByRole("row", { name: /201/ });

    fireEvent.click(
      screen.getByRole("button", {
        name: "Play the message from +15551230001",
      }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "Could not play the message from +15551230001: the audio is not available (HTTP 404).",
    );
    expect(document.querySelector("audio")).toBeNull();
    expect(calls.filter((c) => c.url.endsWith("/audio"))).toHaveLength(1);

    fireEvent.click(
      screen.getByRole("button", { name: "Play the message from 201" }),
    );
    const audio = await screen.findByLabelText(
      "Playback of the message from 201",
    );
    expect(audio).toHaveAttribute("src", "/api/v1/voicemail/messages/21/audio");
    // The player starts from an effect, which may land after the element.
    await waitFor(() =>
      expect(HTMLMediaElement.prototype.play).toHaveBeenCalled(),
    );
    // The card shows the caller and the length from the server.
    expect(screen.getByRole("meter", { name: "201" })).toBeVisible();
    expect(screen.getByText("0:00 / 0:15")).toBeVisible();

    fireEvent.click(screen.getByRole("button", { name: "Stop playing 201" }));
    expect(
      screen.queryByLabelText("Playback of the message from 201"),
    ).toBeNull();
  });

  it("marks a message heard and unheard, keeping the box count in step", async () => {
    const calls = setup({
      "POST /api/v1/voicemail/messages/21/heard": noContent,
    });
    renderApp("/voicemail");
    const row = await screen.findByRole("row", { name: /201/ });

    fireEvent.click(
      within(row).getByRole("button", {
        name: "Mark the message from 201 heard",
      }),
    );
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "POST",
        url: "/api/v1/voicemail/messages/21/heard",
        body: { heard: true },
      }),
    );
    expect(await within(row).findByText("Heard")).toBeVisible();
    expect(
      await screen.findByText("Message from 201 marked heard."),
    ).toBeVisible();
    expect(screen.getByRole("button", { name: /100/ })).not.toHaveTextContent(
      "unheard",
    );

    fireEvent.click(
      within(row).getByRole("button", {
        name: "Mark the message from 201 unheard",
      }),
    );
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "POST",
        url: "/api/v1/voicemail/messages/21/heard",
        body: { heard: false },
      }),
    );
    expect(await within(row).findByText("New")).toBeVisible();
    expect(
      await screen.findByText("Message from 201 marked unheard."),
    ).toBeVisible();
    expect(screen.getByRole("button", { name: /100/ })).toHaveTextContent(
      "1 unheard",
    );
  });

  it("deletes a message after the confirmation and confirms with a toast", async () => {
    const calls = setup({
      "DELETE /api/v1/voicemail/messages/21": noContent,
    });
    renderApp("/voicemail");
    const row = await screen.findByRole("row", { name: /201/ });

    fireEvent.click(
      within(row).getByRole("button", {
        name: "Delete the message from 201",
      }),
    );
    const dialog = screen.getByRole("dialog", { name: "Delete message?" });
    expect(
      within(dialog).getByRole("button", { name: "Cancel" }),
    ).toHaveFocus();
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Delete message" }),
    );
    expect(
      await screen.findByText("Message from 201 deleted."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("row", { name: /201/ })).toBeNull();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(calls).toContainEqual({
      method: "DELETE",
      url: "/api/v1/voicemail/messages/21",
      body: undefined,
    });
  });

  it("keeps the confirmation open with the error when a delete fails", async () => {
    setup({
      "DELETE /api/v1/voicemail/messages/21": () =>
        apiError(500, "internal", "storage offline"),
    });
    renderApp("/voicemail");
    const row = await screen.findByRole("row", { name: /201/ });
    fireEvent.click(
      within(row).getByRole("button", {
        name: "Delete the message from 201",
      }),
    );
    const dialog = screen.getByRole("dialog", { name: "Delete message?" });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Delete message" }),
    );
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "storage offline",
    );
    expect(screen.getByRole("row", { name: /201/ })).toBeInTheDocument();
  });

  it("saves the box settings", async () => {
    const calls = setup({
      "PUT /api/v1/extensions/1/voicemail": () =>
        json({ ...DETAIL, email: "front@hello.lab" }),
    });
    renderApp("/voicemail");
    await screen.findByRole("row", { name: /201/ });
    await screen.findByText("reception@hello.lab");

    fireEvent.click(screen.getByRole("button", { name: "Box settings" }));
    const dialog = screen.getByRole("dialog", {
      name: "Box settings for 100",
    });
    const email = within(dialog).getByLabelText("Email address");
    expect(email).toHaveValue("reception@hello.lab");
    fireEvent.change(email, { target: { value: "front@hello.lab" } });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Save box settings" }),
    );
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PUT",
        url: "/api/v1/extensions/1/voicemail",
        body: { email: "front@hello.lab" },
      }),
    );
    expect(await screen.findByText("Box 100 saved.")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByText("front@hello.lab")).toBeVisible();
  });

  it("uploads a greeting as a multipart part", async () => {
    const calls = setup({
      "PUT /api/v1/extensions/1/voicemail": () => json(DETAIL),
    });
    renderApp("/voicemail");
    await screen.findByText("reception@hello.lab");
    fireEvent.click(screen.getByRole("button", { name: "Box settings" }));

    const input = screen.getByLabelText(
      "Greeting audio (WAV)",
    ) as HTMLInputElement;
    const file = new File(["RIFF...."], "greeting.wav", { type: "audio/wav" });
    Object.defineProperty(input, "files", { value: [file] });
    fireEvent.change(input);
    fireEvent.click(screen.getByRole("button", { name: "Save box settings" }));

    const put = await waitFor(() => {
      const found = calls.find(
        (c) => c.method === "PUT" && c.url === "/api/v1/extensions/1/voicemail",
      );
      expect(found).toBeDefined();
      return found!;
    });
    expect(put.body).toBeInstanceOf(FormData);
    const form = put.body as FormData;
    expect(form.get("greeting")).toBeInstanceOf(File);
    expect((form.get("greeting") as File).name).toBe("greeting.wav");
    expect(form.get("email")).toBe("reception@hello.lab");
  });

  it("checks the settings before sending", async () => {
    const calls = setup();
    renderApp("/voicemail");
    await screen.findByText("reception@hello.lab");
    fireEvent.click(screen.getByRole("button", { name: "Box settings" }));
    fireEvent.change(screen.getByLabelText("New PIN"), {
      target: { value: "12" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save box settings" }));
    expect(
      await screen.findByText("The PIN must be at least 4 characters."),
    ).toBeVisible();
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
  });
});
