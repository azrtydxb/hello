import { describe, expect, it } from "vitest";
import { encodeWav, GREETING_RATE } from "./wav";

async function bytes(b: Blob): Promise<DataView> {
  return new DataView(await b.arrayBuffer());
}

describe("encodeWav", () => {
  it("writes a 16-bit mono PCM WAV the phone system accepts", async () => {
    const wav = encodeWav(
      new Float32Array([0, 0.5, -0.5, 1, -1, 2]),
      GREETING_RATE,
    );
    expect(wav.type).toBe("audio/wav");
    const v = await bytes(wav);
    const tag = (at: number) =>
      String.fromCharCode(
        v.getUint8(at),
        v.getUint8(at + 1),
        v.getUint8(at + 2),
        v.getUint8(at + 3),
      );
    expect(tag(0)).toBe("RIFF");
    expect(tag(8)).toBe("WAVE");
    expect(tag(12)).toBe("fmt ");
    expect(v.getUint16(20, true)).toBe(1); // PCM
    expect(v.getUint16(22, true)).toBe(1); // mono
    expect(v.getUint32(24, true)).toBe(8000);
    expect(v.getUint16(34, true)).toBe(16);
    expect(tag(36)).toBe("data");
    expect(v.getUint32(40, true)).toBe(12);
    expect(v.getUint32(4, true)).toBe(36 + 12);
    expect(wav.size).toBe(44 + 12);
    expect(v.getInt16(46, true)).toBe(Math.trunc(0.5 * 0x7fff));
    expect(v.getInt16(48, true)).toBe(-0x4000);
    // Samples beyond full scale are clamped, not wrapped.
    expect(v.getInt16(50, true)).toBe(0x7fff);
    expect(v.getInt16(52, true)).toBe(-0x8000);
    expect(v.getInt16(54, true)).toBe(0x7fff);
  });
});
