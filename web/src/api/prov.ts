// Phone provisioning API client for the console's Phones pages: the HTTP
// JSON of plan contract 7 (.procoder/plans/phone-auto-provisioning-service.md).

import {
  list,
  listItems,
  request,
  requestText,
  responseJson,
  type FieldError,
  type Id,
} from "../api";

const seg = (value: Id) => encodeURIComponent(String(value));

// --- shared vocabulary ----------------------------------------------------------

/** The vendors Hello provisions; `generic` is any other brand. */
export const VENDORS = [
  "yealink",
  "poly",
  "grandstream",
  "snom",
  "fanvil",
  "generic",
] as const;

export type Vendor = (typeof VENDORS)[number];

export const VENDOR_LABEL: Record<Vendor, string> = {
  yealink: "Yealink",
  poly: "Poly",
  grandstream: "Grandstream",
  snom: "Snom",
  fanvil: "Fanvil",
  generic: "Generic",
};

/** A vendor's display name; an unknown value is shown as sent. */
export const vendorLabel = (v: string) =>
  (VENDOR_LABEL as Record<string, string>)[v] ?? v;

/** A phone's redirect registration state (spec S-11). */
export type RedirectState =
  "not_configured" | "manual" | "pending" | "registered" | "failed";

/** A phone's redirect status; `reason` says why it failed ("drift", …). */
export interface RedirectStatus {
  state: RedirectState | string;
  reason?: string;
  at?: string;
}

// --- phones ------------------------------------------------------------------------

/** A phone as returned. Fetch-state fields are null until the first fetch. */
export interface Phone {
  id: Id;
  mac: string;
  serial: string;
  vendor: Vendor | string;
  model: string;
  label: string;
  /** null while unbound (an unbound phone cannot be enabled). */
  deviceId: Id | null;
  extensionId: Id | null;
  extensionNumber: string;
  /** The template override; null resolves by vendor and model. */
  templateId: Id | null;
  /** BLF keys: extension numbers, in key order. */
  blf: string[];
  enabled: boolean;
  tokenExposed: boolean;
  uaMismatch: boolean;
  bootArmed: boolean;
  bootReclaimed: boolean;
  redirectStatus: RedirectStatus;
  firstFetchAt: string | null;
  lastFetchAt: string | null;
  lastFetchIp: string | null;
  lastFetchUa: string | null;
  lastFetchFile: string | null;
  firmwareSeen: string | null;
  /** True while the phone's latest fetch was a render error. */
  renderError: boolean;
  createdAt: string;
  updatedAt: string;
}

/** Create, rotate-token and re-arm responses: the URL is shown once. */
export interface IssuedPhone extends Phone {
  provisioningUrl: string;
  /** Create with an existing device: its secret was rotated. */
  secretRotated?: boolean;
}

/** POST /api/v1/phones body. Without `deviceId` a device is created. */
export interface PhoneInput {
  mac: string;
  serial?: string;
  vendor: Vendor;
  model: string;
  label: string;
  extensionId: Id;
  deviceId?: Id;
  blf: string[];
  enabled: boolean;
}

/** PATCH /api/v1/phones/{id} body; fetch state is not writable. */
export type PhonePatch = Partial<
  Omit<PhoneInput, "mac"> & { templateId: Id | null }
>;

/** One provisioning request a phone made (prov_fetches, token redacted). */
export interface Fetch {
  at: string;
  ip: string;
  userAgent: string;
  /** The path with the token redacted (the token shown as four asterisks). */
  path: string;
  kind: string;
  result: string;
  status: number;
  bytes: number;
}

/** One page of GET /api/v1/phones/{id}/fetches, newest first. */
export interface FetchPage {
  items: Fetch[];
  /** Cursor for the next (older) page; empty when there is none. */
  next: string;
}

export const listPhones = (signal?: AbortSignal) =>
  list<Phone>("/api/v1/phones", signal);

export const getPhone = (phoneId: Id, signal?: AbortSignal) =>
  request<Phone>("GET", `/api/v1/phones/${seg(phoneId)}`, { signal });

export const createPhone = (input: PhoneInput) =>
  request<IssuedPhone>("POST", "/api/v1/phones", { body: input });

export const updatePhone = (phoneId: Id, patch: PhonePatch) =>
  request<Phone>("PATCH", `/api/v1/phones/${seg(phoneId)}`, { body: patch });

export const deletePhone = (phoneId: Id) =>
  request<void>("DELETE", `/api/v1/phones/${seg(phoneId)}`);

/**
 * POST /api/v1/phones/{id}/rotate-token. Rolling by default (the old token
 * works until the phone's first fetch with the new one or the grace ends);
 * `immediate` revokes the old token at once.
 */
export const rotatePhoneToken = (phoneId: Id, immediate = false) =>
  request<IssuedPhone>(
    "POST",
    `/api/v1/phones/${seg(phoneId)}/rotate-token${immediate ? "?immediate=true" : ""}`,
  );

/** POST /api/v1/phones/{id}/rearm: immediate token rotation plus one more boot hand-off. */
export const rearmPhone = (phoneId: Id) =>
  request<IssuedPhone>("POST", `/api/v1/phones/${seg(phoneId)}/rearm`);

/** POST /api/v1/phones/{id}/admin-password/reveal (audited). */
export const revealAdminPassword = (phoneId: Id) =>
  request<{ adminPassword: string }>(
    "POST",
    `/api/v1/phones/${seg(phoneId)}/admin-password/reveal`,
  );

/** POST /api/v1/phones/{id}/admin-password/rotate: 204; the phone picks it up next fetch. */
export const rotateAdminPassword = (phoneId: Id) =>
  request<void>("POST", `/api/v1/phones/${seg(phoneId)}/admin-password/rotate`);

/** GET /api/v1/phones/{id}/fetches?before=&limit=, newest first. */
export async function listPhoneFetches(
  phoneId: Id,
  opts: { before?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<FetchPage> {
  const q = new URLSearchParams();
  if (opts.before) q.set("before", opts.before);
  if (opts.limit !== undefined) q.set("limit", String(opts.limit));
  const qs = q.toString();
  const path = `/api/v1/phones/${seg(phoneId)}/fetches${qs ? `?${qs}` : ""}`;
  const body = await request<unknown>("GET", path, { signal });
  const page = listItems<Fetch>(body, path);
  const next = (body as Record<string, unknown>).next;
  return { items: page, next: typeof next === "string" ? next : "" };
}

/**
 * GET /api/v1/phones/{id}/preview?file=: the file exactly as the phone would
 * get it, with the SIP secret, admin password and token masked by the server.
 */
export const previewPhoneFile = (
  phoneId: Id,
  file: string,
  signal?: AbortSignal,
) =>
  requestText(
    `/api/v1/phones/${seg(phoneId)}/preview?${new URLSearchParams({ file })}`,
    signal,
  );

/** One row of a CSV import report. */
export interface ImportRow {
  line: number;
  mac: string;
  /** Why the row cannot be imported; empty when it can. */
  errors: (string | { message: string })[];
}

/** POST /api/v1/phones/import response. */
export interface ImportReport {
  rows: ImportRow[];
  ok: boolean;
  /** Apply only: how many phones were created. */
  created?: number;
}

/**
 * POST /api/v1/phones/import?dryRun=: the CSV as text/csv. A dry run reports
 * per-row errors; an apply commits every row in one transaction or none.
 */
export const importPhones = (csv: string, dryRun: boolean) =>
  request<ImportReport>(
    "POST",
    `/api/v1/phones/import?dryRun=${dryRun ? "true" : "false"}`,
    { rawBody: new Blob([csv], { type: "text/csv" }) },
  );

/** An import row error as text. */
export const importErrorText = (e: ImportRow["errors"][number]) =>
  typeof e === "string" ? e : e.message;

// --- templates -----------------------------------------------------------------------

/** One file of a template: a name pattern ({mac}, {MAC}, {model}) and a Go text/template body. */
export interface TemplateFile {
  pattern: string;
  contentType: string;
  body: string;
}

/** A template as returned; built-ins are read-only (`builtin: true`). */
export interface Template {
  id: Id;
  vendor: Vendor | string;
  modelGlob: string;
  priority: number;
  name: string;
  files: TemplateFile[];
  builtin: boolean;
  /** The built-in's own name, or the built-in a copy was made from. */
  builtinRef: string;
  version: number;
  updatedAt: string;
}

/** The writable fields of a template. */
export interface TemplateInput {
  vendor: Vendor;
  modelGlob: string;
  priority: number;
  name: string;
  files: TemplateFile[];
}

/** A template validation failure: `path` such as "files[1].body", with the body line. */
export type TemplateFieldError = FieldError;

export const listTemplates = (signal?: AbortSignal) =>
  list<Template>("/api/v1/prov/templates", signal);

export const getTemplate = (templateId: Id, signal?: AbortSignal) =>
  request<Template>("GET", `/api/v1/prov/templates/${seg(templateId)}`, {
    signal,
  });

export const createTemplate = (input: TemplateInput) =>
  request<Template>("POST", "/api/v1/prov/templates", { body: input });

export const updateTemplate = (templateId: Id, patch: Partial<TemplateInput>) =>
  request<Template>("PATCH", `/api/v1/prov/templates/${seg(templateId)}`, {
    body: patch,
  });

/** DELETE a copy; the built-in it copied applies again. */
export const deleteTemplate = (templateId: Id) =>
  request<void>("DELETE", `/api/v1/prov/templates/${seg(templateId)}`);

/** POST /api/v1/prov/templates/{id}/copy: an editable copy at a higher priority. */
export const copyTemplate = (templateId: Id) =>
  request<Template>("POST", `/api/v1/prov/templates/${seg(templateId)}/copy`);

/**
 * POST /api/v1/prov/templates/validate: resolves when the template is valid;
 * a 400 rejects with ApiError whose `fields` carry the failing file and line.
 */
export const validateTemplate = (input: TemplateInput) =>
  request<unknown>("POST", "/api/v1/prov/templates/validate", { body: input });

/** The variables a template can use (spec S-8), for the editor's reference. */
export const TEMPLATE_VARIABLES: readonly { name: string; doc: string }[] = [
  { name: ".Phone.MAC", doc: "12 lowercase hex digits" },
  { name: ".Phone.MACUpper", doc: "the MAC, uppercase" },
  { name: ".Phone.Vendor", doc: "the phone's vendor" },
  { name: ".Phone.Model", doc: "model, e.g. T54W" },
  { name: ".Phone.Label", doc: "the phone's label" },
  { name: ".Phone.AdminPassword", doc: "web UI password (masked in previews)" },
  { name: ".Line.Username", doc: "SIP username" },
  { name: ".Line.AuthName", doc: "SIP auth name" },
  { name: ".Line.Password", doc: "SIP secret (masked in previews)" },
  { name: ".Line.DisplayName", doc: "the extension's name" },
  { name: ".Line.Label", doc: "the extension number" },
  { name: ".Line.Domain", doc: "SIP domain" },
  { name: ".Line.VoicemailCode", doc: "message key feature code" },
  { name: ".Server.Host", doc: "registrar host" },
  { name: ".Server.Port", doc: "registrar port" },
  { name: ".Server.Transport", doc: "always udp" },
  { name: ".Server.Expiry", doc: "registration expiry, seconds" },
  { name: ".BLF", doc: "list of {Number, Label, URI}" },
  { name: ".Prov.URL", doc: "the phone's own URL (masked in previews)" },
  { name: ".Prov.CAURL", doc: "plain-HTTP CA certificate URL" },
  { name: ".Prov.ResyncSeconds", doc: "re-check interval" },
  { name: ".Firmware", doc: "{URL, Version}, or nil when none is pinned" },
  { name: ".Time.Zone", doc: "time zone" },
  { name: ".Time.NTP", doc: "NTP server" },
];

/** The template functions besides Go's built-ins. */
export const TEMPLATE_FUNCTIONS: readonly { name: string; doc: string }[] = [
  { name: "xml", doc: "escape for XML" },
  { name: "upper", doc: "uppercase" },
  { name: "lower", doc: "lowercase" },
  { name: "default", doc: 'default "x" .Value — x when the value is empty' },
];

/** A template file pattern with a phone's MAC and model filled in. */
export function fileNameFor(
  pattern: string,
  phone: { mac: string; model: string },
): string {
  return pattern
    .replaceAll("{mac}", phone.mac.toLowerCase())
    .replaceAll("{MAC}", phone.mac.toUpperCase())
    .replaceAll("{model}", phone.model);
}

// --- firmware ------------------------------------------------------------------------

/** An uploaded firmware file. */
export interface Firmware {
  id: Id;
  vendor: Vendor | string;
  modelGlob: string;
  version: string;
  filename: string;
  size: number;
  sha256: string;
  uploadedAt: string;
  /** Pinned for its vendor and model glob. */
  pinned: boolean;
}

/** One firmware pin: the version a vendor's model glob upgrades to. */
export interface FirmwarePin {
  vendor: string;
  modelGlob: string;
  firmwareId: Id;
}

/** The largest firmware file the server accepts (spec S-13). */
export const MAX_FIRMWARE_BYTES = 512 * 1024 * 1024;

export const listFirmware = (signal?: AbortSignal) =>
  list<Firmware>("/api/v1/prov/firmware", signal);

/** DELETE a firmware file; 409 while it is pinned. */
export const deleteFirmware = (firmwareId: Id) =>
  request<void>("DELETE", `/api/v1/prov/firmware/${seg(firmwareId)}`);

/** PUT /api/v1/prov/firmware/pins: the full set of pins, replacing what is stored. */
export const setFirmwarePins = (pins: FirmwarePin[]) =>
  request<void>("PUT", "/api/v1/prov/firmware/pins", { body: pins });

/** POST /api/v1/prov/firmware fields besides the file. */
export interface FirmwareUpload {
  vendor: Vendor;
  modelGlob: string;
  version: string;
  file: File;
}

/**
 * POST /api/v1/prov/firmware (multipart: vendor, modelGlob, version, file),
 * through XMLHttpRequest because fetch reports no upload progress.
 * `onProgress` gets 0..1; errors reject as ApiError, like `request`.
 */
export function uploadFirmware(
  input: FirmwareUpload,
  onProgress: (fraction: number) => void,
  signal?: AbortSignal,
): Promise<Firmware> {
  const form = new FormData();
  form.set("vendor", input.vendor);
  form.set("modelGlob", input.modelGlob);
  form.set("version", input.version);
  form.set("file", input.file, input.file.name);
  const path = "/api/v1/prov/firmware";
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", path);
    xhr.setRequestHeader("Accept", "application/json");
    xhr.withCredentials = true;
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable && e.total > 0) onProgress(e.loaded / e.total);
    };
    xhr.onload = () => {
      // Reuse request()'s envelope parsing and 401 handling on the reply.
      const reply = new Response(xhr.responseText || null, {
        status: xhr.status,
        headers: { "Content-Type": "application/json" },
      });
      responseJson<Firmware>(reply, "POST", path).then(resolve, reject);
    };
    xhr.onerror = () => reject(new Error(`POST ${path} failed: network error`));
    xhr.onabort = () =>
      reject(new DOMException("Upload aborted", "AbortError"));
    signal?.addEventListener("abort", () => xhr.abort(), { once: true });
    xhr.send(form);
  });
}

// --- redirect accounts -----------------------------------------------------------------

/** A vendor redirect account; credentials are write-only (`hasCredentials`). */
export interface RedirectAccount {
  vendor: Vendor | string;
  enabled: boolean;
  hasCredentials: boolean;
  /** Credentials come from the deployment's secret; read-only here. */
  fromDeployment: boolean;
  settings: Record<string, unknown>;
  lastCheckAt: string | null;
  lastCheckResult: string | null;
  /** False for vendors with no usable API (Poly, Fanvil): a manual step. */
  supported: boolean;
}

/** PUT /api/v1/prov/redirect/{vendor} body; `credentials` only when changing them. */
export interface RedirectAccountInput {
  enabled: boolean;
  settings: Record<string, unknown>;
  credentials?: Record<string, string>;
}

/** The credential keys per vendor (spec S-11 secret key names). */
export const REDIRECT_CREDENTIAL_KEYS: Partial<
  Record<Vendor, readonly { key: string; label: string; secret: boolean }[]>
> = {
  snom: [
    {
      key: "snomSrapsAccessKeyId",
      label: "SRAPS access key ID",
      secret: false,
    },
    {
      key: "snomSrapsAccessKeySecret",
      label: "SRAPS access key secret",
      secret: true,
    },
  ],
  yealink: [
    { key: "yealinkRpsAccessKey", label: "RPS access key", secret: false },
    { key: "yealinkRpsAccessSecret", label: "RPS access secret", secret: true },
    {
      key: "yealinkYmcsClientId",
      label: "YMCS client ID (optional)",
      secret: false,
    },
    {
      key: "yealinkYmcsClientSecret",
      label: "YMCS client secret (optional)",
      secret: true,
    },
    {
      key: "yealinkYmcsRegion",
      label: "YMCS region (optional)",
      secret: false,
    },
  ],
  grandstream: [
    { key: "gdmsClientId", label: "GDMS client ID", secret: false },
    { key: "gdmsClientSecret", label: "GDMS client secret", secret: true },
    { key: "gdmsUsername", label: "GDMS username", secret: false },
    { key: "gdmsPassword", label: "GDMS password", secret: true },
    { key: "gdmsRegion", label: "GDMS region", secret: false },
    { key: "gdmsSiteId", label: "GDMS site ID", secret: false },
  ],
};

export const listRedirectAccounts = (signal?: AbortSignal) =>
  list<RedirectAccount>("/api/v1/prov/redirect", signal);

export const saveRedirectAccount = (
  vendor: string,
  input: RedirectAccountInput,
) =>
  request<RedirectAccount>("PUT", `/api/v1/prov/redirect/${seg(vendor)}`, {
    body: input,
  });

export const deleteRedirectAccount = (vendor: string) =>
  request<void>("DELETE", `/api/v1/prov/redirect/${seg(vendor)}`);

/** POST /api/v1/prov/redirect/{vendor}/check: runs Check() with the stored credentials. */
export const checkRedirectAccount = (vendor: string) =>
  request<unknown>("POST", `/api/v1/prov/redirect/${seg(vendor)}/check`);

// --- settings ------------------------------------------------------------------------

/** One DHCP option value a vendor's phones read. */
export interface DhcpValue {
  vendor: string;
  /** "66", "160", "43", … */
  option: string;
  value: string;
}

/** GET /api/v1/prov/settings: computed URLs, DHCP values and the CA fingerprint. */
export interface ProvSettings {
  publicUrl: string;
  bootUrl: string;
  caUrl: string;
  caSha256: string;
  dhcp: DhcpValue[];
  sipServer: string;
}

export const getProvSettings = (signal?: AbortSignal) =>
  request<ProvSettings>("GET", "/api/v1/prov/settings", { signal });
