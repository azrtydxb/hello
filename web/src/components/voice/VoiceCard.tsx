import { Link } from "react-router";
import { getVoiceStatus, listVoiceAgents } from "../../api/voice";
import { StatCard } from "../../design/azrty/components";
import { usePolling } from "../../usePolling";

const SLOW_REFRESH_MS = 30_000;

/** The dashboard's voice card: the agents and whether the runtime is live. */
export function VoiceCard() {
  const agents = usePolling(listVoiceAgents, SLOW_REFRESH_MS);
  const status = usePolling(getVoiceStatus, SLOW_REFRESH_MS);
  const items = agents.status === "loading" ? undefined : agents.data;
  const state = status.status === "loading" ? undefined : status.data;
  const enabled = items?.filter((a) => a.enabled).length ?? 0;
  const sub = !state
    ? "Loading…"
    : state.lastSeenAt
      ? state.healthy
        ? `talking-agent live${state.version ? ` · ${state.version}` : ""}`
        : "talking-agent has not reported in over 2 minutes"
      : "talking-agent has never reported";
  return (
    <Link to="/voice/runtime" className="aix-cardlink">
      <StatCard
        label="Voice agents"
        value={items ? String(items.length) : "—"}
        unit={items ? `/ ${enabled} enabled` : undefined}
        icon="bot"
        sub={sub}
      />
    </Link>
  );
}
