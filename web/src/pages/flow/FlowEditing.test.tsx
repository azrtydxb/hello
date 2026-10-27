import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it } from "vitest";
import { apiError, json, mockApi, renderApp, type Call } from "../../test/api";
import { EDIT_BODIES, EDIT_CATALOGUE, EDIT_GRAPH } from "./fixtures";

const CANVAS = {
  id: 1,
  name: "Main office",
  description: "",
  layoutVersion: 3,
  nodeCount: 8,
  createdBy: "admin",
  createdAt: "2026-10-25T10:00:00Z",
  updatedAt: "2026-10-25T10:00:00Z",
  nodes: [
    "inboundRoute:1",
    "ivr:1",
    "ringGroup:1",
    "ringGroup:2",
    "extension:1",
    "extension:2",
    "queue:1",
    "timeCondition:1",
  ].map((node, i) => ({
    node,
    x: (i % 4) * 300,
    y: Math.floor(i / 4) * 220,
    collapsed: false,
  })),
};

const RESULT = (applied: boolean) => ({
  applied,
  ...(applied ? { batchId: "b1" } : {}),
  baseRevision: 7,
  revision: applied ? 8 : 7,
  results: [
    {
      operationId: "putIVRMenu",
      status: "updated",
      id: "1",
      node: "ivr:1",
      before: {},
      after: {},
      diff: [
        {
          path: "/timeout/target",
          before: "sales",
          after: "helpdesk",
        },
      ],
    },
  ],
  warnings: [
    {
      node: "ringGroup:1",
      code: "unreachable",
      message: "sales is unreachable",
    },
  ],
});

let calls: Call[] = [];

function api(role: string, extra: Record<string, () => Response> = {}) {
  const bodies = Object.fromEntries(
    Object.entries(EDIT_BODIES).map(([path, body]) => [
      `GET ${path}`,
      () => json(body),
    ]),
  );
  calls = mockApi({
    "GET /api/v1/auth/me": () => json({ username: "ann", role }),
    "GET /api/v1/flow/catalogue": () => json(EDIT_CATALOGUE),
    "GET /api/v1/flow/graph": () => json(EDIT_GRAPH),
    "GET /api/v1/flow/revision": () => json({ configRevision: 7 }),
    "GET /api/v1/flow/canvases/1": () => json(CANVAS),
    ...bodies,
    ...extra,
  });
  return calls;
}

const batchCalls = () =>
  calls.filter((c) => c.method === "POST" && c.url === "/api/v1/config/batch");
const batchBody = (c: Call) =>
  c.body as { dryRun: boolean; ops: unknown[]; baseRevision: number };

async function open(role = "operator") {
  renderApp("/flow/1");
  // The first render of a file also loads the lazy page: allow for a cold start.
  await screen.findByRole(
    "heading",
    { name: "Main office" },
    { timeout: 60000 },
  );
  await screen.findByText("welcome");
  return role;
}

async function selectIvr() {
  fireEvent.click(screen.getByText("welcome"));
  return screen.findByRole("complementary", { name: "Selected node" });
}

/** Connects the IVR's timeout to the help desk queue through the panel. */
async function stageTimeoutToQueue() {
  const panel = await selectIvr();
  const select = await within(panel).findByLabelText("timeout: connect to");
  fireEvent.change(select, { target: { value: "queue:1|call" } });
  fireEvent.click(
    within(panel).getByRole("button", { name: "Connect timeout" }),
  );
  await screen.findByText(/1 staged change/);
  return panel;
}

beforeEach(() => localStorage.clear());

describe("flow canvas editing", { timeout: 90000 }, () => {
  it("shows no edit controls to a viewer", async () => {
    api("viewer");
    await open();
    expect(
      screen.queryByRole("button", { name: "Add object" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /Review and apply/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("group", { name: "Draft" }),
    ).not.toBeInTheDocument();
    const panel = await selectIvr();
    expect(within(panel).getByText(/View only/)).toBeInTheDocument();
    expect(
      within(panel).queryByRole("button", { name: /Connect/ }),
    ).not.toBeInTheDocument();
    expect(
      within(panel).queryByRole("button", { name: /Delete/ }),
    ).not.toBeInTheDocument();
    expect(batchCalls()).toHaveLength(0);
  });

  it("offers only the connections the catalogue allows", async () => {
    api("operator");
    await open();
    const group = await screen.findByText("sales");
    fireEvent.click(group);
    const panel = await screen.findByRole("complementary", {
      name: "Selected node",
    });
    const select = await within(panel).findByLabelText("No answer: connect to");
    const values = within(select)
      .getAllByRole("option")
      .map((o) => (o as HTMLOptionElement).value);
    expect(values).toContain("queue:1|call");
    expect(values).toContain("ivr:1|call");
    expect(values).toContain("extension:1|ring");
    // A schedule or a trunk is not a call destination.
    expect(values.some((v) => v.startsWith("schedule:"))).toBe(false);
    expect(values.some((v) => v.startsWith("trunk:"))).toBe(false);
    // Neither is the group itself.
    expect(values.some((v) => v.startsWith("ringGroup:1"))).toBe(false);
  });

  it("stages a connection, checks it with a dry run and draws the new edge", async () => {
    api("operator", {
      "POST /api/v1/config/batch": () => json(RESULT(false)),
    });
    await open();
    await stageTimeoutToQueue();
    await waitFor(() => expect(batchCalls()).toHaveLength(1));
    const dry = batchBody(batchCalls()[0]!);
    expect(dry.dryRun).toBe(true);
    expect(dry.baseRevision).toBe(7);
    expect(dry.ops).toMatchObject([
      {
        operationId: "putIVRMenu",
        pathParams: { id: "1" },
        expect: "v-ivr:1",
        body: {
          name: "welcome",
          timeout: { kind: "queue", target: "helpdesk" },
        },
      },
    ]);
    await screen.findByText(/Checked: accepted|Accepted, with 1 warning/);
    // The panel shows the new connection from the draft.
    const panel = screen.getByRole("complementary", { name: "Selected node" });
    const timeout = within(panel).getByRole("group", { name: "timeout" });
    expect(within(timeout).getByText("helpdesk")).toBeInTheDocument();
    // Nothing was applied.
    expect(batchCalls().every((c) => batchBody(c).dryRun)).toBe(true);
  });

  it("never applies without the diff confirmation", async () => {
    api("operator", {
      "POST /api/v1/config/batch": () => json(RESULT(false)),
    });
    await open();
    await stageTimeoutToQueue();
    const apply = await screen.findByRole("button", {
      name: "Review and apply",
    });
    await waitFor(() => expect(apply).toBeEnabled());
    expect(batchCalls().some((c) => !batchBody(c).dryRun)).toBe(false);
    fireEvent.click(apply);
    const dialog = await screen.findByRole("dialog", {
      name: "Apply these changes?",
    });
    // The dialog shows the field-level diff and the warning.
    expect(within(dialog).getByText("/timeout/target")).toBeInTheDocument();
    expect(within(dialog).getByText("helpdesk")).toBeInTheDocument();
    expect(
      within(dialog).getByText("sales is unreachable"),
    ).toBeInTheDocument();
    expect(batchCalls().some((c) => !batchBody(c).dryRun)).toBe(false);
    // Cancelling sends nothing.
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(batchCalls().some((c) => !batchBody(c).dryRun)).toBe(false);
  });

  it("applies after the confirmation and clears the draft", async () => {
    let dry = true;
    api("operator", {
      "POST /api/v1/config/batch": () => json(RESULT(!dry)),
    });
    await open();
    await stageTimeoutToQueue();
    const apply = await screen.findByRole("button", {
      name: "Review and apply",
    });
    await waitFor(() => expect(apply).toBeEnabled());
    fireEvent.click(apply);
    const dialog = await screen.findByRole("dialog", {
      name: "Apply these changes?",
    });
    dry = false;
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Apply 1 change" }),
    );
    await screen.findByText(/Applied 1 change/);
    const sent = batchCalls().filter((c) => !batchBody(c).dryRun);
    expect(sent).toHaveLength(1);
    expect(batchBody(sent[0]!)).toMatchObject({ baseRevision: 7 });
    expect(screen.getByText("No staged changes.")).toBeInTheDocument();
    expect(localStorage.getItem("hello.flow.draft.1.ann")).toBeNull();
  });

  it("shows a rejected change on its node", async () => {
    api("operator", {
      "POST /api/v1/config/batch": () =>
        apiError(422, "validation_failed", "the batch is not valid"),
    });
    // The error envelope of a 422 carries the problems.
    calls = [];
    mockApi({
      "GET /api/v1/auth/me": () => json({ username: "ann", role: "operator" }),
      "GET /api/v1/flow/catalogue": () => json(EDIT_CATALOGUE),
      "GET /api/v1/flow/graph": () => json(EDIT_GRAPH),
      "GET /api/v1/flow/revision": () => json({ configRevision: 7 }),
      "GET /api/v1/flow/canvases/1": () => json(CANVAS),
      "GET /api/v1/ivr-menus/1": () => json(EDIT_BODIES["/api/v1/ivr-menus/1"]),
      "POST /api/v1/config/batch": () =>
        json(
          {
            error: {
              code: "validation_failed",
              message: "the batch is not valid",
              errors: [
                {
                  opIndex: 0,
                  node: "ivr:1",
                  path: "timeout",
                  message: "queue helpdesk has no agents",
                },
              ],
            },
          },
          422,
        ),
    });
    await open();
    await stageTimeoutToQueue();
    const nodeEl = document.querySelector<HTMLElement>(
      '.react-flow__node[data-id="ivr:1"]',
    )!;
    await waitFor(() =>
      expect(within(nodeEl).getByRole("alert")).toHaveTextContent(
        "queue helpdesk has no agents",
      ),
    );
    expect(
      screen.getByRole("button", { name: "Review and apply" }),
    ).toBeDisabled();
  });

  it("handles 409 config_stale: nothing applied, the draft is rebased on the new configuration", async () => {
    let applying = false;
    let graphReads = 0;
    mockApi({
      "GET /api/v1/auth/me": () => json({ username: "ann", role: "operator" }),
      "GET /api/v1/flow/catalogue": () => json(EDIT_CATALOGUE),
      "GET /api/v1/flow/graph": () => {
        graphReads++;
        // After the conflict the IVR has a new version and the revision moved.
        return json(
          graphReads === 1
            ? EDIT_GRAPH
            : {
                ...EDIT_GRAPH,
                configRevision: 8,
                nodes: EDIT_GRAPH.nodes.map((n) =>
                  n.id === "ivr:1" ? { ...n, version: "changed-by-bob" } : n,
                ),
              },
        );
      },
      "GET /api/v1/flow/revision": () => json({ configRevision: 7 }),
      "GET /api/v1/flow/canvases/1": () => json(CANVAS),
      "GET /api/v1/ivr-menus/1": () => json(EDIT_BODIES["/api/v1/ivr-menus/1"]),
      "POST /api/v1/config/batch": () =>
        applying
          ? json(
              {
                error: {
                  code: "config_stale",
                  message: "the configuration changed",
                  conflicts: [{ opIndex: 0, node: "ivr:1" }],
                },
              },
              409,
            )
          : json(RESULT(false)),
    });
    await open();
    await stageTimeoutToQueue();
    const apply = await screen.findByRole("button", {
      name: "Review and apply",
    });
    await waitFor(() => expect(apply).toBeEnabled());
    fireEvent.click(apply);
    const dialog = await screen.findByRole("dialog", {
      name: "Apply these changes?",
    });
    applying = true;
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Apply 1 change" }),
    );
    await screen.findByText(
      /Nothing was applied: the configuration changed under 1 of your changes/,
    );
    // The conflicting change is not kept.
    await screen.findByText("Some staged changes were dropped");
    expect(screen.getByText("No staged changes.")).toBeInTheDocument();
  });

  it("undoes and redoes a staged change", async () => {
    api("operator", {
      "POST /api/v1/config/batch": () => json(RESULT(false)),
    });
    await open();
    await stageTimeoutToQueue();
    fireEvent.click(screen.getByRole("button", { name: "Undo" }));
    await screen.findByText("No staged changes.");
    fireEvent.click(screen.getByRole("button", { name: "Redo" }));
    await screen.findByText(/1 staged change/);
  });

  it("keeps the draft in the browser per canvas and user", async () => {
    api("operator", {
      "POST /api/v1/config/batch": () => json(RESULT(false)),
    });
    const { unmount } = await (async () => {
      const r = renderApp("/flow/1");
      await screen.findByText("welcome");
      return r;
    })();
    await stageTimeoutToQueue();
    await waitFor(() =>
      expect(localStorage.getItem("hello.flow.draft.1.ann")).not.toBeNull(),
    );
    unmount();
    renderApp("/flow/1");
    await screen.findByText(/1 staged change/);
  });

  it("deleting lists what points at the object and stages the clean-up", async () => {
    api("operator", {
      "POST /api/v1/config/batch": () => json(RESULT(false)),
    });
    await open();
    fireEvent.click(screen.getByText("sales"));
    const panel = await screen.findByRole("complementary", {
      name: "Selected node",
    });
    fireEvent.click(
      await within(panel).findByRole("button", { name: "Delete ring group" }),
    );
    const dialog = await screen.findByRole("dialog", { name: "Delete sales?" });
    const used = within(dialog).getByRole("list", { name: "Used by" });
    expect(within(used).getAllByText("welcome")).toHaveLength(2);
    fireEvent.click(within(dialog).getByRole("button", { name: "Delete" }));
    await screen.findByText(/2 staged changes/);
    await waitFor(() => expect(batchCalls().length).toBeGreaterThan(0));
    const ops = batchBody(batchCalls().at(-1)!).ops as {
      operationId: string;
    }[];
    expect(ops.map((o) => o.operationId)).toEqual([
      "putIVRMenu",
      "deleteRingGroup",
    ]);
  });

  it("renames and stages the referrers in the same draft", async () => {
    api("operator", {
      "POST /api/v1/config/batch": () => json(RESULT(false)),
    });
    await open();
    fireEvent.click(screen.getByText("sales"));
    const panel = await screen.findByRole("complementary", {
      name: "Selected node",
    });
    const name = await within(panel).findByLabelText("Name");
    fireEvent.change(name, { target: { value: "sales-team" } });
    fireEvent.blur(name);
    await screen.findByText(/2 staged changes/);
    await waitFor(() => expect(batchCalls().length).toBeGreaterThan(0));
    const ops = batchBody(batchCalls().at(-1)!).ops as {
      operationId: string;
      body: Record<string, unknown>;
    }[];
    const menu = ops.find((o) => o.operationId === "putIVRMenu")!;
    expect(menu.body.timeout).toEqual({
      kind: "ring_group",
      target: "sales-team",
    });
  });

  it("adds an object to the draft from the dialog", async () => {
    api("operator", {
      "POST /api/v1/config/batch": () => json(RESULT(false)),
    });
    await open();
    fireEvent.click(screen.getByRole("button", { name: "Add object" }));
    const dialog = await screen.findByRole("dialog", { name: "Add an object" });
    fireEvent.change(within(dialog).getByLabelText("Kind"), {
      target: { value: "ringGroup" },
    });
    fireEvent.change(await within(dialog).findByLabelText("Name"), {
      target: { value: "night" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Add to draft" }),
    );
    await screen.findByText(/1 staged change/);
    await waitFor(() => expect(batchCalls().length).toBeGreaterThan(0));
    const req = batchBody(batchCalls().at(-1)!) as unknown as {
      ops: { ref: string; operationId: string }[];
      layout: { node: string }[];
    };
    expect(req.ops[0]).toMatchObject({
      ref: "n1",
      operationId: "createRingGroup",
    });
    expect(
      document.querySelector('.react-flow__node[data-id="ringGroup:~n1"]'),
    ).not.toBeNull();
  });

  it("lists applied batches and reverts one after a dry-run preview", async () => {
    let reverted = false;
    api("operator", {
      "GET /api/v1/config/batches?limit=30": () =>
        json({
          items: [
            {
              id: "b1",
              actor: "ann",
              via: "",
              baseRevision: 6,
              revision: 7,
              opCount: 2,
              createdAt: "2026-10-26T10:00:00Z",
            },
          ],
        }),
      "POST /api/v1/config/batches/b1/revert": () => {
        const sent = calls.filter((c) => c.url.endsWith("/revert")).at(-1)!;
        const dry = (sent.body as { dryRun: boolean }).dryRun;
        if (!dry) reverted = true;
        return json({
          ...RESULT(!dry),
          skipped: ["a password was not restored"],
        });
      },
    });
    await open();
    fireEvent.click(screen.getByRole("button", { name: "History" }));
    const history = await screen.findByRole("dialog", {
      name: "Change history",
    });
    fireEvent.click(
      await within(history).findByRole("button", {
        name: /Revert the change of/,
      }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "Revert this change?",
    });
    expect(within(dialog).getByText("/timeout/target")).toBeInTheDocument();
    expect(
      within(dialog).getByText("a password was not restored"),
    ).toBeInTheDocument();
    // The preview was a dry run: nothing reverted yet.
    expect(reverted).toBe(false);
    fireEvent.click(within(dialog).getByRole("button", { name: "Revert" }));
    await waitFor(() => expect(reverted).toBe(true));
    const sent = calls
      .filter((c) => c.url.endsWith("/revert"))
      .map((c) => c.body);
    expect(sent).toEqual([{ dryRun: true }, { dryRun: false }]);
  });

  it("a stale revert is explained, not applied", async () => {
    api("operator", {
      "GET /api/v1/config/batches?limit=30": () =>
        json({
          items: [
            {
              id: "b1",
              actor: "ann",
              via: "",
              baseRevision: 6,
              revision: 7,
              opCount: 1,
              createdAt: "2026-10-26T10:00:00Z",
            },
          ],
        }),
      "POST /api/v1/config/batches/b1/revert": () =>
        apiError(
          409,
          "config_stale",
          "1 of the objects this batch changed have changed since",
        ),
    });
    await open();
    fireEvent.click(screen.getByRole("button", { name: "History" }));
    const history = await screen.findByRole("dialog", {
      name: "Change history",
    });
    fireEvent.click(
      await within(history).findByRole("button", {
        name: /Revert the change of/,
      }),
    );
    expect(
      await within(history).findByText(/Cannot revert: 1 of the objects/),
    ).toBeInTheDocument();
  });
});
