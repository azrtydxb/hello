import type { ReactNode } from "react";
import { cx } from "./cx";
import "./hello.css";

/** Props for <PageHeader>. */
export interface PageHeaderProps {
  /** The nav group above the title ("Directory", "Call flow", …). */
  eyebrow?: string;
  title: ReactNode;
  /** One line under the title. */
  description?: ReactNode;
  /** Right-aligned buttons, LIVE tag, filters. */
  actions?: ReactNode;
  /** Shown above the title in place of the eyebrow (a back link). */
  back?: ReactNode;
  className?: string;
}

/**
 * The console page header: eyebrow, title (the page's only h1,
 * id="page-title"), one-line description and actions on the right.
 */
export function PageHeader({
  eyebrow,
  title,
  description,
  actions,
  back,
  className,
}: PageHeaderProps) {
  return (
    <div className={cx("az-pagehead", className)}>
      <div>
        {back ??
          (eyebrow && (
            <span className="az-eyebrow az-pagehead__eyebrow">{eyebrow}</span>
          ))}
        <h1 id="page-title" className="az-pagehead__title">
          {title}
        </h1>
        {description && <p className="az-pagehead__desc">{description}</p>}
      </div>
      {actions && <div className="az-pagehead__actions">{actions}</div>}
    </div>
  );
}
