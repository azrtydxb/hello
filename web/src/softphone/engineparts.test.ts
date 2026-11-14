import { describe, expect, it } from "vitest";
import { createPhoneLock } from "./lock";
import {
  endReasonOf,
  parseMessageSummary,
  recordingOf,
  ringTagOf,
} from "./jssipEngine";
import {
  foldReport,
  gradeOf,
  summarizeStats,
  type QualitySample,
} from "./quality";

describe("end reasons", () => {
  it("maps SIP outcomes to the banner reason", () => {
    expect(endReasonOf("Busy", "remote", "", false).reason).toBe("busy");
    expect(endReasonOf("Rejected", "remote", "", false).reason).toBe(
      "declined",
    );
    expect(endReasonOf("Canceled", "remote", "", true).reason).toBe(
      "cancelled",
    );
    expect(endReasonOf("Canceled", "local", "", false).reason).toBe("hangup");
    expect(
      endReasonOf("User Denied Media Access", "local", "", false).reason,
    ).toBe("no-media");
    expect(endReasonOf("WebRTC Error", "system", "", false).reason).toBe(
      "media-failed",
    );
  });
  it("reads answered elsewhere from the Reason header", () => {
    const r = endReasonOf(
      "Canceled",
      "remote",
      'SIP;cause=200;text="Call completed elsewhere"',
      true,
    );
    expect(r).toEqual({
      reason: "answered-elsewhere",
      text: "Call completed elsewhere",
    });
  });
});

describe("headers and bodies", () => {
  it("reads the ring tag", () => {
    expect(ringTagOf("<http://hello/ringtag>;info=urgent")).toBe("urgent");
    expect(ringTagOf(undefined)).toBe("");
  });
  it("reads a message summary", () => {
    expect(
      parseMessageSummary("Messages-Waiting: yes\r\nVoice-Message: 2/5\r\n"),
    ).toBe(2);
    expect(
      parseMessageSummary("Messages-Waiting: no\r\nVoice-Message: 0/0\r\n"),
    ).toBe(0);
    expect(parseMessageSummary("Messages-Waiting: yes\r\n")).toBe(1);
  });
});

function fakeLocks() {
  let holder: { release: () => void } | null = null;
  const waiters: Array<() => void> = [];
  const grant = (cb: (l: unknown) => Promise<void> | void) => {
    const p = cb({});
    const done = new Promise<void>((res) => {
      holder = {
        release: () => {
          holder = null;
          res();
          waiters.shift()?.();
        },
      };
    });
    void Promise.resolve(p).then(() => holder?.release());
    return done;
  };
  return {
    request(
      _n: string,
      o: { ifAvailable?: boolean },
      cb: (l: unknown) => Promise<void> | void,
    ) {
      if (!holder) return grant(cb);
      if (o.ifAvailable) {
        void cb(null);
        return Promise.resolve();
      }
      return new Promise<void>((res) => {
        waiters.push(() => void grant(cb).then(res));
      });
    },
  };
}

describe("one phone per browser", () => {
  it("the first tab holds the lock, the second waits and gets it when the first leaves", async () => {
    const locks = fakeLocks();
    const a = createPhoneLock(locks);
    const b = createPhoneLock(locks);
    expect(await a.acquire()).toBe(true);
    expect(await b.acquire()).toBe(false);
    let granted = false;
    b.request(() => {
      granted = true;
    });
    expect(granted).toBe(false);
    a.release();
    await new Promise((r) => setTimeout(r, 0));
    expect(granted).toBe(true);
    expect(b.held()).toBe(true);
  });
  it("is the phone without the Web Locks API", async () => {
    expect(await createPhoneLock(undefined).acquire()).toBe(true);
  });
});

describe("call quality", () => {
  const report = new Map<string, Record<string, unknown>>([
    ["codec-a", { id: "codec-a", type: "codec", mimeType: "audio/opus" }],
    ["codec-v", { id: "codec-v", type: "codec", mimeType: "video/H264" }],
    [
      "rtp-a",
      {
        id: "rtp-a",
        type: "inbound-rtp",
        kind: "audio",
        codecId: "codec-a",
        packetsReceived: 95,
        packetsLost: 5,
        jitter: 0.012,
        bytesReceived: 4000,
      },
    ],
    [
      "rtp-v",
      {
        id: "rtp-v",
        type: "inbound-rtp",
        kind: "video",
        codecId: "codec-v",
        packetsReceived: 100,
        packetsLost: 0,
        frameWidth: 1280,
        frameHeight: 720,
        framesPerSecond: 29.6,
        bytesReceived: 900000,
      },
    ],
    ["tr", { id: "tr", type: "transport", selectedCandidatePairId: "pair" }],
    [
      "pair",
      {
        id: "pair",
        type: "candidate-pair",
        currentRoundTripTime: 0.04,
        localCandidateId: "lc",
        remoteCandidateId: "rc",
      },
    ],
    ["lc", { id: "lc", type: "local-candidate", candidateType: "host" }],
    ["rc", { id: "rc", type: "remote-candidate", candidateType: "relay" }],
  ]) as unknown as RTCStatsReport;
  it("summarizes a stats report", () => {
    const q = summarizeStats(report);
    expect(q.codec).toBe("opus");
    expect(q.lossPct).toBeCloseTo(5);
    expect(q.jitterMs).toBeCloseTo(12);
    expect(q.rttMs).toBeCloseTo(40);
    expect([q.localCandidate, q.remoteCandidate]).toEqual(["host", "relay"]);
    expect(q.video).toMatchObject({ width: 1280, height: 720, codec: "H264" });
    expect(gradeOf(q)).toBe("fair");
    expect(gradeOf(null)).toBe("unknown");
  });
  it("folds samples into the worst case and keeps media out of it", () => {
    const s = (lossPct: number, jitterMs: number): QualitySample => ({
      codec: "opus",
      lossPct,
      jitterMs,
      rttMs: 20,
      localCandidate: "host",
      remoteCandidate: "host",
      bytesReceived: 1,
      videoBytesReceived: 0,
    });
    const r = foldReport([s(1, 5), s(9, 40), s(2, 8)], 61234.4);
    expect(r).toMatchObject({
      lossPct: 9,
      jitterMs: 40,
      codec: "opus",
      durationMs: 61234,
    });
    expect(Object.keys(r ?? {})).not.toContain("bytesReceived");
    expect(foldReport([], 1)).toBeNull();
  });
});

describe("recording signal", () => {
  it("reads the PBX's recording INFO", () => {
    const t = "application/x-hello-recording";
    expect(recordingOf(t, "recording: on\r\nvideo: yes\r\n")).toBe("video");
    expect(recordingOf(t, "recording: on\r\nvideo: no\r\n")).toBe("audio");
    expect(recordingOf(t, "recording: off\r\nvideo: no\r\n")).toBe("off");
    expect(recordingOf(t + "; charset=utf-8", "recording: on\r\n")).toBe(
      "audio",
    );
  });
  it("ignores every other INFO", () => {
    expect(recordingOf("application/dtmf-relay", "Signal=1")).toBeUndefined();
    expect(recordingOf(undefined, "recording: on")).toBeUndefined();
  });
});
