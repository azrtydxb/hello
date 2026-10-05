import type { CSSProperties, ReactNode } from "react";
import { cx } from "./cx";
import { Icon } from "./Icon";

/** Props for <EmptyState>. */
export interface EmptyStateProps {
  icon?: string;
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  className?: string;
  style?: CSSProperties;
}

/** What an empty list says, and what to do about it. */
export function EmptyState({
  icon = "inbox",
  title,
  description,
  action,
  className,
  style,
}: EmptyStateProps) {
  return (
    <div className={cx("az-empty", className)} style={style}>
      <Icon name={icon} size={28} />
      <h3 className="az-empty__title">{title}</h3>
      {description && <p className="az-empty__text">{description}</p>}
      {action && <div className="az-empty__action">{action}</div>}
    </div>
  );
}
