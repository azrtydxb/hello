/** Primary navigation, per spec §21. `phase` is when the page gets content. */
export interface NavItem {
  label: string;
  path: string;
  phase: number;
}

export const NAV_ITEMS: readonly NavItem[] = [
  { label: "Dashboard", path: "/", phase: 0 },
  { label: "Extensions", path: "/extensions", phase: 1 },
  { label: "Devices", path: "/devices", phase: 1 },
  { label: "Trunks", path: "/trunks", phase: 2 },
  { label: "Routes", path: "/routes", phase: 2 },
  { label: "Dial Plans", path: "/dial-plans", phase: 2 },
  { label: "Ring Groups", path: "/ring-groups", phase: 4 },
  { label: "Voicemail", path: "/voicemail", phase: 4 },
  { label: "Active Calls", path: "/active-calls", phase: 1 },
  { label: "Call History", path: "/call-history", phase: 1 },
  { label: "Cluster", path: "/cluster", phase: 3 },
  { label: "Diagnostics", path: "/diagnostics", phase: 2 },
  { label: "System", path: "/system", phase: 3 },
];
