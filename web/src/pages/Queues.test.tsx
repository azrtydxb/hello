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
});
