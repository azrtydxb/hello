import type { CSSProperties, ReactNode } from "react";
import { Avatar } from "./Avatar";
import { cx } from "./cx";
import { IconButton } from "./IconButton";
import { Icon } from "./Icon";

/** Props for <Sidebar>. */
export interface SidebarProps {
  /** Usually a horizontal ProductLogo. */
  brand: ReactNode;
  /** The nav: SidebarNav with SidebarNavGroup / nav items inside. */
  children: ReactNode;
  /** Control-plane status line; `live` pulses the dot, otherwise it is muted. */
  status?: { label: ReactNode; live: boolean };
  user?: { name: string; role: string };
  onSignOut?: () => void;
  signOutLabel?: string;
  /** Extra footer content (e.g. a theme toggle), above the status line. */
  footer?: ReactNode;
  className?: string;
  style?: CSSProperties;
}

/** The fixed 240px app sidebar: brand, nav, status, user. */
export function Sidebar({
  brand,
  children,
  status,
  user,
  onSignOut,
  signOutLabel = "Sign out",
  footer,
  className,
  style,
}: SidebarProps) {
  return (
    <aside className={cx("az-sidebar", className)} style={style}>
      <div className="az-sidebar__brand">{brand}</div>
      {children}
      <div className="az-sidebar__foot">
        {footer}
        {status && (
          <div className="az-connected">
            <span
              className={cx("az-dot", status.live && "az-dot--pulse")}
              style={
                status.live ? undefined : { background: "var(--az-faint)" }
              }
            />
            {status.label}
          </div>
        )}
        {user && (
          <div className="az-profile">
            <Avatar name={user.name} size={32} />
            <span className="az-profile__meta">
              <b>{user.name}</b>
              <small>{user.role}</small>
            </span>
            {onSignOut && (
              <IconButton
                icon="log-out"
                label={signOutLabel}
                size={15}
                onClick={onSignOut}
              />
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
    <nav className="az-nav" aria-label={label}>
      {children}
    </nav>
  );
}

/** A labelled group of nav items (az-eyebrow heading). */
export function SidebarNavGroup({
  label,
  children,
  className,
}: {
  label: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={cx("az-nav", className)} role="group" aria-label={label}>
      <span className="az-eyebrow" aria-hidden="true">
        {label}
      </span>
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
      <Icon name={icon} size={17} />
      <span>{label}</span>
      {badge != null && <span className="az-nav__badge">{badge}</span>}
    </>
  );
}
