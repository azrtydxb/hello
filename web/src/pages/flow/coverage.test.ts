// The canvas covers the whole catalogue (spec flow-canvas S-11, S-15): every
// stored node kind can be created, edited with every field (its own form)
// and deleted, and every output the catalogue allows can be wired. The
// catalogue is the snapshot the Go test TestCatalogueSnapshot keeps in step
// with internal/flow/catalogue.go, so a new kind or port fails here until
// the canvas covers it.

import { describe, expect, it } from "vitest";
import type { FlowCatalogue, FlowNode } from "../../api/flow";
import snapshot from "./catalogue.snapshot.json";
import { emptyDraft } from "./draft";
import {
  connect,
  createFromBody,
  fixedTargets,
  stageForm,
  type EditCtx,
} from "./edits";
import {
  adapterFor,
  canCreateKind,
  canDeleteKind,
  canEditKind,
  createsOwn,
  FORM_KINDS,
  OBJECTS,
} from "./objects";

const CATALOGUE = snapshot as unknown as FlowCatalogue;

/**
 * The kinds that are not configurable objects, each with why. They have no
 * form and are neither created nor deleted; the canvas reaches them through
 * the outputs that lead to them.
 */
const NOT_OBJECTS: Record<string, string> = {
  anyTrunk: "an inbound route without a trunk; set on the route",
  dialPlan: "every outbound route is dialled by internal callers",
  external: "a number typed into the output that dials it",
  sipUri: "an address typed into the output that dials it",
  internalDid: "the route's dialled number, looked up as an extension",
  directory: "the built-in dial-by-name directory",
  missing: "a target that names nothing",
};

/** Outputs that are drawn but stored by nobody (implicit edges). */
const IMPLICIT = new Set(["dialPlan.dial"]);

describe("catalogue coverage", () => {
  const kinds = CATALOGUE.nodes.map((n) => n.kind);

  it("lists every kind that is not an object", () => {
    for (const k of Object.keys(NOT_OBJECTS)) expect(kinds).toContain(k);
    for (const n of CATALOGUE.nodes) {
      if (n.pseudo || n.terminal) {
        expect(NOT_OBJECTS, `${n.kind} is pseudo or terminal`).toHaveProperty(
          n.kind,
        );
      }
    }
  });

  it.each(kinds.filter((k) => !(k in NOT_OBJECTS)))(
    "%s can be created, edited with its own form and deleted",
    (kind) => {
      expect(canCreateKind(kind), "create").toBe(true);
      expect(canDeleteKind(kind), "delete").toBe(true);
      expect(FORM_KINDS.has(kind), "its own form").toBe(true);
      // Either the draft carries the edit, or the form saves it at once.
      expect(
        canEditKind(kind) || Boolean(OBJECTS[kind]?.own?.edit),
        "edit",
      ).toBe(true);
      expect(OBJECTS[kind]?.read, "read").toBeDefined();
    },
  );

  it.each(Object.keys(NOT_OBJECTS))("%s has no form", (kind) => {
    expect(FORM_KINDS.has(kind)).toBe(false);
    expect(canCreateKind(kind)).toBe(false);
    expect(canDeleteKind(kind)).toBe(false);
  });

  it("makes devices, phones and announcements with their own form, outside the draft", () => {
    expect(createsOwn("device")).toBe(true);
    expect(createsOwn("phone")).toBe(true);
    expect(createsOwn("announcement")).toBe(true);
    expect(createsOwn("ringGroup")).toBe(false);
    expect(OBJECTS.announcement?.own?.delete).toBe(true);
  });

  const outputs = CATALOGUE.nodes.flatMap((n) =>
    n.ports
      .filter(
        (p) => p.direction === "out" && !IMPLICIT.has(`${n.kind}.${p.id}`),
      )
      .flatMap((p) =>
        (p.targets ?? [])
          .filter((t) => !t.requires)
          .map((t) => ({ src: n.kind, port: p, tgt: t.kind, tgtPort: t.port })),
      ),
  );

  it.each(
    outputs.map((o) => [`${o.src}.${o.port.id} -> ${o.tgt}.${o.tgtPort}`, o]),
  )("%s is wired on the canvas", async (_name, o) => {
    expect(adapterFor(o.src, o.port.id, o.tgt, o.tgtPort)).toBeDefined();
    const src: FlowNode = {
      id: o.src === "featureCode" ? "featureCode:*72" : `${o.src}:1`,
      kind: o.src,
      label: "src",
      summary: {},
    };
    // A numeric name, so a forward may dial a ring group by it.
    const tgt: FlowNode = {
      id: `${o.tgt}:2`,
      kind: o.tgt,
      label: "200",
      summary: { number: "200", code: "*61", name: "200" },
    };
    const ctx: EditCtx = {
      graph: { configRevision: 1, nodes: [src, tgt], edges: [], warnings: [] },
      catalogue: CATALOGUE,
      draft: emptyDraft(1),
      load: () => Promise.resolve({}),
    };
    const port = o.port.perKey ? `${o.port.id}:1` : o.port.id;
    const r = await connect(ctx, src, port, tgt, o.tgtPort);
    expect(r.ok ? "" : r.error).toBe("");
  });
});

describe("full forms on the canvas", () => {
  const ctx = (nodes: FlowNode[] = []): EditCtx => ({
    graph: { configRevision: 1, nodes, edges: [], warnings: [] },
    catalogue: CATALOGUE,
    draft: emptyDraft(1),
    load: () => Promise.resolve({ items: [] }),
  });

  it("stages a new object from its whole form, with the edges its values draw", () => {
    const ext: FlowNode = {
      id: "extension:5",
      kind: "extension",
      label: "201 Ann",
      summary: { number: "201" },
    };
    const r = createFromBody(ctx([ext]), "pickupGroup", {
      name: "floor-2",
      members: [5],
    });
    if (!r.ok) throw new Error(r.error);
    const op = r.ops[0]!;
    expect(op.kind).toBe("create");
    expect(op.operationId).toBe("createPickupGroup");
    expect(op.body).toEqual({ name: "floor-2", members: [5] });
    expect(op.pending?.label).toBe("floor-2");
    expect(r.node).toBe(op.node);
    expect(op.edges[0]?.add.map((e) => e.id)).toEqual([
      `extension:5.member->${op.node}.members`,
    ]);
  });

  it("does not stage a kind its own form makes at once", () => {
    const r = createFromBody(ctx(), "device", { sipUsername: "x" });
    expect(r.ok).toBe(false);
  });

  it("stages a new feature code as an edit of the one list", async () => {
    const r = await stageForm(ctx(), "featureCode", null, {
      items: [{ code: "*99", action: "directory", argument: "" }],
    });
    if (!r.ok) throw new Error(r.error);
    expect(r.ops).toHaveLength(1);
    expect(r.ops[0]?.operationId).toBe("putFeatureCodes");
    expect(r.ops[0]?.key).toBe("list|putFeatureCodes");
  });

  it("offers the directory and the dialled DID as fixed targets", () => {
    const route: FlowNode = {
      id: "inboundRoute:3",
      kind: "inboundRoute",
      label: "main",
    };
    expect(
      fixedTargets(ctx([route]), route, "destination").map((n) => n.id),
    ).toEqual(["internalDid:3", "directory:names"]);
  });
});

describe("the draft's body in the shape a form reads", () => {
  it("turns member ids back into members, and track ids into tracks", () => {
    expect(
      OBJECTS.pickupGroup?.toView?.({ name: "a", members: [3, 4] }),
    ).toEqual({ name: "a", members: [{ extensionId: 3 }, { extensionId: 4 }] });
    expect(
      OBJECTS.pagingGroup?.toView?.({ members: [1], callers: [2] }),
    ).toEqual({ members: [{ extensionId: 1 }], callers: [{ extensionId: 2 }] });
    expect(OBJECTS.mohClass?.toView?.({ announcementIds: [7] })).toMatchObject({
      tracks: [{ announcementId: 7 }],
    });
    expect(
      OBJECTS.voicemailGroup?.pick?.({
        number: "500",
        name: "v",
        enabled: true,
        maxSeconds: 60,
        members: [{ extensionId: 1, number: "201", name: "Ann" }],
        createdAt: "x",
      }),
    ).toEqual({
      number: "500",
      name: "v",
      enabled: true,
      maxSeconds: 60,
      members: [1],
    });
  });
});
