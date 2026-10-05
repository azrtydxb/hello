import { Fragment, type CSSProperties, type ReactNode } from "react";
import { cx } from "./cx";
import { Icon } from "./Icon";

/** Props for <Topbar>. */
export interface TopbarProps {
  /** Breadcrumb trail; the last one is the current page. */
  crumbs: ReadonlyArray<ReactNode>;
  /** Shows the pulsing LIVE marker; pass false when data is not live. */
  live?: boolean;
  children?: ReactNode;
  className?: string;
  style?: CSSProperties;
}

/** The 65px bar above the page: breadcrumbs left, status and actions right. */
export function Topbar({
  crumbs,
  live = true,
  children,
  className,
  style,
}: TopbarProps) {
  return (
    <header className={cx("az-topbar", className)} style={style}>
      <nav className="az-crumbs" aria-label="Breadcrumb">
        {crumbs.map((c, i) => (
          <Fragment key={i}>
            {i > 0 && <Icon name="chevron-right" size={12} />}
            {i === crumbs.length - 1 ? (
              <b aria-current="page">{c}</b>
            ) : (
              <span>{c}</span>
            )}
          </Fragment>
        ))}
      </nav>
      <div className="az-topbar__right">
        {live && (
          <span className="az-live">
            <span className="az-dot az-dot--pulse" />
            LIVE
          </span>
        )}
        {children}
      </div>
    </header>
  );
}
