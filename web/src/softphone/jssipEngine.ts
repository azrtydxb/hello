// The PhoneEngine over JsSIP (S-19). This is the only file that imports the
// SIP library; everything it exposes is the types of ./types.ts, so a library
// change stays local. Signalling runs over WSS to the edge (RFC 7118); media
// is DTLS-SRTP with ICE, which the browser's RTCPeerConnection does.
//
// Credentials are never logged: nothing here puts the password, a SIP message
// or an SDP into a log line or an event.
import JsSIP from "jssip";
import type { RTCSession } from "jssip/lib/RTCSession";
import type { UA } from "jssip/lib/UA";
import { Subscriber } from "jssip/lib/Subscriber";
import { DTMF_TRANSPORT } from "jssip/lib/Constants";
import type {
  CallInfo,
  CallOptions,
  CallStreams,
  DeviceSelection,
  EndReason,
  EngineEvent,
  EngineListener,
  PhoneEngine,
  Platform,
  RegisterConfig,
} from "./types";

type Anyf = (...a: never[]) => void;

interface Line {
  id: string;
  session: RTCSession;
  info: CallInfo;
  streams: CallStreams;
  remoteStream: MediaStream;
  localVideo: MediaStream | null;
  stopAudio: (() => void) | null;
  iceTimer: ReturnType<typeof setTimeout> | null;
}

const VIDEO_OFFER = /^m=video\s+([1-9]\d*)\s/m;

/** The words of a JsSIP failure cause mapped to ours. */
export function endReasonOf(
  cause: string,
  originator: string,
  reasonHeader: string,
  incoming: boolean,
): { reason: EndReason; text?: string } {
  if (/completed elsewhere|cause=200/i.test(reasonHeader))
    return { reason: "answered-elsewhere", text: elsewhereText(reasonHeader) };
  switch (cause) {
    case "Busy":
      return { reason: "busy" };
    case "Rejected":
      return { reason: "declined" };
    case "Unavailable":
    case "Not Found":
    case "Address Incomplete":
      return { reason: "unavailable" };
    case "No Answer":
    case "Request Timeout":
      return { reason: "no-answer" };
    case "Canceled":
      return { reason: incoming ? "cancelled" : "hangup" };
    case "User Denied Media Access":
      return { reason: "no-media" };
    case "WebRTC Error":
    case "Connection Error":
      return { reason: "media-failed" };
    case "Terminated":
    case "Bye":
      return { reason: "hangup" };
  }
  return { reason: originator === "system" ? "error" : "hangup" };
}

function elsewhereText(reason: string): string {
  const m = /text="([^"]+)"/.exec(reason);
  return m?.[1] ?? "Answered elsewhere";
}

/** Parse an RFC 3842 message-summary body into the count of new voice messages. */
export function parseMessageSummary(body: string): number {
  const m = /^Voice-Message:\s*(\d+)\s*\//im.exec(body);
  if (m) return Number(m[1]);
  return /^Messages-Waiting:\s*yes/im.test(body) ? 1 : 0;
}

export class JsSipEngine implements PhoneEngine {
  private ua: UA | null = null;
  private cfg: RegisterConfig | null = null;
  private listeners = new Set<EngineListener>();
  private lines = new Map<string, Line>();
  private sel: DeviceSelection = {
    audioInput: "",
    audioOutput: "",
    videoInput: "",
  };
  private sub: { terminate?: () => void } | null = null;

  constructor(private readonly platform: Platform) {
    JsSIP.debug.disable("JsSIP:*");
  }

  on(l: EngineListener): () => void {
    this.listeners.add(l);
    return () => this.listeners.delete(l);
  }

  private emit(ev: EngineEvent) {
    this.listeners.forEach((l) => l(ev));
  }

  iceServers(): RTCIceServer[] {
    return this.cfg?.iceServers ?? [];
  }

  hasRelay(): boolean {
    return this.iceServers().some((s) =>
      (Array.isArray(s.urls) ? s.urls : [s.urls]).some((u) =>
        /^turns?:/i.test(u),
      ),
    );
  }

  register(cfg: RegisterConfig): void {
    this.unregister();
    this.cfg = cfg;
    const socket = new JsSIP.WebSocketInterface(cfg.wssUrl);
    const ua = new JsSIP.UA({
      sockets: [socket],
      uri: `sip:${cfg.sipUsername}@${cfg.realm}`,
      password: cfg.sipPassword,
      display_name: cfg.displayName,
      instance_id: cfg.instanceId,
      user_agent: "Hello-Web/1",
      register: true,
      register_expires: 300,
      session_timers: false,
      connection_recovery_min_interval: 2,
      connection_recovery_max_interval: 30,
    });
    this.ua = ua;
    this.emit({ type: "registration", state: "connecting" });
    ua.on("connecting", () =>
      this.emit({ type: "registration", state: "connecting" }),
    );
    ua.on("disconnected", () =>
      this.emit({
        type: "registration",
        state: "unregistered",
        reason: "Connection closed",
      }),
    );
    ua.on("registered", () => {
      this.emit({ type: "registration", state: "registered" });
      this.watchVoicemail();
    });
    ua.on("unregistered", () =>
      this.emit({ type: "registration", state: "unregistered" }),
    );
    ua.on(
      "registrationFailed",
      (e: { response?: { status_code?: number }; cause?: string }) => {
        const code = e.response?.status_code;
        this.emit({
          type: "registration",
          state: "failed",
          code,
          reason:
            code === 401 || code === 403
              ? "Credentials refused"
              : (e.cause ?? "Registration failed"),
        });
      },
    );
    ua.on("newRTCSession", (e: { session: RTCSession }) =>
      this.onSession(e.session),
    );
    ua.start();
  }

  updateCredentials(cfg: RegisterConfig): void {
    // The username carries the expiry, so a new credential is a new UA; the
    // caller does this between calls only.
    if (this.lines.size > 0) return;
    this.register(cfg);
  }

  unregister(): void {
    this.sub?.terminate?.();
    this.sub = null;
    const ua = this.ua;
    this.ua = null;
    this.lines.forEach((l) => {
      try {
        l.session.terminate();
      } catch {
        /* already over */
      }
    });
    if (ua) {
      try {
        ua.stop();
      } catch {
        /* never started */
      }
    }
  }

  private watchVoicemail() {
    if (this.sub || !this.ua || !this.cfg) return;
    try {
      const sub = new Subscriber(
        this.ua,
        `sip:${this.cfg.sipUsername}@${this.cfg.realm}`,
        "message-summary",
        "application/simple-message-summary",
        { expires: 600, contentType: "application/simple-message-summary" },
      );
      (
        sub as unknown as {
          on(ev: string, fn: (...a: unknown[]) => void): void;
        }
      ).on("notify", (...a: unknown[]) => {
        this.emit({
          type: "voicemail",
          waiting: parseMessageSummary(typeof a[2] === "string" ? a[2] : ""),
        });
      });
      sub.subscribe();
      this.sub = sub as unknown as { terminate?: () => void };
    } catch {
      /* a server that does not offer MWI: the dock just shows no count */
    }
  }

  private pcConfig(): RTCConfiguration {
    return { iceServers: this.cfg?.iceServers ?? [] };
  }

  private audioConstraint(): MediaTrackConstraints | boolean {
    return this.sel.audioInput
      ? { deviceId: { ideal: this.sel.audioInput } }
      : true;
  }

  private videoConstraint(on: boolean): MediaTrackConstraints | boolean {
    if (!on) return false;
    return this.sel.videoInput
      ? { deviceId: { ideal: this.sel.videoInput } }
      : true;
  }

  call(target: string, opts: CallOptions = {}): string {
    if (!this.ua || !this.cfg) throw new Error("The phone is not registered");
    const video = !!opts.video;
    const session = this.ua.call(`sip:${target}@${this.cfg.realm}`, {
      mediaConstraints: {
        audio: this.audioConstraint(),
        video: this.videoConstraint(video),
      },
      pcConfig: this.pcConfig(),
      rtcOfferConstraints: {
        offerToReceiveAudio: true,
        offerToReceiveVideo: video,
      },
    });
    return session.id;
  }

  answer(id: string, opts: CallOptions = {}): void {
    const l = this.lines.get(id);
    if (!l) return;
    const video = !!opts.video && l.info.video.offered;
    l.session.answer({
      mediaConstraints: {
        audio: this.audioConstraint(),
        video: this.videoConstraint(video),
      },
      pcConfig: this.pcConfig(),
      rtcAnswerConstraints: {
        offerToReceiveAudio: true,
        offerToReceiveVideo: l.info.video.offered,
      },
    });
  }

  hangup(id: string): void {
    const l = this.lines.get(id);
    if (!l) return;
    if (l.info.direction === "in" && l.info.phase === "ringing") {
      l.session.terminate({ status_code: 486, reason_phrase: "Busy Here" });
    } else {
      l.session.terminate();
    }
  }

  hold(id: string, on: boolean): void {
    const l = this.lines.get(id);
    if (!l) return;
    if (on) l.session.hold();
    else l.session.unhold();
  }

  mute(id: string, on: boolean): void {
    const l = this.lines.get(id);
    if (!l) return;
    if (on) l.session.mute({ audio: true });
    else l.session.unmute({ audio: true });
    this.update(l, { muted: on });
  }

  dtmf(id: string, tones: string): void {
    this.lines.get(id)?.session.sendDTMF(tones, {
      transportType: DTMF_TRANSPORT.RFC2833,
      duration: 100,
      interToneGap: 70,
    });
  }

  transfer(id: string, target: string): void {
    const l = this.lines.get(id);
    if (!l || !this.cfg) return;
    this.update(l, { transferring: true });
    l.session.refer(`sip:${target}@${this.cfg.realm}`, {
      eventHandlers: {
        requestSucceeded: () => undefined,
        requestFailed: () => this.update(l, { transferring: false }),
      },
    });
  }

  transferAttended(id: string, otherId: string): void {
    const l = this.lines.get(id);
    const other = this.lines.get(otherId);
    if (!l || !other) return;
    this.update(l, { transferring: true });
    // REFER the held party to the other line's remote, replacing that dialog.
    l.session.refer(other.session.remote_identity.uri, {
      replaces: other.session,
      eventHandlers: {
        requestFailed: () => this.update(l, { transferring: false }),
      },
    });
  }

  setVideo(id: string, on: boolean): void {
    const l = this.lines.get(id);
    const pc = l?.session.connection as RTCPeerConnection | undefined;
    if (!l || !pc) return;
    void this.applyVideo(l, pc, on);
  }

  private async applyVideo(l: Line, pc: RTCPeerConnection, on: boolean) {
    const tr = pc
      .getTransceivers()
      .find((t) => t.receiver.track.kind === "video");
    if (on) {
      let stream: MediaStream;
      try {
        stream = await this.platform.getUserMedia({
          video: this.videoConstraint(true),
        });
      } catch {
        this.update(l, { video: { ...l.info.video, local: false } });
        return;
      }
      const track = stream.getVideoTracks()[0];
      if (!track) return;
      if (tr) {
        await tr.sender.replaceTrack(track);
        tr.direction = "sendrecv";
      } else {
        pc.addTrack(track, stream);
      }
      l.localVideo = stream;
    } else {
      l.localVideo?.getTracks().forEach((t) => t.stop());
      l.localVideo = null;
      if (tr) {
        await tr.sender.replaceTrack(null);
        tr.direction = l.info.video.remote ? "recvonly" : "inactive";
      }
    }
    this.publishStreams(l);
    this.update(l, { video: { ...l.info.video, local: on } });
    l.session.renegotiate({
      rtcOfferConstraints: { offerToReceiveVideo: true },
    });
  }

  setDevices(sel: DeviceSelection): void {
    this.sel = sel;
    this.lines.forEach((l) => {
      l.stopAudio?.();
      l.stopAudio = this.attach(l);
    });
  }

  async stats(id: string): Promise<RTCStatsReport | null> {
    const pc = this.lines.get(id)?.session.connection as
      RTCPeerConnection | undefined;
    return pc ? pc.getStats() : null;
  }

  // ---- sessions -------------------------------------------------------

  private attach(l: Line): (() => void) | null {
    return l.remoteStream.getAudioTracks().length > 0
      ? this.platform.attachAudio(l.remoteStream, this.sel.audioOutput)
      : null;
  }

  private publishStreams(l: Line) {
    l.streams = { remote: l.remoteStream, local: l.localVideo };
    this.emit({ type: "streams", id: l.id, streams: l.streams });
  }

  private update(l: Line, patch: Partial<CallInfo>) {
    l.info = { ...l.info, ...patch };
    this.emit({ type: "call", call: l.info });
  }

  private onSession(session: RTCSession) {
    const incoming = session.direction === "incoming";
    const req = (
      session as unknown as {
        request?: {
          body?: string;
          getHeader(n: string): string | undefined;
          to?: { uri?: { user?: string } };
        };
      }
    ).request;
    const offered = incoming && VIDEO_OFFER.test(req?.body ?? "");
    const remote = session.remote_identity;
    const info: CallInfo = {
      id: session.id,
      sipCallId:
        (session as unknown as { request?: { call_id?: string } }).request
          ?.call_id ?? "",
      direction: incoming ? "in" : "out",
      phase: incoming ? "ringing" : "dialing",
      remote: remote?.uri?.user ?? "",
      remoteName: remote?.display_name ?? "",
      called: incoming ? (req?.to?.uri?.user ?? "") : "",
      ringTag: incoming ? ringTagOf(req?.getHeader("Alert-Info")) : "",
      heldLocal: false,
      heldRemote: false,
      muted: false,
      video: { local: false, remote: false, offered },
      transferring: false,
      answeredAt: 0,
    };
    const l: Line = {
      id: session.id,
      session,
      info,
      streams: { remote: null, local: null },
      remoteStream: new MediaStream(),
      localVideo: null,
      stopAudio: null,
      iceTimer: null,
    };
    this.lines.set(l.id, l);
    this.emit({ type: "call", call: info });

    session.on("peerconnection", (e) => {
      const pc = e.peerconnection as unknown as RTCPeerConnection;
      pc.addEventListener("track", (t) => {
        l.remoteStream.addTrack(t.track);
        if (t.track.kind === "audio" && !l.stopAudio)
          l.stopAudio = this.attach(l);
        if (t.track.kind === "video") {
          t.track.addEventListener("mute", () =>
            this.update(l, { video: { ...l.info.video, remote: false } }),
          );
          t.track.addEventListener("unmute", () =>
            this.update(l, { video: { ...l.info.video, remote: true } }),
          );
          this.update(l, {
            video: { ...l.info.video, remote: !t.track.muted },
          });
        }
        this.publishStreams(l);
      });
      pc.addEventListener("iceconnectionstatechange", () => this.onIce(l, pc));
    });
    session.on("progress", () => {
      if (!incoming && l.info.phase === "dialing")
        this.update(l, { phase: "early" });
    });
    session.on("accepted", () =>
      this.update(l, { phase: "connecting", answeredAt: this.platform.now() }),
    );
    session.on("confirmed", () => {
      if (!l.info.answeredAt)
        this.update(l, { phase: "active", answeredAt: this.platform.now() });
      else this.update(l, { phase: "active" });
    });
    session.on("hold", (e) => {
      if (e.originator === "local") this.update(l, { heldLocal: true });
      else this.update(l, { heldRemote: true });
    });
    session.on("unhold", (e) => {
      if (e.originator === "local") this.update(l, { heldLocal: false });
      else this.update(l, { heldRemote: false });
    });
    session.on(
      "newDTMF",
      (e: { originator: string; dtmf: { tone: string } }) => {
        if (e.originator === "remote")
          this.emit({ type: "dtmf", id: l.id, tone: e.dtmf.tone });
      },
    );
    // The far end's video can come and go by re-INVITE.
    session.on("sdp", (e) => {
      if (e.originator === "remote") {
        const has = VIDEO_OFFER.test(e.sdp);
        if (has !== l.info.video.offered)
          this.update(l, { video: { ...l.info.video, offered: has } });
      }
    });
    const over = (e: {
      cause?: string;
      originator?: string;
      message?: { getHeader?(n: string): string | undefined };
    }) => {
      const { reason, text } = endReasonOf(
        e.cause ?? "",
        e.originator ?? "",
        e.message?.getHeader?.("Reason") ?? "",
        incoming && l.info.phase === "ringing",
      );
      this.finish(l, reason, text);
    };
    session.on("failed", over);
    session.on("ended", over);
  }

  private onIce(l: Line, pc: RTCPeerConnection) {
    if (l.iceTimer) clearTimeout(l.iceTimer);
    l.iceTimer = null;
    const st = pc.iceConnectionState;
    if (st === "failed") {
      this.finish(l, "media-failed");
      try {
        l.session.terminate();
      } catch {
        /* already over */
      }
    } else if (st === "disconnected") {
      // A blip heals on its own; five seconds without media is a failure.
      l.iceTimer = setTimeout(() => {
        if (pc.iceConnectionState === "disconnected") {
          this.finish(l, "media-failed");
          try {
            l.session.terminate();
          } catch {
            /* already over */
          }
        }
      }, 5000);
    }
  }

  private finish(l: Line, reason: EndReason, text?: string) {
    if (l.info.phase === "ended") return;
    if (l.iceTimer) clearTimeout(l.iceTimer);
    l.stopAudio?.();
    l.localVideo?.getTracks().forEach((t) => t.stop());
    this.update(l, { phase: "ended", endReason: reason, endText: text });
    this.lines.delete(l.id);
  }
}

/** The ring tag name from an Alert-Info header, "" when there is none. */
export function ringTagOf(h: string | undefined): string {
  if (!h) return "";
  const m = /info=([\w.-]+)/i.exec(h) ?? /<[^>]*\/([\w.-]+)>/.exec(h);
  return m?.[1] ?? "";
}

export type { Anyf };
