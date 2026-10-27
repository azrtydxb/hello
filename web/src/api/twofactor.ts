// A wrong code answers 401 invalid_code; the calls that take one pass
// redirectOn401: false so that does not read as an expired session.
// Client for two-factor authentication (TOTP): the signed-in user's own
// enrolment and the administrator's policy and reset. Secrets and recovery
// codes are shown once, so nothing here caches them.
import { request, type Id, type TwoFactorState } from "../api";
import type { Role } from "../role";

/** POST /api/v1/me/2fa/setup: the secret appears in this answer only. */
export interface TwoFactorSetup {
  /** Base32 secret, for typing into an authenticator app. */
  secret: string;
  /** otpauth:// URI, drawn as a QR code in the browser. */
  otpauthUri: string;
}

/** GET /api/v1/auth/2fa-policy */
export interface TwoFactorPolicy {
  /** Roles whose users must use 2FA. */
  require2faRoles: Role[];
}

/** GET /api/v1/me/2fa */
export const getTwoFactor = (signal?: AbortSignal) =>
  request<TwoFactorState>("GET", "/api/v1/me/2fa", { signal });

/** POST /api/v1/me/2fa/setup */
export const setupTwoFactor = () =>
  request<TwoFactorSetup>("POST", "/api/v1/me/2fa/setup");

/** POST /api/v1/me/2fa/enable: confirm with a code; returns the recovery codes. */
export async function enableTwoFactor(code: string): Promise<string[]> {
  const r = await request<{ recoveryCodes: string[] }>(
    "POST",
    "/api/v1/me/2fa/enable",
    { body: { code }, redirectOn401: false },
  );
  return r.recoveryCodes;
}

/** POST /api/v1/me/2fa/disable */
export const disableTwoFactor = (password: string, code: string) =>
  request<void>("POST", "/api/v1/me/2fa/disable", {
    body: { password, code },
    redirectOn401: false,
  });

/** POST /api/v1/me/2fa/recovery-codes: replaces the codes; returns the new ones. */
export async function regenerateRecoveryCodes(code: string): Promise<string[]> {
  const r = await request<{ recoveryCodes: string[] }>(
    "POST",
    "/api/v1/me/2fa/recovery-codes",
    { body: { code }, redirectOn401: false },
  );
  return r.recoveryCodes;
}

/** GET /api/v1/auth/2fa-policy (admin). */
export const getTwoFactorPolicy = (signal?: AbortSignal) =>
  request<TwoFactorPolicy>("GET", "/api/v1/auth/2fa-policy", { signal });

/** PUT /api/v1/auth/2fa-policy (admin). */
export const putTwoFactorPolicy = (require2faRoles: Role[]) =>
  request<TwoFactorPolicy>("PUT", "/api/v1/auth/2fa-policy", {
    body: { require2faRoles },
  });

/** DELETE /api/v1/users/{id}/2fa (admin). */
export const resetUserTwoFactor = (id: Id) =>
  request<void>("DELETE", `/api/v1/users/${id}/2fa`);
