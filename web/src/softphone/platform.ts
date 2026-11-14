// The browser's Platform for the engine.
import type { Platform } from "./types";

type SinkAudio = HTMLAudioElement & {
  setSinkId?: (id: string) => Promise<void>;
};

export const browserPlatform: Platform = {
  getUserMedia: (c) => navigator.mediaDevices.getUserMedia(c),
  enumerateDevices: () => navigator.mediaDevices.enumerateDevices(),
  attachAudio(stream, sinkId) {
    const el: SinkAudio = document.createElement("audio");
    el.autoplay = true;
    el.srcObject = stream;
    if (sinkId && el.setSinkId)
      void el.setSinkId(sinkId).catch(() => undefined);
    void el.play().catch(() => undefined);
    return () => {
      el.pause();
      el.srcObject = null;
    };
  },
  now: () => Date.now(),
};
