import { beforeEach, describe, expect, it } from "vitest";
import {
  compile,
  draftKey,
  draftMarks,
  draftReducer,
  emptyDraft,
  loadDraft,
  project,
  rebase,
  saveDraft,
  type DraftOp,
  type DraftState,
} from "./draft";
import { EDIT_GRAPH } from "./fixtures";

const upd = (
  node: string,
  body: Record<string, unknown>,
  v = `v-${node}`,
): DraftOp => ({
  key: `upd|${node}`,
  kind: "update",
  node,
  operationId: "updateRingGroup",
  pathParams: { id: node.slice(node.indexOf(":") + 1) },
  body,
  expect: v,
  touches: { [node]: v },
  edges: [],
  summary: `edit ${node}`,
});

const stage = (s: DraftState, ...ops: DraftOp[]) =>
  draftReducer(s, { type: "stage", ops });

describe("draft reducer", () => {
  it("merges edits of the same object into one operation", () => {
    let s = emptyDraft(7);
    s = stage(s, upd("ringGroup:1", { strategy: "sequential" }));
    s = stage(s, upd("ringGroup:1", { ringTimeout: 30 }));
    expect(s.ops).toHaveLength(1);
    expect(s.ops[0]?.body).toEqual({ strategy: "sequential", ringTimeout: 30 });
  });

  it("merges edits to the feature codes into one operation", () => {
    const fc = (items: unknown[]): DraftOp => ({
      key: "list|putFeatureCodes",
      kind: "update",
      node: "featureCode:*72",
      operationId: "putFeatureCodes",
      body: { items },
      touches: {},
      edges: [],
      summary: "feature code",
    });
    let s = emptyDraft(7);
    s = stage(s, fc([{ code: "*72" }]));
    s = stage(s, {
      ...fc([{ code: "*72" }, { code: "*73" }]),
      node: "featureCode:*73",
    });
    expect(s.ops).toHaveLength(1);
    expect(s.ops[0]?.body).toEqual({
      items: [{ code: "*72" }, { code: "*73" }],
    });
  });

  it("undo and redo restore the exact operation list", () => {
    let s = emptyDraft(7);
    s = stage(s, upd("ringGroup:1", { strategy: "sequential" }));
    const one = s.ops;
    s = stage(
      s,
      upd("ringGroup:2", { ringTimeout: 5 }),
      upd("ringGroup:1", { hunt: true }),
    );
    const two = s.ops;
    expect(two).toHaveLength(2);
    s = draftReducer(s, { type: "undo" });
    expect(s.ops).toEqual(one);
    s = draftReducer(s, { type: "undo" });
    expect(s.ops).toEqual([]);
    s = draftReducer(s, { type: "redo" });
    expect(s.ops).toEqual(one);
    s = draftReducer(s, { type: "redo" });
    expect(s.ops).toEqual(two);
    // A new edit ends the redo line.
    s = draftReducer(s, { type: "undo" });
    s = stage(s, upd("ringGroup:2", { ringTimeout: 9 }));
    expect(s.future).toEqual([]);
  });

  it("forgets a node that only exists in the draft, with its edits", () => {
    const create: DraftOp = {
      key: "create|n1",
      kind: "create",
      node: "ringGroup:~n1",
      ref: "n1",
      operationId: "createRingGroup",
      body: { name: "new" },
      touches: {},
      edges: [],
      pending: { id: "ringGroup:~n1", kind: "ringGroup", label: "new" },
      summary: "create",
    };
    let s = stage(emptyDraft(7), create);
    s = stage(s, upd("ringGroup:~n1", { strategy: "sequential" }));
    expect(s.ops).toHaveLength(1);
    expect(s.ops[0]?.body).toEqual({ name: "new", strategy: "sequential" });
    s = stage(s, { ...create, key: "del|ringGroup:~n1", kind: "delete" });
    expect(s.ops).toEqual([]);
  });

  it("deleting an object drops its staged edits", () => {
    let s = stage(
      emptyDraft(7),
      upd("ringGroup:1", { strategy: "sequential" }),
    );
    s = stage(s, {
      ...upd("ringGroup:1", {}),
      key: "del|ringGroup:1",
      kind: "delete",
      operationId: "deleteRingGroup",
    });
    expect(s.ops.map((o) => o.kind)).toEqual(["delete"]);
  });
});

describe("rebase", () => {
  it("keeps what is untouched and drops what someone else changed", () => {
    let s = emptyDraft(7);
    s = stage(
      s,
      upd("ringGroup:1", { strategy: "sequential" }),
      upd("ringGroup:2", { ringTimeout: 5 }),
    );
    const moved = {
      ...EDIT_GRAPH,
      configRevision: 8,
      nodes: EDIT_GRAPH.nodes.map((n) =>
        n.id === "ringGroup:1" ? { ...n, version: "changed" } : n,
      ),
    };
    const r = rebase(s, moved);
    expect(r.ops.map((o) => o.node)).toEqual(["ringGroup:2"]);
    expect(r.dropped.map((o) => o.node)).toEqual(["ringGroup:1"]);
  });

  it("drops the whole gesture when one of its changes conflicts", () => {
    // A rename of group 1 and the menu that names it: one gesture.
    const rename = { ...upd("ringGroup:1", { name: "x" }), groups: ["g1"] };
    const menu = { ...upd("ringGroup:2", { ringTimeout: 5 }), groups: ["g1"] };
    const other = upd("extension:1", { name: "y" });
    const s = stage(emptyDraft(7), rename, menu, other);
    const moved = {
      ...EDIT_GRAPH,
      nodes: EDIT_GRAPH.nodes.map((n) =>
        n.id === "ringGroup:1" ? { ...n, version: "changed" } : n,
      ),
    };
    const r = rebase(s, moved);
    expect(r.ops.map((o) => o.node)).toEqual(["extension:1"]);
    expect(r.dropped.map((o) => o.node).sort()).toEqual([
      "ringGroup:1",
      "ringGroup:2",
    ]);
  });

  it("drops an edit of an object someone deleted", () => {
    const s = stage(emptyDraft(7), upd("ringGroup:2", { ringTimeout: 5 }));
    const gone = {
      ...EDIT_GRAPH,
      nodes: EDIT_GRAPH.nodes.filter((n) => n.id !== "ringGroup:2"),
    };
    expect(rebase(s, gone).ops).toEqual([]);
  });
});

describe("compile", () => {
  it("runs creates, then edits, then deletes, and places new nodes", () => {
    const create: DraftOp = {
      key: "create|n1",
      kind: "create",
      node: "ringGroup:~n1",
      ref: "n1",
      operationId: "createRingGroup",
      body: { name: "new" },
      touches: {},
      edges: [],
      pending: { id: "ringGroup:~n1", kind: "ringGroup", label: "new" },
      summary: "create",
    };
    const del: DraftOp = {
      ...upd("ringGroup:2", {}),
      key: "del|ringGroup:2",
      kind: "delete",
      operationId: "deleteRingGroup",
      body: undefined,
    };
    let s = stage(emptyDraft(7), del);
    s = stage(s, upd("ringGroup:1", { strategy: "sequential" }));
    s = stage(s, create);
    const req = compile(s, {
      dryRun: true,
      canvasId: 3,
      positions: { "ringGroup:~n1": { x: 10, y: 20 } },
    });
    expect(req.ops.map((o) => o.operationId)).toEqual([
      "createRingGroup",
      "updateRingGroup",
      "deleteRingGroup",
    ]);
    expect(req.ops[0]?.ref).toBe("n1");
    expect(req.ops[1]?.expect).toBe("v-ringGroup:1");
    expect(req.baseRevision).toBe(7);
    expect(req.dryRun).toBe(true);
    expect(req.layout).toEqual([{ node: "ringGroup:${n1.id}", x: 10, y: 20 }]);
  });
});

describe("project", () => {
  it("draws the draft: new nodes, deleted nodes, changed edges", () => {
    const op: DraftOp = {
      ...upd("ringGroup:1", {}),
      edges: [
        {
          remove: ["ivr:1.timeout->ringGroup:1.ring"],
          add: [
            {
              id: "ivr:1.timeout->ringGroup:2.ring",
              source: { node: "ivr:1", port: "timeout" },
              target: { node: "ringGroup:2", port: "ring" },
              owner: { node: "ivr:1", field: "timeout" },
            },
          ],
        },
      ],
      label: "sales2",
    };
    const del: DraftOp = {
      ...upd("queue:1", {}),
      key: "del|queue:1",
      kind: "delete",
    };
    const s = stage(emptyDraft(7), op, del);
    const g = project(EDIT_GRAPH, s);
    expect(g.nodes.find((n) => n.id === "queue:1")).toBeUndefined();
    expect(g.nodes.find((n) => n.id === "ringGroup:1")?.label).toBe("sales2");
    expect(g.edges.map((e) => e.id)).toContain(
      "ivr:1.timeout->ringGroup:2.ring",
    );
    expect(g.edges.map((e) => e.id)).not.toContain(
      "ivr:1.timeout->ringGroup:1.ring",
    );
    expect(draftMarks(s)).toEqual({
      "ringGroup:1": "changed",
      "queue:1": "deleted",
    });
  });
});

describe("persistence", () => {
  beforeEach(() => localStorage.clear());
  it("saves and loads the draft per canvas and user", () => {
    const key = draftKey(3, "ann");
    const s = stage(emptyDraft(7), upd("ringGroup:1", { hunt: true }));
    saveDraft(key, s, { "ringGroup:~n1": { x: 10, y: 20 } });
    expect(loadDraft(key)).toEqual({
      ...s,
      positions: { "ringGroup:~n1": { x: 10, y: 20 } },
    });
    expect(loadDraft(draftKey(3, "bob"))).toBeNull();
    saveDraft(key, emptyDraft(7));
    expect(loadDraft(key)).toBeNull();
  });
});
