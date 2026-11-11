// Locations client (migration 00080): the sites, floors and rooms phones are
// in, what they are assigned to, the E911 switch and alarm destination of a
// phone group, and the emergency settings.

import { list, request } from "../api";

export type LocationKind = "site" | "floor" | "room";

export interface LocationFields {
  kind: LocationKind;
  name: string;
  /** The site of a floor, the floor or site of a room; null for a site. */
  parentId: number | null;
  /** ISO 3166 alpha-2, capitals, or "". A site only. */
  country: string;
  state: string;
  city: string;
  street: string;
  houseNumber: string;
  postalCode: string;
  latitude: number | null;
  longitude: number | null;
  radiusM: number | null;
}

export interface Location extends LocationFields {
  id: number;
  createdAt: string;
  updatedAt: string;
}

export const listLocations = (signal?: AbortSignal) =>
  list<Location>("/api/v1/locations", signal);
export const createLocation = (body: LocationFields) =>
  request<Location>("POST", "/api/v1/locations", { body });
export const putLocation = (id: number, body: LocationFields) =>
  request<Location>("PUT", `/api/v1/locations/${String(id)}`, { body });
export const deleteLocation = (id: number) =>
  request<void>("DELETE", `/api/v1/locations/${String(id)}`);

export type AssignmentKind = "group" | "extension" | "phone";

export interface LocationAssignment {
  kind: AssignmentKind;
  id: number;
  name: string;
  locationId: number | null;
}

export const listLocationAssignments = (signal?: AbortSignal) =>
  list<LocationAssignment>("/api/v1/location-assignments", signal);
export const putLocationAssignment = (
  kind: AssignmentKind,
  id: number,
  locationId: number | null,
) =>
  request<void>("PUT", `/api/v1/location-assignments/${kind}/${String(id)}`, {
    body: { locationId },
  });

export interface PhoneGroupEmergency {
  e911Enabled: boolean;
  /** The extension or ring group number *55 rings; "" for none. */
  alarmTarget: string;
}

export const getPhoneGroupEmergency = (id: number, signal?: AbortSignal) =>
  request<PhoneGroupEmergency>(
    "GET",
    `/api/v1/phone-groups/${String(id)}/emergency`,
    { signal },
  );
export const putPhoneGroupEmergency = (id: number, body: PhoneGroupEmergency) =>
  request<PhoneGroupEmergency>(
    "PUT",
    `/api/v1/phone-groups/${String(id)}/emergency`,
    { body },
  );

export interface EmergencySettings {
  /** The ring group told of every emergency call; "" for none. */
  notifyGroup: string;
}

export const getEmergencySettings = (signal?: AbortSignal) =>
  request<EmergencySettings>("GET", "/api/v1/emergency", { signal });
export const putEmergencySettings = (body: EmergencySettings) =>
  request<EmergencySettings>("PUT", "/api/v1/emergency", { body });
