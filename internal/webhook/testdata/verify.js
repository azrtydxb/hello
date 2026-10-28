const crypto = require("node:crypto");

// verify throws unless header (Hello-Signature) matches rawBody, the exact
// bytes received, under secret, and is less than 5 minutes old.
function verify(secret, header, rawBody, now = Date.now()) {
  const parts = header.split(",").map((p) => p.trim().split("="));
  const t = Number(parts.find(([k]) => k === "t")?.[1]);
  const sigs = parts.filter(([k]) => k === "v1").map(([, v]) => v);
  if (!Number.isFinite(t) || Math.abs(now / 1000 - t) > 300) {
    throw new Error("stale or missing timestamp");
  }
  const want = crypto
    .createHmac("sha256", secret)
    .update(`${t}.`)
    .update(rawBody)
    .digest();
  const ok = sigs.some((s) => {
    const got = Buffer.from(s, "hex");
    return got.length === want.length && crypto.timingSafeEqual(got, want);
  });
  if (!ok) throw new Error("signature mismatch");
}
