// The softphone's engine contract (clients-webrtc-mobile-video S-19). The UI
// and the call state machine talk to a PhoneEngine and nothing else, so the
// SIP library (JsSIP, in jssipEngine.ts only) can change without touching
// them, and a mobile client can reuse everything in this folder that has no
// DOM or React in it (types, phoneState, quality, backoff). The engine runs
// over a Platform, the few browser facilities it needs.

/** What the phone is doing on the network. */
export type RegState =
  | "off" // the switch is off
  | "connecting" // WSS opening or REGISTER in flight
  | "registered"
  | "unregistered" // closed by us or by the server; reconnecting
  | "failed" // REGISTER refused; see the reason
  | "other-tab"; // another tab of this browser holds the phone

/** The call lifecycle as the UI sees it. */
export type CallPhase =
  | "ringing" // incoming, not answered
  | "dialing" // outgoing, no provisional response yet
  | "early" // outgoing, the far end is ringing
  | "connecting" // answered, media not yet up
  | "active"
  | "ended";

/** Why a call ended, for the banner. */
export type EndReason =
  | "hangup" // either side hung up normally
  | "declined"
  | "busy"
  | "unavailable"
  | "no-answer"
  | "cancelled" // the caller gave up before we answered
  | "answered-elsewhere"
  | "media-failed" // ICE or DTLS failed
  | "no-media" // microphone missing or denied
  | "error";

/** Video as one call sees it. */
export interface VideoState {
  /** We send camera video. */
  local: boolean;
  /** The far end's video is flowing to us. */
  remote: boolean;
  /** The incoming offer carried a video section. */
  offered: boolean;
}

/** One call line. Plain data: the reducer copies it, the UI renders it. */
export interface CallInfo {
  id: string;
  /** The SIP Call-ID of this browser's leg (what the quality report is filed under). */
  sipCallId: string;
  direction: "in" | "out";
  phase: CallPhase;
  /** The far party's number and display name (empty when unknown). */
  remote: string;
  remoteName: string;
  /** The extension an incoming call was dialled to (a ring group, an alias). */
  called: string;
  /** The ring tag from Alert-Info, when the PBX named one. */
  ringTag: string;
  /** We put the call on hold / the far end did. */
  heldLocal: boolean;
  heldRemote: boolean;
  muted: boolean;
  video: VideoState;
  /** A transfer is in progress (REFER sent, waiting for the outcome). */
  transferring: boolean;
  /** Epoch ms when the call was answered; 0 before. */
  answeredAt: number;
  endReason?: EndReason;
  /** Free text for the banner ("answered on Desk phone"). */
  endText?: string;
}

/** Media streams of a call; held apart from CallInfo because they are not data. */
export interface CallStreams {
  remote: MediaStream | null;
  local: MediaStream | null;
}

/** Everything the engine reports. */
export type EngineEvent =
  | { type: "registration"; state: RegState; reason?: string; code?: number }
  | { type: "voicemail"; waiting: number }
  | { type: "call"; call: CallInfo }
  | { type: "streams"; id: string; streams: CallStreams }
  | { type: "dtmf"; id: string; tone: string };

export type EngineListener = (ev: EngineEvent) => void;

/** What register needs. */
export interface RegisterConfig {
  wssUrl: string;
  sipUsername: string;
  sipPassword: string;
  realm: string;
  displayName: string;
  instanceId: string;
  iceServers: RTCIceServer[];
}

export interface CallOptions {
  video?: boolean;
}

/** One media device as the pickers list it. */
export interface DeviceChoice {
  id: string;
  label: string;
  kind: "audioinput" | "audiooutput" | "videoinput";
}

export interface PhoneEngine {
  on(listener: EngineListener): () => void;
  register(cfg: RegisterConfig): void;
  /** Swap the credentials of a live registration (before expiry). */
  updateCredentials(cfg: RegisterConfig): void;
  unregister(): void;
  /** Place a call; returns its id. */
  call(target: string, opts?: CallOptions): string;
  answer(id: string, opts?: CallOptions): void;
  /** Decline an unanswered incoming call (486) or end a call (BYE/CANCEL). */
  hangup(id: string): void;
  hold(id: string, on: boolean): void;
  mute(id: string, on: boolean): void;
  dtmf(id: string, tones: string): void;
  /** Blind transfer to target. */
  transfer(id: string, target: string): void;
  /** Attended transfer: replace `id` with the call `otherId`. */
  transferAttended(id: string, otherId: string): void;
  /** Add (true) or drop (false) our video mid-call. */
  setVideo(id: string, on: boolean): void;
  /** The microphone, camera and speaker to use; empty keeps the default. */
  setDevices(sel: DeviceSelection): void;
  /** Statistics of the call's connection (RTCPeerConnection.getStats). */
  stats(id: string): Promise<RTCStatsReport | null>;
  /** The ICE servers the engine was configured with. */
  iceServers(): RTCIceServer[];
  /** Whether any configured server relays media (TURN), for the media-failed banner. */
  hasRelay(): boolean;
}

export interface DeviceSelection {
  audioInput: string;
  audioOutput: string;
  videoInput: string;
}

/** The browser facilities the engine needs; tests substitute fakes. */
export interface Platform {
  getUserMedia(c: MediaStreamConstraints): Promise<MediaStream>;
  enumerateDevices(): Promise<MediaDeviceInfo[]>;
  /** Play a call's remote audio; returns a stop function. */
  attachAudio(stream: MediaStream, sinkId: string): () => void;
  now(): number;
}
