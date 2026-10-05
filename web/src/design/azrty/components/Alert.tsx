import type { CSSProperties, ReactNode } from "react";
import { cx } from "./cx";
import { Icon } from "./Icon";

/** The allowed tone values. */
export type Tone = "info" | "good" | "warn" | "bad";

const ALERT_ICONS: Record<Tone, string> = {
  info: "info",
  good: "circle-check",
  warn: "triangle-alert",
  bad: "circle-x",
};

/** Props for <Alert>. */
export interface AlertProps {
  tone?: Tone;
  title?: ReactNode;
  children?: ReactNode;
  icon?: string;
  action?: ReactNode;
  id?: string;
  className?: string;
  style?: CSSProperties;
}

/** An inline status message. tone="bad" is announced as role="alert". */
export function Alert({
  tone = "info",
  title,
  children,
  icon,
  action,
  id,
  className,
  style,
}: AlertProps) {
  return (
    <div
      id={id}
      className={cx("az-alert", `az-alert--${tone}`, className)}
      role={tone === "bad" ? "alert" : "status"}
      style={style}
    >
      <Icon name={icon || ALERT_ICONS[tone]} size={16} />
      <div className="az-alert__body">
        {title && <span className="az-alert__title">{title}</span>}
        {children && <span>{children}</span>}
      </div>
      {action && <div className="az-alert__action">{action}</div>}
    </div>
  );
}
