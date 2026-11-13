import { act, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { RecordingVideoStream } from "../../api/media";
import { apiError, json, mockApi } from "../../test/api";
import {
  MAX_DRIFT_SECONDS,
  RecordingPlayer,
  needsResync,
  partAt,
  videoTimeFor,
} from "./RecordingPlayer";

function stream(over: Partial<RecordingVideoStream>): RecordingVideoStream {
  return {
    id: 1,
    part: 1,
    leg: "caller",
    codec: "h264",
    container: "mp4",
    width: 640,
    height: 360,
    offsetMs: 1500,
    durationMs: 60000,
    bytes: 1000,
    state: "finalized",
    finalized: true,
    mediaType: "video/mp4",
    ...over,
  };
}

const REC = {
  id: 7,
  correlationId: "corr-7",
  initiatedBy: "default" as const,
  durationMs: 90000,
  createdAt: "2026-11-13T10:00:00Z",
  video: true,
};

function manifest(streams: RecordingVideoStream[]) {
  return {
    "GET /api/v1/recordings/7/manifest": () =>
      json({
        recordingId: 7,
        correlationId: "corr-7",
        durationMs: 90000,
        streams,
      }),
  };
}

/** Sets what the audio element says: its clock and whether it plays. */
function setAudio(audio: HTMLAudioElement, seconds: number, paused: boolean) {
  Object.defineProperty(audio, "currentTime", {
    configurable: true,
    writable: true,
    value: seconds,
  });
  Object.defineProperty(audio, "paused", {
    configurable: true,
    value: paused,
  });
}

beforeEach(() => {
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe("the player's clock", () => {
  it("puts a video at the audio's time minus its offset, nothing before it begins", () => {
    expect(videoTimeFor(10, 1500)).toBeCloseTo(8.5);
    expect(videoTimeFor(1.5, 1500)).toBe(0);
    expect(videoTimeFor(1, 1500)).toBeNull();
  });

  it("moves a video only when it is more than 100 ms off", () => {
    expect(MAX_DRIFT_SECONDS).toBe(0.1);
    expect(needsResync(5.05, 5)).toBe(false);
    expect(needsResync(5.0, 5.1)).toBe(false);
    expect(needsResync(5.2, 5)).toBe(true);
    expect(needsResync(4.7, 5)).toBe(true);
  });

  it("picks the part the audio is in and finds a take-over's gap", () => {
    const parts = [
      stream({ id: 1, part: 1, offsetMs: 1000, durationMs: 20000 }),
      stream({ id: 2, part: 2, offsetMs: 30000, durationMs: 20000 }),
    ];
    expect(partAt(parts, 500)).toEqual({ part: null, gap: false });
    expect(partAt(parts, 10000).part?.id).toBe(1);
    expect(partAt(parts, 10000).gap).toBe(false);
    // Part 1 ended at 21 s and part 2 begins at 30 s: the stretch between is lost.
    expect(partAt(parts, 25000)).toMatchObject({ gap: true });
    expect(partAt(parts, 25000).part?.id).toBe(1);
    expect(partAt(parts, 31000)).toMatchObject({ gap: false });
    expect(partAt(parts, 31000).part?.id).toBe(2);
    // The last part's end is not a gap: nothing follows.
    expect(partAt(parts, 80000).gap).toBe(false);
  });
});

describe("RecordingPlayer", () => {
  it("shows the caller's video large and the callee's small, muted, and keeps them on the audio's clock", async () => {
    // The two-second check is run by hand.
    const intervals: { fn: () => void; ms: number }[] = [];
    vi.spyOn(window, "setInterval").mockImplementation(((
      fn: () => void,
      ms: number,
    ) => {
      intervals.push({ fn, ms });
      return intervals.length;
    }) as unknown as typeof window.setInterval);
    mockApi(
      manifest([
        stream({ id: 11, leg: "caller", offsetMs: 1500 }),
        stream({ id: 12, leg: "callee", offsetMs: 4000 }),
      ]),
    );
    render(
      <RecordingPlayer recording={REC} title="100 → 200" onStop={() => {}} />,
    );
    const audio = (await screen.findByLabelText(
      "Playback of the recording corr-7",
    )) as HTMLAudioElement;
    expect(audio).toHaveAttribute("src", "/api/v1/recordings/7/audio");
    // Before either video begins nothing plays; the audio is at 0.5 s.
    setAudio(audio, 0.5, false);
    fireEvent(audio, new Event("timeupdate"));
    expect(screen.queryByLabelText("Video of the caller")).toBeNull();
    expect(screen.getAllByText(/The video begins at/)).toHaveLength(2);

    // At 10 s both have begun.
    setAudio(audio, 10, false);
    fireEvent(audio, new Event("timeupdate"));
    const main = (await screen.findByLabelText(
      "Video of the caller",
    )) as HTMLVideoElement;
    const pip = screen.getByLabelText(
      "Video of the callee",
    ) as HTMLVideoElement;
    expect(main).toHaveAttribute("src", "/api/v1/recordings/7/video/11");
    expect(pip).toHaveAttribute("src", "/api/v1/recordings/7/video/12");
    expect(main.muted).toBe(true);
    expect(pip.muted).toBe(true);
    expect(main.closest(".media-video__main")).not.toBeNull();
    expect(pip.closest(".media-video__pip")).not.toBeNull();
    // Each is put at the audio's time minus its own offset.
    expect(main.currentTime).toBeCloseTo(8.5, 1);
    expect(pip.currentTime).toBeCloseTo(6, 1);

    // A seek moves them; a small drift does not.
    setAudio(audio, 30, false);
    fireEvent(audio, new Event("seeked"));
    expect(main.currentTime).toBeCloseTo(28.5, 1);
    expect(pip.currentTime).toBeCloseTo(26, 1);
    main.currentTime = 28.55;
    fireEvent(audio, new Event("seeked"));
    expect(main.currentTime).toBeCloseTo(28.55, 2);

    // Drift is caught by the two-second check.
    expect(intervals.some((i) => i.ms === 2000)).toBe(true);
    main.currentTime = 20;
    act(() => {
      intervals.filter((i) => i.ms === 2000).forEach((i) => i.fn());
    });
    expect(main.currentTime).toBeCloseTo(28.5, 1);

    // A pause of the audio pauses the video.
    setAudio(audio, 30, true);
    const pauses = vi.mocked(HTMLMediaElement.prototype.pause).mock.calls
      .length;
    fireEvent(audio, new Event("pause"));
    expect(
      vi.mocked(HTMLMediaElement.prototype.pause).mock.calls.length,
    ).toBeGreaterThan(pauses);
  });

  it("follows the parts one after another and marks the lost stretch", async () => {
    mockApi(
      manifest([
        stream({ id: 21, part: 1, offsetMs: 1000, durationMs: 20000 }),
        stream({ id: 22, part: 2, offsetMs: 30000, durationMs: 20000 }),
      ]),
    );
    render(
      <RecordingPlayer recording={REC} title="100 → 200" onStop={() => {}} />,
    );
    const audio = (await screen.findByLabelText(
      "Playback of the recording corr-7",
    )) as HTMLAudioElement;
    setAudio(audio, 10, false);
    fireEvent(audio, new Event("timeupdate"));
    expect(
      (await screen.findByLabelText("Video of the caller")).getAttribute("src"),
    ).toBe("/api/v1/recordings/7/video/21");
    expect(screen.queryByText("recording gap")).toBeNull();
    setAudio(audio, 25, false);
    fireEvent(audio, new Event("timeupdate"));
    expect(await screen.findByText("recording gap")).toBeVisible();
    setAudio(audio, 32, false);
    fireEvent(audio, new Event("timeupdate"));
    expect(
      screen.getByLabelText("Video of the caller").getAttribute("src"),
    ).toBe("/api/v1/recordings/7/video/22");
    expect(screen.queryByText("recording gap")).toBeNull();
  });

  it("offers the file as a download when the browser cannot play the stream", async () => {
    mockApi(manifest([stream({ id: 31, codec: "vp8", container: "webm" })]));
    render(
      <RecordingPlayer recording={REC} title="100 → 200" onStop={() => {}} />,
    );
    const audio = (await screen.findByLabelText(
      "Playback of the recording corr-7",
    )) as HTMLAudioElement;
    setAudio(audio, 10, false);
    fireEvent(audio, new Event("timeupdate"));
    fireEvent.error(await screen.findByLabelText("Video of the caller"));
    expect(await screen.findByText(/cannot play the VP8 video/)).toBeVisible();
    expect(
      screen.getByRole("link", { name: "Download the video" }),
    ).toHaveAttribute("href", "/api/v1/recordings/7/video/31?download=1");
  });

  it("says so when the call had video that was not stored, or the manifest fails, and plays the audio on", async () => {
    mockApi(manifest([]));
    const { unmount } = render(
      <RecordingPlayer recording={REC} title="100 → 200" onStop={() => {}} />,
    );
    expect(await screen.findByText("No video was stored")).toBeVisible();
    expect(
      screen.getByLabelText("Playback of the recording corr-7"),
    ).toBeInTheDocument();
    unmount();

    mockApi({
      "GET /api/v1/recordings/7/manifest": () =>
        apiError(500, "internal", "database down"),
    });
    render(
      <RecordingPlayer recording={REC} title="100 → 200" onStop={() => {}} />,
    );
    expect(
      await screen.findByText("The video could not be loaded"),
    ).toBeVisible();
    expect(
      screen.getByLabelText("Playback of the recording corr-7"),
    ).toBeInTheDocument();
  });
});
