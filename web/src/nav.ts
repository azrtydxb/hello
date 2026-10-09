import { atLeast, type Role } from "./role";

/**
 * Primary navigation, from the Kuvryn Hello console design (groups, labels,
 * order and icons). `phase` is when the page gets content (spec §21).
 */
export interface NavItem {
  label: string;
  path: string;
  phase: number;
  /** Lucide icon name (design/azrty icon font). */
  icon: string;
  /** The least role that sees the item (spec S-23); default viewer. */
  minRole?: Role;
}

/** A section of the sidebar; the first one has no heading. */
export interface NavGroup {
  label: string;
  items: readonly NavItem[];
}

/** The phase whose pages are built; later pages render a placeholder. */
export const CURRENT_PHASE = 5;

export const NAV_GROUPS: readonly NavGroup[] = [
  {
    label: "",
    items: [
      { label: "Dashboard", path: "/", phase: 0, icon: "layout-dashboard" },
    ],
  },
  {
    label: "Directory",
    items: [
      {
        label: "Extensions",
        path: "/extensions",
        phase: 1,
        icon: "user-round",
      },
      { label: "Devices", path: "/devices", phase: 1, icon: "smartphone" },
      { label: "Phones", path: "/phones", phase: 5, icon: "phone" },
      {
        label: "Registrations",
        path: "/registrations",
        phase: 1,
        icon: "radio-tower",
      },
    ],
  },
  {
    label: "Call flow",
    items: [
      { label: "Trunks", path: "/trunks", phase: 2, icon: "cable" },
      { label: "Routes", path: "/routes", phase: 2, icon: "route" },
      {
        label: "Route tester",
        path: "/routes/test",
        phase: 2,
        icon: "flask-conical",
        // The tester runs POST /routing/test, a write operation.
        minRole: "operator",
      },
      {
        label: "Ring groups",
        path: "/ring-groups",
        phase: 4,
        icon: "users-round",
      },
    ],
  },
  {
    label: "Media",
    items: [
      { label: "Voicemail", path: "/voicemail", phase: 4, icon: "voicemail" },
      { label: "Recordings", path: "/recordings", phase: 5, icon: "mic" },
      {
        label: "Announcements",
        path: "/announcements",
        phase: 5,
        icon: "megaphone",
      },
    ],
  },
  {
    label: "Activity",
    items: [
      { label: "Active calls", path: "/calls", phase: 1, icon: "phone-call" },
      { label: "Call history", path: "/history", phase: 1, icon: "history" },
    ],
  },
  {
    label: "AI",
    items: [
      {
        label: "Assistant",
        path: "/ai/assistant",
        phase: 5,
        icon: "message-square",
        // Chatting posts messages, a write; viewers do not see it.
        minRole: "operator",
      },
      {
        label: "Findings",
        path: "/ai/findings",
        phase: 5,
        icon: "scan-search",
      },
      {
        label: "Proposals",
        path: "/ai/proposals",
        phase: 5,
        icon: "git-pull-request",
      },
      { label: "AI status", path: "/ai/status", phase: 5, icon: "activity" },
    ],
  },
  {
    label: "Voice agents",
    items: [
      {
        label: "Voice agents",
        path: "/voice/agents",
        phase: 5,
        icon: "bot",
      },
      {
        label: "Voice MCP servers",
        path: "/voice/mcp-servers",
        phase: 5,
        icon: "wrench",
      },
      {
        label: "Voice runtime",
        path: "/voice/runtime",
        phase: 5,
        icon: "activity",
      },
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
      { label: "AI access", path: "/ai", phase: 5, icon: "bot" },
      {
        label: "Users",
        path: "/users",
        phase: 5,
        icon: "user-cog",
        minRole: "admin",
      },
    ],
  },
];

/**
 * The groups and items `role` may see; empty groups are dropped. The AI
 * group shows in full only while AI is enabled; while it is off, only its
 * status page stays, so the reason can be read.
 */
export function navGroupsFor(
  role: string | undefined,
  aiEnabled = false,
): NavGroup[] {
  return NAV_GROUPS.map((g) => ({
    ...g,
    items: g.items.filter(
      (i) =>
        atLeast(role, i.minRole ?? "viewer") &&
        (g.label !== "AI" || aiEnabled || i.path === "/ai/status"),
    ),
  })).filter((g) => g.items.length > 0);
}

/** Every nav item, in sidebar order. */
export const NAV_ITEMS: readonly NavItem[] = NAV_GROUPS.flatMap((g) => g.items);

/** Nav items whose link is active only on their exact path. */
export const EXACT_PATHS: ReadonlySet<string> = new Set(
  NAV_ITEMS.filter((a) =>
    NAV_ITEMS.some((b) => b !== a && b.path.startsWith(a.path + "/")),
  )
    .map((i) => i.path)
    .concat("/", "/voice/agents"),
);

/** The breadcrumb title for a location (the design's page titles). */
export function pageTitle(pathname: string): string | null {
  const exact = NAV_ITEMS.find((i) => i.path === pathname);
  if (exact) return exact.label;
  if (/^\/history\/[^/]+$/.test(pathname)) return "Call detail";
  if (/^\/ai\/proposals\/[^/]+$/.test(pathname)) return "Proposal";
  if (/^\/voice\/agents\/[^/]+$/.test(pathname)) return "Voice agent";
  const parent = NAV_ITEMS.filter(
    (i) => i.path !== "/" && pathname.startsWith(i.path + "/"),
  ).sort((a, b) => b.path.length - a.path.length)[0];
  return parent ? parent.label : null;
}
