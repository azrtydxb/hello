import { generateKeyPairSync, verify } from "node:crypto";
import { readFileSync } from "node:fs";
import { inflateRawSync } from "node:zlib";
import { describe, expect, it } from "vitest";
import { crx3, extensionId, publicKeyOf } from "./crx.mjs";
import { zip } from "./zip.mjs";

const manifest = JSON.parse(readFileSync("extension/manifest.json", "utf8"));

describe("manifest", () => {
  it("has the fixed key and so the fixed extension id the server registers", () => {
    // internal/oauth/preregistered.go carries the same id; its test recomputes it too.
    expect(extensionId(manifest.key)).toBe("kengpjalagamnmmkljldjhhgonmbdcph");
  });
  it("is Manifest V3 with minimal permissions and no blanket host access", () => {
    expect(manifest.manifest_version).toBe(3);
    expect([...manifest.permissions].sort()).toEqual([
      "alarms",
      "identity",
      "notifications",
      "scripting",
      "storage",
    ]);
    expect(manifest.host_permissions).toBeUndefined();
    expect(manifest.content_scripts).toBeUndefined();
    expect(manifest.optional_host_permissions.length).toBeGreaterThan(0);
  });
});

describe("zip", () => {
  it("is deterministic and holds the files", () => {
    const files = [
      { name: "a.txt", data: Buffer.from("hello hello hello") },
      { name: "d/b.txt", data: Buffer.from("x") },
    ];
    const z = zip(files);
    expect(z.equals(zip(files))).toBe(true);
    expect(z.readUInt16LE(z.length - 22 + 10)).toBe(2);
    const csize = z.readUInt32LE(18);
    const comp = z.subarray(30 + 5, 30 + 5 + csize);
    expect(inflateRawSync(comp).toString()).toBe("hello hello hello");
  });
});

describe("crx3", () => {
  it("carries the key and a signature that verifies over the zip", () => {
    const { privateKey, publicKey } = generateKeyPairSync("rsa", {
      modulusLength: 2048,
    });
    const pem = privateKey.export({ type: "pkcs8", format: "pem" });
    const z = zip([{ name: "a", data: Buffer.from("a") }]);
    const crx = crx3(z, pem);
    expect(crx.subarray(0, 4).toString()).toBe("Cr24");
    expect(crx.readUInt32LE(4)).toBe(3);
    const hlen = crx.readUInt32LE(8);
    expect(crx.subarray(12 + hlen).equals(z)).toBe(true);
    const header = crx.subarray(12, 12 + hlen);
    const der = publicKey.export({ type: "spki", format: "der" });
    const at = header.indexOf(der);
    expect(at).toBeGreaterThan(0);
    // The signature field follows the key: tag 0x12, then a two-byte length of 256.
    const sigAt = at + der.length;
    expect([header[sigAt], header[sigAt + 1], header[sigAt + 2]]).toEqual([
      0x12, 0x80, 0x02,
    ]);
    const signature = header.subarray(sigAt + 3, sigAt + 3 + 256);
    const signedData = Buffer.concat([
      Buffer.from([0x0a, 0x10]),
      header.subarray(header.length - 16),
    ]);
    const le = Buffer.alloc(4);
    le.writeUInt32LE(signedData.length);
    const message = Buffer.concat([
      Buffer.from("CRX3 SignedData\0"),
      le,
      signedData,
      z,
    ]);
    expect(
      verify(
        "sha256",
        message,
        publicKey.export({ type: "spki", format: "pem" }),
        signature,
      ),
    ).toBe(true);
    expect(publicKeyOf(pem)).toBe(der.toString("base64"));
  });
});
