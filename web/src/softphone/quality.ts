// Call quality from RTCPeerConnection.getStats (S-18 indicator, S-20 report).
// Pure: it reads a stats report and returns numbers, so it is unit-tested
// with a fake report and reused by a mobile client.

export interface QualitySample {
  /** Audio codec name from the codec stats ("opus", "PCMU"). */
  codec: string;
  /** Percent of inbound audio packets lost, 0 to 100. */
  lossPct: number;
  /** Inbound audio jitter in milliseconds. */
  jitterMs: number;
  /** Round-trip time of the selected candidate pair in milliseconds; 0 when unknown. */
  rttMs: number;
  /** The selected pair's local and remote candidate types (host, srflx, relay). */
  localCandidate: string;
  remoteCandidate: string;
  /** Inbound video, when there is some. */
  video?: {
    width: number;
    height: number;
    fps: number;
    codec: string;
    lossPct: number;
  };
  /** Inbound video bitrate is not here: it needs two samples (see bitrates). */
  bytesReceived: number;
  videoBytesReceived: number;
}

type Entry = Record<string, unknown>;

const num = (v: unknown): number =>
  typeof v === "number" && isFinite(v) ? v : 0;
const str = (v: unknown): string => (typeof v === "string" ? v : "");

/** Reduce a stats report to a sample. */
export function summarizeStats(report: RTCStatsReport): QualitySample {
  const byId = new Map<string, Entry>();
  report.forEach((v: unknown) => {
    const e = v as Entry;
    byId.set(str(e.id), e);
  });
  const codecName = (e: Entry): string => {
    const c = byId.get(str(e.codecId));
    return c ? str(c.mimeType).replace(/^(audio|video)\//, "") : "";
  };
  const out: QualitySample = {
    codec: "",
    lossPct: 0,
    jitterMs: 0,
    rttMs: 0,
    localCandidate: "",
    remoteCandidate: "",
    bytesReceived: 0,
    videoBytesReceived: 0,
  };
  let selectedPair: Entry | undefined;
  let transportPair = "";
  report.forEach((v: unknown) => {
    const e = v as Entry;
    const type = str(e.type);
    if (type === "transport" && e.selectedCandidatePairId)
      transportPair = str(e.selectedCandidatePairId);
    if (type === "inbound-rtp") {
      const kind = str(e.kind) || str(e.mediaType);
      const recv = num(e.packetsReceived);
      const lost = num(e.packetsLost);
      const pct = recv + lost > 0 ? (100 * lost) / (recv + lost) : 0;
      if (kind === "audio") {
        out.codec = codecName(e);
        out.lossPct = Math.max(0, pct);
        out.jitterMs = num(e.jitter) * 1000;
        out.bytesReceived = num(e.bytesReceived);
      } else if (kind === "video") {
        out.video = {
          width: num(e.frameWidth),
          height: num(e.frameHeight),
          fps: num(e.framesPerSecond),
          codec: codecName(e),
          lossPct: Math.max(0, pct),
        };
        out.videoBytesReceived = num(e.bytesReceived);
      }
    }
    if (
      type === "candidate-pair" &&
      (e.selected === true || (e.nominated === true && e.state === "succeeded"))
    )
      selectedPair ??= e;
  });
  const pair = (transportPair && byId.get(transportPair)) || selectedPair;
  if (pair) {
    out.rttMs = num(pair.currentRoundTripTime) * 1000;
    out.localCandidate = str(
      byId.get(str(pair.localCandidateId))?.candidateType,
    );
    out.remoteCandidate = str(
      byId.get(str(pair.remoteCandidateId))?.candidateType,
    );
  }
  return out;
}

/** A coarse grade for the dock indicator. */
export type Grade = "good" | "fair" | "poor" | "unknown";

export function gradeOf(q: QualitySample | null): Grade {
  if (!q) return "unknown";
  if (q.lossPct > 8 || q.jitterMs > 80 || q.rttMs > 500) return "poor";
  if (q.lossPct > 2 || q.jitterMs > 30 || q.rttMs > 250) return "fair";
  return "good";
}

/** The end-of-call report body (POST /me/calls/{id}/quality). Never media. */
export interface QualityReport {
  codec: string;
  lossPct: number;
  jitterMs: number;
  rttMs: number;
  candidateLocal: string;
  candidateRemote: string;
  video?: { width: number; height: number; fps: number; codec: string };
  durationMs: number;
}

/** Fold samples taken during a call into one report: the worst loss/jitter/RTT. */
export function foldReport(
  samples: QualitySample[],
  durationMs: number,
): QualityReport | null {
  if (samples.length === 0) return null;
  const last = samples[samples.length - 1];
  if (!last) return null;
  const withVideo = [...samples]
    .reverse()
    .find((s) => s.video && s.video.width > 0);
  return {
    codec: last.codec,
    lossPct: round1(Math.max(...samples.map((s) => s.lossPct))),
    jitterMs: round1(Math.max(...samples.map((s) => s.jitterMs))),
    rttMs: round1(Math.max(...samples.map((s) => s.rttMs))),
    candidateLocal: last.localCandidate,
    candidateRemote: last.remoteCandidate,
    ...(withVideo?.video
      ? {
          video: {
            width: withVideo.video.width,
            height: withVideo.video.height,
            fps: Math.round(withVideo.video.fps),
            codec: withVideo.video.codec,
          },
        }
      : {}),
    durationMs: Math.max(0, Math.round(durationMs)),
  };
}

const round1 = (n: number) => Math.round(n * 10) / 10;
