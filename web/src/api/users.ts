// Client for user accounts (spec S-23): the Users page (admin) and the
// signed-in user's own profile and password. The shared request helpers live
// in ../api.
import {
  request,
  type Id,
  type VoicemailEmailAfter,
  type VoicemailEmailPrefs,
} from "../api";
import type { Role } from "../role";

/** A user as an administrator sees them; never a password or hash. */
export interface User {
  id: Id;
  username: string;
  displayName: string;
  email: string;
  authMethod: "local";
  role: Role;
  enabled: boolean;
  /** The user has to choose a new password at next sign-in. */
  mustChangePassword: boolean;
  /** The primary extension, whose phones get the user's personal contacts. */
  extensionId: Id | null;
  /** Every extension the user manages, the primary first. */
  extensionIds: Id[];
  voicemailEmail: VoicemailEmailPrefs;
  createdAt: string;
}

/** POST /api/v1/users body. */
export interface NewUser {
  username: string;
  displayName: string;
  email: string;
  role: Role;
  /** The initial password; the user must replace it unless told otherwise. */
  password: string;
  mustChangePassword: boolean;
  extensionIds: Id[];
  voicemailEmail?: Partial<VoicemailEmailPrefs>;
}

/** PATCH /api/v1/users/{id} body: only what changes. */
export interface UserChange {
  role?: Role;
  displayName?: string;
  email?: string;
  enabled?: boolean;
  /** Reset the password. */
  password?: string;
  mustChangePassword?: boolean;
  extensionIds?: Id[];
  voicemailEmail?: Partial<VoicemailEmailPrefs>;
}

/** GET /api/v1/users (admin). */
export async function listUsers(signal?: AbortSignal): Promise<User[]> {
  const r = await request<{ items: User[] }>("GET", "/api/v1/users", {
    signal,
  });
  return r.items;
}

/** POST /api/v1/users (admin). */
export function createUser(input: NewUser): Promise<User> {
  return request<User>("POST", "/api/v1/users", { body: input });
}

/** PATCH /api/v1/users/{id} (admin). */
export function updateUser(id: Id, change: UserChange): Promise<User> {
  return request<User>("PATCH", `/api/v1/users/${id}`, { body: change });
}

/** PATCH /api/v1/users/{id} (admin): change a user's role. */
export function setUserRole(id: Id, role: Role): Promise<User> {
  return updateUser(id, { role });
}

/** DELETE /api/v1/users/{id} (admin). */
export function deleteUser(id: Id): Promise<void> {
  return request<void>("DELETE", `/api/v1/users/${id}`);
}

/** POST /api/v1/auth/password: the caller's own password. */
export function changePassword(
  currentPassword: string,
  newPassword: string,
): Promise<void> {
  return request<void>("POST", "/api/v1/auth/password", {
    body: { currentPassword, newPassword },
  });
}

/** PATCH /api/v1/me/profile: the caller's own name, email and options. */
export function updateProfile(change: {
  displayName?: string;
  email?: string;
  voicemailEmail?: Partial<VoicemailEmailPrefs>;
}): Promise<void> {
  return request<void>("PATCH", "/api/v1/me/profile", { body: change });
}

/** The labels the voicemail "after sending" choice shows. */
export const AFTER_LABELS: Record<VoicemailEmailAfter, string> = {
  none: "Leave the message as it is",
  mark_read: "Mark it as read",
  delete: "Delete it",
};

/** A random initial password an administrator can hand over. */
export function generatePassword(): string {
  const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789";
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return Array.from(bytes, (b) => alphabet[b % alphabet.length]).join("");
}
