// The sidebar's degraded badges, from the Platform pages' health rules.
import type { ReactNode } from "react";
import { getCluster, listTrunkStatus } from "../../api";
import { usePolling } from "../../usePolling";
import { clusterIssues, trunkIssues } from "./health";
import "./platform.css";

/** How often the sidebar re-reads cluster and trunk health. */
export const BADGE_REFRESH_MS = 15_000;

/** A load that never answers: the poll stays in its first state. */
const idle = () => new Promise<never>(() => undefined);

/** The sidebar badge: the count, read out as "N issues". */
function issueBadge(n: number): ReactNode {
  if (n <= 0) return undefined;
  return (
    <>
      <span aria-hidden="true">{n}</span>
      <span className="visually-hidden">
        , {n} {n === 1 ? "issue" : "issues"}
      </span>
    </>
  );
}

/**
 * The sidebar's degraded badges, by nav path: Cluster counts nodes not
 * READY plus Valkey or PostgreSQL down; Trunks counts destinations down.
 * A view that cannot be read shows no badge (unknown, not healthy).
 */
export function usePlatformBadges(
  enabled = true,
): Readonly<Record<string, ReactNode>> {
  // A self-service user may not read these views; asking would only fill
  // the console with 403s.
  const cluster = usePolling(enabled ? getCluster : idle, BADGE_REFRESH_MS);
  const trunks = usePolling(enabled ? listTrunkStatus : idle, BADGE_REFRESH_MS);
  return {
    "/cluster":
      cluster.status === "ready"
        ? issueBadge(clusterIssues(cluster.data))
        : undefined,
    "/trunks":
      trunks.status === "ready"
        ? issueBadge(trunkIssues(trunks.data))
        : undefined,
  };
}
