import { Fragment, type ReactNode } from "react";

/** A URL in a message: http or https only, trailing punctuation left out. */
const URL_RE = /\bhttps?:\/\/[^\s<>"']+/gi;
const TRAILING = /[.,;:!?)\]}]+$/;

type Part = { text: string } | { text: string; url: URL };

/** Splits a message into plain text and links. Never builds markup. */
export function splitLinks(text: string): Part[] {
  const parts: Part[] = [];
  let last = 0;
  for (const m of text.matchAll(URL_RE)) {
    const start = m.index;
    const raw = m[0].replace(TRAILING, "");
    let url: URL;
    try {
      url = new URL(raw);
    } catch {
      continue;
    }
    if (url.protocol !== "http:" && url.protocol !== "https:") continue;
    if (start > last) parts.push({ text: text.slice(last, start) });
    parts.push({ text: raw, url });
    last = start + raw.length;
  }
  if (last < text.length) parts.push({ text: text.slice(last) });
  return parts;
}

/**
 * A visitor's message as text with its links made clickable. The links carry
 * rel="noopener noreferrer nofollow" and do not open by themselves: `onOpen`
 * decides (the inbox asks once per host before it leaves Hello).
 */
export function LinkedText({
  text,
  onOpen,
}: {
  text: string;
  onOpen: (url: URL) => void;
}): ReactNode {
  return (
    <>
      {splitLinks(text).map((p, i) => (
        <Fragment key={i}>
          {"url" in p ? (
            <a
              href={p.url.href}
              target="_blank"
              rel="noopener noreferrer nofollow"
              onClick={(e) => {
                e.preventDefault();
                onOpen(p.url);
              }}
            >
              {p.text}
            </a>
          ) : (
            p.text
          )}
        </Fragment>
      ))}
    </>
  );
}
