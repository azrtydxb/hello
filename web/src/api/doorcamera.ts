// Client for the door station cameras (spec phone-feature-parity S-49).
import { request, type Id } from "../api";

const seg = (value: Id) => encodeURIComponent(String(value));

/** How a camera gives its picture. */
export type CameraKind = "mjpeg" | "snapshot";

/** A station's camera. The password is never part of it. */
export interface DoorCamera {
  configured: boolean;
  kind: CameraKind | "";
  url: string;
  username: string;
  passwordSet: boolean;
}

export const getDoorCamera = (id: Id, signal?: AbortSignal) =>
  request<DoorCamera>("GET", `/api/v1/extensions/${seg(id)}/camera`, {
    signal,
  });

/** `password` is write-only: absent keeps the stored one, "" clears it. */
export const putDoorCamera = (
  id: Id,
  camera: {
    kind: CameraKind;
    url: string;
    username: string;
    password?: string;
  },
) =>
  request<DoorCamera>("PUT", `/api/v1/extensions/${seg(id)}/camera`, {
    body: camera,
  });

export const deleteDoorCamera = (id: Id) =>
  request<void>("DELETE", `/api/v1/extensions/${seg(id)}/camera`);

/** The address of the proxied picture, for an <img>. */
export const doorCameraViewUrl = (id: Id, tick = 0) =>
  `/api/v1/extensions/${seg(id)}/camera/view${tick ? `?t=${String(tick)}` : ""}`;
