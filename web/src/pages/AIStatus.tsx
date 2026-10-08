import { useState } from "react";
import { errorMessage } from "../api";
import { getAIStatus, runAgent, type AIStatus as Status } from "../api/aiagent";
import { AIOff } from "../components/ai/AIOff";
import {
  Alert,
  Badge,
  Button,
  PageHeader,
  PropertyList,
  Spinner,
  Table,
  type TableColumn,
} from "../design/azrty/components";
import { Can } from "../role";
import { usePolling } from "../usePolling";
import "../components/ai/ai.css";

const when = (t: string | null) => (t ? new Date(t).toLocaleString() : "—");

/** AI status: the endpoint (host only), budget, agents and recent errors. */
export function AIStatus() {
  const state = usePolling(getAIStatus, 10_000);
  const [error, setError] = useState<string | null>(null);
  const s: Status | undefined =
    state.status === "loading" ? undefined : state.data;

  const agentCols: TableColumn<Status["agents"][number]>[] = [
    { key: "name", label: "Agent", mono: true },
    {
      key: "interval",
      label: "Every",
      render: (a) => `${a.intervalSeconds} s`,
    },
    {
      key: "last",
      label: "Last run",
      render: (a) => when(a.lastRun?.startedAt ?? null),
    },
    {
      key: "outcome",
      label: "Outcome",
      render: (a) => a.lastRun?.outcome ?? "—",
    },
    { key: "next", label: "Next due", render: (a) => when(a.nextDueAt) },
    {
      key: "run",
      label: "",
      render: (a) => (
        <Can>
          <Button
            size="sm"
            variant="secondary"
            disabled={a.runRequested}
            onClick={() => {
              setError(null);
              runAgent(a.name).then(state.reload, (e: unknown) =>
                setError(errorMessage(e)),
              );
            }}
          >
            {a.runRequested ? "Requested" : "Run now"}
          </Button>
        </Can>
      ),
    },
  ];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="AI"
        title="Status"
        description="The model endpoint, today's budget and the background agents."
      />
      <div className="aix-stack">
        {state.status === "loading" && <Spinner label="Loading AI status…" />}
        {state.status === "error" && (
          <Alert tone="bad" title="Could not load the AI status">
            {state.message}
          </Alert>
        )}
        {error && (
          <Alert tone="bad" title="Could not start the agent">
            {error}
          </Alert>
        )}
        {s && !s.enabled && <AIOff reason={s.reason} />}
        {s?.enabled && (
          <>
            <PropertyList
              items={[
                { label: "Provider", value: s.provider },
                { label: "Model", value: s.model, mono: true },
                { label: "Endpoint host", value: s.endpointHost, mono: true },
                {
                  label: "Endpoint network",
                  value: s.endpointPrivate ? (
                    <Badge tone="good">private</Badge>
                  ) : (
                    <Badge tone="warn">public</Badge>
                  ),
                },
                { label: "Structured output", value: s.structuredOutput },
                {
                  label: "Tokens today",
                  value: `${s.tokensToday.toLocaleString()} of ${s.dailyTokenBudget.toLocaleString()} (background limit ${s.backgroundTokenLimit.toLocaleString()})`,
                },
                {
                  label: "Concurrency",
                  value: `${s.concurrencyInUse} of ${s.maxConcurrency}`,
                },
                {
                  label: "Open findings",
                  value: `${s.openFindings.critical} critical, ${s.openFindings.warning} warning, ${s.openFindings.info} info`,
                },
                { label: "Open proposals", value: String(s.openProposals) },
                { label: "Health score", value: `${s.healthScore} of 100` },
              ]}
            />
            <Table
              columns={agentCols}
              rows={s.agents}
              rowKey={(a) => a.name}
              caption="Agents"
            />
            <h2 className="aix-card__title">Recent errors</h2>
            {s.recentErrors.length === 0 ? (
              <p className="aix-muted">No recent errors.</p>
            ) : (
              <ul className="aix-ops">
                {s.recentErrors.map((e, i) => (
                  <li key={i}>
                    <span className="aix-mono">{e.code}</span> at {when(e.at)}
                  </li>
                ))}
              </ul>
            )}
          </>
        )}
      </div>
    </section>
  );
}
