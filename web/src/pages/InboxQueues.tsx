import { errorMessage } from "../api";
import {
  AGENT_STATE_LABEL,
  listTextQueues,
  type AgentState,
  type TextQueue,
} from "../api/inboxRouting";
import {
  Alert,
  Badge,
  EmptyState,
  PageHeader,
  Spinner,
  Table,
} from "../design/azrty/components";
import { usePolling } from "../usePolling";

const REFRESH_MS = 10_000;

const stateTone = (s: AgentState) =>
  s === "available"
    ? ("good" as const)
    : s === "paused" || s === "at_capacity" || s === "on_call"
      ? ("warn" as const)
      : ("neutral" as const);

/** "3 min", "45 s": how long a conversation has waited. */
function waited(seconds: number): string {
  if (seconds < 60) return `${String(seconds)} s`;
  const m = Math.floor(seconds / 60);
  return m < 120 ? `${String(m)} min` : `${String(Math.floor(m / 60))} h`;
}

function QueueCard({ q }: { q: TextQueue }) {
  return (
    <section className="az-card inbox-queue" aria-label={`Queue ${q.name}`}>
      <h3>{q.name}</h3>
      <p className="az-caption">
        Strategy {q.strategy}, {q.capacity} open per agent. {q.waitingTotal}{" "}
        waiting
        {q.waitingTotal > 0 &&
          `, the longest for ${waited(q.oldestWaitSeconds)}`}
        . {q.assigned} with agents.
      </p>
      {q.waiting.length > 0 && (
        <Table
          caption={`Waiting in ${q.name}`}
          rows={q.waiting}
          rowKey={(w) => w.conversationId}
          columns={[
            { key: "pos", label: "Place", render: (w) => w.position },
            {
              key: "id",
              label: "Conversation",
              mono: true,
              render: (w) => `#${String(w.conversationId)}`,
            },
            { key: "ch", label: "Channel", render: (w) => w.channel },
            {
              key: "wait",
              label: "Waiting",
              render: (w) => waited(w.waitSeconds),
            },
          ]}
        />
      )}
      <Table
        caption={`Agents of ${q.name}`}
        rows={q.agents}
        rowKey={(a) => `${String(a.userId)}-${a.extension}`}
        columns={[
          { key: "name", label: "Agent", render: (a) => a.name },
          {
            key: "ext",
            label: "Extension",
            mono: true,
            render: (a) => a.extension,
          },
          {
            key: "state",
            label: "State",
            render: (a) => (
              <Badge tone={stateTone(a.state)}>
                {AGENT_STATE_LABEL[a.state]}
              </Badge>
            ),
          },
          {
            key: "open",
            label: "Open",
            align: "right",
            render: (a) => `${String(a.open)} of ${String(q.capacity)}`,
          },
          {
            key: "answered",
            label: "Given",
            align: "right",
            render: (a) => a.answered,
          },
        ]}
      />
    </section>
  );
}

/**
 * The text queues as a supervisor sees them: who waits, for how long, and
 * which agents can take the next conversation. No customer text or number.
 */
export function InboxQueues() {
  const queues = usePolling(listTextQueues, REFRESH_MS);
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Messaging"
        title="Text queues"
        description="The queues that take customer texts. A conversation waits here until an agent who is logged in, not paused and under their limit can take it; with ring_all it stays in a shared pool and the first agent to reply takes it."
      />
      {queues.status === "loading" && <Spinner label="Loading the queues…" />}
      {queues.status === "error" && (
        <Alert tone="bad" title="Could not load the queues">
          {queues.message || errorMessage(queues.message)}
        </Alert>
      )}
      {queues.status === "ready" && queues.data.length === 0 && (
        <EmptyState
          icon="inbox"
          title="No queue takes texts"
          description="Turn on texting on a queue, then route a number to it."
        />
      )}
      {queues.status === "ready" &&
        queues.data.map((q) => <QueueCard key={q.id} q={q} />)}
    </section>
  );
}
