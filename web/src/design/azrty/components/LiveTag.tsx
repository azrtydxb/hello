import { cx } from "./cx";
import "./hello.css";

/** Props for <LiveTag>. */
export interface LiveTagProps {
  /** False shows PAUSED with a still dot (the view could not refresh). */
  live?: boolean;
  /** The refresh interval, e.g. "5 s", shown as "LIVE · 5 S". */
  every?: string;
  className?: string;
}

/** The pulsing LIVE tag of a view that refreshes itself. */
export function LiveTag({ live = true, every, className }: LiveTagProps) {
  return (
    <span className={cx("az-live", className)}>
      <span
        className={cx("az-dot", live ? "az-dot--pulse" : "az-dot--idle")}
        aria-hidden="true"
      />
      {live ? "LIVE" : "PAUSED"}
      {live && every ? ` · ${every.toUpperCase()}` : ""}
    </span>
  );
}
