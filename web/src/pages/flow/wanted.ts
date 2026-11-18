// The edges an object's stored fields draw (the graph builder's rules, read
// the other way): after a whole-object edit from a form, the draft's graph
// has to show the connections the new values make without a round trip.
// Only the field groups listed per kind are reconciled; every other edge the
// object owns is left alone.

import type { FlowEdge, FlowGraph, FlowNode } from "../../api/flow";
import type { Body } from "./draft";

export interface Wanted {
  edge: FlowEdge;
  /** A terminal node the edge leads to that the graph does not have. */
  node?: FlowNode;
}

const edgeId = (s: string, sp: string, t: string, tp: string) =>
  `${s}.${sp}->${t}.${tp}`;

const str = (v: unknown): string => (typeof v === "string" ? v : "");
const rec = (v: unknown): Body =>
  typeof v === "object" && v !== null ? (v as Body) : {};
const arr = (v: unknown): Body[] => (Array.isArray(v) ? (v as Body[]) : []);

class Lookup {
  constructor(private readonly g: FlowGraph) {}
  find(kind: string, test: (n: FlowNode) => boolean): FlowNode | undefined {
    return this.g.nodes.find((n) => n.kind === kind && test(n));
  }
  byId(kind: string, id: unknown): FlowNode | undefined {
    if (id === null || id === undefined || id === "") return undefined;
    return this.g.nodes.find((n) => n.id === `${kind}:${String(id)}`);
  }
  /** A stored (kind, target) destination as the node input it reaches. */
  dest(
    kind: string,
    target: string,
  ): { node: FlowNode; port: string; created?: boolean } | undefined {
    const label = (k: string) => this.find(k, (n) => n.label === target);
    const num = (k: string, key: string) =>
      this.find(k, (n) => str(n.summary?.[key]) === target);
    const term = (k: string, summary: Body) => {
      const id = `${k}:${target}`;
      const have = this.g.nodes.find((n) => n.id === id);
      return have
        ? { node: have }
        : { node: { id, kind: k, label: target, summary }, created: true };
    };
    switch (kind) {
      case "extension": {
        // The older way: a numeric ring group named like the number.
        const grp = label("ringGroup");
        if (grp) return { node: grp, port: "ring" };
        const n = num("extension", "number");
        return n && { node: n, port: "ring" };
      }
      case "voicemail": {
        const n = num("extension", "number");
        return n && { node: n, port: "voicemail" };
      }
      case "ring_group": {
        const n = label("ringGroup");
        return n && { node: n, port: "ring" };
      }
      case "ivr":
      case "queue":
      case "announcement": {
        const n = label(kind);
        return n && { node: n, port: "call" };
      }
      case "time_condition": {
        const n = label("timeCondition");
        return n && { node: n, port: "call" };
      }
      case "paging": {
        const n = num("pagingGroup", "code");
        return n && { node: n, port: "call" };
      }
      case "conference": {
        const n = num("conference", "number");
        return n && { node: n, port: "call" };
      }
      case "voicemail_group": {
        const n = num("voicemailGroup", "number");
        return n && { node: n, port: "call" };
      }
      case "fax": {
        const n = label("fax");
        return n && { node: n, port: "call" };
      }
      case "directory": {
        const id = "directory:names";
        const have = this.g.nodes.find((n) => n.id === id);
        return have
          ? { node: have, port: "call" }
          : {
              node: { id, kind: "directory", label: "Dial-by-name directory" },
              port: "call",
              created: true,
            };
      }
      case "external":
        return { ...term("external", { number: target }), port: "call" };
      case "sip_uri":
        return { ...term("sipUri", { uri: target }), port: "call" };
    }
    return undefined;
  }
  /** A forward target: a ring group by name, an extension by number, else a number. */
  forward(target: string) {
    const grp = this.find("ringGroup", (n) => n.label === target);
    if (grp) return { node: grp, port: "ring" };
    const ext = this.find(
      "extension",
      (n) => str(n.summary?.number) === target,
    );
    if (ext) return { node: ext, port: "ring" };
    return this.dest("external", target);
  }
}

type Spec = {
  /** Owner fields this reconciles (the graph's `owner.field`). */
  fields: string[];
  wanted: (node: FlowNode, body: Body, l: Lookup) => Wanted[];
};

function mk(
  src: string,
  sp: string,
  tgt: string,
  tp: string,
  owner: string,
  field: string,
  extra?: { node?: FlowNode; data?: Body },
): Wanted {
  return {
    edge: {
      id: edgeId(src, sp, tgt, tp),
      source: { node: src, port: sp },
      target: { node: tgt, port: tp },
      owner: { node: owner, field },
      ...(extra?.data ? { data: extra.data } : {}),
    },
    node: extra?.node,
  };
}

function destEdge(
  l: Lookup,
  node: FlowNode,
  port: string,
  field: string,
  kind: unknown,
  target: unknown,
  ownerNode = node.id,
): Wanted[] {
  const k = str(kind);
  if (!k || k === "hangup" || k === "none") return [];
  if (k === "internal") {
    // The dialled DID itself, looked up as an extension: one node per route.
    const id = `internalDid:${node.id.slice(node.id.indexOf(":") + 1)}`;
    return [
      mk(node.id, port, id, "call", ownerNode, field, {
        node: l.byId("internalDid", id.slice(id.indexOf(":") + 1))
          ? undefined
          : { id, kind: "internalDid", label: "DID as extension" },
      }),
    ];
  }
  const d = l.dest(k, str(target));
  if (!d) return [];
  return [
    mk(node.id, port, d.node.id, d.port, ownerNode, field, {
      node: "created" in d && d.created ? d.node : undefined,
    }),
  ];
}

const destOf = (b: Body, key: string) => rec(b[key]);

function members(
  l: Lookup,
  node: FlowNode,
  list: Body[],
  port: string,
  field: string,
): Wanted[] {
  const out: Wanted[] = [];
  list.forEach((m, i) => {
    const ext = l.byId("extension", m.extensionId ?? m);
    if (ext) {
      out.push(
        mk(ext.id, "member", node.id, port, node.id, field, {
          data: { position: i + 1 },
        }),
      );
    }
  });
  return out;
}

function usedBy(
  l: Lookup,
  kind: string,
  id: unknown,
  node: FlowNode,
  port: string,
  field: string,
  srcPort: string,
): Wanted[] {
  const src = l.byId(kind, id);
  return src ? [mk(src.id, srcPort, node.id, port, node.id, field)] : [];
}

const moh = (n: FlowNode, b: Body, l: Lookup) =>
  usedBy(l, "mohClass", b.mohClassId, n, "holdMusic", "mohClassId", "use");

const prompts = (
  n: FlowNode,
  b: Body,
  l: Lookup,
  map: Record<string, string>,
): Wanted[] =>
  Object.entries(map).flatMap(([port, key]) =>
    usedBy(l, "announcement", b[key], n, port, key, "audio"),
  );

const exits = (n: FlowNode, b: Body, l: Lookup, names: string[]): Wanted[] =>
  names.flatMap((p) => {
    const d = destOf(b, p);
    return destEdge(l, n, p, p, d.kind, d.target);
  });

const memberList = (port: string) => ({
  fields: [port],
  wanted: (n: FlowNode, b: Body, l: Lookup) =>
    members(l, n, arr(b[port]), port, port),
});

export const OWNED: Record<string, Spec> = {
  pickupGroup: memberList("members"),
  voicemailGroup: memberList("members"),
  pagingGroup: {
    fields: ["members", "callers", "multicastChannelId"],
    wanted: (n, b, l) => [
      ...members(l, n, arr(b.members), "members", "members"),
      ...members(l, n, arr(b.callers), "callers", "callers"),
      ...(() => {
        const mc = l.byId("multicastChannel", b.multicastChannelId);
        return mc
          ? [mk(n.id, "multicast", mc.id, "page", n.id, "multicastChannelId")]
          : [];
      })(),
    ],
  },
  mohClass: {
    fields: ["announcementIds"],
    wanted: (n, b, l) => {
      const out: Wanted[] = [];
      (Array.isArray(b.announcementIds) ? b.announcementIds : []).forEach(
        (id, i) => {
          const a = l.byId("announcement", id);
          if (a) {
            out.push(
              mk(a.id, "audio", n.id, "tracks", n.id, "announcementIds", {
                data: { position: i + 1 },
              }),
            );
          }
        },
      );
      return out;
    },
  },
  schedule: {
    fields: ["calendarIds"],
    wanted: (n, b, l) =>
      (Array.isArray(b.calendarIds) ? b.calendarIds : []).flatMap((id) =>
        usedBy(l, "holidayCalendar", id, n, "calendars", "calendarIds", "use"),
      ),
  },
  device: {
    fields: ["extensionId"],
    wanted: (n, b, l) => {
      const ext = l.byId("extension", b.extensionId);
      return ext
        ? [mk(n.id, "extension", ext.id, "devices", n.id, "extensionId")]
        : [];
    },
  },
  phone: {
    fields: ["deviceId"],
    wanted: (n, b, l) => {
      const dev = l.byId("device", b.deviceId);
      return dev ? [mk(n.id, "device", dev.id, "phone", n.id, "deviceId")] : [];
    },
  },
  featureCode: {
    fields: ["argument", "action"],
    wanted: (n, b, l) => {
      const code = n.id.slice(n.id.indexOf(":") + 1);
      const item = arr(b.items).find((i) => str(i.code) === code);
      if (!item) return [];
      const action = str(item.action);
      const field = action === "directory" ? "action" : "argument";
      if (action !== "directory" && !str(item.argument)) return [];
      return destEdge(l, n, "target", field, action, item.argument);
    },
  },
  ringGroup: {
    fields: ["failureKind+failureTarget", "members", "mohClassId"],
    wanted: (n, b, l) => [
      ...destEdge(
        l,
        n,
        "noAnswer",
        "failureKind+failureTarget",
        b.failureKind,
        b.failureTarget,
      ),
      ...members(l, n, arr(b.members), "members", "members"),
      ...moh(n, b, l),
    ],
  },
  ivr: {
    fields: [
      "options",
      "timeout",
      "invalid",
      "greetingId",
      "invalidPromptId",
      "timeoutPromptId",
    ],
    wanted: (n, b, l) => [
      ...arr(b.options).flatMap((o) => {
        const w = destEdge(
          l,
          n,
          `key:${str(o.digit)}`,
          "options",
          o.kind,
          o.target,
        );
        return w.map((x) => ({
          ...x,
          edge: { ...x.edge, data: { digit: str(o.digit) } },
        }));
      }),
      ...exits(n, b, l, ["timeout", "invalid"]),
      ...prompts(n, b, l, {
        greeting: "greetingId",
        invalidPrompt: "invalidPromptId",
        timeoutPrompt: "timeoutPromptId",
      }),
    ],
  },
  queue: {
    fields: [
      "members",
      "mohClassId",
      "timeout",
      "full",
      "noAgents",
      "breakoutVoicemail",
      "periodicPromptId",
      "positionPromptId",
      "waitPromptId",
      "minutesPromptId",
      "breakoutPromptId",
    ],
    wanted: (n, b, l) => [
      ...members(l, n, arr(b.members), "members", "members"),
      ...moh(n, b, l),
      ...exits(n, b, l, ["timeout", "full", "noAgents"]),
      ...(str(b.breakoutVoicemail)
        ? destEdge(
            l,
            n,
            "breakout",
            "breakoutVoicemail",
            "voicemail",
            b.breakoutVoicemail,
          )
        : []),
      ...prompts(n, b, l, {
        periodicPrompt: "periodicPromptId",
        positionPrompt: "positionPromptId",
        waitPrompt: "waitPromptId",
        minutesPrompt: "minutesPromptId",
        breakoutPrompt: "breakoutPromptId",
      }),
    ],
  },
  timeCondition: {
    fields: ["open", "closed", "holiday", "scheduleId"],
    wanted: (n, b, l) => [
      ...exits(n, b, l, ["open", "closed", "holiday"]),
      ...usedBy(
        l,
        "schedule",
        b.scheduleId,
        n,
        "schedule",
        "scheduleId",
        "use",
      ),
    ],
  },
  conference: {
    fields: ["entryAnnouncement", "exitAnnouncement"],
    wanted: (n, b, l) =>
      ["entryAnnouncement", "exitAnnouncement"].flatMap((p) => {
        const a = l.find("announcement", (x) => x.label === str(b[p]));
        return a && str(b[p]) ? [mk(a.id, "audio", n.id, p, n.id, p)] : [];
      }),
  },
  inboundRoute: {
    fields: ["destinationKind+destination", "trunkId", "mohClassId"],
    wanted: (n, b, l) => {
      const out = destEdge(
        l,
        n,
        "destination",
        "destinationKind+destination",
        b.destinationKind,
        b.destination,
      );
      const trunk =
        b.trunkId === null || b.trunkId === undefined
          ? l.find("anyTrunk", () => true)
          : l.byId("trunk", b.trunkId);
      if (trunk)
        out.push(mk(trunk.id, "inbound", n.id, "trunk", n.id, "trunkId"));
      return [...out, ...moh(n, b, l)];
    },
  },
  outboundRoute: {
    fields: ["trunks"],
    wanted: (n, b, l) => {
      const out: Wanted[] = [];
      (Array.isArray(b.trunks) ? b.trunks : []).forEach((id, i) => {
        const t = l.byId("trunk", id);
        if (t) {
          out.push(
            mk(n.id, "trunks", t.id, "carrier", n.id, "trunks", {
              data: { order: i + 1 },
            }),
          );
        }
      });
      return out;
    },
  },
  extension: {
    fields: ["forwardAlways", "forwardBusy", "forwardNoAnswer", "mohClassId"],
    wanted: (n, b, l) => [
      ...["forwardAlways", "forwardBusy", "forwardNoAnswer"].flatMap((p) => {
        const t = str(b[p]);
        const d = t ? l.forward(t) : undefined;
        return d
          ? [
              mk(n.id, p, d.node.id, d.port, n.id, p, {
                node: "created" in d && d.created ? d.node : undefined,
              }),
            ]
          : [];
      }),
      ...moh(n, b, l),
    ],
  },
};

/**
 * The edges a node's owned field groups draw from `body`, and the ids of the
 * graph's edges those groups own now (which the new ones replace).
 */
export function reconcile(
  graph: FlowGraph,
  node: FlowNode,
  body: Body,
): { remove: string[]; add: Wanted[] } | null {
  const spec = OWNED[node.kind];
  if (!spec) return null;
  const l = new Lookup(graph);
  const remove = graph.edges
    .filter(
      (e) =>
        !e.implicit &&
        e.owner.node === node.id &&
        spec.fields.includes(e.owner.field),
    )
    .map((e) => e.id);
  return { remove, add: spec.wanted(node, body, l) };
}
