// Voicemail groups API client (pbx-gaps S-14): a dial number and member
// extensions that have a voicemail box. A message left for a group, or
// broadcast by a member with *63 plus the number, lands in every member's box.

import { list, request, type Id, type PagingExtension } from "../api";

const seg = (value: Id) => encodeURIComponent(String(value));

/** Message length bounds, in seconds (PUT /api/v1/voicemail-groups). */
export const VM_GROUP_SECONDS = { min: 10, max: 600, default: 180 } as const;

/** The most members a group may have. */
export const VM_GROUP_MAX_MEMBERS = 200;

/** The writable fields of a group (PUT replaces the whole group). */
export interface VoicemailGroupFields {
  number: string;
  name: string;
  enabled: boolean;
  maxSeconds: number;
  /** Extension ids; each must have a voicemail box. */
  members: Id[];
}

/** A voicemail group as returned. */
export interface VoicemailGroup {
  id: Id;
  number: string;
  name: string;
  enabled: boolean;
  maxSeconds: number;
  members: PagingExtension[];
  createdAt: string;
  updatedAt: string;
}

export const listVoicemailGroups = (signal?: AbortSignal) =>
  list<VoicemailGroup>("/api/v1/voicemail-groups", signal);

export const createVoicemailGroup = (input: VoicemailGroupFields) =>
  request<VoicemailGroup>("POST", "/api/v1/voicemail-groups", { body: input });

export const putVoicemailGroup = (groupId: Id, input: VoicemailGroupFields) =>
  request<VoicemailGroup>("PUT", `/api/v1/voicemail-groups/${seg(groupId)}`, {
    body: input,
  });

export const deleteVoicemailGroup = (groupId: Id) =>
  request<void>("DELETE", `/api/v1/voicemail-groups/${seg(groupId)}`);
