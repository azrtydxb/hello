// What the canvas knows about editing each kind of object: which API
// operations change it, how to read it, which settings the panel edits, and
// how a connection between two nodes is stored in the object that owns it.
//
// Which connections are allowed is not decided here: that is the catalogue
// (`canConnect`). This table only says how an allowed connection is written,
// and a connection with no entry here is drawn but edited on the object's
// own page.

import type { FlowCatalogue, FlowNode } from "../../api/flow";
import type { Body } from "./draft";

/** "ringGroup:12" -> "12"; "featureCode:*72" -> "*72". */
export const idOf = (nodeId: string): string =>
  nodeId.slice(nodeId.indexOf(":") + 1);

export const kindOf = (nodeId: string): string =>
  nodeId.slice(0, nodeId.indexOf(":"));

// --- settings -----------------------------------------------------------------

export interface Setting {
  key: string;
  label: string;
  type: "text" | "number" | "bool" | "select";
  options?: string[];
}

// --- objects ------------------------------------------------------------------

export interface ObjectDef {
  /** GET path of one object. */
  read?: (id: string) => string;
  /** The operation that changes it. */
  update?: string;
  create?: string;
  delete?: string;
  /** The key of the object's name; renaming it cascades to its referrers. */
  nameKey?: string;
  /** Turns the GET answer into a body the update accepts. */
  pick?: (got: Body) => Body;
  /** True when the update replaces the whole object (PUT). */
  put?: boolean;
  settings?: Setting[];
}

const only =
  (keys: readonly string[]) =>
  (got: Body): Body => {
    const out: Body = {};
    for (const k of keys) if (got[k] !== undefined) out[k] = got[k];
    return out;
  };

const members = (list: unknown, keys: readonly string[]): Body[] =>
  Array.isArray(list) ? (list as Body[]).map((m) => only(keys)(m)) : [];

const IVR_KEYS = [
  "name",
  "number",
  "enabled",
  "greetingId",
  "invalidPromptId",
  "timeoutPromptId",
  "digitTimeout",
  "interDigitTimeout",
  "maxRetries",
  "allowDirectDial",
  "timeout",
  "invalid",
  "options",
] as const;

const QUEUE_KEYS = [
  "name",
  "number",
  "enabled",
  "strategy",
  "agentTimeout",
  "retryDelay",
  "wrapUp",
  "maxCallers",
  "maxWait",
  "joinEmpty",
  "leaveEmpty",
  "timeout",
  "full",
  "noAgents",
  "mohClassId",
  "announceFrequency",
  "announcePosition",
  "announceWait",
  "periodicPromptId",
  "positionPromptId",
  "waitPromptId",
  "minutesPromptId",
  "digitsPrefix",
  "breakoutKey",
  "breakoutPromptId",
  "breakoutVoicemail",
] as const;

const TC_KEYS = [
  "name",
  "scheduleId",
  "open",
  "closed",
  "holiday",
  "toggleNumber",
] as const;

export const OBJECTS: Record<string, ObjectDef> = {
  extension: {
    read: (id) => `/api/v1/extensions/${id}`,
    update: "updateExtension",
    create: "createExtension",
    delete: "deleteExtension",
    nameKey: "name",
    settings: [
      { key: "name", label: "Name", type: "text" },
      { key: "dnd", label: "Do not disturb", type: "bool" },
    ],
  },
  ringGroup: {
    read: (id) => `/api/v1/ring-groups/${id}`,
    update: "updateRingGroup",
    create: "createRingGroup",
    delete: "deleteRingGroup",
    nameKey: "name",
    pick: (g) => ({
      ...g,
      members: members(g.members, [
        "extensionId",
        "position",
        "weight",
        "delay",
      ]),
    }),
    settings: [
      { key: "name", label: "Name", type: "text" },
      {
        key: "strategy",
        label: "Strategy",
        type: "select",
        options: [
          "ring-all",
          "sequential",
          "round-robin",
          "longest-idle",
          "weighted",
        ],
      },
      { key: "ringTimeout", label: "Ring time (seconds)", type: "number" },
    ],
  },
  inboundRoute: {
    read: (id) => `/api/v1/routes/inbound/${id}`,
    update: "updateInboundRoute",
    create: "createInboundRoute",
    delete: "deleteInboundRoute",
    nameKey: "name",
    settings: [
      { key: "name", label: "Name", type: "text" },
      {
        key: "didKind",
        label: "Number match",
        type: "select",
        options: ["any", "exact", "prefix", "regex"],
      },
      { key: "did", label: "Number", type: "text" },
      { key: "enabled", label: "Enabled", type: "bool" },
    ],
  },
  outboundRoute: { delete: "deleteOutboundRoute" },
  trunk: { delete: "deleteTrunk" },
  ivr: {
    read: (id) => `/api/v1/ivr-menus/${id}`,
    update: "putIVRMenu",
    create: "createIVRMenu",
    delete: "deleteIVRMenu",
    nameKey: "name",
    put: true,
    pick: (g) => ({
      ...only(IVR_KEYS)(g),
      options: members(g.options, ["digit", "kind", "target"]),
    }),
    settings: [
      { key: "name", label: "Name", type: "text" },
      { key: "enabled", label: "Enabled", type: "bool" },
      { key: "maxRetries", label: "Retries", type: "number" },
      {
        key: "digitTimeout",
        label: "Wait for a key (seconds)",
        type: "number",
      },
      {
        key: "allowDirectDial",
        label: "Allow dialling an extension",
        type: "bool",
      },
    ],
  },
  queue: {
    read: (id) => `/api/v1/queues/${id}`,
    update: "putQueue",
    create: "createQueue",
    delete: "deleteQueue",
    nameKey: "name",
    put: true,
    pick: (g) => ({
      ...only(QUEUE_KEYS)(g),
      members: members(g.members, [
        "extensionId",
        "dynamic",
        "penalty",
        "position",
      ]),
    }),
    settings: [
      { key: "name", label: "Name", type: "text" },
      { key: "enabled", label: "Enabled", type: "bool" },
      {
        key: "strategy",
        label: "Strategy",
        type: "select",
        options: [
          "ring_all",
          "longest_idle",
          "round_robin",
          "fewest_calls",
          "random",
          "linear",
        ],
      },
      { key: "maxWait", label: "Longest wait (seconds)", type: "number" },
      { key: "maxCallers", label: "Most callers", type: "number" },
    ],
  },
  timeCondition: {
    read: (id) => `/api/v1/time-conditions/${id}`,
    update: "putTimeCondition",
    create: "createTimeCondition",
    delete: "deleteTimeCondition",
    nameKey: "name",
    put: true,
    pick: only(TC_KEYS),
    settings: [
      { key: "name", label: "Name", type: "text" },
      { key: "toggleNumber", label: "Toggle number", type: "text" },
    ],
  },
  schedule: { create: "createSchedule", delete: "deleteSchedule" },
  holidayCalendar: { delete: "deleteCalendar" },
  mohClass: { delete: "deleteMOHClass" },
  parkLot: { delete: "deleteParkLot" },
  pickupGroup: { delete: "deletePickupGroup" },
  conference: { create: "createConference", delete: "deleteConference" },
  multicastChannel: { delete: "deleteMulticastChannel" },
  pagingGroup: { delete: "deletePagingGroup" },
  featureCode: {
    read: () => "/api/v1/feature-codes",
    update: "putFeatureCodes",
    put: true,
    pick: (g) => ({
      items: members(g.items, ["code", "action", "argument"]),
    }),
  },
};

/** The one operation that replaces the feature codes as a whole. */
export const FEATURE_CODES_OP = "putFeatureCodes";

export const canEditKind = (kind: string): boolean =>
  Boolean(OBJECTS[kind]?.update);
export const canCreateKind = (kind: string): boolean =>
  Boolean(OBJECTS[kind]?.create);
export const canDeleteKind = (kind: string): boolean =>
  Boolean(OBJECTS[kind]?.delete);

// --- stored destinations --------------------------------------------------------

/** The stored kind of a destination, by the input it reaches. */
const STORED_KIND: Record<string, string> = {
  "extension.ring": "extension",
  "extension.voicemail": "voicemail",
  "ringGroup.ring": "ring_group",
  "ivr.call": "ivr",
  "queue.call": "queue",
  "announcement.call": "announcement",
  "pagingGroup.call": "paging",
  "conference.call": "conference",
  "timeCondition.call": "time_condition",
  "external.call": "external",
  "sipUri.call": "sip_uri",
  "internalDid.call": "internal",
};

export const storedKindOf = (kind: string, port: string): string | undefined =>
  STORED_KIND[`${kind}.${port}`];

/** What a destination stores to name this node (spec S-5's lookup keys). */
export function targetString(node: FlowNode): string {
  const s = node.summary ?? {};
  const text = (v: unknown) => (typeof v === "string" ? v : "");
  switch (node.kind) {
    case "extension":
      return text(s.number);
    case "pagingGroup":
      return text(s.code);
    case "conference":
      return text(s.number);
    case "external":
      return text(s.number) || node.label;
    case "internalDid":
      return "";
    default:
      return node.label;
  }
}

/** The feature-code action that goes with the kind of its target. */
export const FEATURE_ACTION: Record<string, string> = {
  announcement: "announcement",
  ivr: "ivr",
  timeCondition: "time_condition",
};

// --- the catalogue's compatibility check -----------------------------------------

export interface CanConnect {
  ok: boolean;
  /** The feature the connection waits for, when it cannot be stored yet. */
  requires?: string;
  note?: string;
  reason?: string;
}

/** "key:5" -> "key". */
export const portBase = (port: string): string => port.split(":")[0] ?? port;

/**
 * Whether an output may connect to an input, from the catalogue's target
 * lists: the single source of truth (the UI never hard-codes compatibility).
 */
export function canConnect(
  cat: FlowCatalogue | undefined,
  srcKind: string,
  srcPort: string,
  dstKind: string,
  dstPort: string,
): CanConnect {
  const src = cat?.nodes
    .find((n) => n.kind === srcKind)
    ?.ports.find((p) => p.id === portBase(srcPort) && p.direction === "out");
  if (!src) return { ok: false, reason: "not an output" };
  const dst = cat?.nodes
    .find((n) => n.kind === dstKind)
    ?.ports.find((p) => p.id === dstPort && p.direction === "in");
  if (!dst) return { ok: false, reason: "not an input" };
  const t = src.targets?.find((x) => x.kind === dstKind && x.port === dstPort);
  if (!t) {
    return { ok: false, reason: `${src.label} cannot connect to ${dst.label}` };
  }
  return { ok: !t.requires, requires: t.requires, note: t.note };
}

// --- edge adapters ----------------------------------------------------------------

/** What an adapter is given about one connection. */
export interface Conn {
  src: FlowNode;
  tgt: FlowNode;
  /** The source port as drawn ("key:5"). */
  srcPort: string;
  /** The key of a per-key port ("5"), or "". */
  key: string;
  tgtPort: string;
  /** The stored destination kind of the target, when it has one. */
  kind: string | undefined;
  /** The text a destination stores to name the target. */
  target: string;
  /** The target (or source) as an id in a body: a number, or a placeholder. */
  idOf: (node: FlowNode) => unknown;
}

export interface EdgeAdapter {
  /** Which node's object stores the connection. */
  owner: "source" | "target";
  /** The target kinds/ports this entry is for; absent: every one. */
  when?: (dstKind: string, dstPort: string) => boolean;
  /** Whether the stored value names its target by name (a rename follows it). */
  byName: boolean;
  /** The new values of the owner's body; a string is the reason it cannot. */
  connect: (view: Body, c: Conn) => Body | string;
  /** The values that remove the connection; absent when it cannot be removed. */
  disconnect?: (view: Body, c: Conn) => Body;
  /** Why the connection cannot be removed. */
  keep?: string;
}

const dest = (c: Conn) => ({ kind: c.kind ?? "hangup", target: c.target });
const hangup = { kind: "hangup", target: "" };

function exitAdapter(field: string): EdgeAdapter {
  return {
    owner: "source",
    byName: true,
    connect: (_v, c) => (c.kind ? { [field]: dest(c) } : "Not a destination"),
    disconnect: () => ({ [field]: hangup }),
  };
}

function forwardAdapter(field: string): EdgeAdapter {
  return {
    owner: "source",
    byName: true,
    connect: (_v, c) => {
      if (c.tgt.kind === "ringGroup" && !/^[0-9*#+]+$/.test(c.target)) {
        return "A ring group can be dialled from a forward only when its name is a number. Rename it first.";
      }
      return { [field]: c.target };
    },
    disconnect: () => ({ [field]: "" }),
  };
}

const list = (v: unknown): Body[] => (Array.isArray(v) ? (v as Body[]) : []);

function memberAdapter(keys: readonly string[]): EdgeAdapter {
  return {
    owner: "target",
    byName: false,
    connect: (v, c) => {
      const id = c.idOf(c.src);
      const cur = list(v.members);
      if (cur.some((m) => m.extensionId === id)) return { members: cur };
      const position =
        cur.reduce((n, m) => Math.max(n, Number(m.position) || 0), 0) + 1;
      return {
        members: [
          ...cur,
          only(keys)({ extensionId: id, position, dynamic: false }),
        ],
      };
    },
    disconnect: (v, c) => {
      const id = c.idOf(c.src);
      return {
        members: list(v.members)
          .filter((m) => m.extensionId !== id)
          .map((m, i) => ({ ...m, position: i + 1 })),
      };
    },
  };
}

/**
 * How each connection is stored, by the source node kind and port. The
 * entries only say how; whether a connection is allowed is the catalogue's.
 */
export const EDGE_ADAPTERS: Record<string, EdgeAdapter[]> = {
  "inboundRoute.destination": [
    {
      owner: "source",
      byName: true,
      connect: (_v, c) =>
        c.kind
          ? { destinationKind: c.kind, destination: c.target }
          : "Not a destination",
      keep: "An inbound route always needs a destination: connect it to another one instead.",
    },
  ],
  "ringGroup.noAnswer": [
    {
      owner: "source",
      byName: true,
      connect: (_v, c) =>
        c.kind
          ? { failureKind: c.kind, failureTarget: c.target }
          : "Not a destination",
      disconnect: () => ({ failureKind: "none", failureTarget: "" }),
    },
  ],
  "ivr.key": [
    {
      owner: "source",
      byName: true,
      connect: (v, c) => {
        if (!c.kind) return "Not a destination";
        if (!/^[0-9*#]$/.test(c.key)) return "A key is one of 0-9, * or #";
        const opts = list(v.options);
        const next = { digit: c.key, kind: c.kind, target: c.target };
        return {
          options: opts.some((o) => o.digit === c.key)
            ? opts.map((o) => (o.digit === c.key ? next : o))
            : [...opts, next],
        };
      },
      disconnect: (v, c) => ({
        options: list(v.options).filter((o) => o.digit !== c.key),
      }),
    },
  ],
  "ivr.timeout": [exitAdapter("timeout")],
  "ivr.invalid": [exitAdapter("invalid")],
  "queue.timeout": [exitAdapter("timeout")],
  "queue.full": [exitAdapter("full")],
  "queue.noAgents": [exitAdapter("noAgents")],
  "queue.breakout": [
    {
      owner: "source",
      byName: true,
      connect: (_v, c) => ({ breakoutVoicemail: c.target }),
      disconnect: () => ({ breakoutVoicemail: "" }),
    },
  ],
  "timeCondition.open": [exitAdapter("open")],
  "timeCondition.closed": [exitAdapter("closed")],
  "timeCondition.holiday": [
    {
      owner: "source",
      byName: true,
      connect: (_v, c) => (c.kind ? { holiday: dest(c) } : "Not a destination"),
      disconnect: () => ({ holiday: null }),
    },
  ],
  "extension.forwardAlways": [forwardAdapter("forwardAlways")],
  "extension.forwardBusy": [forwardAdapter("forwardBusy")],
  "extension.forwardNoAnswer": [forwardAdapter("forwardNoAnswer")],
  "extension.member": [
    {
      ...memberAdapter(["extensionId", "position", "weight", "delay"]),
      when: (k) => k === "ringGroup",
    },
    {
      ...memberAdapter(["extensionId", "dynamic", "penalty", "position"]),
      when: (k) => k === "queue",
    },
  ],
  "featureCode.target": [
    {
      owner: "source",
      byName: true,
      connect: (v, c) => {
        const action = FEATURE_ACTION[c.tgt.kind];
        if (!action) return "Not a feature code target";
        const code = idOf(c.src.id);
        return {
          items: list(v.items).map((i) =>
            i.code === code ? { ...i, action, argument: c.target } : i,
          ),
        };
      },
      keep: "A feature code needs a target: connect it to another one instead.",
    },
  ],
  "trunk.inbound": [
    {
      owner: "target",
      byName: false,
      connect: (_v, c) => ({ trunkId: c.idOf(c.src) }),
      keep: "An inbound route's trunk cannot be cleared here: connect it to another trunk, or to Any trunk.",
    },
  ],
  "anyTrunk.inbound": [
    {
      owner: "target",
      byName: false,
      connect: () => ({ trunkId: null }),
      keep: "Connect the inbound route to a trunk instead.",
    },
  ],
};

/** The adapter for a connection, or undefined when it is edited elsewhere. */
export function adapterFor(
  srcKind: string,
  srcPort: string,
  dstKind: string,
  dstPort: string,
): EdgeAdapter | undefined {
  return EDGE_ADAPTERS[`${srcKind}.${portBase(srcPort)}`]?.find(
    (a) => !a.when || a.when(dstKind, dstPort),
  );
}

/** Whether an output can be wired on the canvas at all. */
export const portIsEditable = (srcKind: string, srcPort: string): boolean =>
  `${srcKind}.${portBase(srcPort)}` in EDGE_ADAPTERS;

// --- creating -----------------------------------------------------------------------

export interface CreateField {
  key: string;
  label: string;
  type: "text" | "select" | "node";
  required?: boolean;
  options?: string[];
  default?: string;
  hint?: string;
  /** For "node": which kinds may be chosen. */
  kinds?: string[];
  /** For "node": the body key that holds the chosen object's id... */
  bodyKey?: string;
  /** ...and the connection that this draws, or */
  edge?: { sourcePort: string; targetPort: string };
  /** ...the output of the new node that is wired to the chosen one. */
  connectPort?: string;
}

export interface CreateForm {
  fields: CreateField[];
  /** The body of the create call, from the plain fields. */
  build: (v: Record<string, string>) => Body;
  /** What the new node's summary shows. */
  summary: (v: Record<string, string>) => Record<string, unknown>;
}

const name = (): CreateField => ({
  key: "name",
  label: "Name",
  type: "text",
  required: true,
});

export const CREATE_FORMS: Record<string, CreateForm> = {
  extension: {
    fields: [
      { key: "number", label: "Number", type: "text", required: true },
      name(),
    ],
    build: (v) => ({ number: v.number, name: v.name }),
    summary: (v) => ({ number: v.number, name: v.name }),
  },
  ringGroup: {
    fields: [
      name(),
      {
        key: "strategy",
        label: "Strategy",
        type: "select",
        options: [
          "ring-all",
          "sequential",
          "round-robin",
          "longest-idle",
          "weighted",
        ],
        default: "ring-all",
      },
    ],
    build: (v) => ({
      name: v.name,
      strategy: v.strategy || "ring-all",
      members: [],
    }),
    summary: (v) => ({ strategy: v.strategy || "ring-all", members: 0 }),
  },
  ivr: {
    fields: [
      name(),
      {
        key: "greeting",
        label: "Greeting",
        type: "node",
        required: true,
        kinds: ["announcement"],
        bodyKey: "greetingId",
        edge: { sourcePort: "audio", targetPort: "greeting" },
        hint: "Announcements are recorded on the Announcements page.",
      },
    ],
    build: (v) => ({ name: v.name }),
    summary: () => ({ options: 0 }),
  },
  queue: {
    fields: [name()],
    build: (v) => ({ name: v.name }),
    summary: () => ({ agents: 0 }),
  },
  timeCondition: {
    fields: [
      name(),
      {
        key: "schedule",
        label: "Schedule",
        type: "node",
        required: true,
        kinds: ["schedule"],
        bodyKey: "scheduleId",
        edge: { sourcePort: "use", targetPort: "schedule" },
      },
    ],
    build: (v) => ({ name: v.name }),
    summary: () => ({}),
  },
  schedule: {
    fields: [
      {
        ...name(),
        hint: "Letters, digits, dot, underscore and dash. Open hours are set on the Time conditions page.",
      },
    ],
    build: (v) => ({ name: v.name }),
    summary: () => ({}),
  },
  conference: {
    fields: [
      name(),
      { key: "number", label: "Number", type: "text", required: true },
    ],
    build: (v) => ({ name: v.name, number: v.number }),
    summary: (v) => ({ number: v.number }),
  },
  inboundRoute: {
    fields: [
      name(),
      {
        key: "did",
        label: "Number (empty: any)",
        type: "text",
      },
      {
        key: "destination",
        label: "Destination",
        type: "node",
        required: true,
        connectPort: "destination",
      },
    ],
    build: (v) => ({
      name: v.name,
      didKind: v.did ? "exact" : "any",
      did: v.did ?? "",
      enabled: true,
    }),
    summary: (v) => ({ did: v.did ?? "", enabled: true }),
  },
};
