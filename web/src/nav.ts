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
    label: "My phone",
    items: [
      {
        label: "Address book",
        path: "/me/contacts",
        phase: 5,
        icon: "contact",
        minRole: "user",
      },
      {
        label: "Voicemail",
        path: "/me/voicemail",
        phase: 5,
        icon: "voicemail",
        minRole: "user",
      },
      {
        label: "Call forwarding",
        path: "/me/forwarding",
        phase: 5,
        icon: "phone-forwarded",
        minRole: "user",
      },
      {
        label: "Call history",
        path: "/me/history",
        phase: 5,
        icon: "history",
        minRole: "user",
      },
      {
        label: "Messages",
        path: "/me/messages",
        phase: 5,
        icon: "message-square",
        minRole: "user",
      },
      {
        label: "Profile",
        path: "/me/profile",
        phase: 5,
        icon: "circle-user",
        minRole: "user",
      },
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
        label: "Onboarding",
        path: "/onboarding",
        phase: 5,
        icon: "user-plus",
        // Adopting creates phones, a write; viewers do not see it.
        minRole: "operator",
      },
      { label: "Phonebooks", path: "/phonebooks", phase: 5, icon: "book-user" },
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
      { label: "Blocklist", path: "/blocklist", phase: 5, icon: "shield-ban" },
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
      { label: "Paging", path: "/paging", phase: 5, icon: "megaphone" },
      {
        label: "Conferences",
        path: "/conferences",
        phase: 5,
        icon: "users",
      },
      { label: "IVR", path: "/ivr", phase: 5, icon: "list-tree" },
      { label: "Queues", path: "/queues", phase: 5, icon: "headset" },
      {
        label: "Time conditions",
        path: "/time-conditions",
        phase: 5,
        icon: "clock",
      },
      {
        label: "Park and pickup",
        path: "/park",
        phase: 5,
        icon: "square-parking",
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
      { label: "Music on hold", path: "/moh", phase: 5, icon: "music" },
    ],
  },
  {
    label: "Activity",
    items: [
      { label: "Active calls", path: "/calls", phase: 1, icon: "phone-call" },
      {
        label: "Queue wallboard",
        path: "/wallboard",
        phase: 5,
        icon: "gauge",
      },
      { label: "Call history", path: "/history", phase: 1, icon: "history" },
      {
        label: "Messages",
        path: "/messages",
        phase: 5,
        icon: "messages-square",
        // Sending is a write; reading the text is admin only (the page says so).
        minRole: "operator",
      },
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
    label: "Integrations",
    items: [
      {
        label: "Overview",
        path: "/integrations",
        phase: 5,
        icon: "plug",
        minRole: "admin",
      },
      {
        label: "API access",
        path: "/integrations/api-access",
        phase: 5,
        icon: "key-round",
        minRole: "admin",
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
      {
        label: "Voice (TTS)",
        path: "/settings/voice",
        phase: 5,
        icon: "audio-lines",
        // The page holds the provider key (admin-only routes).
        minRole: "admin",
      },
      {
        label: "Email",
        path: "/settings/email",
        phase: 5,
        icon: "mail",
        minRole: "admin",
      },
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

/** The sidebar group of the extension self-service pages. */
export const MY_PHONE_GROUP = "My phone";

/**
 * The groups and items `role` may see; empty groups are dropped. The AI
 * group shows in full only while AI is enabled; while it is off, only its
 * status page stays, so the reason can be read. A user (the self-service
 * role) sees only My phone. Everyone else sees My phone only when they have
 * an extension of their own.
 */
export function navGroupsFor(
  role: string | undefined,
  aiEnabled = false,
  hasExtensions = false,
): NavGroup[] {
  if (role === "user") {
    return NAV_GROUPS.filter((g) => g.label === MY_PHONE_GROUP);
  }
  return NAV_GROUPS.map((g) => ({
    ...g,
    items: g.items.filter(
      (i) =>
        atLeast(role, i.minRole ?? "viewer") &&
        (g.label !== "AI" || aiEnabled || i.path === "/ai/status") &&
        (g.label !== MY_PHONE_GROUP || hasExtensions),
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
    .concat("/"),
);

/** The breadcrumb title for a location (the design's page titles). */
export function pageTitle(pathname: string): string | null {
  const exact = NAV_ITEMS.find((i) => i.path === pathname);
  if (exact) return exact.label;
  if (/^\/history\/[^/]+$/.test(pathname)) return "Call detail";
  if (/^\/ai\/proposals\/[^/]+$/.test(pathname)) return "Proposal";
  const parent = NAV_ITEMS.filter(
    (i) => i.path !== "/" && pathname.startsWith(i.path + "/"),
  ).sort((a, b) => b.path.length - a.path.length)[0];
  return parent ? parent.label : null;
}

/**
 * The label of the group holding the nav item for `pathname` (the longest
 * matching path), or null. Call detail belongs to Call history.
 */
export function activeGroupLabel(
  groups: readonly NavGroup[],
  pathname: string,
): string | null {
  const path = /^\/history\/[^/]+$/.test(pathname) ? "/history" : pathname;
  let best: { label: string; len: number } | null = null;
  for (const g of groups) {
    for (const i of g.items) {
      const hit =
        i.path === path || (i.path !== "/" && path.startsWith(i.path + "/"));
      if (hit && (!best || i.path.length > best.len)) {
        best = { label: g.label, len: i.path.length };
      }
    }
  }
  return best ? best.label : null;
}
