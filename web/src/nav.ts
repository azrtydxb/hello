/** Primary navigation, per spec §21. `phase` is when the page gets content. */
export interface NavItem {
  label: string;
  path: string;
  phase: number;
}

/** The phase whose pages are built; later pages render a placeholder. */
export const CURRENT_PHASE = 5;

export const NAV_ITEMS: readonly NavItem[] = [
  { label: "Dashboard", path: "/", phase: 0 },
  { label: "Extensions", path: "/extensions", phase: 1 },
  { label: "Devices", path: "/devices", phase: 1 },
  { label: "Registrations", path: "/registrations", phase: 1 },
  { label: "Trunks", path: "/trunks", phase: 2 },
  { label: "Routes", path: "/routes", phase: 2 },
  { label: "Ring Groups", path: "/ring-groups", phase: 4 },
  { label: "Voicemail", path: "/voicemail", phase: 4 },
  { label: "Recordings", path: "/recordings", phase: 5 },
  { label: "Announcements", path: "/announcements", phase: 5 },
  { label: "Active Calls", path: "/calls", phase: 1 },
  { label: "Call History", path: "/history", phase: 1 },
  { label: "Cluster", path: "/cluster", phase: 3 },
  { label: "Diagnostics", path: "/diagnostics", phase: 3 },
  { label: "System", path: "/system", phase: 3 },
];
