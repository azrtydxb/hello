// Follow-me profiles (migration 00072): GET|PUT /api/v1/extensions/{id}/follow-me
// for operators and GET|PUT /api/v1/me/extensions/{id}/follow-me for the user
// of the extension.
import { request, type Id } from "../api";

const seg = (value: Id) => encodeURIComponent(String(value));

export type FollowMeKind = "external" | "extension" | "ring_group";
export type FollowMeStrategy = "simultaneous" | "sequential";

/** The most targets a profile holds. */
export const FOLLOW_ME_MAX_TARGETS = 5;

export interface FollowMeTarget {
  kind: FollowMeKind;
  /** The number (external), the extension number or the ring group name. */
  target: string;
  /** Sequential only: seconds the target rings before the next starts. */
  delaySeconds: number;
}

export interface FollowMe {
  extensionId: Id;
  number: string;
  enabled: boolean;
  strategy: FollowMeStrategy;
  ringOwnDevices: boolean;
  timeoutSeconds: number;
  confirmExternal: boolean;
  dndBlocks: boolean;
  targets: FollowMeTarget[];
}

/** The fields of a change; a field left out keeps its stored value. */
export type FollowMeChange = Partial<Omit<FollowMe, "extensionId" | "number">>;

export const getFollowMe = (id: Id, signal?: AbortSignal) =>
  request<FollowMe>("GET", `/api/v1/extensions/${seg(id)}/follow-me`, {
    signal,
  });

export const putFollowMe = (id: Id, change: FollowMeChange) =>
  request<FollowMe>("PUT", `/api/v1/extensions/${seg(id)}/follow-me`, {
    body: change,
  });

export const getMyFollowMe = (id: Id, signal?: AbortSignal) =>
  request<FollowMe>("GET", `/api/v1/me/extensions/${seg(id)}/follow-me`, {
    signal,
  });

export const putMyFollowMe = (id: Id, change: FollowMeChange) =>
  request<FollowMe>("PUT", `/api/v1/me/extensions/${seg(id)}/follow-me`, {
    body: change,
  });
