import type { CSSProperties } from "react";
import { cx } from "./cx";

/** Props for <Icon>. */
export interface IconProps {
  /** A Lucide icon name, e.g. "server" (see tokens/icons.css). */
  name: string;
  size?: number;
  color?: string;
  className?: string;
  style?: CSSProperties;
  /** Accessible name; without it the icon is decorative (aria-hidden). */
  label?: string;
}

/** A Lucide glyph from the self-hosted icon font. */
export function Icon({
  name,
  size = 16,
  color,
  className,
  style,
  label,
}: IconProps) {
  return (
    <i
      className={cx(`icon-${name}`, "az-icon", className)}
      style={{ fontSize: size, color, ...style }}
      aria-hidden={label ? undefined : true}
      aria-label={label}
      role={label ? "img" : undefined}
    />
  );
}
