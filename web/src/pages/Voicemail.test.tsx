import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, ME, mockApi, noContent, renderApp } from "../test/api";

const CREATED = "2026-10-01T10:00:00Z";

const EXT = {
  id: 1,
  number: "100",
  name: "Reception",
  dnd: false,
  forwardAlways: "",
  forwardBusy: "",
  forwardNoAnswer: "",
  voicemailEnabled: true,
  voicemailBoxId: 11,
  createdAt: CREATED,
  updatedAt: CREATED,
};

const BOX = {
  id: 11,
  extensionId: 1,
  email: "",
  hasPassword: true,
  hasGreeting: false,
  hasUnreachableGreeting: false,
  createdAt: CREATED,
  updatedAt: CREATED,
};

const UNHEARD = {
  id: 21,
  boxId: 11,
  caller: "201",
  durationMs: 15000,
  heard: false,
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

/** Render /voicemail with one box and two messages loaded. */
function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/extensions": () => json({ items: [EXT] }),
    "GET /api/v1/extensions/1/voicemail": () => json(BOX),
    "GET /api/v1/voicemail/messages?box=11": () =>
      json({ items: [UNHEARD, HEARD] }),
    ...extra,
  });
}

describe("Voicemail", () => {
  it("plays only messages whose audio answers the gate", async () => {
    const calls = setup({
      "GET /api/v1/voicemail/messages/21/audio": () =>
        new Response(null, {
          status: 302,
          headers: { Location: "https://minio.example/box/11/a.wav" },
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
    const audio = (await screen.findByLabelText(
      "Playback of the message from 201",
    )) as HTMLAudioElement;
    expect(audio).toHaveAttribute("src", "/api/v1/voicemail/messages/21/audio");
  });

  it("marks a message heard and unheard", async () => {
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
    expect(await within(row).findByText("Yes")).toBeVisible();

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
  });

  it("deletes a message after the in-page confirmation", async () => {
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
    const confirm = screen.getByRole("group", {
      name: "Delete the message from 201?",
    });
    expect(
      within(confirm).getByRole("button", { name: "Cancel" }),
    ).toHaveFocus();
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Delete message" }),
    );
    await screen.findByRole("row", { name: /\+15551230001/ });
    expect(screen.queryByRole("row", { name: /201/ })).toBeNull();
    expect(calls).toContainEqual({
      method: "DELETE",
      url: "/api/v1/voicemail/messages/21",
      body: undefined,
    });
  });

  it("saves the box settings", async () => {
    const calls = setup({
      "PUT /api/v1/extensions/1/voicemail": () =>
        json({ ...BOX, email: "reception@example.com" }),
    });
    renderApp("/voicemail");
    await screen.findByRole("row", { name: /201/ });

    fireEvent.click(
      screen.getByRole("button", { name: "Box settings for 100" }),
    );
    const email = await screen.findByLabelText("Email address");
    await waitFor(() => expect(email).toBeEnabled());
    fireEvent.change(email, {
      target: { value: "reception@example.com" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save box settings" }));
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PUT",
        url: "/api/v1/extensions/1/voicemail",
        body: { email: "reception@example.com" },
      }),
    );
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("uploads a greeting as a multipart part", async () => {
    const calls = setup({
      "PUT /api/v1/extensions/1/voicemail": () => json(BOX),
    });
    renderApp("/voicemail");
    await screen.findByRole("row", { name: /201/ });

    fireEvent.click(
      screen.getByRole("button", { name: "Box settings for 100" }),
    );
    await waitFor(() =>
      expect(screen.getByLabelText("Greeting audio (WAV)")).toBeEnabled(),
    );
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
    expect(form.get("email")).toBe("");
  });

  it("refetches unheard-only from the query parameter", async () => {
    const calls = setup({
      "GET /api/v1/voicemail/messages?box=11&unheard=true": () =>
        json({ items: [UNHEARD] }),
    });
    renderApp("/voicemail");
    await screen.findByRole("row", { name: /\+15551230001/ });

    fireEvent.click(screen.getByLabelText("Unheard only"));
    expect(await screen.findByRole("row", { name: /201/ })).toBeInTheDocument();
    expect(screen.queryByRole("row", { name: /\+15551230001/ })).toBeNull();
    expect(
      calls.some(
        (c) =>
          c.method === "GET" &&
          c.url === "/api/v1/voicemail/messages?box=11&unheard=true",
      ),
    ).toBe(true);
  });
});
