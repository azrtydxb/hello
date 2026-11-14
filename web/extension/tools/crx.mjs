// The extension id and the CRX3 package. A Chrome extension id is the first 16
// bytes of the SHA-256 of its public key (DER, SubjectPublicKeyInfo) written
// with the letters a to p; a fixed key in the manifest therefore fixes the id,
// and the OAuth redirect address https://<id>.chromiumapp.org/ with it.
import {
  createHash,
  createPrivateKey,
  createPublicKey,
  sign,
} from "node:crypto";

/** The extension id for a base64 DER public key (the manifest's "key"). */
export function extensionId(keyB64) {
  const h = createHash("sha256")
    .update(Buffer.from(keyB64, "base64"))
    .digest()
    .subarray(0, 16);
  return [...h]
    .map(
      (b) =>
        String.fromCharCode(97 + (b >> 4)) + String.fromCharCode(97 + (b & 15)),
    )
    .join("");
}

/** The base64 DER public key of a PEM private key. */
export function publicKeyOf(pem) {
  return createPublicKey(createPrivateKey(pem))
    .export({ type: "spki", format: "der" })
    .toString("base64");
}

function varint(n) {
  const out = [];
  for (; n > 0x7f; n >>>= 7) out.push((n & 0x7f) | 0x80);
  out.push(n);
  return Buffer.from(out);
}

// A protobuf length-delimited field.
function field(num, bytes) {
  return Buffer.concat([varint((num << 3) | 2), varint(bytes.length), bytes]);
}

const le32 = (n) => {
  const b = Buffer.alloc(4);
  b.writeUInt32LE(n);
  return b;
};

/** Signs zipBytes with the PEM private key and returns the CRX3 file. */
export function crx3(zipBytes, pem) {
  const key = createPrivateKey(pem);
  const pub = createPublicKey(key).export({ type: "spki", format: "der" });
  const crxId = createHash("sha256").update(pub).digest().subarray(0, 16);
  const signedData = field(1, crxId);
  const signature = sign(
    "sha256",
    Buffer.concat([
      Buffer.from("CRX3 SignedData\0"),
      le32(signedData.length),
      signedData,
      zipBytes,
    ]),
    key,
  );
  const proof = Buffer.concat([field(1, pub), field(2, signature)]);
  const header = Buffer.concat([field(2, proof), field(10000, signedData)]);
  return Buffer.concat([
    Buffer.from("Cr24"),
    le32(3),
    le32(header.length),
    header,
    zipBytes,
  ]);
}
