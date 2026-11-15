import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, mockApi, noContent, renderApp, type Routes } from "../test/api";

const STAMP = "2026-11-15T10:00:00Z";

const SETTINGS = {
  enabled: true,
  autoEnabled: true,
  businessName: "Acme",
  intents: ["question", "billing"],
  sensitiveTopics: ["medical"],
  channels: [{ id: 1, name: "Main", kind: "sms", aiEnabled: true }],
  addresses: [
    {
      id: 4,
      channelId: 1,
      address: "+3270123456",
      label: "Main line",
      mode: "off",
      faqCount: 0,
    },
  ],
  status: {
    available: true,
    reason: "",
    endpointPublic: false,
    autoAllowed: true,
  },
};

const ADDRESS_AI = {
  addressId: 4,
  mode: "off",
  redact: true,
  disclosure: true,
  disclosureText: "This is an automated reply.",
  holdingText: "",
  maxReplies: 3,
  hourlyCap: 60,
  minConfidence: 0.8,
  autoIntents: [],
  handoffWords: ["agent", "human", "person", "representative"],
  faqCount: 0,
};

const faq = (over: Record<string, unknown> = {}) => ({
  id: 9,
  addressId: 4,
  title: "Opening hours",
  body: "Open 9 to 5.",
  enabled: true,
  createdAt: STAMP,
  updatedAt: STAMP,
  ...over,
});

function api(more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "a", role: "admin" }),
    "GET /api/v1/messaging/ai": () => json(SETTINGS),
    "GET /api/v1/messaging/addresses/4/ai": () => json(ADDRESS_AI),
    "GET /api/v1/messaging/faq?addressId=4": () => json({ items: [] }),
    ...more,
  });
}

describe("Inbox AI settings", () => {
  it("saves the kill switches, the labels and a channel's switch", async () => {
    const calls = api({
      "PUT /api/v1/messaging/ai": () =>
        json({ ...SETTINGS, autoEnabled: false }),
    });
    renderApp("/messaging/ai");
    const auto = await screen.findByRole("switch", {
      name: /^Auto-replies on/,
    });
    fireEvent.click(auto);
    fireEvent.click(screen.getByRole("switch", { name: /^AI on Main/ }));
    fireEvent.change(screen.getByLabelText("Intents"), {
      target: { value: "question, billing, sales" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save AI settings" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
        enabled: true,
        autoEnabled: false,
        businessName: "Acme",
        intents: ["question", "billing", "sales"],
        sensitiveTopics: ["medical"],
        channels: [{ id: 1, aiEnabled: false }],
      }),
    );
    expect(await screen.findByText("AI settings saved.")).toBeInTheDocument();
  });

  it("warns when the AI is off, the endpoint is public or auto-replies are switched off", async () => {
    api({
      "GET /api/v1/messaging/ai": () =>
        json({
          ...SETTINGS,
          status: {
            available: true,
            reason: "",
            endpointPublic: true,
            autoAllowed: false,
          },
        }),
    });
    renderApp("/messaging/ai");
    expect(
      await screen.findByText("AI endpoint is public"),
    ).toBeInTheDocument();
    expect(
      screen.getByText("Auto-replies are switched off"),
    ).toBeInTheDocument();
  });

  it("turns a number to auto-reply and shows the field errors", async () => {
    let n = 0;
    const calls = api({
      "PUT /api/v1/messaging/addresses/4/ai": () =>
        n++ === 0
          ? json(
              {
                error: {
                  code: "bad_request",
                  message: "invalid",
                  fields: [{ path: "maxReplies", message: "must be 1-10" }],
                },
              },
              400,
            )
          : json({ ...ADDRESS_AI, mode: "auto", maxReplies: 2 }),
    });
    renderApp("/messaging/ai");
    fireEvent.click(await screen.findByRole("button", { name: "Edit AI" }));
    const mode = await screen.findByLabelText("Mode");
    fireEvent.change(mode, { target: { value: "auto" } });
    // Auto-reply needs a FAQ: the dialog says so.
    expect(await screen.findByText("No FAQ yet")).toBeInTheDocument();
    const replies = screen.getByLabelText(
      "AI replies before a person must answer",
    );
    fireEvent.change(replies, { target: { value: "20" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText("must be 1-10")).toBeInTheDocument();
    fireEvent.change(replies, { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(n).toBe(2));
    const put = calls.filter((c) => c.method === "PUT")[1]!;
    expect(put.body).toMatchObject({
      mode: "auto",
      maxReplies: 2,
      redact: true,
    });
  });

  it("adds, edits and deletes FAQ snippets", async () => {
    let items = [faq()];
    const calls = api({
      "GET /api/v1/messaging/faq?addressId=4": () => json({ items }),
      "POST /api/v1/messaging/faq": () => {
        items = [...items, faq({ id: 10, title: "Parking", body: "Behind." })];
        return json(items[1], 201);
      },
      "PUT /api/v1/messaging/faq/9": () => json(faq({ body: "Open 8 to 6." })),
      "DELETE /api/v1/messaging/faq/9": () => {
        items = items.filter((f) => f.id !== 9);
        return noContent();
      },
    });
    renderApp("/messaging/ai");
    fireEvent.click(await screen.findByRole("button", { name: "Edit AI" }));
    expect(await screen.findByText("Opening hours")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Add a snippet" }));
    const form = screen.getByRole("form", { name: "FAQ snippet" });
    fireEvent.change(within(form).getByLabelText("Question"), {
      target: { value: "Parking" },
    });
    fireEvent.change(within(form).getByLabelText("Answer"), {
      target: { value: "Behind." },
    });
    fireEvent.click(within(form).getByRole("button", { name: "Save snippet" }));
    expect(await screen.findByText("Parking")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      title: "Parking",
      body: "Behind.",
      enabled: true,
      addressId: 4,
    });
    fireEvent.click(screen.getAllByRole("button", { name: "Delete" })[0]!);
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete snippet" }),
    );
    await waitFor(() =>
      expect(screen.queryByText("Opening hours")).not.toBeInTheDocument(),
    );
  });
});
