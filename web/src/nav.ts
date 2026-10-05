/** Primary navigation, per spec §21. `phase` is when the page gets content. */
export interface NavItem {
  label: string;
  path: string;
  phase: number;
  /** Lucide icon name (design/azrty icon font). */
  icon: string;
}

/** A labelled section of the sidebar. */
export interface NavGroup {
  label: string;
  items: readonly NavItem[];
}

/** The phase whose pages are built; later pages render a placeholder. */
export const CURRENT_PHASE = 5;

export const NAV_GROUPS: readonly NavGroup[] = [
  {
    label: "Overview",
    items: [
      { label: "Dashboard", path: "/", phase: 0, icon: "layout-dashboard" },
    ],
  },
  {
    label: "Directory",
    items: [
      { label: "Extensions", path: "/extensions", phase: 1, icon: "users" },
      { label: "Devices", path: "/devices", phase: 1, icon: "smartphone" },
      {
        label: "Registrations",
        path: "/registrations",
        phase: 1,
        icon: "radio-tower",
      },
      {
        label: "Ring Groups",
        path: "/ring-groups",
        phase: 4,
        icon: "users-round",
      },
      { label: "Voicemail", path: "/voicemail", phase: 4, icon: "voicemail" },
    ],
  },
  {
    label: "Routing",
    items: [
      { label: "Trunks", path: "/trunks", phase: 2, icon: "cable" },
      { label: "Routes", path: "/routes", phase: 2, icon: "route" },
    ],
  },
  {
    label: "Media",
    items: [
      { label: "Recordings", path: "/recordings", phase: 5, icon: "disc-3" },
      {
        label: "Announcements",
        path: "/announcements",
        phase: 5,
        icon: "megaphone",
      },
    ],
  },
  {
    label: "Calls",
    items: [
      { label: "Active Calls", path: "/calls", phase: 1, icon: "phone-call" },
      { label: "Call History", path: "/history", phase: 1, icon: "history" },
    ],
  },
  {
    label: "Platform",
    items: [
      { label: "Cluster", path: "/cluster", phase: 3, icon: "server" },
      {
        label: "Diagnostics",
        path: "/diagnostics",
        phase: 3,
        icon: "stethoscope",
      },
      { label: "System", path: "/system", phase: 3, icon: "settings" },
    ],
  },
];

/** Every nav item, in sidebar order. */
export const NAV_ITEMS: readonly NavItem[] = NAV_GROUPS.flatMap((g) => g.items);

/** The nav item and group for a location, for the breadcrumb. */
export function navFor(
  pathname: string,
): { group: NavGroup; item: NavItem } | null {
  let best: { group: NavGroup; item: NavItem } | null = null;
  for (const group of NAV_GROUPS) {
    for (const item of group.items) {
      const match =
        item.path === "/"
          ? pathname === "/"
          : pathname === item.path || pathname.startsWith(item.path + "/");
      if (match && (!best || item.path.length > best.item.path.length)) {
        best = { group, item };
      }
    }
  }
  return best;
}
