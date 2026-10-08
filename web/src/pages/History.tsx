import { useEffect, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { errorMessage, type Cdr, type CdrPage } from "../api";
import {
  cdrExportUrl,
  getCdrCounts,
  listCdrPage,
  type CdrCounts,
  type CdrFilter,
} from "../api/calls";
import {
  Alert,
  Badge,
  Button,
  EmptyState,
  Icon,
  PageHeader,
  Spinner,
  type TabItem,
  Table,
  type TableColumn,
  Tabs,
} from "../design/azrty/components";
import { formatDuration } from "../format";
import { clock, day, directionOf, statusTone } from "./calls/common";

export const PAGE_SIZE = 50;

/** The tabs of Call history, also its ?tab= values. */
export type HistoryTab = "all" | "failed" | "inbound" | "outbound" | "internal";

const TAB_IDS: readonly HistoryTab[] = [
  "all",
  "failed",
  "inbound",
  "outbound",
  "internal",
];

const FILTERS: Record<HistoryTab, CdrFilter> = {
  all: {},
  failed: { failed: true },
  inbound: { direction: "inbound" },
  outbound: { direction: "outbound" },
  internal: { direction: "internal" },
};

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; page: CdrPage };

const detailPath = (cdr: Cdr) =>
  `/history/${encodeURIComponent(String(cdr.id))}`;

/** Call history: CDRs newest first, by tab, paged with the `next` cursor. */
export function History() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const raw = params.get("tab");
  const tab: HistoryTab = TAB_IDS.includes(raw as HistoryTab)
    ? (raw as HistoryTab)
    : "all";
  // Cursors of the pages visited: "" is the newest page; the last is shown.
  const [cursors, setCursors] = useState<string[]>([""]);
  const [state, setState] = useState<State>({ status: "loading" });
  const [counts, setCounts] = useState<CdrCounts>();
  const before = cursors[cursors.length - 1] ?? "";

  useEffect(() => {
    const controller = new AbortController();
    listCdrPage(
      { ...FILTERS[tab], before: before || undefined, limit: PAGE_SIZE },
      controller.signal,
    )
      .then((page) => setState({ status: "ready", page }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [before, tab]);

  useEffect(() => {
    const controller = new AbortController();
    getCdrCounts(controller.signal)
      .then(setCounts)
      .catch(() => {
        // The tab counts are optional: without them the tabs show none.
      });
    return () => controller.abort();
  }, []);

  function go(next: string[]) {
    setState({ status: "loading" });
    setCursors(next);
  }

  function selectTab(next: HistoryTab) {
    if (next === tab) return;
    setState({ status: "loading" });
    setCursors([""]);
    setParams(next === "all" ? {} : { tab: next }, { replace: true });
  }

  const tabs: TabItem<HistoryTab>[] = [
    { id: "all", label: "All", count: counts?.all },
    { id: "failed", label: "Failed", count: counts?.failed },
    { id: "inbound", label: "Inbound" },
    { id: "outbound", label: "Outbound" },
    { id: "internal", label: "Internal" },
  ];

  const columns: TableColumn<Cdr>[] = [
    {
      key: "started",
      label: "Started",
      mono: true,
      render: (c) => (
        <>
          <Link
            className="calls-row-link"
            to={detailPath(c)}
            aria-label={`Call at ${clock(c.startTime)} ${day(c.startTime)} from ${c.source} to ${c.originalDestination || c.destination}: details`}
            onClick={(e) => e.stopPropagation()}
          >
            {clock(c.startTime)}
          </Link>
          <small>{day(c.startTime)}</small>
        </>
      ),
    },
    {
      key: "direction",
      label: "Direction",
      primary: false,
      render: (c) => {
        const d = directionOf(c);
        return (
          <span className="calls-dir">
            <Icon name={d.icon} size={14} />
            {d.label}
          </span>
        );
      },
    },
    {
      key: "from",
      label: "From",
      mono: true,
      render: (c) => <span className="calls-strong">{c.source}</span>,
    },
    {
      key: "destination",
      label: "Destination",
      render: (c) => {
        const orig = c.originalDestination || c.destination;
        const rew =
          c.rewrittenDestination && c.rewrittenDestination !== orig
            ? c.rewrittenDestination
            : "";
        return (
          <>
            <span className="calls-mono calls-strong">{orig}</span>
            {rew && <small className="calls-mono">→ {rew}</small>}
            {c.voiceAgentName && (
              <small>
                {" · "}
                <Icon name="bot" size={12} /> {c.voiceAgentName}
              </small>
            )}
          </>
        );
      },
    },
    { key: "route", label: "Route", render: (c) => c.route || "—" },
    {
      key: "trunk",
      label: "Trunk",
      mono: true,
      render: (c) => c.trunk || "—",
    },
    {
      key: "status",
      label: "Status",
      render: (c) => (
        <Badge tone={statusTone(c.finalStatus)} className="calls-mono">
          {c.finalStatus}
        </Badge>
      ),
    },
    {
      key: "duration",
      label: "Duration",
      align: "right",
      mono: true,
      render: (c) => formatDuration(c.durationMs),
    },
  ];

  const page = state.status === "ready" ? state.page : undefined;

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Activity"
        title="Call history"
        description="Every call record carries its routing trace. Open one to see why it went where it did."
        actions={
          <a
            className="az-btn az-btn--secondary"
            href={cdrExportUrl(FILTERS[tab])}
            download
          >
            <Icon name="download" size={15} />
            Export CSV
          </a>
        }
      />
      <Tabs
        items={tabs}
        value={tab}
        onChange={selectTab}
        aria-label="Calls shown"
        idPrefix="history"
        className="calls-tabs"
      />
      <div
        role="tabpanel"
        id={`history-panel-${tab}`}
        aria-labelledby={`history-tab-${tab}`}
      >
        {state.status === "loading" && <Spinner label="Loading call history" />}
        {state.status === "error" && (
          <Alert tone="bad" title="Could not load call history">
            {state.message}
          </Alert>
        )}
        {page && page.items.length === 0 && (
          <EmptyState
            icon="history"
            title={
              cursors.length > 1
                ? "No older calls"
                : tab === "failed"
                  ? "No failed calls"
                  : "No calls recorded"
            }
            description="A call record is written when a call ends."
          />
        )}
        {page && page.items.length > 0 && (
          <Table
            caption="Call history"
            columns={columns}
            rows={page.items}
            rowKey={(c) => c.id}
            onRowClick={(c) => void navigate(detailPath(c))}
          />
        )}
        <nav className="calls-pager" aria-label="Call history pages">
          <span>Newest first · {PAGE_SIZE} per page</span>
          <span className="calls-pager__buttons">
            <Button
              variant="secondary"
              size="sm"
              icon="chevron-left"
              disabled={cursors.length === 1 || state.status === "loading"}
              onClick={() => go(cursors.slice(0, -1))}
            >
              Newer
            </Button>
            <Button
              variant="secondary"
              size="sm"
              iconRight="chevron-right"
              disabled={!page?.next}
              onClick={() => page?.next && go([...cursors, page.next])}
            >
              Older
            </Button>
          </span>
        </nav>
      </div>
    </section>
  );
}
