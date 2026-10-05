// Media API client for the console's Voicemail, Recordings and Announcements
// pages: the calls and fields the shared client in ../api.ts does not carry.

import {
  list,
  recordingAudioPath,
  request,
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
  createdAt: string;
  updatedAt: string;
}

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
  return "Default greeting";
}

// --- recordings ---------------------------------------------------------------

/** A recording with the parties of its call's first CDR. */
export interface CallRecording extends Recording {
  /** The call's first CDR; absent until the CDR is written. */
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
 * GET /api/v1/recordings/{id}/audio?download=1: the presigned URL carries
 * an attachment disposition, so following it saves recording-<id>.wav.
 */
export const recordingDownloadPath = (recordingId: Id) =>
  `${recordingAudioPath(recordingId)}?download=1`;

// --- announcements ------------------------------------------------------------

/** GET /api/v1/announcements/{id}/audio: 302 to a presigned URL. */
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
