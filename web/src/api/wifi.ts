// Wi-Fi profiles and roaming client (migration 00074): the networks Wi-Fi
// handsets join, assigned to phone groups and phones. A pre-shared key and an
// 802.1X password are write-only: the API reports only that one is stored.

import { list, request, type Id } from "../api";

const seg = (value: Id) => encodeURIComponent(String(value));

export const WIFI_SECURITIES = [
  { value: "open", label: "Open" },
  { value: "wpa2-psk", label: "WPA2 personal (PSK)" },
  { value: "wpa3-sae", label: "WPA3 personal (SAE)" },
  { value: "owe", label: "Enhanced Open (OWE)" },
  { value: "wpa2-enterprise", label: "WPA2 enterprise (802.1X)" },
  { value: "wpa3-enterprise", label: "WPA3 enterprise (802.1X)" },
] as const;

export type WifiSecurity = (typeof WIFI_SECURITIES)[number]["value"];
export type WifiCACert = "system" | "tofu" | "skip";

export const usesPSK = (s: string) => s === "wpa2-psk" || s === "wpa3-sae";
export const isEnterprise = (s: string) =>
  s === "wpa2-enterprise" || s === "wpa3-enterprise";

export interface WifiProfile {
  id: Id;
  name: string;
  ssid: string;
  security: WifiSecurity;
  /** A key is stored; it is never returned. */
  pskSet: boolean;
  eapMethod: string;
  identity: string;
  anonymousIdentity: string;
  passwordSet: boolean;
  caCert: WifiCACert;
  priority: number;
  hidden: boolean;
  preferAccount: number;
  verified: boolean;
  groups: number;
  phones: number;
  updatedAt: string;
}

/** On create name, ssid and security are required; psk and password are
 * kept when left out and cleared when sent empty. */
export interface WifiProfileInput {
  name?: string;
  ssid?: string;
  security?: WifiSecurity;
  psk?: string;
  eapMethod?: string;
  identity?: string;
  anonymousIdentity?: string;
  password?: string;
  caCert?: WifiCACert;
  priority?: number;
  hidden?: boolean;
  preferAccount?: number;
  verified?: boolean;
}

/** A field that is absent is not written to the phones. */
export interface WifiRadio {
  band?: "both" | "2.4" | "5";
  roamMode?: "default" | "boost" | "fast" | "custom";
  threshold?: number;
  poorScan?: number;
  goodScan?: number;
  gain?: number;
  dot11r?: boolean;
  dot11v?: boolean;
  prompt?: boolean;
}

export interface WifiScope {
  profileIds: Id[];
  inheritedProfileIds: Id[];
  /** A verified profile is among them, so the networks are written. */
  pushing: boolean;
  radio?: WifiRadio;
}

export const listWifiProfiles = (signal?: AbortSignal) =>
  list<WifiProfile>("/api/v1/wifi-profiles", signal);
export const createWifiProfile = (body: WifiProfileInput) =>
  request<WifiProfile>("POST", "/api/v1/wifi-profiles", { body });
export const updateWifiProfile = (id: Id, body: WifiProfileInput) =>
  request<WifiProfile>("PATCH", `/api/v1/wifi-profiles/${seg(id)}`, { body });
export const deleteWifiProfile = (id: Id) =>
  request<void>("DELETE", `/api/v1/wifi-profiles/${seg(id)}`);

export const getGroupWifi = (id: Id, signal?: AbortSignal) =>
  request<WifiScope>("GET", `/api/v1/phone-groups/${seg(id)}/wifi`, { signal });
export const putGroupWifi = (id: Id, profileIds: Id[], radio: WifiRadio) =>
  request<WifiScope>("PUT", `/api/v1/phone-groups/${seg(id)}/wifi`, {
    body: { profileIds, radio },
  });
export const getPhoneWifi = (id: Id, signal?: AbortSignal) =>
  request<WifiScope>("GET", `/api/v1/phones/${seg(id)}/wifi`, { signal });
export const putPhoneWifi = (id: Id, profileIds: Id[]) =>
  request<WifiScope>("PUT", `/api/v1/phones/${seg(id)}/wifi`, {
    body: { profileIds },
  });
