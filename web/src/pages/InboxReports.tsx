import { useEffect, useState, type FormEvent } from "react";
import { errorMessage, fieldErrors } from "../api";
import {
  getInboxReport,
  getInboxSLA,
  inboxReportCsvUrl,
  putInboxSLA,
  type Durations,
  type InboxReport,
  type ReportGroup,
  type ReportQuery,
  type ReportRow,
} from "../api/inboxReports";
import {
  Alert,
  Button,
  EmptyState,
  Icon,
  Input,
  PageHeader,
  Select,
  Spinner,
  StatCard,
  Table,
  useToast,
} from "../design/azrty/components";
import { Can } from "../role";
import "./inbox-reports.css";

/** The periods the page offers, in days back from now. */
const PERIODS = [
  { value: "1", label: "Last 24 hours" },
  { value: "7", label: "Last 7 days" },
  { value: "30", label: "Last 30 days" },
  { value: "90", label: "Last 90 days" },
  { value: "365", label: "Last 365 days" },
];

const GROUPS: { value: ReportGroup; label: string }[] = [
  { value: "channel", label: "By channel" },
  { value: "queue", label: "By queue" },
  { value: "agent", label: "By agent" },
  { value: "day", label: "By day" },
];

/** A duration in seconds as "45 s", "12 min", "3.5 h", "2.1 d". */
export function duration(seconds: number): string {
  if (seconds < 60) return `${String(Math.round(seconds))} s`;
  if (seconds < 3600) return `${String(Math.round(seconds / 60))} min`;
  if (seconds < 86400) return `${(seconds / 3600).toFixed(1)} h`;
  return `${(seconds / 86400).toFixed(1)} d`;
}

/** The median of a set of durations, or "—" when there is none. */
const median = (d: Durations) => (d.count > 0 ? duration(d.p50Seconds) : "—");

function periodQuery(days: string, groupBy: ReportGroup): ReportQuery {
  const to = new Date();
  const from = new Date(to.getTime() - Number(days) * 86_400_000);
  return { from: from.toISOString(), to: to.toISOString(), groupBy };
}

const target = (seconds: number) => (seconds > 0 ? duration(seconds) : "off");

/**
 * How the messaging inbox performs: volumes, how long customers wait for a
 * first answer and for a resolution, and the conversations past the
 * service-level targets, in total and by channel, queue, agent or day.
 * Counts and durations only: no customer text or number.
 */
export function InboxReports() {
  const [days, setDays] = useState("7");
  const [groupBy, setGroupBy] = useState<ReportGroup>("channel");
  const [query, setQuery] = useState(() => periodQuery("7", "channel"));
  const [report, setReport] = useState<InboxReport | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const c = new AbortController();
    getInboxReport(query, c.signal).then(
      (r) => {
        setReport(r);
        setError(null);
      },
      (err: unknown) => {
        if (!c.signal.aborted) setError(errorMessage(err));
      },
    );
    return () => c.abort();
  }, [query]);

  const choose = (d: string, g: ReportGroup) => {
    setDays(d);
    setGroupBy(g);
    setReport(null);
    setError(null);
    setQuery(periodQuery(d, g));
  };

  const t = report?.totals;
  const columns = [
    {
      key: "label",
      label: groupLabel(groupBy),
      render: (r: ReportRow) => r.label,
    },
    {
      key: "conv",
      label: "Conversations",
      align: "right" as const,
      render: (r: ReportRow) => r.conversations,
    },
    {
      key: "msgs",
      label: "Messages in / out",
      align: "right" as const,
      render: (r: ReportRow) =>
        `${String(r.messagesIn)} / ${String(r.messagesOut)}`,
    },
    {
      key: "frt",
      label: "First response (median)",
      align: "right" as const,
      render: (r: ReportRow) => median(r.firstResponse),
    },
    {
      key: "res",
      label: "Resolution (median)",
      align: "right" as const,
      render: (r: ReportRow) => median(r.resolution),
    },
    {
      key: "breach",
      label: "Past target",
      align: "right" as const,
      render: (r: ReportRow) =>
        `${String(r.slaFirstResponseBreaches)} / ${String(r.slaResolutionBreaches)}`,
    },
  ];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Messaging"
        title="Inbox reports"
        description="How quickly customers get an answer and how many conversations the inbox handles. Counts and durations only; spam is left out, and conversations past their retention are no longer counted."
        actions={
          <a
            className="az-btn az-btn--secondary"
            href={inboxReportCsvUrl(query)}
            download
          >
            <Icon name="download" size={15} />
            Export CSV
          </a>
        }
      />
      <div className="msg-toolbar" role="group" aria-label="Report period">
        <Select
          label="Period"
          value={days}
          options={PERIODS}
          onChange={(e) => choose(e.target.value, groupBy)}
        />
        <Select
          label="Split"
          value={groupBy}
          options={GROUPS}
          onChange={(e) => choose(days, e.target.value as ReportGroup)}
        />
      </div>
      {error && (
        <Alert tone="bad" title="Could not load the report">
          {error}
        </Alert>
      )}
      {!report && !error && <Spinner label="Loading the report…" />}
      {report && t && (
        <>
          <div className="inbox-report-stats">
            <StatCard
              label="Conversations"
              value={String(t.conversations)}
              icon="messages-square"
              sub={`${String(t.inboundConversations)} from customers, ${String(t.resolved)} resolved`}
            />
            <StatCard
              label="Messages"
              value={String(t.messagesIn + t.messagesOut)}
              icon="message-square"
              sub={`${String(t.messagesIn)} in, ${String(t.messagesOut)} out`}
            />
            <StatCard
              label="First response"
              value={median(t.firstResponse)}
              icon="timer"
              sub={
                t.firstResponse.count > 0
                  ? `median; 90% within ${duration(t.firstResponse.p90Seconds)}`
                  : "no answered conversation"
              }
            />
            <StatCard
              label="Resolution"
              value={median(t.resolution)}
              icon="circle-check"
              sub={
                t.resolution.count > 0
                  ? `median; 90% within ${duration(t.resolution.p90Seconds)}`
                  : "none resolved"
              }
            />
            <StatCard
              label="Past target"
              value={String(t.slaFirstResponseBreaches)}
              icon="alarm-clock"
              sub={`first response over ${target(report.sla.firstResponseSeconds)}; ${String(t.slaResolutionBreaches)} resolution over ${target(report.sla.resolutionSeconds)}`}
            />
          </div>
          {report.rows.length === 0 ? (
            <EmptyState
              icon="chart-bar"
              title="No conversations in this period"
              description="Pick a longer period."
            />
          ) : (
            <Table
              caption={`Inbox report by ${groupLabel(groupBy).toLowerCase()}`}
              rows={report.rows}
              rowKey={(r) => r.key || "-"}
              columns={columns}
            />
          )}
        </>
      )}
      <Can min="admin">
        <TargetsForm onSaved={() => setQuery(periodQuery(days, groupBy))} />
      </Can>
    </section>
  );
}

function groupLabel(g: ReportGroup): string {
  switch (g) {
    case "channel":
      return "Channel";
    case "queue":
      return "Queue";
    case "agent":
      return "Agent";
    case "day":
      return "Day";
  }
  return "Group";
}

/** The service-level targets, in minutes in the form and seconds in the API. */
function TargetsForm({ onSaved }: { onSaved: () => void }) {
  const [first, setFirst] = useState("");
  const [resolve, setResolve] = useState("");
  const [loaded, setLoaded] = useState(false);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  useEffect(() => {
    const c = new AbortController();
    getInboxSLA(c.signal).then(
      (t) => {
        setFirst(String(t.firstResponseSeconds / 60));
        setResolve(String(t.resolutionSeconds / 60));
        setLoaded(true);
      },
      (err: unknown) => {
        if (!c.signal.aborted) setError(errorMessage(err));
      },
    );
    return () => c.abort();
  }, []);

  async function save(e: FormEvent) {
    e.preventDefault();
    const f = Number(first);
    const r = Number(resolve);
    const next: Record<string, string> = {};
    if (!Number.isFinite(f) || f < 0 || f > 10080) {
      next.firstResponseSeconds = "Enter 0 (off) to 10080 minutes.";
    }
    if (!Number.isFinite(r) || r < 0 || r > 43200) {
      next.resolutionSeconds = "Enter 0 (off) to 43200 minutes.";
    }
    setFields(next);
    if (Object.keys(next).length > 0) return;
    setBusy(true);
    setError(null);
    try {
      await putInboxSLA({
        firstResponseSeconds: Math.round(f * 60),
        resolutionSeconds: Math.round(r * 60),
      });
      toast.show("Targets saved.");
      onSaved();
    } catch (err: unknown) {
      setFields(
        Object.fromEntries(fieldErrors(err).map((x) => [x.path, x.message])),
      );
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <>
      <h2 className="az-h3" style={{ marginTop: 32 }}>
        Service-level targets
      </h2>
      <p>
        A conversation a customer started is past the first-response target when
        nobody answered it in time; any conversation is past the resolution
        target when it was not resolved in time. 0 turns a target off.
      </p>
      {error && (
        <Alert tone="bad" title="Targets">
          {error}
        </Alert>
      )}
      {loaded && (
        <form className="msg-toolbar" onSubmit={save}>
          <Input
            label="First response within (minutes)"
            inputMode="decimal"
            value={first}
            error={fields.firstResponseSeconds}
            onChange={(e) => setFirst(e.target.value)}
          />
          <Input
            label="Resolved within (minutes)"
            inputMode="decimal"
            value={resolve}
            error={fields.resolutionSeconds}
            onChange={(e) => setResolve(e.target.value)}
          />
          <Button type="submit" disabled={busy}>
            Save targets
          </Button>
        </form>
      )}
      {toast.node}
    </>
  );
}
