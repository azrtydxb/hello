import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { AIProposal } from "../../../api/aiagent";
import { json, mockApi, renderApp, type Routes } from "../../../test/api";
import { CANVAS, CATALOGUE, GRAPH } from "../fixtures";

const PROPOSAL: AIProposal = {
  id: "p1",
  source: "assistant",
  sessionId: "s1",
  findingId: null,
  title: "Send the main number to a new menu",
  rationale: "The sales group is replaced by an after-hours menu.",
  status: "open",
  configRevision: GRAPH.configRevision,
  hasDelete: true,
  createdBy: 2,
  createdAt: "2026-11-08T10:00:00Z",
  updatedAt: "2026-11-08T10:00:00Z",
  appliedBy: null,
  appliedAt: null,
  dismissedBy: null,
  dismissedAt: null,
  dismissReason: null,
  dismissText: null,
  failure: null,
  appliedVia: null,
  batchId: null,
  actions: [
    {
      operationId: "applyConfigBatch",
      pathParams: {},
      body: { baseRevision: GRAPH.configRevision, ops: [] },
      destructive: true,
      preview: {
        baseRevision: GRAPH.configRevision,
        results: [
          {
            operationId: "createIVRMenu",
            status: "created",
            node: "ivr:90",
            before: null,
            after: {},
            diff: [],
          },
          {
            operationId: "deleteRingGroup",
            status: "deleted",
            node: "ringGroup:1",
            before: {},
            after: null,
            diff: [],
          },
        ],
        warnings: [],
        flowDiff: {
          added: [
            { id: "ivr:90", kind: "ivr", label: "After hours", summary: {} },
          ],
          changed: [],
          removed: ["ringGroup:1"],
          edgesAdded: [
            {
              id: "inboundRoute:1.destination>ivr:90.call",
              source: { node: "inboundRoute:1", port: "destination" },
              target: { node: "ivr:90", port: "call" },
              owner: { node: "inboundRoute:1", field: "destination" },
            },
          ],
          edgesRemoved: ["inboundRoute:1.destination>ringGroup:1.ring"],
        },
      },
    },
  ],
};

const ON = { enabled: true, reason: null, agents: [] };

function api(role: string, extra: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "u", role }),
    "GET /api/v1/flow/catalogue": () => json(CATALOGUE),
    "GET /api/v1/flow/graph": () => json(GRAPH),
    "GET /api/v1/flow/revision": () =>
      json({ configRevision: GRAPH.configRevision }),
    "GET /api/v1/flow/canvases/1": () => json(CANVAS),
    "GET /api/v1/ai/status": () => json(ON),
    "GET /api/v1/ai/proposals?status=open": () => json({ items: [PROPOSAL] }),
    "GET /api/v1/ai/proposals/p1": () => json(PROPOSAL),
    ...extra,
  });
}

async function open(path = "/flow/1") {
  renderApp(path);
  await screen.findByRole(
    "heading",
    { name: "Main office" },
    { timeout: 15_000 },
  );
  await screen.findByText("Main number");
}

const nodeEl = (id: string) =>
  document.querySelector<HTMLElement>(`.react-flow__node[data-id="${id}"]`);

/** A finished answer as the server streams it. */
function stream(proposalId: string | null) {
  const message = {
    id: 2,
    role: "assistant",
    content: "I replaced the sales group with a menu.",
    toolCalls: [],
    citations: [],
    proposalId,
    taskId: "t1",
    createdAt: "2026-11-08T10:00:01Z",
  };
  const frames = [
    `event: started\ndata: ${JSON.stringify({ taskId: "t1", messageId: 1 })}\n\n`,
    `event: done\ndata: ${JSON.stringify({
      taskId: "t1",
      messageId: 1,
      message,
      proposalId,
      usage: { inputTokens: 1, outputTokens: 1, reasoningTokens: 0, calls: 1 },
    })}\n\n`,
  ].join("");
  return () =>
    new Response(frames, {
      status: 200,
      headers: { "Content-Type": "text/event-stream" },
    });
}

describe("AI on the flow canvas", () => {
  it("says the agent is off and offers nothing to send without a model", async () => {
    api("operator", {
      "GET /api/v1/ai/status": () =>
        json({ enabled: false, reason: "not_configured", agents: [] }),
    });
    await open();
    fireEvent.click(screen.getByRole("button", { name: "Ask AI" }));
    const panel = await screen.findByRole("complementary", { name: "Ask AI" });
    expect(
      await within(panel).findByText("The AI agent is off"),
    ).toBeInTheDocument();
    expect(within(panel).queryByRole("button", { name: "Send" })).toBeNull();
  });

  it("draws a proposal from ?proposal= for a viewer, without apply or asking", async () => {
    const calls = api("viewer");
    await open("/flow/1?proposal=p1");
    const panel = await screen.findByRole("complementary", { name: "Ask AI" });
    const review = await within(panel).findByRole("region", {
      name: `Proposal: ${PROPOSAL.title}`,
    });
    expect(within(review).getByText("1 new")).toBeInTheDocument();
    expect(within(review).queryByRole("button", { name: "Apply" })).toBeNull();
    expect(
      within(panel).getByText(/needs the operator role/),
    ).toBeInTheDocument();
    expect(within(panel).queryByRole("button", { name: "Send" })).toBeNull();
    // A viewer can never let the AI apply.
    expect(
      within(panel).queryByRole("switch", { name: /Let AI apply changes/ }),
    ).toBeNull();
    await waitFor(() =>
      expect(nodeEl("ivr:90")).toHaveClass("flow-node--ai-added"),
    );
    expect(nodeEl("ringGroup:1")).toHaveClass("flow-node--ai-removed");
    // Nothing is applied by showing it.
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("sends the canvas context, draws the proposal and applies it only after confirmation", async () => {
    const applied = { ...PROPOSAL, status: "applied" };
    const calls = api("operator", {
      "POST /api/v1/ai/sessions": () =>
        json(
          { id: "s1", owner: 2, title: "", createdAt: "", lastActiveAt: "" },
          201,
        ),
      "POST /api/v1/ai/sessions/s1/messages": stream("p1"),
      "POST /api/v1/ai/proposals/p1/apply": () => json(applied),
    });
    await open();
    fireEvent.click(screen.getByRole("button", { name: "Ask AI" }));
    const panel = await screen.findByRole("complementary", { name: "Ask AI" });
    fireEvent.change(
      await within(panel).findByLabelText("Ask, or describe a change"),
      { target: { value: "Replace sales with a menu" } },
    );
    // Ask first is the default.
    expect(
      within(panel).getByRole("switch", { name: /Let AI apply changes/ }),
    ).not.toBeChecked();
    fireEvent.click(within(panel).getByRole("button", { name: "Send" }));

    expect(
      await within(panel).findByText("I replaced the sales group with a menu."),
    ).toBeInTheDocument();
    const post = calls.find((c) => c.url === "/api/v1/ai/sessions/s1/messages");
    expect(post?.body).toEqual({
      content: "Replace sales with a menu",
      context: { canvasId: 1 },
    });
    await waitFor(() =>
      expect(nodeEl("ivr:90")).toHaveClass("flow-node--ai-added"),
    );
    // The proposal was drawn, not applied.
    expect(calls.some((c) => c.url.endsWith("/apply"))).toBe(false);

    const review = within(panel).getByRole("region", {
      name: `Proposal: ${PROPOSAL.title}`,
    });
    fireEvent.click(within(review).getByRole("button", { name: "Apply" }));
    const dialog = await screen.findByRole("dialog", {
      name: "Apply this proposal?",
    });
    expect(calls.some((c) => c.url.endsWith("/apply"))).toBe(false);
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Apply and delete" }),
    );
    await screen.findByText(/Applied the AI proposal/);
    expect(
      calls.filter((c) => c.url === "/api/v1/ai/proposals/p1/apply"),
    ).toHaveLength(1);
    await waitFor(() => expect(nodeEl("ivr:90")).toBeNull());
  });

  it("discards a proposal as not needed and takes it off the canvas", async () => {
    const calls = api("operator", {
      "POST /api/v1/ai/proposals/p1/dismiss": () =>
        json({ ...PROPOSAL, status: "dismissed" }),
    });
    await open("/flow/1?proposal=p1");
    const panel = await screen.findByRole("complementary", { name: "Ask AI" });
    const review = await within(panel).findByRole("region", {
      name: `Proposal: ${PROPOSAL.title}`,
    });
    await waitFor(() => expect(nodeEl("ivr:90")).not.toBeNull());
    fireEvent.click(within(review).getByRole("button", { name: "Discard" }));
    const dialog = await screen.findByRole("dialog", {
      name: "Discard this proposal?",
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Discard" }));
    await waitFor(() => expect(nodeEl("ivr:90")).toBeNull());
    const dismiss = calls.find((c) => c.url.endsWith("/dismiss"));
    expect(dismiss?.body).toEqual({ reason: "not_needed" });
    expect(calls.some((c) => c.url.endsWith("/apply"))).toBe(false);
  });

  it("with Let AI apply changes on, shows what the assistant applied, marked, and reverts it", async () => {
    const aiApplied: AIProposal = {
      ...PROPOSAL,
      id: "p2",
      status: "applied",
      appliedBy: 2,
      appliedAt: "2026-11-10T10:00:02Z",
      appliedVia: "ai:s1",
      batchId: "b9",
      actions: [
        {
          ...PROPOSAL.actions[0]!,
          preview: {
            ...PROPOSAL.actions[0]!.preview!,
            flowDiff: {
              ...PROPOSAL.actions[0]!.preview!.flowDiff,
              changed: [GRAPH.nodes[0]!],
            },
          },
        },
      ],
    };
    let reverted = false;
    const calls = api("operator", {
      "POST /api/v1/ai/sessions": () =>
        json(
          { id: "s1", owner: 2, title: "", createdAt: "", lastActiveAt: "" },
          201,
        ),
      "POST /api/v1/ai/sessions/s1/messages": stream("p2"),
      "GET /api/v1/ai/proposals/p2": () => json(aiApplied),
      "POST /api/v1/config/batches/b9/revert": () => {
        const sent = calls.filter((c) => c.url.endsWith("/revert")).at(-1)!;
        if (!(sent.body as { dryRun: boolean }).dryRun) reverted = true;
        return json({
          applied: !(sent.body as { dryRun: boolean }).dryRun,
          baseRevision: 12,
          revision: 13,
          results: [
            {
              operationId: "deleteIVRMenu",
              status: "deleted",
              node: "ivr:90",
              before: {},
              after: null,
              diff: [],
            },
          ],
          warnings: [],
        });
      },
    });
    await open();
    fireEvent.click(screen.getByRole("button", { name: "Ask AI" }));
    const panel = await screen.findByRole("complementary", { name: "Ask AI" });
    const toggle = await within(panel).findByRole("switch", {
      name: /Let AI apply changes/,
    });
    fireEvent.click(toggle);
    expect(toggle).toBeChecked();
    fireEvent.change(
      within(panel).getByLabelText("Ask, or describe a change"),
      {
        target: { value: "Replace sales with a menu" },
      },
    );
    fireEvent.click(within(panel).getByRole("button", { name: "Send" }));

    const review = await within(panel).findByRole("region", {
      name: `Applied by the assistant: ${PROPOSAL.title}`,
    });
    const post = calls.find((c) => c.url === "/api/v1/ai/sessions/s1/messages");
    expect(post?.body).toEqual({
      content: "Replace sales with a menu",
      context: { canvasId: 1, allowApply: true },
    });
    expect(
      within(review).getByText(/applied this for you/),
    ).toBeInTheDocument();
    expect(
      within(panel).getByRole("link", {
        name: "What the assistant applied, with its full diff",
      }),
    ).toBeInTheDocument();
    // The console applied nothing itself: the assistant did.
    expect(calls.some((c) => c.url.endsWith("/apply"))).toBe(false);
    // What changed is marked on the canvas, not drawn as a ghost.
    await waitFor(() =>
      expect(nodeEl("inboundRoute:1")?.querySelector(".flow-node")).toHaveClass(
        "flow-node--draft-changed",
      ),
    );
    expect(nodeEl("ivr:90")).toBeNull();
    await screen.findByText(/The assistant applied ".*" for you/);

    fireEvent.click(within(review).getByRole("button", { name: "Revert" }));
    const dialog = await screen.findByRole("dialog", {
      name: "Revert what the assistant applied?",
    });
    expect(reverted).toBe(false);
    fireEvent.click(within(dialog).getByRole("button", { name: "Revert" }));
    await waitFor(() => expect(reverted).toBe(true));
    expect(
      calls.filter((c) => c.url.endsWith("/revert")).map((c) => c.body),
    ).toEqual([{ dryRun: true }, { dryRun: false }]);
    await waitFor(() =>
      expect(
        within(panel).queryByRole("region", {
          name: `Applied by the assistant: ${PROPOSAL.title}`,
        }),
      ).toBeNull(),
    );
    await screen.findByText("Reverted what the assistant applied.");
  });

  it("labels a batch the assistant applied in the history", async () => {
    api("operator", {
      "GET /api/v1/config/batches?limit=30": () =>
        json({
          items: [
            {
              id: "b9",
              actor: "user:ann",
              via: "ai:s1",
              baseRevision: 12,
              revision: 13,
              opCount: 2,
              createdAt: "2026-11-10T10:00:02Z",
            },
          ],
        }),
    });
    await open();
    fireEvent.click(screen.getByRole("button", { name: "History" }));
    const history = await screen.findByRole("dialog", {
      name: "Change history",
    });
    expect(
      await within(history).findByText(/by the assistant for user:ann/),
    ).toBeInTheDocument();
    expect(
      within(history).getByText("Applied by the assistant"),
    ).toBeInTheDocument();
    expect(within(history).queryByText(/\(ai:s1\)/)).toBeNull();
  });
});
