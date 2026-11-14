import type { InboxMedia } from "../../api/inbox";

/** A byte count as the inbox shows it. */
export function formatSize(n: number): string {
  if (n >= 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
  if (n >= 1024) return `${Math.round(n / 1024)} KB`;
  return `${n} bytes`;
}

/** Why an attachment has no file, in words. */
export function mediaReason(m: InboxMedia): string {
  switch (m.reason) {
    case "too_large":
      return "it was larger than the inbox keeps";
    case "type_not_allowed":
      return "files of this type are not accepted";
    case "type_mismatch":
      return "the file was not what its type said";
    case "active_content":
      return "it carried markup or script, which is never accepted";
    case "media_host":
      return "its address was not the carrier's own";
    case "provider_cannot_fetch":
      return "this carrier's media cannot be downloaded";
    case "gone_at_provider":
    case "download_window_passed":
      return "the carrier no longer had it";
    case "retention":
      return "it passed the media retention";
  }
  return m.status === "expired" ? "it expired" : "it was refused";
}

/**
 * The attachments of one message (spec messaging-channels M-18). Images are
 * thumbnails that open in a new tab, audio and video play in place, anything
 * else is a download. Every file comes from Hello's own authorised route; a
 * file name is shown as text.
 */
export function MessageMedia({
  media,
  url,
}: {
  media: InboxMedia[];
  url: (id: number) => string;
}) {
  if (media.length === 0) return null;
  return (
    <ul className="inbox-media" aria-label="Attachments">
      {media.map((m) => (
        <li key={m.id} className="inbox-media__item">
          <MediaItem m={m} href={url(m.id)} />
        </li>
      ))}
    </ul>
  );
}

function MediaItem({ m, href }: { m: InboxMedia; href: string }) {
  const name = m.filename || "attachment";
  if (m.status === "pending") {
    return <span className="az-caption">Downloading an attachment…</span>;
  }
  if (m.status !== "ready") {
    return (
      <span className="az-caption">
        An attachment was not kept: {mediaReason(m)}.
      </span>
    );
  }
  if (m.contentType.startsWith("image/")) {
    return (
      <a href={href} target="_blank" rel="noopener noreferrer">
        <img
          className="inbox-media__thumb"
          src={href}
          alt={name}
          loading="lazy"
        />
      </a>
    );
  }
  if (m.contentType.startsWith("audio/")) {
    return (
      <audio controls preload="none" src={href} aria-label={name}>
        <a href={href} download={name}>
          {name}
        </a>
      </audio>
    );
  }
  if (m.contentType.startsWith("video/")) {
    return (
      <video
        className="inbox-media__video"
        controls
        preload="metadata"
        src={href}
        aria-label={name}
      >
        <a href={href} download={name}>
          {name}
        </a>
      </video>
    );
  }
  return (
    <a className="inbox-media__file" href={href} download={name}>
      📎 {name} ({formatSize(m.size)})
    </a>
  );
}
