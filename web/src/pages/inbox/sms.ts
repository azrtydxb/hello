// How many texts a message costs: the carrier splits a long text, so the
// composer says so. GSM-7 text fits 160 characters in one text and 153 in
// each part of a longer one; anything outside it makes the text UCS-2, 70 and
// 67. The carrier does the splitting; Hello stores one message.

const GSM =
  "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà";
const GSM_EXT = "^{}\\[~]|€\f";

/** Characters and the number of texts a message makes. */
export function smsSegments(text: string): {
  chars: number;
  segments: number;
  unicode: boolean;
} {
  let units = 0;
  let unicode = false;
  for (const ch of text) {
    if (GSM.includes(ch)) units += 1;
    else if (GSM_EXT.includes(ch)) units += 2;
    else {
      unicode = true;
      break;
    }
  }
  if (unicode) {
    // UCS-2 counts UTF-16 code units.
    const n = text.length;
    return {
      chars: n,
      segments: n === 0 ? 0 : n <= 70 ? 1 : Math.ceil(n / 67),
      unicode,
    };
  }
  const chars = [...text].length;
  return {
    chars,
    segments: units === 0 ? 0 : units <= 160 ? 1 : Math.ceil(units / 153),
    unicode,
  };
}

/** The customer's name when a shared source has one, else their number. */
export function peerLabel(c: { peer: string; peerName: string }): string {
  return c.peerName ? `${c.peerName} (${c.peer})` : c.peer;
}
