import { useCallback, useEffect, useState } from "react";
import { errorMessage } from "../api";
import {
  listRedactions,
  listSupervisorViews,
  peerExportUrl,
  type RedactionCount,
  type SupervisorView,
  type SupervisorViewTotal,
} from "../api/inboxReports";
import {
  Alert,
  Button,
  Checkbox,
  EmptyState,
  Icon,
  Input,
  PageHeader,
  Select,
  Spinner,
  Table,
} from "../design/azrty/components";
import { formatTime } from "../format";
import "./inbox-reports.css";

const PERIODS = [
  { value: "7", label: "Last 7 days" },
  { value: "30", label: "Last 30 days" },
  { value: "90", label: "Last 90 days" },
  { value: "365", label: "Last 365 days" },
];

/** A plausible phone number or chat visitor id, before the server checks. */
const peerLike = (v: string) =>
  /^\+?[0-9 ()./-]{3,32}$/.test(v) || /^visitor:[0-9a-f-]{1,64}$/i.test(v);

/**
 * The inbox's privacy tools for administrators: the export of everything
 * kept of one customer (a data subject access request), what the carriers
 * did with the deletions an erase or redaction asked for, and who looked at
 * conversations that were not theirs. Erasing a customer is on the
 * Messaging settings page.
 */
export function MessagingPrivacy() {
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Messaging"
        title="Privacy"
        description="Export what the inbox keeps of a customer, follow the deletions at the carriers, and review who opened conversations that were not assigned to them."
      />
      <ExportForm />
      <Redactions />
      <SupervisorViews />
    </section>
  );
}

function ExportForm() {
  const [peer, setPeer] = useState("");
  const [notes, setNotes] = useState(false);
  const v = peer.trim();
  const ok = peerLike(v);
  return (
    <>
      <h2 className="az-h3">Export a customer</h2>
      <p>
        Downloads a zip of every conversation and message with a phone number
        (or a chat visitor, <code>visitor:&lt;id&gt;</code>) on every channel,
        with times and delivery states, and the customer&apos;s opt-out records.
        The agents&apos; internal notes are left out unless you include them.
        The export is recorded in the audit log, without its content.
      </p>
      <div className="msg-toolbar" role="group" aria-label="Export">
        <Input
          label="Customer's number"
          value={peer}
          error={
            v !== "" && !ok
              ? "Enter a phone number or visitor:<id>."
              : undefined
          }
          onChange={(e) => setPeer(e.target.value)}
        />
        <Checkbox
          label="Include internal notes"
          checked={notes}
          onChange={(e) => setNotes(e.target.checked)}
        />
        {ok ? (
          <a
            className="az-btn az-btn--secondary"
            href={peerExportUrl(v, notes)}
            download
          >
            <Icon name="download" size={15} />
            Download export
          </a>
        ) : (
          <Button variant="secondary" icon="download" disabled>
            Download export
          </Button>
        )}
      </div>
    </>
  );
}

function Redactions() {
  const [rows, setRows] = useState<RedactionCount[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const c = new AbortController();
    listRedactions(c.signal).then(setRows, (err: unknown) => {
      if (!c.signal.aborted) setError(errorMessage(err));
    });
    return () => c.abort();
  }, []);
  return (
    <>
      <h2 className="az-h3" style={{ marginTop: 32 }}>
        Deletions at the carriers
      </h2>
      <p>
        Erasing a customer or redacting a message deletes the texts Hello sent
        through a carrier API at the carrier too (Twilio deletes the message and
        its media). A carrier that cannot be reached is retried for a day.
        Telnyx has no way to delete one message: turn on redaction in its
        messaging profile. Carriers keep their own logs under their own terms.
        The last 30 days:
      </p>
      {error && (
        <Alert tone="bad" title="Could not load the deletions">
          {error}
        </Alert>
      )}
      {!rows && !error && <Spinner label="Loading…" />}
      {rows && rows.length === 0 && (
        <p className="az-caption">
          No deletion was needed in the last 30 days.
        </p>
      )}
      {rows && rows.length > 0 && (
        <Table
          caption="Deletions at the carriers"
          rows={rows}
          rowKey={(r) => r.provider}
          columns={[
            { key: "p", label: "Carrier", render: (r) => r.provider },
            {
              key: "done",
              label: "Deleted",
              align: "right",
              render: (r) => r.done,
            },
            {
              key: "pending",
              label: "Retrying",
              align: "right",
              render: (r) => r.pending,
            },
            {
              key: "failed",
              label: "Refused",
              align: "right",
              render: (r) => r.failed,
            },
            {
              key: "unsupported",
              label: "Not possible",
              align: "right",
              render: (r) => r.unsupported,
            },
          ]}
        />
      )}
    </>
  );
}

function SupervisorViews() {
  const [days, setDays] = useState("30");
  const [actor, setActor] = useState("");
  const [items, setItems] = useState<SupervisorView[]>([]);
  const [totals, setTotals] = useState<SupervisorViewTotal[] | null>(null);
  const [next, setNext] = useState(0);
  const [error, setError] = useState<string | null>(null);
  const [period] = useState(() => new Date());

  const range = useCallback(() => {
    const to = new Date(period.getTime() + 60_000);
    const from = new Date(period.getTime() - Number(days) * 86_400_000);
    return { from: from.toISOString(), to: to.toISOString() };
  }, [days, period]);

  useEffect(() => {
    const c = new AbortController();
    listSupervisorViews({ ...range(), actor }, c.signal).then(
      (r) => {
        setItems(r.items);
        setTotals(r.totals);
        setNext(r.nextBefore);
        setError(null);
      },
      (err: unknown) => {
        if (!c.signal.aborted) setError(errorMessage(err));
      },
    );
    return () => c.abort();
  }, [range, actor]);

  async function more() {
    try {
      const r = await listSupervisorViews({ ...range(), actor, before: next });
      setItems((xs) => [...xs, ...r.items]);
      setNext(r.nextBefore);
    } catch (err: unknown) {
      setError(errorMessage(err));
    }
  }

  const who = (v: { userName: string; actor: string }) => v.userName || v.actor;
  return (
    <>
      <h2 className="az-h3" style={{ marginTop: 32 }}>
        Supervisor views
      </h2>
      <p>
        Each time an operator or administrator opens a conversation that is not
        assigned to them, it is recorded (once per person and conversation per
        hour). The record names who and which conversation, never the content.
      </p>
      <div className="msg-toolbar" role="group" aria-label="Supervisor views">
        <Select
          label="Period"
          value={days}
          options={PERIODS}
          onChange={(e) => {
            setTotals(null);
            setError(null);
            setDays(e.target.value);
          }}
        />
        <Select
          label="Person"
          value={actor}
          options={[
            { value: "", label: "Everyone" },
            ...(totals ?? []).map((t) => ({ value: t.actor, label: who(t) })),
            ...(actor && !(totals ?? []).some((t) => t.actor === actor)
              ? [{ value: actor, label: actor }]
              : []),
          ]}
          onChange={(e) => {
            setTotals(null);
            setError(null);
            setActor(e.target.value);
          }}
        />
      </div>
      {error && (
        <Alert tone="bad" title="Could not load the supervisor views">
          {error}
        </Alert>
      )}
      {!totals && !error && <Spinner label="Loading…" />}
      {totals && totals.length === 0 && (
        <EmptyState
          icon="eye"
          title="No supervisor views"
          description="Nobody opened a conversation that was not theirs in this period."
        />
      )}
      {totals && totals.length > 0 && (
        <>
          <Table
            caption="Supervisor views per person"
            rows={totals}
            rowKey={(t) => t.actor}
            columns={[
              { key: "who", label: "Person", render: (t) => who(t) },
              {
                key: "views",
                label: "Views",
                align: "right",
                render: (t) => t.views,
              },
              {
                key: "convs",
                label: "Conversations",
                align: "right",
                render: (t) => t.conversations,
              },
            ]}
          />
          <Table
            caption="Supervisor views"
            style={{ marginTop: 16 }}
            rows={items}
            rowKey={(v) => v.id}
            columns={[
              { key: "at", label: "When", render: (v) => formatTime(v.at) },
              { key: "who", label: "Person", render: (v) => who(v) },
              {
                key: "conv",
                label: "Conversation",
                mono: true,
                render: (v) => `#${String(v.conversationId)}`,
              },
              {
                key: "via",
                label: "Through",
                render: (v) => v.via || "console",
              },
            ]}
          />
          {next > 0 && (
            <div style={{ marginTop: 8 }}>
              <Button variant="ghost" size="sm" onClick={() => void more()}>
                Show older views
              </Button>
            </div>
          )}
        </>
      )}
    </>
  );
}
