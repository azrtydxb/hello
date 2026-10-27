import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../test/api";

const CREATED = "2026-10-25T10:00:00Z";
const EXTENSIONS = [
  {
    id: 1,
    number: "100",
    name: "Reception",
    createdAt: CREATED,
    updatedAt: CREATED,
  },
  {
    id: 2,
    number: "200",
    name: "Kitchen",
    createdAt: CREATED,
    updatedAt: CREATED,
  },
];

const QUEUE = {
  id: 7,
  name: "sales",
  number: "600",
  enabled: true,
  strategy: "longest_idle",
  agentTimeout: 20,
  retryDelay: 5,
  wrapUp: 10,
  maxCallers: 0,
  maxWait: 600,
  joinEmpty: true,
  leaveEmpty: false,
  timeout: { kind: "hangup", target: "" },
  full: { kind: "hangup", target: "" },
  noAgents: { kind: "queue", target: "overflow" },
  mohClassId: null,
  announceFrequency: 0,
  announcePosition: false,
  announceWait: false,
  periodicPromptId: null,
  periodicPromptName: "",
  positionPromptId: null,
  positionPromptName: "",
  waitPromptId: null,
  waitPromptName: "",
  minutesPromptId: null,
  minutesPromptName: "",
  digitsPrefix: "",
  breakoutKey: "",
  breakoutPromptId: null,
  breakoutPromptName: "",
  breakoutVoicemail: "",
  members: [
    {
      extensionId: 1,
      number: "100",
      name: "Reception",
      dynamic: true,
      penalty: 2,
      position: 1,
    },
  ],
  createdAt: CREATED,
  updatedAt: CREATED,
};

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/extensions": () => json({ items: EXTENSIONS }),
    "GET /api/v1/announcements": () => json({ items: [] }),
    "GET /api/v1/ring-groups": () => json({ items: [] }),
    "GET /api/v1/paging-groups": () => json({ items: [] }),
    "GET /api/v1/ivr-menus": () => json({ items: [] }),
    "GET /api/v1/moh-classes": () => json({ items: [] }),
    "GET /api/v1/queues": () => json({ items: [QUEUE] }),
    ...extra,
  });
}

describe("Queues", () => {
  it("lists a queue with its agents and failover destinations", async () => {
    setup();
    renderApp("/queues");
    expect(await screen.findByText("sales")).toBeVisible();
    expect(screen.getByText("Reception")).toBeVisible();
    expect(screen.getByText(/penalty 2/)).toBeVisible();
    expect(screen.getByText("Call queue overflow")).toBeVisible();
  });

  it("creates a queue with an agent", async () => {
    const calls = setup({
      "GET /api/v1/queues": () => json({ items: [] }),
      "POST /api/v1/queues": () => json(QUEUE, 201),
    });
    renderApp("/queues");
    const [create] = await screen.findAllByRole("button", {
      name: "New queue",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "sales" },
    });
    const add = screen.getByLabelText("Add an agent");
    await waitFor(() => expect(add.querySelectorAll("option").length).toBe(3));
    fireEvent.change(add, { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Add agent" }));
    fireEvent.click(screen.getByRole("button", { name: "Create queue" }));
    expect(await screen.findByText("Queue sales created.")).toBeVisible();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toMatchObject({
      name: "sales",
      strategy: "ring_all",
      agentTimeout: 20,
      maxWait: 600,
      noAgents: { kind: "hangup", target: "" },
      members: [{ extensionId: 1, dynamic: false, penalty: 0, position: 1 }],
    });
  });

  it("refuses a queue that announces the position without a prompt", async () => {
    const calls = setup({ "GET /api/v1/queues": () => json({ items: [] }) });
    renderApp("/queues");
    const [create] = await screen.findAllByRole("button", {
      name: "New queue",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "sales" },
    });
    fireEvent.click(screen.getByLabelText("Announce the position"));
    fireEvent.click(screen.getByRole("button", { name: "Create queue" }));
    expect(
      await screen.findByText(/Set an interval and the digits prefix/),
    ).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("saves the callback settings and the prompt language", async () => {
    const calls = setup({
      "GET /api/v1/queues": () => json({ items: [] }),
      "POST /api/v1/queues": () => json(QUEUE, 201),
    });
    renderApp("/queues");
    const [create] = await screen.findAllByRole("button", {
      name: "New queue",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "sales" },
    });
    fireEvent.click(
      screen.getByLabelText("Offer a callback to callers who would wait long"),
    );
    fireEvent.change(screen.getByLabelText("Offer after (s)"), {
      target: { value: "45" },
    });
    fireEvent.change(screen.getByLabelText("Callback key"), {
      target: { value: "5" },
    });
    fireEvent.change(screen.getByLabelText("Prompt language"), {
      target: { value: "nl" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create queue" }));
    expect(await screen.findByText("Queue sales created.")).toBeVisible();
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({
      callbackEnabled: true,
      callbackOfferAfterSeconds: 45,
      callbackDigit: "5",
      promptLanguage: "nl",
    });
  });

  it("refuses a callback key that is the voicemail key", async () => {
    const calls = setup({ "GET /api/v1/queues": () => json({ items: [] }) });
    renderApp("/queues");
    const [create] = await screen.findAllByRole("button", {
      name: "New queue",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "sales" },
    });
    fireEvent.change(screen.getByLabelText("Voicemail key"), {
      target: { value: "2" },
    });
    fireEvent.click(
      screen.getByLabelText("Offer a callback to callers who would wait long"),
    );
    fireEvent.click(screen.getByRole("button", { name: "Create queue" }));
    expect(
      await screen.findByText("Must differ from the voicemail key."),
    ).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("links a queue with callbacks on to its requests", async () => {
    setup({
      "GET /api/v1/queues": () =>
        json({ items: [{ ...QUEUE, callbackEnabled: true }] }),
    });
    renderApp("/queues");
    const link = await screen.findByRole("link", {
      name: "Callbacks of queue sales",
    });
    expect(link).toHaveAttribute("href", "/queues/7/callbacks");
  });
});
