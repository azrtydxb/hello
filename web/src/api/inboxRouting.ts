// Routing and ACD of the messaging inbox (spec messaging-channels M-13,
// M-14): where a number's conversations go, what the text queues look like
// and an agent's availability: the HTTP JSON of internal/api/inbox_routing.go.

import { request, type Id } from "../api";

export type RouteKind = "inbox" | "queue" | "user" | "extension" | "ring_group";

export const ROUTE_KIND_LABEL: Record<RouteKind, string> = {
  inbox: "The supervisors' pool",
  queue: "A queue",
  user: "One user",
  extension: "The user of an extension",
  ring_group: "The users of a ring group",
};

export interface ScheduleDay {
  /** 0 is Sunday. */
  day: number;
  from: string;
  to: string;
}

export interface Schedule {
  zone: string;
  days: ScheduleDay[];
}

export type AfterHoursAction = "none" | "reply" | "route";

export interface AfterHours {
  action: AfterHoursAction;
  text: string;
  routeKind: RouteKind;
  routeTarget: string;
  routeName: string;
}

export interface MessagingRouting {
  addressId: number;
  routeKind: RouteKind;
  routeTarget: string;
  routeName: string;
  schedule: Schedule;
  afterHours: AfterHours;
  /** Sent once to a customer who texts the number for the first time. */
  firstContactText: string;
}

export interface RoutingInput {
  routeKind: RouteKind;
  routeTarget: string;
  schedule: Schedule;
  firstContactText: string;
  afterHours: {
    action: AfterHoursAction;
    text: string;
    routeKind: RouteKind;
    routeTarget: string;
  };
}

export const getRouting = (addressId: Id, signal?: AbortSignal) =>
  request<MessagingRouting>(
    "GET",
    `/api/v1/messaging/addresses/${addressId}/routing`,
    { signal },
  );

export const putRouting = (addressId: Id, body: RoutingInput) =>
  request<MessagingRouting>(
    "PUT",
    `/api/v1/messaging/addresses/${addressId}/routing`,
    { body },
  );

export type AgentState =
  "available" | "paused" | "logged_out" | "on_call" | "at_capacity" | "unknown";

export const AGENT_STATE_LABEL: Record<AgentState, string> = {
  available: "Available",
  paused: "Paused",
  logged_out: "Logged out",
  on_call: "On a call",
  at_capacity: "At capacity",
  unknown: "Unknown",
};

export interface QueueWaiting {
  conversationId: number;
  channel: string;
  position: number;
  waitSeconds: number;
}

export interface QueueAgent {
  userId: number;
  name: string;
  extension: string;
  dynamic: boolean;
  open: number;
  answered: number;
  state: AgentState;
}

export interface TextQueue {
  id: number;
  name: string;
  strategy: string;
  capacity: number;
  waiting: QueueWaiting[];
  waitingTotal: number;
  assigned: number;
  oldestWaitSeconds: number;
  agents: QueueAgent[];
}

export const listTextQueues = async (signal?: AbortSignal) =>
  (
    await request<{ items: TextQueue[] }>("GET", "/api/v1/inbox/queues", {
      signal,
    })
  ).items;

export interface AgentExtension {
  number: string;
  name: string;
  queues: string[];
  dynamic: boolean;
  loggedIn: boolean;
  paused: boolean;
}

export interface AgentAvailability {
  /** Whether any of the caller's extensions answers a text queue. */
  member: boolean;
  needsLogin: boolean;
  loggedIn: boolean;
  paused: boolean;
  reason: string;
  extensions: AgentExtension[];
}

export const getAgentAvailability = (signal?: AbortSignal) =>
  request<AgentAvailability>("GET", "/api/v1/me/inbox/agent", { signal });

export const setAgentAvailability = (body: {
  loggedIn?: boolean;
  paused?: boolean;
  reason?: string;
}) => request<AgentAvailability>("POST", "/api/v1/me/inbox/agent", { body });
