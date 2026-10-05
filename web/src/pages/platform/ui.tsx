// Page furniture the Platform pages share: the page header, the LIVE tag,
// a toast and the sidebar's degraded badges.
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { getCluster, listTrunkStatus } from "../../api";
import { Icon, IconButton } from "../../design/azrty/components";
import { usePolling } from "../../usePolling";
import { clusterIssues, trunkIssues } from "./health";
import "./platform.css";

/** The design's page header: eyebrow, title, one line of description, actions. */
export function PageHeader({
  title,
  description,
  children,
}: {
  title: string;
  description: ReactNode;
  children?: ReactNode;
}) {
  return (
    <div className="pf-head">
      <div>
        <span className="az-eyebrow pf-head__eyebrow">Platform</span>
        <h1 id="page-title" className="pf-head__title">
          {title}
        </h1>
        <p className="pf-head__desc">{description}</p>
      </div>
      {children}
    </div>
  );
}

/** The pulsing LIVE tag; `every` is the refresh, e.g. "5 s". */
export function LiveTag({
  every,
  live = true,
}: {
  every?: string;
  live?: boolean;
}) {
  return (
    <span className="az-live">
      <span
        className={live ? "az-dot az-dot--pulse" : "az-dot pf-dot--idle"}
        aria-hidden="true"
      />
      {live ? "LIVE" : "PAUSED"}
      {every ? ` · ${every.toUpperCase()}` : ""}
    </span>
  );
}

/** A toast: one message at a time, announced politely, gone after 5 s. */
export function useToast(): [ReactNode, (message: string) => void] {
  const [message, setMessage] = useState<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const show = useCallback((m: string) => {
    clearTimeout(timer.current);
    setMessage(m);
    timer.current = setTimeout(() => setMessage(null), 5000);
  }, []);
  useEffect(() => () => clearTimeout(timer.current), []);
  const node = (
    <div className="az-toast-stack" role="status" aria-live="polite">
      {message && (
        <div className="az-toast az-toast--good">
          <Icon name="circle-check" size={16} />
          <div className="az-toast__body">{message}</div>
          <IconButton
            icon="x"
            label="Dismiss"
            size={14}
            onClick={() => setMessage(null)}
          />
        </div>
      )}
    </div>
  );
  return [node, show];
}

/** How often the sidebar re-reads cluster and trunk health. */
export const BADGE_REFRESH_MS = 15_000;

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
export function usePlatformBadges(): Readonly<Record<string, ReactNode>> {
  const cluster = usePolling(getCluster, BADGE_REFRESH_MS);
  const trunks = usePolling(listTrunkStatus, BADGE_REFRESH_MS);
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
