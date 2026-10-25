// Text-to-speech API client: the OpenRouter settings and the one-time
// generation of prompt audio. Generation only ever runs on an explicit click;
// playback reads the stored audio and never reaches the provider.

import { request, type Announcement, type Id } from "../api";

const seg = (value: Id) => encodeURIComponent(String(value));

/** GET /api/v1/tts/settings: the key itself is never returned. */
export interface TtsSettings {
  provider: string;
  /** Whether an API key is stored. */
  configured: boolean;
  /** The last four characters of the stored key; "" when none. */
  keyHint: string;
  /** The default OpenRouter model, e.g. elevenlabs/eleven-multilingual-v2. */
  model: string;
  /** The default voice; "" for none. */
  voice: string;
  updatedAt: string;
}

/** The longest prompt text the server accepts. */
export const MAX_TTS_CHARS = 2500;

/** The model a fresh install starts with. */
export const DEFAULT_TTS_MODEL = "elevenlabs/eleven-multilingual-v2";

export const getTtsSettings = (signal?: AbortSignal) =>
  request<TtsSettings>("GET", "/api/v1/tts/settings", { signal });

/** PUT /api/v1/tts/settings; every field is optional, `apiKey` is write-only. */
export const saveTtsSettings = (input: {
  apiKey?: string;
  model?: string;
  voice?: string;
}) => request<TtsSettings>("PUT", "/api/v1/tts/settings", { body: input });

/** DELETE /api/v1/tts/settings/key. */
export const deleteTtsKey = () =>
  request<TtsSettings>("DELETE", "/api/v1/tts/settings/key");

/** POST /api/v1/tts/settings/test: checks the stored key with OpenRouter. */
export const testTtsConnection = () =>
  request<{ ok: boolean }>("POST", "/api/v1/tts/settings/test");

/** One OpenRouter ElevenLabs model and its voices. */
export interface TtsModel {
  id: string;
  name: string;
  voices: string[];
}

/** GET /api/v1/tts/voices. */
export async function listTtsModels(signal?: AbortSignal): Promise<TtsModel[]> {
  const body = await request<{ models?: TtsModel[] }>(
    "GET",
    "/api/v1/tts/voices",
    { signal },
  );
  return body.models ?? [];
}

/** The text, voice and model of a prompt; empty voice/model use the defaults. */
export interface PromptInput {
  text: string;
  voiceId?: string;
  modelId?: string;
  /** Generate even when the stored audio came from the same prompt. */
  force?: boolean;
}

/** The answer of the generate routes. */
export interface GenerationResult {
  announcement: Announcement;
  /** False when the audio already came from this prompt, so nothing ran. */
  regenerated: boolean;
}

/** POST /api/v1/announcements/generate: a new announcement from text. */
export const createGeneratedAnnouncement = (name: string, input: PromptInput) =>
  request<GenerationResult>("POST", "/api/v1/announcements/generate", {
    body: { name, ...input },
  });

/** PATCH /api/v1/announcements/{id}: save the text without generating. */
export const saveAnnouncementPrompt = (
  announcementId: Id,
  input: PromptInput,
) =>
  request<Announcement>(
    "PATCH",
    `/api/v1/announcements/${seg(announcementId)}`,
    {
      body: {
        text: input.text,
        voiceId: input.voiceId,
        modelId: input.modelId,
      },
    },
  );

/** POST /api/v1/announcements/{id}/generate: generate or regenerate. */
export const generateAnnouncement = (
  announcementId: Id,
  input: Partial<PromptInput>,
) =>
  request<GenerationResult>(
    "POST",
    `/api/v1/announcements/${seg(announcementId)}/generate`,
    { body: input },
  );

/** How a voicemail greeting was generated from text. */
export interface TtsRecord {
  text: string;
  voice: string;
  model: string;
  hash: string;
  generatedAt: string;
}

/** PUT /api/v1/extensions/{id}/voicemail/greeting. */
export type GreetingRequest = {
  slot: "greeting" | "unreachable";
} & (
  | { source: "announcement"; announcementId: Id }
  | ({ source: "text" } & PromptInput)
);

/** The answer: the box (as GET sends it) and whether audio was generated. */
export interface GreetingResult {
  box: import("./media").VoicemailBoxDetail;
  regenerated: boolean;
}

export const putVoicemailGreeting = (extensionId: Id, input: GreetingRequest) =>
  request<GreetingResult>(
    "PUT",
    `/api/v1/extensions/${seg(extensionId)}/voicemail/greeting`,
    { body: input },
  );
