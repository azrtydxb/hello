// Phone numbers as Hello takes them (docs/click-to-call.md): digits, +, * and
// #, at most 64. Mirrors dialable() in web/src/api/originate.ts.

/** The destination a tel: or callto: link or a typed number carries, or "". */
export function dialable(raw) {
  let n = String(raw ?? "").trim();
  n = n.replace(/^(tel|callto):/i, "").replace(/^\/\//, "");
  n = n.split(/[;?]/)[0] ?? "";
  try {
    n = decodeURIComponent(n);
  } catch {
    return "";
  }
  n = n.replace(/[\s().-]/g, "");
  return /^[0-9+*#]{1,64}$/.test(n) ? n : "";
}

/** Whether href is a tel: or callto: link. */
export function isPhoneHref(href) {
  return /^(tel|callto):/i.test(String(href ?? "").trim());
}

// A number on a page: an international form, or a long enough local form with
// separators. Deliberately strict: a false positive turns ordinary text into a
// link, so dates, prices and ids are left alone.
const NUMBER =
  /(?<![\w+])(\+\d{1,3}[\s.-]?(?:\(\d{1,4}\)[\s.-]?)?\d{1,4}(?:[\s.-]?\d{2,4}){2,4}|\(?0\d{1,3}\)?[\s.-]\d{2,4}(?:[\s.-]\d{2,4}){1,3})(?![\w])/g;

/** Finds the phone numbers in text: [{start, end, text, number}]. */
export function findNumbers(text) {
  const out = [];
  for (const m of String(text).matchAll(NUMBER)) {
    const number = dialable(m[0]);
    const digits = number.replace(/\D/g, "");
    if (number === "" || digits.length < 7 || digits.length > 15) continue;
    // A run like 2026-11-12 or 12.50.100 is not a phone number.
    if (/^\d{4}-\d{2}-\d{2}$/.test(m[0].trim())) continue;
    out.push({
      start: m.index,
      end: m.index + m[0].length,
      text: m[0],
      number,
    });
  }
  return out;
}
