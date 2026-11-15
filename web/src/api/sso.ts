// Single sign-on (docs/integrations.md, "Single sign-on"): the providers on
// the login page, the administrator settings, and the identities linked to
// users.
import { request } from "../api";

export type SSOProviderName = "google" | "entra";

/** A provider as the login page shows it. */
export interface PublicProvider {
  provider: SSOProviderName;
  name: string;
}

export interface SSOProvider {
  provider: SSOProviderName;
  enabled: boolean;
  clientId: string;
  /** A client secret is stored; it is never returned. */
  hasClientSecret: boolean;
  /** Entra: the directory (tenant) id. */
  tenant: string;
  /** Google: the Workspace domains. */
  domains: string[];
  jit: boolean;
  updatedAt: string;
}

export interface BreakGlassUser {
  id: number;
  username: string;
  enabled: boolean;
}

export interface SSOSettings {
  providers: SSOProvider[];
  ssoRequired: boolean;
  breakGlass: BreakGlassUser[];
  redirectUris: Record<string, string>;
  available: boolean;
}

export interface SSOProviderInput {
  provider: SSOProviderName;
  enabled?: boolean;
  clientId?: string;
  clientSecret?: string;
  tenant?: string;
  domains?: string[];
  jit?: boolean;
}

export interface SSOTestResult {
  ok: boolean;
  issuer: string;
  keys: number;
  errorDetail: string;
}

export interface Identity {
  id: number;
  userId: number;
  provider: SSOProviderName;
  subject: string;
  tenant: string;
  email: string;
  createdAt: string;
  lastLoginAt: string | null;
}

/** The enabled providers, for the login page (public). */
export async function listSSOProviders(): Promise<PublicProvider[]> {
  const r = await request<{ items: PublicProvider[] }>(
    "GET",
    "/api/v1/auth/oidc/providers",
    { redirectOn401: false },
  );
  return r.items;
}

/** Where the login page's button sends the browser. */
export function ssoStartURL(provider: SSOProviderName, next: string): string {
  const q = next && next !== "/" ? `?next=${encodeURIComponent(next)}` : "";
  return `/api/v1/auth/oidc/${provider}/start${q}`;
}

export const getSSOSettings = () =>
  request<SSOSettings>("GET", "/api/v1/auth/sso");

export const putSSOSettings = (body: {
  providers?: SSOProviderInput[];
  ssoRequired?: boolean;
}) => request<SSOSettings>("PUT", "/api/v1/auth/sso", { body });

export const testSSOProvider = (p: SSOProviderName) =>
  request<SSOTestResult>("POST", `/api/v1/auth/sso/${p}/test`);

export async function listMyIdentities(): Promise<Identity[]> {
  const r = await request<{ items: Identity[] }>(
    "GET",
    "/api/v1/me/identities",
  );
  return r.items;
}

/** Starts linking an identity: the URL to send the browser to. */
export const linkMyIdentity = (p: SSOProviderName) =>
  request<{ url: string }>("POST", `/api/v1/me/identities/${p}/link`);

export const unlinkMyIdentity = (p: SSOProviderName) =>
  request<undefined>("DELETE", `/api/v1/me/identities/${p}`);

export async function listUserIdentities(id: number): Promise<Identity[]> {
  const r = await request<{ items: Identity[] }>(
    "GET",
    `/api/v1/users/${String(id)}/identities`,
  );
  return r.items;
}

export const unlinkUserIdentity = (id: number, p: SSOProviderName) =>
  request<undefined>("DELETE", `/api/v1/users/${String(id)}/identities/${p}`);

/** The login page's words for an sso_error code. */
export function ssoErrorText(code: string): string {
  switch (code) {
    case "unavailable":
      return "Single sign-on is not available right now. Try again later or ask an administrator.";
    case "throttled":
      return "Too many sign-in attempts. Wait a minute and try again.";
    case "retry":
      return "The sign-in did not complete. Start it again from this page.";
    default:
      return "Single sign-on did not sign you in. If this keeps happening, ask an administrator to check your account.";
  }
}
