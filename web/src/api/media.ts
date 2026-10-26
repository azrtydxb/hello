// Media API client for the console's Voicemail, Recordings and Announcements
// pages: the calls and fields the shared client in ../api.ts does not carry.

import type { TtsRecord } from "./tts";
import {
  list,
  recordingAudioPath,
  request,
  responseJson,
  type Announcement,
  type Id,
  type Recording,
} from "../api";

const seg = (value: Id) => encodeURIComponent(String(value));

// --- voicemail ----------------------------------------------------------------

/** GET /api/v1/voicemail/boxes: one box with its extension and counts. */
export interface VoicemailBoxSummary {
  /** The box id: the messages list's `box` parameter. */
  id: Id;
  extensionId: Id;
  number: string;
  name: string;
  unheard: number;
  total: number;
}

/** GET /api/v1/voicemail/boxes, ordered by extension number. */
export const listVoicemailBoxes = (signal?: AbortSignal) =>
  list<VoicemailBoxSummary>("/api/v1/voicemail/boxes", signal);

/** GET /api/v1/extensions/{id}/voicemail as the server sends it. */
export interface VoicemailBoxDetail {
  id: Id;
  extensionId: Id;
  email: string;
  hasPassword: boolean;
  /** MinIO key of the uploaded greeting; "" when none. */
  greetingObject?: string;
  /** MinIO key of the unreachable greeting; "" when none. */
  unreachableObject?: string;
  /** MinIO key of the busy greeting; "" when none. */
  busyObject?: string;
  /** MinIO key of the owner's recorded name; "" when none. */
  nameObject?: string;
  createdAt: string;
  updatedAt: string;
  /** How the greeting was generated from text; null for uploaded audio. */
  greetingTts?: TtsRecord | null;
  /** How the unreachable greeting was generated; null for uploaded audio. */
  unreachableTts?: TtsRecord | null;
  /** How the busy greeting was generated; null for uploaded audio. */
  busyTts?: TtsRecord | null;
  /** Whether this deployment can email voicemail (SMTP is configured). */
  emailDelivery?: boolean;
  /**
   * Whether a text-to-speech key is set: a greeting slot with no audio of
   * its own is then spoken in the AI voice ("Default (AI voice)").
   */
  aiVoice?: boolean;
}

/** The audio slots of a box. */
export type GreetingSlot = "greeting" | "busy" | "unreachable" | "name";

/** The slots whose default is a spoken greeting that can be previewed. */
export type PreviewSlot = Exclude<GreetingSlot, "name">;

/** Whether a slot holds the box's own audio (an upload, recording or text). */
export function isCustomGreeting(
  box: VoicemailBoxDetail,
  slot: GreetingSlot,
): boolean {
  const object = {
    greeting: box.greetingObject,
    busy: box.busyObject,
    unreachable: box.unreachableObject,
    name: box.nameObject,
  }[slot];
  return Boolean(object);
}

/** The status badge text of a slot: Custom, or what callers hear instead. */
export function greetingStatus(
  box: VoicemailBoxDetail,
  slot: GreetingSlot,
): "Custom" | "Default (AI voice)" | "Default (no voice set)" {
  if (isCustomGreeting(box, slot)) return "Custom";
  return box.aiVoice === false
    ? "Default (no voice set)"
    : "Default (AI voice)";
}

/**
 * POST /api/v1/extensions/{id}/voicemail/greeting/preview: the default
 * spoken greeting of a variant, as WAV audio. The first request after a
 * voice change generates it (and spends credits); later ones are stored.
 */
export async function previewDefaultGreeting(
  extensionId: Id,
  slot: PreviewSlot,
  signal?: AbortSignal,
): Promise<Blob> {
  const path = `/api/v1/extensions/${seg(extensionId)}/voicemail/greeting/preview`;
  const res = await fetch(path, {
    method: "POST",
    headers: { Accept: "audio/wav", "Content-Type": "application/json" },
    body: JSON.stringify({ slot }),
    credentials: "same-origin",
    signal,
  });
  if (!res.ok) {
    // responseJson throws the API error envelope as an ApiError.
    await responseJson<unknown>(res, "POST", path);
  }
  return res.blob();
}

/**
 * DELETE /api/v1/extensions/{id}/voicemail/greeting/{slot}: drop the box's
 * own audio so callers hear the default spoken prompt again.
 */
export const revertGreeting = (extensionId: Id, slot: GreetingSlot) =>
  request<VoicemailBoxDetail>(
    "DELETE",
    `/api/v1/extensions/${seg(extensionId)}/voicemail/greeting/${slot}`,
  );

/** GET /api/v1/extensions/{id}/voicemail. */
export const getVoicemailBoxDetail = (extensionId: Id, signal?: AbortSignal) =>
  request<VoicemailBoxDetail>(
    "GET",
    `/api/v1/extensions/${seg(extensionId)}/voicemail`,
    { signal },
  );

/** What the box says about its greetings, in the design's words. */
export function greetingLabel(box: VoicemailBoxDetail): string {
  const greeting = Boolean(box.greetingObject);
  const unreachable = Boolean(box.unreachableObject);
  if (greeting && unreachable) return "Greeting + unreachable uploaded";
  if (greeting) return "Greeting uploaded";
  if (unreachable) return "Unreachable greeting uploaded";
  if (box.busyObject) return "Busy greeting uploaded";
  return box.aiVoice ? "Default greeting (AI voice)" : "Default greeting";
}

// --- recordings ---------------------------------------------------------------

/** A recording with the parties of its call's CDR. */
export interface CallRecording extends Recording {
  /** The call's CDR; absent until the CDR is written. */
  cdrId?: Id;
  /** "" until the CDR is written. */
  source?: string;
  destination?: string;
}

/** One page of GET /api/v1/recordings. */
export interface CallRecordingPage {
  items: CallRecording[];
  /** Cursor for the next (older) page; "" at the end. */
  next: string;
}

/** GET /api/v1/recordings?extension=&before=&limit=, newest first. */
export async function listCallRecordings(
  opts: { extension?: string; before?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<CallRecordingPage> {
  const q = new URLSearchParams();
  if (opts.extension) q.set("extension", opts.extension);
  if (opts.before) q.set("before", opts.before);
  if (opts.limit !== undefined) q.set("limit", String(opts.limit));
  const qs = q.toString();
  const body = await request<{ items?: unknown; next?: unknown }>(
    "GET",
    `/api/v1/recordings${qs ? `?${qs}` : ""}`,
    { signal },
  );
  if (!Array.isArray(body.items)) {
    throw new Error("GET /api/v1/recordings returned an unexpected payload");
  }
  return {
    items: body.items as CallRecording[],
    next: typeof body.next === "string" ? body.next : "",
  };
}

/**
 * GET /api/v1/recordings/{id}/audio?download=1: the audio with an attachment
 * disposition, so following it saves recording-<id>.wav.
 */
export const recordingDownloadPath = (recordingId: Id) =>
  `${recordingAudioPath(recordingId)}?download=1`;

// --- announcements ------------------------------------------------------------

/** GET /api/v1/announcements/{id}/audio: the audio, streamed. */
export const announcementAudioPath = (announcementId: Id) =>
  `/api/v1/announcements/${seg(announcementId)}/audio`;

/**
 * PUT /api/v1/announcements/{id}: multipart with the new WAV as `file`.
 * The name stays; the audio is overwritten in place.
 */
export function replaceAnnouncement(
  announcementId: Id,
  file: File,
): Promise<Announcement> {
  const form = new FormData();
  form.set("file", file, file.name);
  return request<Announcement>(
    "PUT",
    `/api/v1/announcements/${seg(announcementId)}`,
    { rawBody: form },
  );
}

/** An announcement's "Updated" cell: "—" until its audio is replaced. */
export function wasReplaced(a: Announcement): boolean {
  if (!a.updatedAt) return false;
  const created = Date.parse(a.createdAt);
  const updated = Date.parse(a.updatedAt);
  return Number.isFinite(created) && Number.isFinite(updated)
    ? updated - created >= 1000
    : a.updatedAt !== a.createdAt;
}
