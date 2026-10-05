import type { CSSProperties, ReactNode } from "react";
import { cx } from "./cx";
import { Icon } from "./Icon";

/** The allowed badge tone values. */
export type BadgeTone =
  "neutral" | "good" | "warn" | "bad" | "info" | "pillar" | "outline";

/** Props for <Badge>. */
export interface BadgeProps {
  tone?: BadgeTone;
  /** A leading status dot. */
  dot?: boolean;
  icon?: string;
  children?: ReactNode;
  className?: string;
  style?: CSSProperties;
}

/** A small status label. Status is never colour alone: always say it. */
export function Badge({
  tone = "neutral",
  dot,
  icon,
  children,
  className,
  style,
}: BadgeProps) {
  return (
    <span
      className={cx("az-badge", `az-badge--${tone}`, className)}
      style={style}
    >
      {dot && <span className="az-badge__dot" />}
      {icon && <Icon name={icon} size={11} />}
      {children}
    </span>
  );
}
