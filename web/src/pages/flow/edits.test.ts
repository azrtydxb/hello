import { describe, expect, it } from "vitest";
import type { FlowNode } from "../../api/flow";
import {
  draftReducer,
  emptyDraft,
  project,
  type DraftOp,
  type DraftState,
} from "./draft";
import {
  connect,
  connectChoices,
  connectionValid,
  createNode,
  deleteNode,
  disconnect,
  referrersOf,
  rename,
  setSetting,
  stageBody,
  terminalNode,
  type EditCtx,
} from "./edits";
import { EDIT_BODIES, EDIT_CATALOGUE, EDIT_GRAPH } from "./fixtures";
import { OBJECTS, idOf } from "./objects";

function ctxFor(draft: DraftState = emptyDraft(7)): EditCtx {
  return {
    graph: project(EDIT_GRAPH, draft),
    catalogue: EDIT_CATALOGUE,
    draft,
    load: (node: FlowNode) => {
      const path = OBJECTS[node.kind]?.read?.(idOf(node.id)) ?? "";
      const got = EDIT_BODIES[path];
      if (!got) return Promise.reject(new Error(`no body for ${path}`));
      const pick = OBJECTS[node.kind]?.pick;
      return Promise.resolve(pick ? pick(got) : { ...got });
    },
  };
}

const node = (id: string): FlowNode => {
  const n = EDIT_GRAPH.nodes.find((x) => x.id === id);
  if (!n) throw new Error(id);
  return n;
};

async function ok(p: ReturnType<typeof connect>): Promise<DraftOp[]> {
  const r = await p;
  if (!r.ok) throw new Error(r.error);
  return r.ops;
}

describe("connect", () => {
  it("stores an IVR key in the whole menu, without the fields the update refuses", async () => {
    const ops = await ok(
      connect(ctxFor(), node("ivr:1"), "key:2", node("queue:1"), "call"),
    );
    expect(ops).toHaveLength(1);
    const op = ops[0]!;
    expect(op.operationId).toBe("putIVRMenu");
    expect(op.pathParams).toEqual({ id: "1" });
    expect(op.expect).toBe("v-ivr:1");
    expect(op.body?.options).toEqual([
      { digit: "1", kind: "ring_group", target: "sales" },
      { digit: "2", kind: "queue", target: "helpdesk" },
    ]);
    expect(op.body).not.toHaveProperty("id");
    expect(op.body).not.toHaveProperty("createdAt");
    expect(op.body).not.toHaveProperty("greetingName");
    expect(op.body?.name).toBe("welcome");
    expect(op.edges[0]?.add[0]?.id).toBe("ivr:1.key:2->queue:1.call");
  });

  it("repoints a single-destination output and replaces its edge", async () => {
    const ops = await ok(
      connect(
        ctxFor(),
        node("inboundRoute:1"),
        "destination",
        node("ringGroup:2"),
        "ring",
      ),
    );
    const op = ops[0]!;
    expect(op.operationId).toBe("updateInboundRoute");
    expect(op.body).toEqual({
      destinationKind: "ring_group",
      destination: "support",
    });
    expect(op.edges[0]?.remove).toContain(
      "inboundRoute:1.destination->ivr:1.call",
    );
    const g = project(
      EDIT_GRAPH,
      draftReducer(emptyDraft(7), { type: "stage", ops }),
    );
    const from = g.edges.filter((e) => e.source.node === "inboundRoute:1");
    expect(from.map((e) => e.target.node)).toEqual(["ringGroup:2"]);
  });

  it("refuses what the catalogue does not allow", async () => {
    const r = await connect(
      ctxFor(),
      node("ringGroup:1"),
      "noAnswer",
      node("schedule:1"),
      "use",
    );
    expect(r.ok).toBe(false);
    expect(
      connectionValid(EDIT_GRAPH, EDIT_CATALOGUE, {
        source: "ringGroup:1",
        sourceHandle: "out:noAnswer",
        target: "schedule:1",
        targetHandle: "in:calendars",
      }),
    ).toBe(false);
    expect(
      connectionValid(EDIT_GRAPH, EDIT_CATALOGUE, {
        source: "ringGroup:1",
        sourceHandle: "out:noAnswer",
        target: "queue:1",
        targetHandle: "in:call",
      }),
    ).toBe(true);
    // An output into an output, or a node into itself.
    expect(
      connectionValid(EDIT_GRAPH, EDIT_CATALOGUE, {
        source: "ringGroup:1",
        sourceHandle: "out:noAnswer",
        target: "ringGroup:1",
        targetHandle: "in:ring",
      }),
    ).toBe(false);
  });

  it("offers only the targets the catalogue lists for a port", () => {
    const labels = connectChoices(
      { graph: EDIT_GRAPH, catalogue: EDIT_CATALOGUE },
      "ringGroup",
      "noAnswer",
    ).map((c) => c.value);
    expect(labels).toContain("queue:1|call");
    expect(labels).toContain("ivr:1|call");
    expect(labels.some((v) => v.startsWith("schedule:"))).toBe(false);
    expect(labels.some((v) => v.startsWith("trunk:"))).toBe(false);
  });

  it("adds a member to a ring group and keeps the others", async () => {
    const ops = await ok(
      connect(
        ctxFor(),
        node("extension:2"),
        "member",
        node("ringGroup:1"),
        "members",
      ),
    );
    const op = ops[0]!;
    expect(op.operationId).toBe("updateRingGroup");
    expect(op.body?.members).toEqual([
      { extensionId: 1, position: 1, weight: 1, delay: 0 },
      { extensionId: 2, position: 2 },
    ]);
  });

  it("dials a typed external number", async () => {
    const ext = terminalNode("external", "+97142000001");
    const ops = await ok(
      connect(ctxFor(), node("ringGroup:2"), "noAnswer", ext, "call"),
    );
    expect(ops[0]?.body).toEqual({
      failureKind: "external",
      failureTarget: "+97142000001",
    });
    const g = project(
      EDIT_GRAPH,
      draftReducer(emptyDraft(7), { type: "stage", ops }),
    );
    expect(g.nodes.some((n) => n.id === ext.id)).toBe(true);
  });

  it("refuses a forward to a ring group that is not numeric, with the reason", async () => {
    const r = await connect(
      ctxFor(),
      node("extension:2"),
      "forwardAlways",
      node("ringGroup:1"),
      "ring",
    );
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.error).toMatch(/name is a number/);
  });

  it("two feature-code edits become one operation", async () => {
    let draft = emptyDraft(7);
    const a = await ok(
      connect(
        ctxFor(draft),
        node("featureCode:*72"),
        "target",
        node("timeCondition:1"),
        "call",
      ),
    );
    draft = draftReducer(draft, { type: "stage", ops: a });
    const b = await ok(
      connect(
        ctxFor(draft),
        node("featureCode:*73"),
        "target",
        node("announcement:1"),
        "call",
      ),
    );
    draft = draftReducer(draft, { type: "stage", ops: b });
    expect(draft.ops).toHaveLength(1);
    const op = draft.ops[0]!;
    expect(op.operationId).toBe("putFeatureCodes");
    expect(op.body?.items).toEqual([
      { code: "*72", action: "time_condition", argument: "hours" },
      { code: "*73", action: "announcement", argument: "greeting" },
      { code: "*30", action: "dnd_on", argument: "" },
    ]);
  });
});

describe("disconnect", () => {
  it("removes an IVR key and leaves the others", async () => {
    const edge = EDIT_GRAPH.edges.find((e) => e.source.port === "key:1")!;
    const ops = await ok(disconnect(ctxFor(), edge));
    expect(ops[0]?.body?.options).toEqual([]);
    expect(ops[0]?.edges[0]?.remove).toEqual([edge.id]);
  });

  it("explains why an inbound route's destination cannot be cleared", async () => {
    const edge = EDIT_GRAPH.edges.find(
      (e) => e.source.node === "inboundRoute:1",
    )!;
    const r = await disconnect(ctxFor(), edge);
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.error).toMatch(/always needs a destination/);
  });
});

describe("rename", () => {
  it("rewrites every referrer in the same step", async () => {
    const ops = await ok(rename(ctxFor(), node("ringGroup:1"), "sales-team"));
    const byOp = Object.fromEntries(ops.map((o) => [o.operationId, o]));
    expect(Object.keys(byOp).sort()).toEqual(["putIVRMenu", "updateRingGroup"]);
    expect(byOp.updateRingGroup?.body).toEqual({ name: "sales-team" });
    expect(byOp.updateRingGroup?.label).toBe("sales-team");
    // The menu names the group twice (a key and the timeout): one operation.
    expect(byOp.putIVRMenu?.body?.options).toEqual([
      { digit: "1", kind: "ring_group", target: "sales-team" },
    ]);
    expect(byOp.putIVRMenu?.body?.timeout).toEqual({
      kind: "ring_group",
      target: "sales-team",
    });
  });

  it("a rename through setSetting is the same cascade", async () => {
    const ops = await ok(
      setSetting(ctxFor(), node("ringGroup:1"), "name", "x", "Name"),
    );
    expect(ops).toHaveLength(2);
  });

  it("changes a plain setting without touching referrers", async () => {
    const ops = await ok(
      setSetting(ctxFor(), node("ivr:1"), "maxRetries", 5, "Retries"),
    );
    expect(ops).toHaveLength(1);
    expect(ops[0]?.body?.maxRetries).toBe(5);
    expect(ops[0]?.body?.name).toBe("welcome");
  });
});

describe("delete", () => {
  it("lists what points at an object and clears it first", async () => {
    const refs = referrersOf(EDIT_GRAPH, node("ringGroup:1"));
    expect(refs.map((r) => r.owner.id).sort()).toEqual(["ivr:1", "ivr:1"]);
    const ops = await ok(deleteNode(ctxFor(), node("ringGroup:1")));
    expect(ops.map((o) => o.operationId)).toEqual([
      "putIVRMenu",
      "deleteRingGroup",
    ]);
    expect(ops[0]?.body?.options).toEqual([]);
    expect(ops[0]?.body?.timeout).toEqual({ kind: "hangup", target: "" });
    expect(ops[1]?.kind).toBe("delete");
  });

  it("refuses to delete what an inbound route needs, naming it", async () => {
    const r = await deleteNode(ctxFor(), node("ivr:1"));
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.error).toMatch(/Main number/);
  });

  it("removes a deleted extension from the groups it belongs to", async () => {
    const ops = await ok(deleteNode(ctxFor(), node("extension:1")));
    expect(ops[0]?.operationId).toBe("updateRingGroup");
    expect(ops[0]?.body?.members).toEqual([]);
  });
});

describe("create", () => {
  it("makes a new ring group the canvas can draw and wire", async () => {
    const r = await createNode(ctxFor(), "ringGroup", { name: "night" });
    if (!r.ok) throw new Error(r.error);
    expect(r.node).toBe("ringGroup:~n1");
    let draft = draftReducer(emptyDraft(7), { type: "stage", ops: r.ops });
    expect(draft.ops[0]?.body).toEqual({
      name: "night",
      strategy: "ring-all",
      members: [],
    });
    const g = project(EDIT_GRAPH, draft);
    const created = g.nodes.find((n) => n.id === "ringGroup:~n1")!;
    // Wire the route to it: the new node is addressed by its name.
    const ops = await ok(
      connect(
        ctxFor(draft),
        node("inboundRoute:1"),
        "destination",
        created,
        "ring",
      ),
    );
    draft = draftReducer(draft, { type: "stage", ops });
    expect(
      draft.ops.find((o) => o.operationId === "updateInboundRoute")?.body,
    ).toEqual({
      destinationKind: "ring_group",
      destination: "night",
    });
  });

  it("refers to a new extension by its placeholder in member lists", async () => {
    const r = await createNode(ctxFor(), "extension", {
      number: "300",
      name: "Cy",
    });
    if (!r.ok) throw new Error(r.error);
    const draft = draftReducer(emptyDraft(7), { type: "stage", ops: r.ops });
    const created = project(EDIT_GRAPH, draft).nodes.find(
      (n) => n.id === "extension:~n1",
    )!;
    const ops = await ok(
      connect(ctxFor(draft), created, "member", node("ringGroup:2"), "members"),
    );
    expect(ops[0]?.body?.members).toEqual([
      { extensionId: "${n1.id}", position: 1 },
    ]);
  });

  it("creates an inbound route with its destination in one step", async () => {
    const r = await createNode(ctxFor(), "inboundRoute", {
      name: "Sales line",
      did: "+3225559999",
      destination: "ringGroup:1|ring",
    });
    if (!r.ok) throw new Error(r.error);
    const draft = draftReducer(emptyDraft(7), { type: "stage", ops: r.ops });
    expect(draft.ops).toHaveLength(1);
    expect(draft.ops[0]?.body).toEqual({
      name: "Sales line",
      didKind: "exact",
      did: "+3225559999",
      enabled: true,
      destinationKind: "ring_group",
      destination: "sales",
    });
  });

  it("asks for what is missing", async () => {
    const r = await createNode(ctxFor(), "ringGroup", { name: " " });
    expect(r.ok).toBe(false);
  });
});

describe("the other connections", () => {
  it("hold music is a class id on the object that plays it", async () => {
    const ops = await ok(
      connect(
        ctxFor(),
        node("mohClass:1"),
        "use",
        node("queue:1"),
        "holdMusic",
      ),
    );
    expect(ops[0]?.operationId).toBe("putQueue");
    expect(ops[0]?.body?.mohClassId).toBe(1);
    expect(ops[0]?.edges[0]?.add[0]?.id).toBe(
      "mohClass:1.use->queue:1.holdMusic",
    );
  });

  it("outbound trunk order is the trunks list of the route", async () => {
    const ops = await ok(
      connect(
        ctxFor(),
        node("outboundRoute:1"),
        "trunks",
        node("trunk:1"),
        "carrier",
      ),
    );
    expect(ops[0]?.operationId).toBe("updateOutboundRoute");
    expect(ops[0]?.body).toEqual({ trunks: [1] });
  });

  it("a calendar joins a schedule through calendarIds", async () => {
    const ops = await ok(
      connect(
        ctxFor(),
        node("holidayCalendar:1"),
        "use",
        node("schedule:1"),
        "calendars",
      ),
    );
    expect(ops[0]?.operationId).toBe("putSchedule");
    expect(ops[0]?.body?.calendarIds).toEqual([1]);
    expect(ops[0]?.body?.name).toBe("office");
  });

  it("a greeting cannot be cleared, with the reason", async () => {
    const g: FlowEdgeLike = {
      id: "announcement:1.audio->ivr:1.greeting",
      source: { node: "announcement:1", port: "audio" },
      target: { node: "ivr:1", port: "greeting" },
      owner: { node: "ivr:1", field: "greetingId" },
    };
    const r = await disconnect(ctxFor(), g);
    expect(r.ok).toBe(false);
    if (!r.ok) expect(r.error).toMatch(/always needs a greeting/);
  });
});

describe("a whole-object form edit", () => {
  it("stages only what changed, rewires the edges and cascades a rename", async () => {
    const body = {
      ...EDIT_BODIES["/api/v1/ring-groups/1"],
      name: "sales-team",
      ringTimeout: 45,
      failureKind: "queue",
      failureTarget: "helpdesk",
      members: [{ extensionId: 1, position: 1, weight: 1, delay: 0 }],
    };
    const r = await stageBody(ctxFor(), node("ringGroup:1"), body);
    if (!r.ok) throw new Error(r.error);
    const group = r.ops.find((o) => o.operationId === "updateRingGroup")!;
    expect(group.body).toEqual({
      name: "sales-team",
      ringTimeout: 45,
      failureKind: "queue",
      failureTarget: "helpdesk",
    });
    expect(r.ops.some((o) => o.operationId === "putIVRMenu")).toBe(true);
    const g = project(
      EDIT_GRAPH,
      draftReducer(emptyDraft(7), { type: "stage", ops: r.ops }),
    );
    expect(g.edges.map((e) => e.id)).toContain(
      "ringGroup:1.noAnswer->queue:1.call",
    );
    // The member edge is still there.
    expect(g.edges.map((e) => e.id)).toContain(
      "extension:1.member->ringGroup:1.members",
    );
  });

  it("an unchanged form stages nothing", async () => {
    const r = await stageBody(ctxFor(), node("ringGroup:2"), {
      ...EDIT_BODIES["/api/v1/ring-groups/2"],
    });
    expect(r).toEqual({ ok: true, ops: [] });
  });
});

type FlowEdgeLike = Parameters<typeof disconnect>[1];
