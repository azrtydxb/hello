import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, ME, mockApi, renderApp } from "../test/api";

const SETTINGS = {
  provider: "openrouter",
  configured: true,
  keyHint: "ab12",
  model: "elevenlabs/eleven-multilingual-v2",
  voice: "sarah",
  updatedAt: "2026-10-04T09:00:00Z",
};

const VOICES = {
  models: [
    {
      id: "elevenlabs/eleven-multilingual-v2",
      name: "Eleven Multilingual v2",
      voices: ["sarah", "george"],
    },
  ],
};

describe("Voice settings", () => {
  it("shows the key as stored without revealing it, and saves without resending it", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/tts/settings": () => json(SETTINGS),
      "GET /api/v1/tts/voices": () => json(VOICES),
      "PUT /api/v1/tts/settings": () => json({ ...SETTINGS, voice: "george" }),
    });
    renderApp("/settings/voice");
    expect(await screen.findByText("Key stored")).toBeVisible();
    expect(screen.getByLabelText("OpenRouter API key")).toHaveValue("");
    expect(screen.getByText(/ending in ab12/)).toBeVisible();
    await screen.findByRole("option", { name: "george" });
    fireEvent.change(screen.getByLabelText("Voice"), {
      target: { value: "george" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Save voice settings" }),
    );
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.body).toEqual({
      model: "elevenlabs/eleven-multilingual-v2",
      voice: "george",
    });
  });

  it("sends a pasted key once and clears the field", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/tts/settings": () =>
        json({ ...SETTINGS, configured: false, keyHint: "" }),
      "PUT /api/v1/tts/settings": () => json(SETTINGS),
    });
    renderApp("/settings/voice");
    const field = await screen.findByLabelText("OpenRouter API key");
    fireEvent.change(field, { target: { value: "pasted-key-value" } });
    fireEvent.click(
      screen.getByRole("button", { name: "Save voice settings" }),
    );
    await waitFor(() => expect(field).toHaveValue(""));
    const put = calls.find((c) => c.method === "PUT");
    expect((put?.body as { apiKey: string }).apiKey).toBe("pasted-key-value");
    expect(screen.getByText("Key stored")).toBeVisible();
  });

  it("reports a failed connection test", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/tts/settings": () => json(SETTINGS),
      "GET /api/v1/tts/voices": () => json(VOICES),
      "POST /api/v1/tts/settings/test": () =>
        apiError(502, "upstream", "OpenRouter rejected the API key (401)"),
    });
    renderApp("/settings/voice");
    await screen.findByText("Key stored");
    fireEvent.click(screen.getByRole("button", { name: "Test connection" }));
    expect(await screen.findByText(/rejected the API key/)).toBeVisible();
  });
});
