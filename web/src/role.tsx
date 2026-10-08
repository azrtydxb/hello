import type { ReactNode } from "react";
import { useAuth } from "./auth";

/** A user's role (spec S-23), least to most privileged. */
export type Role = "viewer" | "operator" | "admin";

export const ROLES: readonly Role[] = ["viewer", "operator", "admin"];

const RANK: Record<Role, number> = { viewer: 1, operator: 2, admin: 3 };

/** Whether `role` is `min` or above; an unknown role is below every role. */
export function atLeast(role: string | undefined, min: Role): boolean {
  const r = RANK[role as Role] ?? 0;
  return r > 0 && r >= RANK[min];
}

/** The signed-in user's role, or undefined while signed out. */
export function useRole(): Role | undefined {
  const { state } = useAuth();
  return state.status === "signedIn" ? state.role : undefined;
}

/** Whether the signed-in user has at least role `min`. */
export function useCan(min: Role): boolean {
  return atLeast(useRole(), min);
}

/**
 * Renders children only for a user with at least role `min` (default
 * operator, the role of write operations). The API stays the enforcement
 * point; this only hides what the caller cannot use.
 */
export function Can({
  min = "operator",
  fallback = null,
  children,
}: {
  min?: Role;
  /** Shown instead, e.g. a toggle's state as text. */
  fallback?: ReactNode;
  children: ReactNode;
}) {
  return <>{useCan(min) ? children : fallback}</>;
}
