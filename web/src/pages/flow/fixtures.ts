// Fixtures shared by the flow canvas tests: a small catalogue and a graph
// shaped like the API's (inbound route -> ring group <- extensions).
import type {
  FlowCanvasDetail,
  FlowCatalogue,
  FlowGraph,
  FlowNode,
  FlowPort,
} from "../../api/flow";

export const CATALOGUE: FlowCatalogue = {
  portTypes: [
    { type: "call", label: "Call", colour: "#2563EB", shape: "circle" },
    { type: "member", label: "Member", colour: "#16A34A", shape: "diamond" },
    { type: "carrier", label: "Carrier", colour: "#EA580C", shape: "square" },
  ],
  nodes: [
    {
      kind: "inboundRoute",
      label: "Inbound route",
      ports: [
        {
          id: "trunk",
          direction: "in",
          type: "carrier",
          many: false,
          label: "Trunk",
        },
        {
          id: "destination",
          direction: "out",
          type: "call",
          many: false,
          label: "Destination",
        },
      ],
    },
    {
      kind: "ringGroup",
      label: "Ring group",
      ports: [
        {
          id: "ring",
          direction: "in",
          type: "call",
          many: true,
          label: "Ring",
        },
        {
          id: "members",
          direction: "in",
          type: "member",
          many: true,
          label: "Members",
        },
        {
          id: "noAnswer",
          direction: "out",
          type: "call",
          many: false,
          label: "No answer",
        },
      ],
    },
    {
      kind: "extension",
      label: "Extension",
      ports: [
        {
          id: "ring",
          direction: "in",
          type: "call",
          many: true,
          label: "Ring",
        },
        {
          id: "member",
          direction: "out",
          type: "member",
          many: true,
          label: "Member of",
        },
      ],
    },
    {
      kind: "ivr",
      label: "IVR menu",
      ports: [
        {
          id: "call",
          direction: "in",
          type: "call",
          many: true,
          label: "Call",
        },
        {
          id: "timeout",
          direction: "out",
          type: "call",
          many: false,
          label: "Timeout",
        },
      ],
    },
  ],
};

export const GRAPH: FlowGraph = {
  configRevision: 12,
  nodes: [
    {
      id: "inboundRoute:1",
      kind: "inboundRoute",
      label: "Main number",
      summary: { did: "+3225551234", position: 1, enabled: true },
    },
    {
      id: "ringGroup:1",
      kind: "ringGroup",
      label: "Sales",
      summary: { strategy: "ring_all", members: 2 },
    },
    {
      id: "extension:1",
      kind: "extension",
      label: "100 Ann",
      summary: { number: "100", dnd: false },
    },
    {
      id: "extension:2",
      kind: "extension",
      label: "101 Bob",
      summary: { number: "101", dnd: false },
    },
    { id: "ivr:5", kind: "ivr", label: "Welcome", summary: { number: "800" } },
  ],
  edges: [
    {
      id: "inboundRoute:1.destination>ringGroup:1.ring",
      source: { node: "inboundRoute:1", port: "destination" },
      target: { node: "ringGroup:1", port: "ring" },
      owner: { node: "inboundRoute:1", field: "destinationKind+destination" },
    },
    {
      id: "extension:1.member>ringGroup:1.members",
      source: { node: "extension:1", port: "member" },
      target: { node: "ringGroup:1", port: "members" },
      owner: { node: "ringGroup:1", field: "members" },
      data: { position: 1 },
    },
    {
      id: "extension:2.member>ringGroup:1.members",
      source: { node: "extension:2", port: "member" },
      target: { node: "ringGroup:1", port: "members" },
      owner: { node: "ringGroup:1", field: "members" },
      data: { position: 2 },
    },
    {
      // An IVR key: a port the catalogue does not list.
      id: "ivr:5.1>extension:2.ring",
      source: { node: "ivr:5", port: "1" },
      target: { node: "extension:2", port: "ring" },
      owner: { node: "ivr:5", field: "options" },
    },
  ],
  warnings: [
    {
      node: "ringGroup:1",
      code: "empty_group",
      message: "Ring group Sales has no members.",
    },
  ],
};

export const CANVAS: FlowCanvasDetail = {
  id: 1,
  name: "Main office",
  description: "",
  layoutVersion: 3,
  nodeCount: 3,
  createdBy: "admin",
  createdAt: "2026-10-25T10:00:00Z",
  updatedAt: "2026-10-25T10:00:00Z",
  nodes: [
    { node: "inboundRoute:1", x: 0, y: 0, collapsed: false },
    { node: "ringGroup:1", x: 400, y: 0, collapsed: false },
    { node: "extension:1", x: 0, y: 200, collapsed: false },
  ],
};

// --- editing ---------------------------------------------------------------------

type PortSpec = Partial<FlowPort> & Pick<FlowPort, "id" | "direction">;

const port = (p: PortSpec): FlowPort => ({
  type: "call",
  many: p.direction === "in",
  label: p.id,
  ...p,
});

const t = (kind: string, p: string) => ({ kind, port: p });

const EXIT = [
  t("extension", "ring"),
  t("extension", "voicemail"),
  t("ringGroup", "ring"),
  t("ivr", "call"),
  t("queue", "call"),
  t("timeCondition", "call"),
  t("external", "call"),
];

/** A catalogue with the connections the editing tests use. */
export const EDIT_CATALOGUE: FlowCatalogue = {
  portTypes: CATALOGUE.portTypes,
  nodes: [
    {
      kind: "inboundRoute",
      label: "Inbound route",
      ports: [
        port({ id: "trunk", direction: "in", type: "carrier", many: false }),
        port({
          id: "destination",
          direction: "out",
          many: false,
          label: "Destination",
          field: "destinationKind+destination",
          targets: EXIT,
        }),
      ],
    },
    {
      kind: "trunk",
      label: "Trunk",
      ports: [
        port({
          id: "inbound",
          direction: "out",
          type: "carrier",
          many: true,
          targets: [t("inboundRoute", "trunk")],
        }),
      ],
    },
    {
      kind: "ringGroup",
      label: "Ring group",
      ports: [
        port({ id: "ring", direction: "in" }),
        port({ id: "members", direction: "in", type: "member" }),
        port({
          id: "noAnswer",
          direction: "out",
          many: false,
          label: "No answer",
          field: "failureKind+failureTarget",
          targets: EXIT,
        }),
      ],
    },
    {
      kind: "extension",
      label: "Extension",
      ports: [
        port({ id: "ring", direction: "in" }),
        port({ id: "voicemail", direction: "in", type: "voicemail" }),
        port({
          id: "forwardAlways",
          direction: "out",
          many: false,
          field: "forwardAlways",
          targets: [
            t("extension", "ring"),
            t("ringGroup", "ring"),
            t("external", "call"),
          ],
        }),
        port({
          id: "member",
          direction: "out",
          type: "member",
          many: true,
          field: "members",
          targets: [t("ringGroup", "members")],
        }),
      ],
    },
    {
      kind: "ivr",
      label: "IVR menu",
      ports: [
        port({ id: "call", direction: "in" }),
        port({
          id: "key",
          direction: "out",
          many: false,
          perKey: true,
          field: "options",
          targets: EXIT,
        }),
        port({
          id: "timeout",
          direction: "out",
          many: false,
          field: "timeout",
          targets: EXIT,
        }),
      ],
    },
    {
      kind: "queue",
      label: "Call queue",
      ports: [port({ id: "call", direction: "in" })],
    },
    {
      kind: "timeCondition",
      label: "Time condition",
      ports: [
        port({ id: "call", direction: "in" }),
        port({
          id: "open",
          direction: "out",
          many: false,
          field: "open",
          targets: EXIT,
        }),
      ],
    },
    {
      kind: "external",
      label: "External number",
      terminal: true,
      ports: [port({ id: "call", direction: "in" })],
    },
    {
      kind: "featureCode",
      label: "Feature code",
      ports: [
        port({
          id: "target",
          direction: "out",
          many: false,
          field: "argument",
          targets: [
            t("announcement", "call"),
            t("ivr", "call"),
            t("timeCondition", "call"),
          ],
        }),
      ],
    },
    {
      kind: "schedule",
      label: "Schedule",
      ports: [
        port({
          id: "use",
          direction: "out",
          type: "schedule",
          targets: [t("timeCondition", "schedule")],
        }),
      ],
    },
    {
      kind: "announcement",
      label: "Announcement",
      ports: [
        port({ id: "call", direction: "in" }),
        port({
          id: "audio",
          direction: "out",
          type: "prompt",
          targets: [t("ivr", "greeting")],
        }),
      ],
    },
  ],
};

const n = (id: string, label: string, summary: FlowNode["summary"] = {}) => ({
  id,
  kind: id.slice(0, id.indexOf(":")),
  label,
  summary,
  version: `v-${id}`,
});

/** A small office: route -> IVR -> sales group, a queue and a time condition. */
export const EDIT_GRAPH: FlowGraph = {
  configRevision: 7,
  nodes: [
    n("trunk:1", "carrier"),
    n("inboundRoute:1", "Main number"),
    n("ivr:1", "welcome", { number: "800" }),
    n("ringGroup:1", "sales"),
    n("ringGroup:2", "support"),
    n("extension:1", "100 Ann", { number: "100" }),
    n("extension:2", "101 Bob", { number: "101" }),
    n("queue:1", "helpdesk"),
    n("timeCondition:1", "hours"),
    n("schedule:1", "office"),
    n("announcement:1", "greeting"),
    n("featureCode:*72", "*72", { code: "*72", action: "announcement" }),
    n("featureCode:*73", "*73", { code: "*73", action: "ivr" }),
  ],
  edges: [
    {
      id: "inboundRoute:1.destination->ivr:1.call",
      source: { node: "inboundRoute:1", port: "destination" },
      target: { node: "ivr:1", port: "call" },
      owner: { node: "inboundRoute:1", field: "destinationKind+destination" },
    },
    {
      id: "ivr:1.key:1->ringGroup:1.ring",
      source: { node: "ivr:1", port: "key:1" },
      target: { node: "ringGroup:1", port: "ring" },
      owner: { node: "ivr:1", field: "options" },
    },
    {
      id: "ivr:1.timeout->ringGroup:1.ring",
      source: { node: "ivr:1", port: "timeout" },
      target: { node: "ringGroup:1", port: "ring" },
      owner: { node: "ivr:1", field: "timeout" },
    },
    {
      id: "extension:1.member->ringGroup:1.members",
      source: { node: "extension:1", port: "member" },
      target: { node: "ringGroup:1", port: "members" },
      owner: { node: "ringGroup:1", field: "members" },
    },
  ],
  warnings: [],
};

/** What GET returns for the objects of EDIT_GRAPH. */
export const EDIT_BODIES: Record<string, Record<string, unknown>> = {
  "/api/v1/ivr-menus/1": {
    id: 1,
    name: "welcome",
    number: "800",
    enabled: true,
    greetingId: 1,
    greetingName: "greeting",
    maxRetries: 3,
    timeout: { kind: "ring_group", target: "sales" },
    invalid: { kind: "hangup", target: "" },
    options: [{ digit: "1", kind: "ring_group", target: "sales" }],
    createdAt: "x",
  },
  "/api/v1/ring-groups/1": {
    id: 1,
    name: "sales",
    strategy: "ring-all",
    ringTimeout: 20,
    failureKind: "none",
    failureTarget: "",
    members: [
      {
        extensionId: 1,
        number: "100",
        name: "Ann",
        position: 1,
        weight: 1,
        delay: 0,
      },
    ],
  },
  "/api/v1/ring-groups/2": {
    id: 2,
    name: "support",
    strategy: "ring-all",
    ringTimeout: 20,
    failureKind: "none",
    failureTarget: "",
    members: [],
  },
  "/api/v1/routes/inbound/1": {
    id: 1,
    name: "Main number",
    didKind: "exact",
    did: "+3225551234",
    destinationKind: "ivr",
    destination: "welcome",
    enabled: true,
  },
  "/api/v1/time-conditions/1": {
    id: 1,
    name: "hours",
    scheduleId: 1,
    open: { kind: "ring_group", target: "sales" },
    closed: { kind: "hangup", target: "" },
    holiday: null,
    toggleNumber: "",
    override: "auto",
  },
  "/api/v1/queues/1": { id: 1, name: "helpdesk", members: [] },
  "/api/v1/extensions/1": { id: 1, number: "100", name: "Ann" },
  "/api/v1/extensions/2": { id: 2, number: "101", name: "Bob" },
  "/api/v1/feature-codes": {
    items: [
      { code: "*72", action: "announcement", argument: "greeting" },
      { code: "*73", action: "ivr", argument: "welcome" },
      { code: "*30", action: "dnd_on", argument: "" },
    ],
  },
};
