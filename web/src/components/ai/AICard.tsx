import { Link } from "react-router";
import { getAIStatus } from "../../api/aiagent";
import { StatCard } from "../../design/azrty/components";
import { usePolling } from "../../usePolling";

/** The dashboard's AI card; nothing while AI is off or unreadable. */
export function AICard() {
  const state = usePolling(getAIStatus, 30_000);
  const s = state.status === "loading" ? undefined : state.data;
  if (!s?.enabled) return null;
  const f = s.openFindings;
  const total = f.info + f.warning + f.critical;
  return (
    <Link to="/ai/findings" className="aix-cardlink">
      <StatCard
        label="AI findings"
        value={String(total)}
        unit="open"
        icon="bot"
        sub={`${f.critical} critical · ${s.openProposals} ${s.openProposals === 1 ? "proposal" : "proposals"} to review`}
      />
    </Link>
  );
}
