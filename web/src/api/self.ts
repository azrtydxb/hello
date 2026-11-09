// Client for the extension self-service routes (/api/v1/me/...): everything
// here acts on the signed-in user's own extensions only.
import { request, type Id } from "../api";

const seg = (value: Id) => encodeURIComponent(String(value));

/** An extension as its own user sees it. */
export interface SelfExtension {
  id: Id;
  number: string;
  /** The display label. */
  name: string;
  dnd: boolean;
  forwardAlways: string;
  forwardBusy: string;
  forwardNoAnswer: string;
  voicemailEnabled: boolean;
}

/** The user's voicemail box. */
export interface SelfVoicemailBox {
  extensionId: Id;
  boxId: Id;
  hasPin: boolean;
  hasGreeting: boolean;
  hasUnreachableGreeting: boolean;
  /** The box's own prompt language ("en", "nl"); "" inherits. */
  promptLanguage: string;
  /** The languages the box can be set to. */
  languages: string[];
}

/** One message in the user's box. */
export interface SelfMessage {
  id: Id;
  boxId: Id;
  caller: string;
  durationMs: number;
  heard: boolean;
  createdAt: string;
}

/** One call of the user's history. */
export interface SelfCall {
  id: Id;
  startTime: string;
  durationMs: number;
  direction: string;
  kind: string;
  source: string;
  destination: string;
  answered: boolean;
}

export async function listMyExtensions(
  signal?: AbortSignal,
): Promise<SelfExtension[]> {
  const r = await request<{ items: SelfExtension[] }>(
    "GET",
    "/api/v1/me/extensions",
    { signal },
  );
  return r.items;
}

export function updateMyExtension(
  id: Id,
  change: Partial<
    Pick<
      SelfExtension,
      "name" | "dnd" | "forwardAlways" | "forwardBusy" | "forwardNoAnswer"
    >
  >,
): Promise<SelfExtension> {
  return request<SelfExtension>("PATCH", `/api/v1/me/extensions/${seg(id)}`, {
    body: change,
  });
}

export function getMyVoicemail(
  extensionId: Id,
  signal?: AbortSignal,
): Promise<SelfVoicemailBox> {
  return request<SelfVoicemailBox>(
    "GET",
    `/api/v1/me/extensions/${seg(extensionId)}/voicemail`,
    { signal },
  );
}

/**
 * Change the PIN, the prompt language and/or upload greetings (WAV files).
 * The language travels as JSON, so with files it is sent first, on its own.
 */
export async function updateMyVoicemail(
  extensionId: Id,
  input: {
    pin?: string;
    promptLanguage?: string;
    greeting?: Blob;
    unreachable?: Blob;
  },
): Promise<SelfVoicemailBox> {
  const path = `/api/v1/me/extensions/${seg(extensionId)}/voicemail`;
  const files = Boolean(input.greeting || input.unreachable);
  if (!files) {
    return request<SelfVoicemailBox>("PUT", path, {
      body: {
        ...(input.pin !== undefined ? { pin: input.pin } : {}),
        ...(input.promptLanguage !== undefined
          ? { promptLanguage: input.promptLanguage }
          : {}),
      },
    });
  }
  if (input.promptLanguage !== undefined) {
    await request<SelfVoicemailBox>("PUT", path, {
      body: { promptLanguage: input.promptLanguage },
    });
  }
  const form = new FormData();
  if (input.pin) form.set("pin", input.pin);
  if (input.greeting) form.set("greeting", input.greeting, "greeting.wav");
  if (input.unreachable) {
    form.set("unreachable", input.unreachable, "unreachable.wav");
  }
  return request<SelfVoicemailBox>("PUT", path, { rawBody: form });
}

export async function listMyMessages(
  extensionId: Id,
  signal?: AbortSignal,
): Promise<SelfMessage[]> {
  const r = await request<{ items: SelfMessage[] }>(
    "GET",
    `/api/v1/me/extensions/${seg(extensionId)}/voicemail/messages`,
    { signal },
  );
  return r.items;
}

export function markMyMessageHeard(id: Id, heard: boolean): Promise<void> {
  return request<void>(
    "POST",
    `/api/v1/me/voicemail/messages/${seg(id)}/heard`,
    { body: { heard } },
  );
}

export function deleteMyMessage(id: Id): Promise<void> {
  return request<void>("DELETE", `/api/v1/me/voicemail/messages/${seg(id)}`);
}

/** The audio of one of the user's messages; `download` makes it a file. */
export function myMessageAudioPath(id: Id, download = false): string {
  return `/api/v1/me/voicemail/messages/${seg(id)}/audio${download ? "?download=1" : ""}`;
}

export async function listMyCalls(
  cursor?: string,
  signal?: AbortSignal,
): Promise<{ items: SelfCall[]; next: string }> {
  const q = new URLSearchParams({ limit: "50" });
  if (cursor) q.set("cursor", cursor);
  return request<{ items: SelfCall[]; next: string }>(
    "GET",
    `/api/v1/me/calls?${q.toString()}`,
    { signal },
  );
}
