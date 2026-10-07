// Client for the Users page (spec S-23). The shared request helpers live in
// ../api.
import { request, type Id } from "../api";
import type { Role } from "../role";

/** A management user and their role. */
export interface User {
  id: Id;
  username: string;
  role: Role;
  createdAt: string;
}

/** GET /api/v1/users (admin). */
export async function listUsers(signal?: AbortSignal): Promise<User[]> {
  const r = await request<{ items: User[] }>("GET", "/api/v1/users", {
    signal,
  });
  return r.items;
}

/** PATCH /api/v1/users/{id} (admin): change a user's role. */
export function setUserRole(id: Id, role: Role): Promise<User> {
  return request<User>("PATCH", `/api/v1/users/${id}`, { body: { role } });
}
