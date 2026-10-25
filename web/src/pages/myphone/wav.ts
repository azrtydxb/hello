// WAV encoding for greetings recorded in the browser. A greeting is a PCM WAV
// the phone system plays on a call, so it is mono, 16-bit and 8 kHz.

/** The sample rate greetings are recorded at (telephony). */
export const GREETING_RATE = 8000;

/** Encode mono float samples (-1..1) as a 16-bit PCM WAV. */
export function encodeWav(samples: Float32Array, sampleRate: number): Blob {
  const bytes = samples.length * 2;
  const buf = new ArrayBuffer(44 + bytes);
  const v = new DataView(buf);
  const text = (at: number, s: string) => {
    for (let i = 0; i < s.length; i++) v.setUint8(at + i, s.charCodeAt(i));
  };
  text(0, "RIFF");
  v.setUint32(4, 36 + bytes, true);
  text(8, "WAVE");
  text(12, "fmt ");
  v.setUint32(16, 16, true); // fmt chunk size
  v.setUint16(20, 1, true); // PCM
  v.setUint16(22, 1, true); // mono
  v.setUint32(24, sampleRate, true);
  v.setUint32(28, sampleRate * 2, true); // byte rate
  v.setUint16(32, 2, true); // block align
  v.setUint16(34, 16, true); // bits per sample
  text(36, "data");
  v.setUint32(40, bytes, true);
  for (let i = 0; i < samples.length; i++) {
    const s = Math.max(-1, Math.min(1, samples[i] ?? 0));
    v.setInt16(44 + i * 2, s < 0 ? s * 0x8000 : s * 0x7fff, true);
  }
  return new Blob([buf], { type: "audio/wav" });
}

/** Decode a recording (any format the browser can) to an 8 kHz mono WAV. */
export async function recordingToWav(recording: Blob): Promise<Blob> {
  const data = await recording.arrayBuffer();
  const ctx = new AudioContext();
  try {
    const decoded = await ctx.decodeAudioData(data);
    const frames = Math.max(1, Math.ceil(decoded.duration * GREETING_RATE));
    const offline = new OfflineAudioContext(1, frames, GREETING_RATE);
    const src = offline.createBufferSource();
    src.buffer = decoded;
    src.connect(offline.destination); // mixes down to the one channel
    src.start();
    const rendered = await offline.startRendering();
    return encodeWav(rendered.getChannelData(0), GREETING_RATE);
  } finally {
    void ctx.close();
  }
}

/** Whether this browser can record from a microphone. */
export function canRecord(): boolean {
  return (
    typeof MediaRecorder !== "undefined" &&
    typeof navigator !== "undefined" &&
    !!navigator.mediaDevices?.getUserMedia &&
    typeof AudioContext !== "undefined"
  );
}
