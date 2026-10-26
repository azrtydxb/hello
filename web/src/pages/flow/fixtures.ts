// Fixtures shared by the flow canvas tests: a small catalogue and a graph
// shaped like the API's (inbound route -> ring group <- extensions).
import type {
  FlowCanvasDetail,
  FlowCatalogue,
  FlowGraph,
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
