// Setting overrides of phone groups and phones (phone-feature-parity S-12):
// vendor keys on top of the template, template then group then phone.

import { request, type Id } from "../api";

const seg = (value: Id) => encodeURIComponent(String(value));

/** What a secret value reads back as, and what keeps the stored one. */
export const SECRET_MASK = "********";

export type OverrideScope = "phones" | "phone-groups";

export interface SettingOverride {
  key: string;
  value: string;
  secret: boolean;
  updatedAt?: string;
}

/** One row of a replacement; a secret with no value keeps the stored one. */
export interface SettingOverrideInput {
  key: string;
  value?: string | null;
  secret?: boolean;
}

const path = (scope: OverrideScope, id: Id) =>
  `/api/v1/${scope}/${seg(id)}/settings`;

export const listSettingOverrides = (
  scope: OverrideScope,
  id: Id,
  signal?: AbortSignal,
) =>
  request<{ items: SettingOverride[] }>("GET", path(scope, id), {
    signal,
  }).then((r) => r.items);

export const putSettingOverrides = (
  scope: OverrideScope,
  id: Id,
  overrides: SettingOverrideInput[],
) =>
  request<{ items: SettingOverride[] }>("PUT", path(scope, id), {
    body: { overrides },
  }).then((r) => r.items);

/**
 * The key formats the server accepts: a Grandstream P-value (P71) or a
 * Yealink dotted key (features.text_message.enable).
 */
export const OVERRIDE_KEY = /^(P[0-9]{1,6}|[a-z][a-z0-9_]*(\.[a-z0-9_]+)+)$/;
