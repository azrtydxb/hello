// Client for the system email (SMTP) settings (admin).
import { request } from "../api";

export type SmtpSecurity = "none" | "starttls" | "tls";

/** GET /api/v1/settings/smtp: the password is never returned. */
export interface SmtpSettings {
  host: string;
  port: number;
  security: SmtpSecurity | "opportunistic";
  username: string;
  from: string;
  /** Whether a password is stored. */
  passwordSet: boolean;
  /** Where the settings in force come from. */
  source: "database" | "environment" | "none";
  updatedAt: string | null;
}

/** PUT /api/v1/settings/smtp body. */
export interface SmtpInput {
  host: string;
  port: number;
  security: SmtpSecurity;
  username: string;
  from: string;
  /** Omit to keep the stored password; "" clears it. */
  password?: string;
}

export function getSmtp(signal?: AbortSignal): Promise<SmtpSettings> {
  return request<SmtpSettings>("GET", "/api/v1/settings/smtp", { signal });
}

export function putSmtp(input: SmtpInput): Promise<SmtpSettings> {
  return request<SmtpSettings>("PUT", "/api/v1/settings/smtp", {
    body: input,
  });
}

export function sendTestEmail(to: string): Promise<void> {
  return request<void>("POST", "/api/v1/settings/smtp/test", {
    body: { to },
  });
}
