import type { CSSProperties } from "react";
import { cx } from "./cx";

/** Props for <Spinner>. */
export interface SpinnerProps {
  size?: number;
  /** Visible text next to the spinner; also its accessible name. */
  label?: string;
  className?: string;
  style?: CSSProperties;
}

/** An indeterminate loading indicator (role="status"). */
export function Spinner({ size = 16, label, className, style }: SpinnerProps) {
  const s = (
    <span
      className="az-spinner"
      style={{ width: size, height: size, borderWidth: size >= 24 ? 3 : 2 }}
      role="status"
      aria-label={label || "Loading"}
    />
  );
  if (!label) {
    return (
      <span
        className={className || undefined}
        style={{ display: "inline-flex", ...style }}
      >
        {s}
      </span>
    );
  }
  return (
    <span className={cx("az-spinner-wrap", className)} style={style}>
      {s}
      {label}
    </span>
  );
}
