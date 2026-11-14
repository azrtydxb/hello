/* Proof of work for the chat session challenge: find a counter so that
 * SHA-256(nonce + ":" + counter) starts with `bits` zero bits. Plain
 * JavaScript SHA-256, because crypto.subtle needs a secure context and an
 * intranet page is often plain http. */
(function (root) {
  'use strict';
  var K = new Uint32Array([
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
  ]);
  var w = new Uint32Array(64);

  /* sha256 of an ASCII string; returns the eight state words. */
  function sha256(str) {
    var n = str.length;
    var blocks = ((n + 9 + 63) >> 6);
    var buf = new Uint8Array(blocks * 64);
    for (var i = 0; i < n; i++) { buf[i] = str.charCodeAt(i) & 0xff; }
    buf[n] = 0x80;
    var bitsHi = Math.floor((n * 8) / 4294967296);
    var bitsLo = (n * 8) >>> 0;
    var end = blocks * 64;
    buf[end - 8] = bitsHi >>> 24; buf[end - 7] = bitsHi >>> 16; buf[end - 6] = bitsHi >>> 8; buf[end - 5] = bitsHi;
    buf[end - 4] = bitsLo >>> 24; buf[end - 3] = bitsLo >>> 16; buf[end - 2] = bitsLo >>> 8; buf[end - 1] = bitsLo;
    var h = new Uint32Array([0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a, 0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19]);
    for (var b = 0; b < blocks; b++) {
      var o = b * 64;
      for (var t = 0; t < 16; t++) {
        w[t] = (buf[o + 4 * t] << 24) | (buf[o + 4 * t + 1] << 16) | (buf[o + 4 * t + 2] << 8) | buf[o + 4 * t + 3];
      }
      for (t = 16; t < 64; t++) {
        var x = w[t - 15], y = w[t - 2];
        var s0 = ((x >>> 7) | (x << 25)) ^ ((x >>> 18) | (x << 14)) ^ (x >>> 3);
        var s1 = ((y >>> 17) | (y << 15)) ^ ((y >>> 19) | (y << 13)) ^ (y >>> 10);
        w[t] = (w[t - 16] + s0 + w[t - 7] + s1) | 0;
      }
      var a = h[0], bb = h[1], c = h[2], d = h[3], e = h[4], f = h[5], g = h[6], hh = h[7];
      for (t = 0; t < 64; t++) {
        var S1 = ((e >>> 6) | (e << 26)) ^ ((e >>> 11) | (e << 21)) ^ ((e >>> 25) | (e << 7));
        var ch = (e & f) ^ (~e & g);
        var t1 = (hh + S1 + ch + K[t] + w[t]) | 0;
        var S0 = ((a >>> 2) | (a << 30)) ^ ((a >>> 13) | (a << 19)) ^ ((a >>> 22) | (a << 10));
        var maj = (a & bb) ^ (a & c) ^ (bb & c);
        var t2 = (S0 + maj) | 0;
        hh = g; g = f; f = e; e = (d + t1) | 0; d = c; c = bb; bb = a; a = (t1 + t2) | 0;
      }
      h[0] += a; h[1] += bb; h[2] += c; h[3] += d; h[4] += e; h[5] += f; h[6] += g; h[7] += hh;
    }
    return h;
  }

  function leadingZeros(h) {
    var n = 0;
    for (var i = 0; i < 8; i++) {
      if (h[i] === 0) { n += 32; continue; }
      return n + Math.clz32(h[i]);
    }
    return n;
  }

  /* solve calls done(solution) once found, working in slices so the page
   * stays responsive; progress(fraction) is optional. */
  function solve(nonce, bits, done, progress) {
    var counter = 0;
    var expected = Math.pow(2, bits);
    function slice() {
      var stop = counter + 20000;
      for (; counter < stop; counter++) {
        if (leadingZeros(sha256(nonce + ':' + counter)) >= bits) {
          done(String(counter));
          return;
        }
      }
      if (progress) { progress(Math.min(0.99, counter / expected)); }
      setTimeout(slice, 0);
    }
    slice();
  }

  var api = { sha256: sha256, leadingZeros: leadingZeros, solve: solve };
  if (typeof module !== 'undefined' && module.exports) { module.exports = api; }
  root.HelloPow = api;
})(typeof window !== 'undefined' ? window : globalThis);
