import { useEffect, useState } from "react";
import { fetchVersion } from "./api";

/** Whether the control plane answers, and its version when it does. */
export type ControlPlane =
  | { status: "checking" }
  | { status: "reachable"; version: string }
  | { status: "unreachable" };

/** How often the reachability check repeats. */
export const CONTROL_PLANE_POLL_MS = 30_000;

/**
 * Polls the public GET /api/v1/version (no session needed): a successful
 * answer means the control plane is reachable and gives its version.
 */
export function useControlPlane(): ControlPlane {
  const [state, setState] = useState<ControlPlane>({ status: "checking" });

  useEffect(() => {
    let controller = new AbortController();
    const check = () => {
      controller.abort();
      controller = new AbortController();
      const { signal } = controller;
      fetchVersion(signal)
        .then((info) =>
          setState({ status: "reachable", version: info.version }),
        )
        .catch(() => {
          if (!signal.aborted) setState({ status: "unreachable" });
        });
    };
    check();
    const timer = setInterval(check, CONTROL_PLANE_POLL_MS);
    return () => {
      clearInterval(timer);
      controller.abort();
    };
  }, []);

  return state;
}
