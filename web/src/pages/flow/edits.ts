// Turns what the user does on the canvas (connect, disconnect, rename, change
// a setting, create, delete) into draft operations. The catalogue decides
// what may be connected; objects.ts says how each connection is stored. All
// edits of one gesture go through one session, so two changes to the same
// object (a rename that rewrites a referrer that also holds the deleted
// target) become one operation, not two that overwrite each other.

import type {
  FlowCatalogue,
  FlowEdge,
  FlowGraph,
  FlowNode,
} from "../../api/flow";
import {
  isPending,
  nextRef,
  pendingRef,
  type Body,
  type DraftOp,
  type DraftState,
  type EdgeChange,
} from "./draft";
import { kindLabel } from "./model";
import { reconcile } from "./wanted";
import {
  adapterFor,
  canConnect,
  CREATE_FORMS,
  FEATURE_CODES_OP,
  idOf,
  OBJECTS,
  portBase,
  storedKindOf,
  targetString,
  type Conn,
  type CreateField,
} from "./objects";

export interface EditCtx {
  /** The graph with the draft applied. */
  graph: FlowGraph;
  catalogue: FlowCatalogue;
  draft: DraftState;
  /** The stored object, as its update accepts it. */
  load: (node: FlowNode) => Promise<Body>;
}

export type EditResult =
  { ok: true; ops: DraftOp[]; node?: string } | { ok: false; error: string };

const fail = (error: string): EditResult => ({ ok: false, error });

/** All feature codes are one object: one slot, one operation. */
const slotOf = (n: FlowNode): string =>
  n.kind === "featureCode" ? "featureCode:*" : n.id;

class Session {
  private acc = new Map<string, Body>();
  private nodes = new Map<string, FlowNode>();
  private bases = new Map<string, Body>();
  private changes = new Map<
    string,
    { edges: EdgeChange[]; label?: string; text: string[] }
  >();

  constructor(
    readonly ctx: EditCtx,
    /** Operations staged by the same gesture, not in the draft yet. */
    private readonly seed: readonly DraftOp[] = [],
  ) {}

  private existing(n: FlowNode): DraftOp | undefined {
    const ref = pendingRef(n.id);
    const key =
      ref !== null
        ? `create|${ref}`
        : n.kind === "featureCode"
          ? `list|${FEATURE_CODES_OP}`
          : `upd|${n.id}`;
    return (
      this.seed.find((o) => o.key === key) ??
      this.ctx.draft.ops.find((o) => o.key === key)
    );
  }

  async view(n: FlowNode): Promise<Body> {
    const slot = slotOf(n);
    let base = this.bases.get(slot);
    if (!base) {
      base = isPending(n.id) ? {} : await this.ctx.load(n);
      this.bases.set(slot, base);
    }
    return { ...base, ...this.existing(n)?.body, ...this.acc.get(slot) };
  }

  edit(
    n: FlowNode,
    delta: Body,
    edges: EdgeChange | null,
    text: string,
    label?: string,
  ): void {
    const slot = slotOf(n);
    this.nodes.set(slot, n);
    this.acc.set(slot, { ...this.acc.get(slot), ...delta });
    const ch = this.changes.get(slot) ?? { edges: [], text: [] };
    if (edges) ch.edges.push(edges);
    if (label) ch.label = label;
    if (text) ch.text.push(text);
    this.changes.set(slot, ch);
  }

  ops(): DraftOp[] {
    const out: DraftOp[] = [];
    for (const [slot, delta] of this.acc) {
      const n = this.nodes.get(slot);
      if (!n) continue;
      const ch = this.changes.get(slot);
      const def = OBJECTS[n.kind];
      const text = ch?.text.join("; ") ?? "";
      const ref = pendingRef(n.id);
      const base = {
        kind: "update" as const,
        node: n.id,
        edges: ch?.edges ?? [],
        label: ch?.label,
        summary: text,
      };
      if (ref !== null) {
        out.push({
          ...base,
          key: `upd|${n.id}`,
          operationId: def?.create ?? "",
          body: delta,
          touches: {},
          pending: n.label ? { ...n, label: ch?.label ?? n.label } : undefined,
        });
        continue;
      }
      const full: Body = {
        ...(def?.put ? this.bases.get(slot) : undefined),
        ...this.existing(n)?.body,
        ...delta,
      };
      if (n.kind === "featureCode") {
        const touches: Record<string, string> = {};
        for (const f of this.ctx.graph.nodes) {
          if (f.kind === "featureCode") touches[f.id] = f.version ?? "";
        }
        out.push({
          ...base,
          key: `list|${FEATURE_CODES_OP}`,
          operationId: FEATURE_CODES_OP,
          body: full,
          touches,
        });
        continue;
      }
      out.push({
        ...base,
        key: `upd|${n.id}`,
        operationId: def?.update ?? "",
        pathParams: { id: idOf(n.id) },
        body: full,
        expect: n.version,
        touches: { [n.id]: n.version ?? "" },
      });
    }
    return out;
  }
}

const byId = (g: FlowGraph, id: string): FlowNode | undefined =>
  g.nodes.find((n) => n.id === id);

function mkConn(
  src: FlowNode,
  srcPort: string,
  tgt: FlowNode,
  tgtPort: string,
): Conn {
  const sid = (n: FlowNode): unknown => {
    const ref = pendingRef(n.id);
    return ref !== null ? `\${${ref}.id}` : Number(idOf(n.id));
  };
  return {
    src,
    tgt,
    srcPort,
    key: srcPort.includes(":") ? srcPort.slice(srcPort.indexOf(":") + 1) : "",
    tgtPort,
    kind: storedKindOf(tgt.kind, tgtPort),
    target: targetString(tgt),
    idOf: sid,
  };
}

/** The catalogue output port of a node kind. */
function outPort(cat: FlowCatalogue, kind: string, port: string) {
  return cat.nodes
    .find((n) => n.kind === kind)
    ?.ports.find((p) => p.id === portBase(port) && p.direction === "out");
}

function inPort(cat: FlowCatalogue, kind: string, port: string) {
  return cat.nodes
    .find((n) => n.kind === kind)
    ?.ports.find((p) => p.id === port && p.direction === "in");
}

const edgeId = (s: string, sp: string, t: string, tp: string) =>
  `${s}.${sp}->${t}.${tp}`;

/** Wires src.srcPort to tgt.tgtPort inside a session; null on success. */
async function connectIn(
  s: Session,
  src: FlowNode,
  srcPort: string,
  tgt: FlowNode,
  tgtPort: string,
): Promise<string | null> {
  const cat = s.ctx.catalogue;
  const cc = canConnect(cat, src.kind, srcPort, tgt.kind, tgtPort);
  if (!cc.ok) {
    return cc.requires
      ? `Not available yet: ${cc.requires}`
      : (cc.reason ?? "These cannot be connected");
  }
  const ad = adapterFor(src.kind, srcPort, tgt.kind, tgtPort);
  if (!ad) return "This connection is changed on the object's own page.";
  const perKey = outPort(cat, src.kind, srcPort)?.perKey;
  if (perKey && !srcPort.includes(":")) return "Choose a key first.";
  const conn = mkConn(src, srcPort, tgt, tgtPort);
  const owner = ad.owner === "source" ? src : tgt;
  const view = await s.view(owner);
  const delta = ad.connect(view, conn);
  if (typeof delta === "string") return delta;
  const out = outPort(cat, src.kind, srcPort);
  const inn = inPort(cat, tgt.kind, tgtPort);
  const drop = s.ctx.graph.edges
    .filter(
      (e) =>
        !e.implicit &&
        ((out &&
          !out.many &&
          e.source.node === src.id &&
          e.source.port === srcPort) ||
          (inn &&
            !inn.many &&
            e.target.node === tgt.id &&
            e.target.port === tgtPort)),
    )
    .map((e) => e.id);
  const id = edgeId(src.id, srcPort, tgt.id, tgtPort);
  s.edit(
    owner,
    delta,
    {
      remove: [...drop, id],
      nodes: byId(s.ctx.graph, tgt.id) ? undefined : [tgt],
      add: [
        {
          id,
          source: { node: src.id, port: srcPort },
          target: { node: tgt.id, port: tgtPort },
          owner: { node: owner.id, field: out?.field ?? "" },
        },
      ],
    },
    `Connect ${src.label} to ${tgt.label}`,
  );
  return null;
}

async function disconnectIn(
  s: Session,
  edge: FlowEdge,
): Promise<string | null> {
  const src = byId(s.ctx.graph, edge.source.node);
  const tgt = byId(s.ctx.graph, edge.target.node);
  if (!src || !tgt) return "That connection no longer exists.";
  const ad = adapterFor(src.kind, edge.source.port, tgt.kind, edge.target.port);
  if (!ad) return "This connection is changed on the object's own page.";
  if (!ad.disconnect) return ad.keep ?? "This connection cannot be removed.";
  const owner = ad.owner === "source" ? src : tgt;
  const view = await s.view(owner);
  const delta = ad.disconnect(
    view,
    mkConn(src, edge.source.port, tgt, edge.target.port),
  );
  s.edit(
    owner,
    delta,
    { remove: [edge.id], add: [] },
    `Disconnect ${src.label} from ${tgt.label}`,
  );
  return null;
}

async function finish(s: Session, run: () => Promise<string | null>) {
  try {
    const err = await run();
    return err ? fail(err) : { ok: true as const, ops: s.ops() };
  } catch (e) {
    return fail(e instanceof Error ? e.message : String(e));
  }
}

// --- gestures -------------------------------------------------------------------

export function connect(
  ctx: EditCtx,
  src: FlowNode,
  srcPort: string,
  tgt: FlowNode,
  tgtPort: string,
): Promise<EditResult> {
  const s = new Session(ctx);
  return finish(s, () => connectIn(s, src, srcPort, tgt, tgtPort));
}

export function disconnect(ctx: EditCtx, edge: FlowEdge): Promise<EditResult> {
  const s = new Session(ctx);
  return finish(s, () => disconnectIn(s, edge));
}

/** The label a node shows once its name is `name`. */
function labelFor(node: FlowNode, name: string): string {
  if (node.kind === "extension") {
    const n = node.summary?.number;
    return `${typeof n === "string" ? n : ""} ${name}`.trim();
  }
  return name;
}

/**
 * Renames an object and rewrites every object that names it, in the same
 * draft step. Refused, with the reason, when a referrer cannot store the
 * new name.
 */
export function rename(
  ctx: EditCtx,
  node: FlowNode,
  name: string,
): Promise<EditResult> {
  const def = OBJECTS[node.kind];
  const key = def?.nameKey;
  if (!key) return Promise.resolve(fail("This object cannot be renamed here."));
  const trimmed = name.trim();
  if (!trimmed) return Promise.resolve(fail("A name is needed."));
  const s = new Session(ctx);
  return finish(s, async () => {
    await s.view(node);
    return renameIn(s, node, key, trimmed, {});
  });
}

/** The rename inside a session: the object, then every object that names it. */
async function renameIn(
  s: Session,
  node: FlowNode,
  key: string,
  name: string,
  more: Body,
  edges: EdgeChange | null = null,
): Promise<string | null> {
  const label = labelFor(node, name);
  s.edit(
    node,
    { ...more, [key]: name },
    edges,
    `Rename ${node.label} to ${label}`,
    label,
  );
  if (node.kind === "extension") return null; // the number is what is stored
  const renamed: FlowNode = { ...node, label };
  for (const e of s.ctx.graph.edges) {
    if (e.implicit || e.target.node !== node.id || e.owner.node === node.id) {
      continue;
    }
    const src = byId(s.ctx.graph, e.source.node);
    if (!src) continue;
    const ad = adapterFor(src.kind, e.source.port, node.kind, e.target.port);
    if (!ad?.byName) continue;
    const err = await connectIn(s, src, e.source.port, renamed, e.target.port);
    if (err) return `${src.label} cannot follow the new name: ${err}`;
  }
  return null;
}

const same = (a: unknown, b: unknown): boolean => {
  if (a === b) return true;
  if (typeof a !== "object" || typeof b !== "object" || !a || !b) return false;
  if (Array.isArray(a) !== Array.isArray(b)) return false;
  const ka = Object.keys(a as Body);
  const kb = Object.keys(b as Body);
  return (
    ka.length === kb.length &&
    ka.every((k) => same((a as Body)[k], (b as Body)[k]))
  );
};

/**
 * Stages what a form of the object produced: only the fields that differ from
 * the object as it stands (stored plus the draft), the rename cascade when the
 * name changed, and the edges the new values draw.
 */
export function stageBody(
  ctx: EditCtx,
  node: FlowNode,
  body: Body,
): Promise<EditResult> {
  const def = OBJECTS[node.kind];
  if (!def?.update) {
    return Promise.resolve(fail("This object is changed on its own page."));
  }
  const s = new Session(ctx);
  return finish(s, async () => {
    const view = await s.view(node);
    const delta: Body = {};
    for (const [k, v] of Object.entries(body)) {
      if (!same(v, view[k])) delta[k] = v;
    }
    if (Object.keys(delta).length === 0) return null;
    const next = { ...view, ...delta };
    const rec = reconcile(ctx.graph, node, next);
    const edges: EdgeChange | null = rec
      ? {
          remove: rec.remove,
          add: rec.add.map((w) => w.edge),
          nodes: rec.add.flatMap((w) => (w.node ? [w.node] : [])),
        }
      : null;
    const key = def.nameKey;
    if (key && delta[key] !== undefined && delta[key] !== view[key]) {
      const { [key]: name, ...more } = delta;
      return renameIn(s, node, key, String(name).trim(), more, edges);
    }
    s.edit(node, delta, edges, `Change ${node.label}`);
    return null;
  });
}

/** Changes one setting of an object. */
export function setSetting(
  ctx: EditCtx,
  node: FlowNode,
  key: string,
  value: unknown,
  label: string,
): Promise<EditResult> {
  if (key === OBJECTS[node.kind]?.nameKey) {
    return rename(ctx, node, String(value));
  }
  const s = new Session(ctx);
  return finish(s, async () => {
    await s.view(node);
    s.edit(node, { [key]: value }, null, `Set ${label} of ${node.label}`);
    return null;
  });
}

export interface Referrer {
  edge: FlowEdge;
  /** The object that stores the connection. */
  owner: FlowNode;
  /** Why it cannot be cleared, when it cannot. */
  blocked?: string;
}

/** What points at a node, and whether each can be cleared. */
export function referrersOf(graph: FlowGraph, node: FlowNode): Referrer[] {
  const out: Referrer[] = [];
  for (const e of graph.edges) {
    if (e.implicit || e.owner.node === node.id) continue;
    if (e.source.node !== node.id && e.target.node !== node.id) continue;
    const owner = byId(graph, e.owner.node);
    const src = byId(graph, e.source.node);
    const tgt = byId(graph, e.target.node);
    if (!owner || !src || !tgt) continue;
    const ad = adapterFor(src.kind, e.source.port, tgt.kind, e.target.port);
    const blocked = !ad
      ? `${owner.label} is changed on its own page.`
      : !ad.disconnect
        ? (ad.keep ?? "It cannot be cleared here.")
        : undefined;
    out.push({ edge: e, owner, blocked });
  }
  return out;
}

/** Deletes an object after clearing everything that points at it. */
export function deleteNode(ctx: EditCtx, node: FlowNode): Promise<EditResult> {
  const def = OBJECTS[node.kind];
  if (!def?.delete) {
    return Promise.resolve(fail("This object is deleted on its own page."));
  }
  const refs = referrersOf(ctx.graph, node);
  const blocker = refs.find((r) => r.blocked);
  if (blocker) {
    return Promise.resolve(
      fail(`${blocker.owner.label}: ${blocker.blocked ?? ""}`),
    );
  }
  const s = new Session(ctx);
  return finish(s, async () => {
    for (const r of refs) {
      const err = await disconnectIn(s, r.edge);
      if (err) return err;
    }
    return null;
  }).then((res) => {
    if (!res.ok) return res;
    const del: DraftOp = {
      key: `del|${node.id}`,
      kind: "delete",
      node: node.id,
      operationId: def.delete ?? "",
      pathParams: { id: idOf(node.id) },
      expect: node.version,
      touches: { [node.id]: node.version ?? "" },
      edges: [],
      summary: `Delete ${kindLabel(ctx.catalogue, node.kind).toLowerCase()} ${node.label}`,
    };
    return { ok: true as const, ops: [...res.ops, del] };
  });
}

// --- creating ----------------------------------------------------------------------

/** The values of a create form, by field key; node fields hold `nodeId|port`. */
export type FormValues = Record<string, string>;

/**
 * The inputs an output may be wired to, from the catalogue's target list and
 * the nodes that exist, as `nodeId|port` options.
 */
export function connectChoices(
  ctx: Pick<EditCtx, "graph" | "catalogue">,
  kind: string,
  port: string,
  exclude?: string,
): { value: string; label: string; kind: string }[] {
  const cat = ctx.catalogue;
  const out: { value: string; label: string; kind: string }[] = [];
  for (const t of outPort(cat, kind, port)?.targets ?? []) {
    if (t.requires || t.kind === "external" || t.kind === "sipUri") continue;
    if (!adapterFor(kind, port, t.kind, t.port)) continue;
    const portLabel = inPort(cat, t.kind, t.port)?.label;
    for (const n of ctx.graph.nodes) {
      if (n.kind !== t.kind || n.id === exclude) continue;
      out.push({
        kind: t.kind,
        value: `${n.id}|${t.port}`,
        label:
          t.port === "ring" ||
          t.port === "call" ||
          t.port === "members" ||
          t.port === "trunk"
            ? `${kindLabel(cat, n.kind)}: ${n.label}`
            : `${kindLabel(cat, n.kind)}: ${n.label} (${portLabel ?? t.port})`,
      });
    }
  }
  return out;
}

/** The terminal kinds an output may dial a typed value into. */
export function typedTargets(
  cat: FlowCatalogue,
  kind: string,
  port: string,
): { kind: "external" | "sipUri"; label: string }[] {
  const out: { kind: "external" | "sipUri"; label: string }[] = [];
  for (const t of outPort(cat, kind, port)?.targets ?? []) {
    if (t.requires || !adapterFor(kind, port, t.kind, t.port)) continue;
    if (t.kind === "external")
      out.push({ kind: "external", label: "External number" });
    if (t.kind === "sipUri") out.push({ kind: "sipUri", label: "SIP URI" });
  }
  return out;
}

/** A terminal node for a typed number or URI. */
export function terminalNode(
  kind: "external" | "sipUri",
  value: string,
): FlowNode {
  const v = value.trim();
  return {
    id: `${kind}:${v}`,
    kind,
    label: v,
    summary: kind === "external" ? { number: v } : { target: v },
  };
}

/** Whether a drag from one handle to another is a connection the catalogue allows. */
export function connectionValid(
  graph: FlowGraph,
  cat: FlowCatalogue,
  conn: {
    source: string;
    sourceHandle?: string | null;
    target: string;
    targetHandle?: string | null;
  },
): boolean {
  const sp = conn.sourceHandle?.startsWith("out:")
    ? conn.sourceHandle.slice(4)
    : "";
  const tp = conn.targetHandle?.startsWith("in:")
    ? conn.targetHandle.slice(3)
    : "";
  if (!sp || !tp || conn.source === conn.target) return false;
  const src = byId(graph, conn.source);
  const tgt = byId(graph, conn.target);
  if (!src || !tgt) return false;
  return (
    canConnect(cat, src.kind, sp, tgt.kind, tp).ok &&
    adapterFor(src.kind, sp, tgt.kind, tp) !== undefined
  );
}

/** The objects a "node" field of a create form may choose, as plain ids. */
export function nodeChoices(
  ctx: Pick<EditCtx, "graph">,
  field: CreateField,
): { value: string; label: string }[] {
  return ctx.graph.nodes
    .filter((n) => field.kinds?.includes(n.kind) && !isPending(n.id))
    .map((n) => ({ value: n.id, label: n.label }));
}

/** Everything a create form needs before it can be submitted. */
export function missingFields(kind: string, values: FormValues): string[] {
  const form = CREATE_FORMS[kind];
  return (form?.fields ?? [])
    .filter((f) => f.required && !(values[f.key] ?? "").trim())
    .map((f) => f.label);
}

export function createNode(
  ctx: EditCtx,
  kind: string,
  values: FormValues,
): Promise<EditResult> {
  const def = OBJECTS[kind];
  const form = CREATE_FORMS[kind];
  if (!def?.create || !form) {
    return Promise.resolve(
      fail("This kind of object is created on its own page."),
    );
  }
  const missing = missingFields(kind, values);
  if (missing.length > 0) {
    return Promise.resolve(fail(`Fill in: ${missing.join(", ")}.`));
  }
  const plain: FormValues = {};
  for (const f of form.fields) {
    if (f.type !== "node")
      plain[f.key] = (values[f.key] ?? f.default ?? "").trim();
  }
  const { ref } = nextRef(ctx.draft);
  const id = `${kind}:~${ref}`;
  const label = plain.name || plain.number || ref;
  const node: FlowNode = {
    id,
    kind,
    label,
    summary: form.summary(plain),
  };
  const create: DraftOp = {
    key: `create|${ref}`,
    kind: "create",
    node: id,
    ref,
    operationId: def.create,
    body: form.build(plain),
    touches: {},
    edges: [],
    pending: node,
    summary: `Create ${kindLabel(ctx.catalogue, kind).toLowerCase()} ${label}`,
  };
  const s = new Session(ctx, [create]);
  return finish(s, async () => {
    for (const f of form.fields) {
      if (f.type !== "node") continue;
      const chosen = values[f.key];
      if (!chosen) continue;
      if (f.connectPort) {
        const [nid, port] = chosen.split("|");
        const tgt = nid ? byId(ctx.graph, nid) : undefined;
        if (!tgt || !port) return "Choose where it goes.";
        const err = await connectIn(s, node, f.connectPort, tgt, port);
        if (err) return err;
      } else if (f.bodyKey && f.edge) {
        const src = byId(ctx.graph, chosen);
        if (!src) return `Choose a ${f.label.toLowerCase()}.`;
        const sid = edgeId(src.id, f.edge.sourcePort, id, f.edge.targetPort);
        s.edit(
          node,
          { [f.bodyKey]: Number(idOf(src.id)) },
          {
            remove: [],
            add: [
              {
                id: sid,
                source: { node: src.id, port: f.edge.sourcePort },
                target: { node: id, port: f.edge.targetPort },
                owner: { node: id, field: f.bodyKey },
              },
            ],
          },
          "",
        );
      }
    }
    return null;
  }).then((res) =>
    res.ok ? { ok: true as const, ops: [create, ...res.ops], node: id } : res,
  );
}
