import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, onTestFinished, vi } from "vitest";
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

  it("generates a greeting from text through its own button", async () => {
    const calls = setup({
      "GET /api/v1/tts/voices": () => json({ models: [] }),
      "PUT /api/v1/extensions/1/voicemail/greeting": () =>
        json({
          box: {
            ...DETAIL,
            greetingTts: {
              text: "Leave a message.",
              voice: "",
              model: "",
              hash: "h",
              generatedAt: CREATED,
            },
          },
          regenerated: true,
        }),
    });
    renderApp("/voicemail");
    await screen.findByText("reception@hello.lab");
    fireEvent.click(screen.getByRole("button", { name: "Box settings" }));
    const dialog = screen.getByRole("dialog", {
      name: "Box settings for 100",
    });
    fireEvent.change(within(dialog).getByLabelText("Greeting source"), {
      target: { value: "text" },
    });
    fireEvent.change(within(dialog).getByLabelText("Text to speak"), {
      target: { value: "Leave a message." },
    });
    // Typing alone never generates.
    expect(calls.some((c) => c.url.endsWith("/greeting"))).toBe(false);
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Generate greeting" }),
    );
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PUT",
        url: "/api/v1/extensions/1/voicemail/greeting",
        body: { slot: "greeting", source: "text", text: "Leave a message." },
      }),
    );
    expect(
      await within(dialog).findByRole("button", {
        name: "Regenerate greeting",
      }),
    ).toBeVisible();
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
  // The drawer says, per slot, whether callers hear the AI voice or the box's
  // own audio.
  it("shows each greeting as Default (AI voice) or Custom", async () => {
    setup({
      "GET /api/v1/extensions/1/voicemail": () =>
        json({
          ...DETAIL,
          aiVoice: true,
          busyObject: "box/11/busy.wav",
          nameObject: "",
        }),
    });
    renderApp("/voicemail");
    await screen.findByText("reception@hello.lab");
    fireEvent.click(screen.getByRole("button", { name: "Box settings" }));
    const dialog = screen.getByRole("dialog", { name: "Box settings for 100" });
    const slot = (name: string) =>
      within(within(dialog).getByRole("group", { name }));
    expect(slot("Greeting").getByText("Custom")).toBeVisible();
    expect(slot("Busy greeting").getByText("Custom")).toBeVisible();
    expect(slot("Unreachable greeting").getByText("Default (AI voice)")).toBeVisible();
    expect(slot("Recorded name").getByText("Default (AI voice)")).toBeVisible();
  });

  it("says when there is no voice to fall back on", async () => {
    setup({
      "GET /api/v1/extensions/1/voicemail": () =>
        json({ ...DETAIL, aiVoice: false, greetingObject: "" }),
    });
    renderApp("/voicemail");
    await screen.findByText("reception@hello.lab");
    fireEvent.click(screen.getByRole("button", { name: "Box settings" }));
    const dialog = screen.getByRole("dialog", { name: "Box settings for 100" });
    expect(
      within(within(dialog).getByRole("group", { name: "Greeting" })).getByText(
        "Default (no voice set)",
      ),
    ).toBeVisible();
  });

  it("previews the default greeting by asking the server to speak it", async () => {
    const created: Blob[] = [];
    // jsdom has no object URLs; give it just these two, and take them away
    // again afterwards.
    const url = URL as unknown as Record<string, unknown>;
    url.createObjectURL = (b: Blob) => {
      created.push(b);
      return "blob:preview";
    };
    url.revokeObjectURL = vi.fn();
    onTestFinished(() => {
      delete url.createObjectURL;
      delete url.revokeObjectURL;
    });
    const calls = setup({
      "GET /api/v1/extensions/1/voicemail": () =>
        json({ ...DETAIL, aiVoice: true }),
      "POST /api/v1/extensions/1/voicemail/greeting/preview": () =>
        new Response(new Uint8Array([82, 73, 70, 70]), {
          status: 200,
          headers: { "Content-Type": "audio/wav" },
        }),
    });
    renderApp("/voicemail");
    await screen.findByText("reception@hello.lab");
    fireEvent.click(screen.getByRole("button", { name: "Box settings" }));
    const dialog = screen.getByRole("dialog", { name: "Box settings for 100" });
    // Nothing is spoken until the button is pressed.
    expect(calls.some((c) => c.url.endsWith("/preview"))).toBe(false);
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Preview default greeting" }),
    );
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "POST",
        url: "/api/v1/extensions/1/voicemail/greeting/preview",
        body: { slot: "greeting" },
      }),
    );
    const player = await within(dialog).findByLabelText(
      "Preview of the default greeting",
    );
    expect(player).toHaveAttribute("src", "blob:preview");
    expect(created).toHaveLength(1);
    // The busy variant previews its own wording.
    fireEvent.click(
      within(dialog).getByRole("button", {
        name: "Preview default busy greeting",
      }),
    );
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "POST",
        url: "/api/v1/extensions/1/voicemail/greeting/preview",
        body: { slot: "busy" },
      }),
    );
    // A recorded name has no spoken default to preview.
    expect(
      within(dialog).queryByRole("button", {
        name: /Preview default recorded name/,
      }),
    ).toBeNull();
  });

  it("explains a preview that cannot be spoken", async () => {
    setup({
      "POST /api/v1/extensions/1/voicemail/greeting/preview": () =>
        apiError(409, "conflict", "no OpenRouter API key is configured"),
    });
    renderApp("/voicemail");
    await screen.findByText("reception@hello.lab");
    fireEvent.click(screen.getByRole("button", { name: "Box settings" }));
    const dialog = screen.getByRole("dialog", { name: "Box settings for 100" });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Preview default greeting" }),
    );
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "no OpenRouter API key is configured",
    );
  });

  it("reverts a custom greeting to the default", async () => {
    const calls = setup({
      "GET /api/v1/extensions/1/voicemail": () =>
        json({ ...DETAIL, aiVoice: true }),
      "DELETE /api/v1/extensions/1/voicemail/greeting/greeting": () =>
        json({ ...DETAIL, aiVoice: true, greetingObject: "" }),
    });
    renderApp("/voicemail");
    await screen.findByText("reception@hello.lab");
    fireEvent.click(screen.getByRole("button", { name: "Box settings" }));
    const dialog = screen.getByRole("dialog", { name: "Box settings for 100" });
    // Only a custom slot offers the revert.
    expect(
      within(dialog).queryByRole("button", {
        name: "Revert busy greeting to default",
      }),
    ).toBeNull();
    fireEvent.click(
      within(dialog).getByRole("button", {
        name: "Revert greeting to default",
      }),
    );
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "DELETE",
        url: "/api/v1/extensions/1/voicemail/greeting/greeting",
        body: undefined,
      }),
    );
    expect(
      await screen.findByText("Greeting of box 100 is back to the default."),
    ).toBeInTheDocument();
    const group = within(dialog).getByRole("group", { name: "Greeting" });
    expect(await within(group).findByText("Default (AI voice)")).toBeVisible();
    expect(
      within(dialog).queryByRole("button", {
        name: "Revert greeting to default",
      }),
    ).toBeNull();
  });

  it("uploads the busy greeting and the recorded name", async () => {
    const calls = setup({
      "PUT /api/v1/extensions/1/voicemail": () => json(DETAIL),
    });
    renderApp("/voicemail");
    await screen.findByText("reception@hello.lab");
    fireEvent.click(screen.getByRole("button", { name: "Box settings" }));
    const attach = (label: string, name: string) => {
      const input = screen.getByLabelText(label) as HTMLInputElement;
      Object.defineProperty(input, "files", {
        value: [new File(["RIFF...."], name, { type: "audio/wav" })],
      });
      fireEvent.change(input);
    };
    attach("Busy greeting audio (WAV)", "busy.wav");
    attach("Recorded name audio (WAV)", "name.wav");
    fireEvent.click(screen.getByRole("button", { name: "Save box settings" }));
    const put = await waitFor(() => {
      const found = calls.find(
        (c) => c.method === "PUT" && c.url === "/api/v1/extensions/1/voicemail",
      );
      expect(found).toBeDefined();
      return found!;
    });
    const form = put.body as FormData;
    expect((form.get("busy") as File).name).toBe("busy.wav");
    expect((form.get("name") as File).name).toBe("name.wav");
    expect(form.get("greeting")).toBeNull();
  });

  it("names a caller by the caller-ID name when the message has one", async () => {
    setup({
      "GET /api/v1/voicemail/messages?box=11": () =>
        json({ items: [{ ...UNHEARD, callerName: "Acme Ltd" }] }),
    });
    renderApp("/voicemail");
    expect(await screen.findByText("Acme Ltd (201)")).toBeVisible();
  });
});
