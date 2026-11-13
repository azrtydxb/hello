// SCEP and 802.1X client (migration 00052, phone-feature-parity S-25): the
// Hello device CA, the certificates it issued, the reusable 802.1X profiles
// and each phone group's SCEP switch and wired profile. No response carries
// a key, a challenge or a password.

import { type Id, list, request } from "../api";

const seg = (value: Id) => encodeURIComponent(String(value));

/** The active device CA (never its key). */
export interface SCEPCA {
  id: Id;
  subject: string;
  notBefore: string;
  notAfter: string;
  fingerprintSha256: string;
  source: "generated" | "imported";
  certPem: string;
  createdAt: string;
}

/** GET /api/v1/scep: the server switch, the CA and its public URLs. */
export interface SCEPStatus {
  /** HELLO_SCEP: the endpoint answers (still only for groups with SCEP on). */
  serverEnabled: boolean;
  ca: SCEPCA | null;
  caUrl: string;
  crlUrl: string;
}

/** PUT /api/v1/scep/ca: generate a CA, or import a certificate and RSA key. */
export type SCEPCAInput =
  | {
      mode: "generate";
      commonName?: string;
      validityYears?: number;
      replace?: boolean;
    }
  | { mode: "import"; certPem: string; keyPem: string; replace?: boolean };

/** The state of an issued certificate. */
export type CertStatus = "active" | "expired" | "revoked";

/** One certificate the device CA issued. */
export interface SCEPCertificate {
  id: Id;
  caId: Id;
  phoneId: Id | null;
  mac: string;
  serial: string;
  subject: string;
  kind: "enrol" | "renewal";
  notBefore: string;
  notAfter: string;
  issuedAt: string;
  revokedAt: string | null;
  revokeReason: string | null;
  status: CertStatus;
}

/** The CRL reasons an administrator may give. */
export type RevokeReason =
  "unspecified" | "key_compromise" | "superseded" | "cessation_of_operation";

/** The EAP methods an 802.1X profile may use. */
export type Dot1XMethod = "eap-tls" | "peap" | "eap-md5";

export const DOT1X_METHODS: { value: Dot1XMethod; label: string }[] = [
  { value: "eap-tls", label: "EAP-TLS (certificate from SCEP)" },
  { value: "peap", label: "PEAP (MSCHAPv2)" },
  { value: "eap-md5", label: "EAP-MD5 (wired only)" },
];

/** An 802.1X profile; the password is write-only (hasPassword). */
export interface Dot1XProfile {
  id: Id;
  name: string;
  method: Dot1XMethod;
  identity: string;
  anonymousIdentity: string;
  hasPassword: boolean;
  caCertPem: string;
  serverDomain: string;
  usedByGroups: Id[];
  /** The enterprise Wi-Fi profiles that authenticate with it. */
  usedByWifiProfiles: Id[];
  updatedAt: string;
}

/** An 802.1X profile create or replace. */
export interface Dot1XProfileInput {
  name: string;
  method: Dot1XMethod;
  identity: string;
  anonymousIdentity: string;
  /** Left out keeps the stored password; "" removes it. */
  password?: string;
  caCertPem: string;
  serverDomain: string;
}

/** A phone group's SCEP switch, certificate lifetime and wired profile. */
export interface GroupSecurity {
  groupId: Id;
  scepEnabled: boolean;
  certValidityDays: number;
  wiredDot1xProfileId: Id | null;
}

export const getSCEP = (signal?: AbortSignal) =>
  request<SCEPStatus>("GET", "/api/v1/scep", { signal });
export const setSCEPCA = (body: SCEPCAInput) =>
  request<SCEPCA>("PUT", "/api/v1/scep/ca", { body });

export const listCertificates = (
  filter: { phoneId?: Id; status?: CertStatus } = {},
  signal?: AbortSignal,
) => {
  const q = new URLSearchParams();
  if (filter.phoneId !== undefined) q.set("phoneId", String(filter.phoneId));
  if (filter.status) q.set("status", filter.status);
  const qs = q.toString();
  return list<SCEPCertificate>(
    `/api/v1/scep/certificates${qs ? `?${qs}` : ""}`,
    signal,
  );
};
export const revokeCertificate = (id: Id, reason: RevokeReason) =>
  request<SCEPCertificate>(
    "POST",
    `/api/v1/scep/certificates/${seg(id)}/revoke`,
    { body: { reason } },
  );
export const rotateSCEPChallenge = (phoneId: Id) =>
  request<void>("POST", `/api/v1/phones/${seg(phoneId)}/scep-challenge`);

export const listDot1XProfiles = (signal?: AbortSignal) =>
  list<Dot1XProfile>("/api/v1/dot1x-profiles", signal);
export const createDot1XProfile = (body: Dot1XProfileInput) =>
  request<Dot1XProfile>("POST", "/api/v1/dot1x-profiles", { body });
export const updateDot1XProfile = (id: Id, body: Dot1XProfileInput) =>
  request<Dot1XProfile>("PUT", `/api/v1/dot1x-profiles/${seg(id)}`, { body });
export const deleteDot1XProfile = (id: Id) =>
  request<void>("DELETE", `/api/v1/dot1x-profiles/${seg(id)}`);

export const getGroupSecurity = (groupId: Id, signal?: AbortSignal) =>
  request<GroupSecurity>(
    "GET",
    `/api/v1/phone-groups/${seg(groupId)}/security`,
    { signal },
  );
export const setGroupSecurity = (
  groupId: Id,
  body: Omit<GroupSecurity, "groupId">,
) =>
  request<GroupSecurity>(
    "PUT",
    `/api/v1/phone-groups/${seg(groupId)}/security`,
    { body },
  );
