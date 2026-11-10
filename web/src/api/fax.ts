// Fax client (migration 00076, docs/fax.md): received and sent faxes, the PDF
// download and the fax destinations routes and IVR keys name.

import { list, request, type Id } from "../api";

const seg = (value: Id) => encodeURIComponent(String(value));

export type FaxDirection = "inbound" | "outbound";

export type FaxStatus =
  | "queued"
  | "sending"
  | "receiving"
  | "received"
  | "sent"
  | "partial"
  | "failed"
  | "cancelled";

export type FaxMailStatus = "none" | "pending" | "sending" | "sent" | "failed";

/** GET /api/v1/faxes. */
export interface Fax {
  id: Id;
  direction: FaxDirection;
  status: FaxStatus;
  peerNumber: string;
  localNumber: string;
  destination: string;
  ownerExtension: string;
  pages: number;
  mode: "" | "g711" | "t38";
  bitRate: number;
  resultCode: number | null;
  resultText: string;
  hasPdf: boolean;
  correlationId: string;
  createdBy: string;
  createdAt: string;
  finishedAt: string | null;
  attempts: number;
  nextAttemptAt: string | null;
  mailTo: string;
  mailStatus: FaxMailStatus;
}

export interface FaxFilter {
  direction?: FaxDirection | "";
  status?: FaxStatus | "";
}

export const listFaxes = (
  filter: FaxFilter,
  signal?: AbortSignal,
  mine = false,
) => {
  const q = new URLSearchParams();
  if (filter.direction) q.set("direction", filter.direction);
  if (filter.status) q.set("status", filter.status);
  const qs = q.toString();
  const base = mine ? "/api/v1/me/faxes" : "/api/v1/faxes";
  return list<Fax>(`${base}${qs ? `?${qs}` : ""}`, signal);
};

/** The PDF route; a plain link downloads it with the session cookie. */
export const faxPdfPath = (faxId: Id, mine = false) =>
  `${mine ? "/api/v1/me/faxes" : "/api/v1/faxes"}/${seg(faxId)}/pdf`;

export const deleteFax = (faxId: Id) =>
  request<void>("DELETE", `/api/v1/faxes/${seg(faxId)}`);

/** POST /api/v1/faxes: multipart `file`, `to` and an optional `callerId`. */
export function sendFax(
  file: File,
  to: string,
  callerId: string,
): Promise<Fax> {
  const form = new FormData();
  form.set("to", to);
  if (callerId) form.set("callerId", callerId);
  form.set("file", file, file.name);
  return request<Fax>("POST", "/api/v1/faxes", { rawBody: form });
}

/** A fax destination: the owner extension and an optional email address. */
export interface FaxDestination {
  id: Id;
  name: string;
  extension: string;
  email: string;
  createdAt: string;
  updatedAt: string;
}

export const listFaxDestinations = (signal?: AbortSignal) =>
  list<FaxDestination>("/api/v1/fax-destinations", signal);

export const createFaxDestination = (body: {
  name: string;
  extension: string;
  email: string;
}) => request<FaxDestination>("POST", "/api/v1/fax-destinations", { body });

export const deleteFaxDestination = (destinationId: Id) =>
  request<void>("DELETE", `/api/v1/fax-destinations/${seg(destinationId)}`);

/** The largest PDF the API takes (10 MB). */
export const MAX_FAX_BYTES = 10 << 20;
