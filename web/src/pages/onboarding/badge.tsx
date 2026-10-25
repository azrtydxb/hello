// The sidebar badge of the Onboarding item: how many phones are waiting.
import type { ReactNode } from "react";
import { listCandidates, type Candidate } from "../../api/onboarding";
import { useCan } from "../../role";
import { usePolling } from "../../usePolling";

/** How often the sidebar re-counts the queue. */
export const ONBOARDING_BADGE_REFRESH_MS = 30_000;

// A viewer may not read the queue (operators and admins may): no request.
const nobody = () => Promise.resolve<Candidate[]>([]);

/**
 * The badge for the "/onboarding" nav item, by nav path like
 * usePlatformBadges: the number of waiting phones, read out as "N waiting".
 * A queue that cannot be read shows no badge.
 */
export function useOnboardingBadge(): Readonly<Record<string, ReactNode>> {
  const can = useCan("operator");
  const queue = usePolling(
    can ? listCandidates : nobody,
    ONBOARDING_BADGE_REFRESH_MS,
  );
  const n = queue.status === "ready" ? queue.data.length : 0;
  if (n <= 0) return {};
  return {
    "/onboarding": (
      <>
        <span aria-hidden="true">{n}</span>
        <span className="visually-hidden">, {n} waiting</span>
      </>
    ),
  };
}
