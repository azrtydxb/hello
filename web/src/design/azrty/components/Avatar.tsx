import type { CSSProperties } from "react";
import { cx } from "./cx";

const AV_STATUS = {
  online: "var(--az-good)",
  busy: "var(--az-warn)",
  offline: "var(--az-faint)",
} as const;

/** Props for <Avatar>. */
export interface AvatarProps {
  name?: string;
  src?: string;
  size?: number;
  tone?: "neutral" | "pillar" | "mint" | "sky";
  status?: keyof typeof AV_STATUS;
  className?: string;
  style?: CSSProperties;
}

/** Initials (or a picture) in a circle. */
export function Avatar({
  name = "",
  src,
  size = 32,
  tone = "neutral",
  status,
  className,
  style,
}: AvatarProps) {
  const initials = name
    .split(/\s+/)
    .filter(Boolean)
    .map((p) => p[0])
    .slice(0, 2)
    .join("")
    .toUpperCase();
  return (
    <span
      className={cx("az-av", tone !== "neutral" && `az-av--${tone}`, className)}
      style={{
        width: size,
        height: size,
        fontSize: Math.round(size * 0.36),
        ...style,
      }}
      title={name}
      aria-label={name}
      role="img"
    >
      {src ? <img src={src} alt="" /> : initials}
      {status && (
        <span
          className="az-av__status"
          style={{ background: AV_STATUS[status] }}
        />
      )}
    </span>
  );
}
