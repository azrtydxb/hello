import type { CSSProperties, ReactNode } from "react";
import { cx } from "./cx";
import { Icon } from "./Icon";

/** Props for <Sidebar>. */
export interface SidebarProps {
  /** Usually a horizontal ProductLogo. */
  brand: ReactNode;
  /** The nav: SidebarNav with SidebarNavGroup and nav items inside. */
  children: ReactNode;
  /** The status line; `live` pulses the dot, otherwise it is muted. */
  status?: { label: ReactNode; live: boolean };
  user?: { name: string; role: string };
  onSignOut?: () => void;
  signOutLabel?: string;
  className?: string;
  style?: CSSProperties;
}

function initials(name: string): string {
  return name
    .split(/\s+/)
    .filter(Boolean)
    .map((w) => w[0])
    .join("")
    .slice(0, 2)
    .toUpperCase();
}

/**
 * The console sidebar as the Kuvryn Hello console design draws it: brand,
 * grouped nav, then the status line and the user block with log out.
 */
export function Sidebar({
  brand,
  children,
  status,
  user,
  onSignOut,
  signOutLabel = "Log out",
  className,
  style,
}: SidebarProps) {
  return (
    <aside className={cx("az-sidebar", className)} style={style}>
      <div className="az-sidebar__brand">{brand}</div>
      {children}
      <div className="az-sidebar__foot">
        {status && (
          <div className="az-connected">
            <span
              className={cx("az-dot", status.live && "az-dot--pulse")}
              style={
                status.live ? undefined : { background: "var(--az-faint)" }
              }
              aria-hidden="true"
            />
            {status.label}
          </div>
        )}
        {user && (
          <div className="az-profile">
            <span className="az-avatar" aria-hidden="true">
              {initials(user.name)}
            </span>
            <span className="az-profile__meta">
              <b>{user.name}</b>
              <small>{user.role}</small>
            </span>
            {onSignOut && (
              <button
                type="button"
                className="az-iconbtn"
                title={signOutLabel}
                aria-label={signOutLabel}
                onClick={onSignOut}
              >
                <Icon name="log-out" size={15} />
              </button>
            )}
          </div>
        )}
      </div>
    </aside>
  );
}

/** The nav landmark inside the sidebar. */
export function SidebarNav({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <nav className="az-sidebar__nav" aria-label={label}>
      {children}
    </nav>
  );
}

/** A group of nav items under an eyebrow heading (none when `label` is empty). */
export function SidebarNavGroup({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <div
      className="az-sidebar__group"
      role="group"
      aria-label={label || undefined}
    >
      {label && (
        <span className="az-eyebrow" aria-hidden="true">
          {label}
        </span>
      )}
      {children}
    </div>
  );
}

/** Class names for a nav item (link or button), active or not. */
export function navItemClassName(active: boolean): string {
  return cx("az-nav__item", active && "az-nav__item--active");
}

/** The inside of a nav item: icon, label, optional badge. */
export function NavItemContent({
  icon,
  label,
  badge,
}: {
  icon: string;
  label: string;
  badge?: ReactNode;
}) {
  return (
    <>
      <Icon name={icon} size={16} />
      <span>{label}</span>
      {badge != null && <span className="az-nav__badge">{badge}</span>}
    </>
  );
}
