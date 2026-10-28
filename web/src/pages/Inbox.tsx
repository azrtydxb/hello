import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { errorMessage } from "../api";
import {
  getInboxStatus,
  listInboxAgents,
  myInbox,
  supervisorInbox,
  type Conversation,
  type ConversationStatus,
  type InboxAgent,
  type InboxApi,
  type InboxMessage,
  type StatusFilter,
} from "../api/inbox";
import {
  Alert,
  Badge,
  Button,
  EmptyState,
  Input,
  LinkButton,
  PageHeader,
  Select,
  Spinner,
  Switch,
  useToast,
} from "../design/azrty/components";
import { formatTime } from "../format";
import { Can } from "../role";
import { usePolling } from "../usePolling";
import { peerLabel, smsSegments } from "./inbox/sms";
import { useInboxStream, type InboxEvent } from "./inbox/useInboxStream";
import "./inbox/inbox.css";

const REFRESH_MS = 30_000;
const SOUND_KEY = "hello.inbox.sound";

/** What a reply's delivery state says, in words (never colour alone). */
function deliveryText(m: InboxMessage): string {
  switch (m.status) {
    case "queued":
      return "Queued";
    case "sending":
      return "Sending";
    case "sent":
      return "Sent to the carrier";
    case "delivered":
    case "read":
      return "Delivered";
    case "failed":
      return m.errorCode ? `Failed: ${m.errorCode}` : "Failed";
    default:
      return "Outcome unknown: it may have been delivered";
  }
}

function statusTone(s: ConversationStatus) {
  switch (s) {
    case "open":
      return "info" as const;
    case "resolved":
      return "good" as const;
    case "spam":
      return "bad" as const;
    default:
      return "warn" as const;
  }
}

function whoLabel(m: InboxMessage): string {
  switch (m.senderKind) {
    case "customer":
      return "Customer";
    case "user":
      return m.senderName || "Agent";
    case "ai":
      return "AI assistant";
    default:
      return "Hello";
  }
}

/** A short tone when a customer text arrives; the browser may refuse it. */
function beep() {
  try {
    const Ctx = window.AudioContext;
    const ctx = new Ctx();
    const osc = ctx.createOscillator();
    osc.connect(ctx.destination);
    osc.frequency.value = 880;
    osc.start();
    osc.stop(ctx.currentTime + 0.15);
    osc.onended = () => void ctx.close();
  } catch {
    // No audio: the notification and the badge still say it.
  }
}

function newClientId(): string {
  return typeof crypto !== "undefined" && "randomUUID" in crypto
    ? crypto.randomUUID()
    : `c-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

interface ThreadData {
  conv: Conversation;
  /** Oldest first. */
  messages: InboxMessage[];
  next: string;
}

async function fetchThread(
  api: InboxApi,
  id: number,
  signal?: AbortSignal,
): Promise<ThreadData> {
  const [conv, page] = await Promise.all([
    api.conversation(id, signal),
    api.messages(id, undefined, signal),
  ]);
  return { conv, messages: [...page.items].reverse(), next: page.next };
}

interface ThreadProps {
  api: InboxApi;
  conversation: Conversation;
  agents: InboxAgent[];
  supervisor: boolean;
  /** Called when the conversation or its thread changed here. */
  onChanged: () => void;
  refreshKey: number;
}

/** The thread, its composer and the details of one conversation. */
function Thread({
  api,
  conversation,
  agents,
  supervisor,
  onChanged,
  refreshKey,
}: ThreadProps) {
  const id = conversation.id;
  const [conv, setConv] = useState(conversation);
  const [messages, setMessages] = useState<InboxMessage[]>([]);
  const [older, setOlder] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [mode, setMode] = useState<"reply" | "note">("reply");
  const [draft, setDraft] = useState("");
  const [busy, setBusy] = useState(false);
  const [sendError, setSendError] = useState<string | null>(null);
  const [tags, setTags] = useState(conversation.tags.join(", "));
  const clientId = useRef(newClientId());
  const toast = useToast();

  const apply = useCallback(
    (r: ThreadData) => {
      setConv(r.conv);
      setTags(r.conv.tags.join(", "));
      setMessages(r.messages);
      setOlder(r.next);
      setError(null);
      // Opening a conversation reads it.
      if (r.conv.unread > 0) {
        void api.read(id).then(onChanged, () => undefined);
      }
    },
    [api, id, onChanged],
  );

  const load = useCallback(
    () =>
      fetchThread(api, id).then(apply, (err: unknown) =>
        setError(errorMessage(err)),
      ),
    [api, id, apply],
  );

  useEffect(() => {
    const controller = new AbortController();
    fetchThread(api, id, controller.signal)
      .then((r) => {
        if (!controller.signal.aborted) apply(r);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [api, id, refreshKey, apply]);

  async function loadOlder() {
    try {
      const page = await api.messages(id, older);
      setMessages((m) => [...[...page.items].reverse(), ...m]);
      setOlder(page.next);
    } catch (err: unknown) {
      setError(errorMessage(err));
    }
  }

  async function run(action: () => Promise<Conversation>, done: string) {
    try {
      const c = await action();
      setConv(c);
      toast.show(done);
      onChanged();
      void load();
    } catch (err: unknown) {
      setError(errorMessage(err));
    }
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    const text = draft.trim();
    if (!text) return;
    setBusy(true);
    setSendError(null);
    try {
      if (mode === "note") await api.note(id, text);
      else await api.send(id, text, clientId.current);
      // A sent text is done: the next one is a new message.
      clientId.current = newClientId();
      setDraft("");
      onChanged();
      await load();
    } catch (err: unknown) {
      // The same client id stays, so retrying cannot send twice.
      setSendError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const sms = smsSegments(draft);
  const canReply = !conv.optedOut && conv.status !== "spam";
  const unassigned = conv.assigneeUserId === null;
  const tomorrow = () => {
    const d = new Date();
    d.setDate(d.getDate() + 1);
    d.setHours(9, 0, 0, 0);
    return d.toISOString();
  };

  return (
    <>
      <div className="az-card inbox-panel">
        <div className="inbox-panel__head">
          <div>
            <h2 className="az-h4" style={{ margin: 0 }}>
              {peerLabel(conv)}
            </h2>
            <span className="az-caption">
              to {conv.address} · {conv.channelName}
            </span>
          </div>
          <Badge tone={statusTone(conv.status)}>{conv.status}</Badge>
        </div>
        {conv.optedOut && (
          <Alert tone="warn" title="The customer opted out">
            Nobody can text this customer until they reply START. You can still
            write internal notes.
          </Alert>
        )}
        {error && (
          <Alert tone="bad" title="Could not load the conversation">
            {error}
          </Alert>
        )}
        {loading && <Spinner label="Loading messages…" />}
        {older !== "" && (
          <div style={{ padding: "8px 16px" }}>
            <Button variant="ghost" size="sm" onClick={() => void loadOlder()}>
              Load earlier messages
            </Button>
          </div>
        )}
        <ol className="inbox-thread" aria-label="Messages">
          {messages.map((m) => {
            const cls =
              m.kind === "message"
                ? m.direction === "in"
                  ? "in"
                  : "out"
                : m.kind;
            return (
              <li key={m.id} className={`inbox-msg inbox-msg--${cls}`}>
                {m.kind !== "system" && (
                  <div className="inbox-msg__meta">
                    <span>
                      {m.kind === "note" ? "Note by " : ""}
                      {whoLabel(m)}
                    </span>
                    <span>{formatTime(m.createdAt)}</span>
                    {m.kind === "message" && m.direction === "out" && (
                      <Badge tone={m.status === "failed" ? "bad" : "neutral"}>
                        {deliveryText(m)}
                      </Badge>
                    )}
                  </div>
                )}
                {/* Customer text is shown as text, never as markup. */}
                <p className="inbox-msg__body">{m.body}</p>
              </li>
            );
          })}
        </ol>
        <form className="inbox-composer" onSubmit={(e) => void submit(e)}>
          <div className="inbox-actions" role="group" aria-label="Compose">
            <Button
              type="button"
              size="sm"
              variant={mode === "reply" ? "primary" : "secondary"}
              aria-pressed={mode === "reply"}
              onClick={() => setMode("reply")}
            >
              Reply
            </Button>
            <Button
              type="button"
              size="sm"
              variant={mode === "note" ? "primary" : "secondary"}
              aria-pressed={mode === "note"}
              onClick={() => setMode("note")}
            >
              Internal note
            </Button>
          </div>
          <label className="visually-hidden" htmlFor="inbox-draft">
            {mode === "note" ? "Internal note" : "Reply to the customer"}
          </label>
          <textarea
            id="inbox-draft"
            value={draft}
            maxLength={mode === "note" ? 4000 : 2000}
            placeholder={
              mode === "note"
                ? "Only agents and supervisors see a note."
                : "Write a reply"
            }
            disabled={mode === "reply" && !canReply}
            onChange={(e) => setDraft(e.target.value)}
          />
          {sendError && (
            <Alert tone="bad" title="Not sent">
              {sendError}
            </Alert>
          )}
          <div className="inbox-composer__foot">
            <span className="az-caption">
              {mode === "reply"
                ? `${sms.chars} characters, ${sms.segments} ${sms.segments === 1 ? "text" : "texts"}${sms.unicode ? " (non-GSM characters)" : ""}`
                : "Never sent to the customer"}
            </span>
            <Button
              type="submit"
              icon="send"
              disabled={
                busy || draft.trim() === "" || (mode === "reply" && !canReply)
              }
            >
              {mode === "note" ? "Add note" : "Send"}
            </Button>
          </div>
        </form>
      </div>

      <div className="az-card inbox-details" aria-label="Conversation details">
        <h3 className="az-h4" style={{ margin: 0 }}>
          Details
        </h3>
        <dl style={{ margin: 0 }}>
          <dt className="az-caption">Customer</dt>
          <dd style={{ margin: 0 }}>{conv.peer}</dd>
          {conv.peerName && (
            <>
              <dt className="az-caption">Name</dt>
              <dd style={{ margin: 0 }}>{conv.peerName}</dd>
            </>
          )}
          <dt className="az-caption">Assigned to</dt>
          <dd style={{ margin: 0 }}>
            {unassigned ? "Nobody" : conv.assigneeName}
          </dd>
          <dt className="az-caption">Last message</dt>
          <dd style={{ margin: 0 }}>{formatTime(conv.lastMessageAt)}</dd>
        </dl>
        <div className="inbox-actions">
          {unassigned && conv.status !== "resolved" && (
            <Button
              size="sm"
              icon="user-check"
              onClick={() => void run(() => api.claim(id), "You took it.")}
            >
              Take it
            </Button>
          )}
          {conv.status === "resolved" || conv.status === "spam" ? (
            <Button
              size="sm"
              variant="secondary"
              onClick={() =>
                void run(() => api.setStatus(id, "open"), "Reopened.")
              }
            >
              Reopen
            </Button>
          ) : (
            <>
              <Button
                size="sm"
                variant="secondary"
                icon="circle-check"
                onClick={() =>
                  void run(() => api.setStatus(id, "resolved"), "Resolved.")
                }
              >
                Resolve
              </Button>
              <Button
                size="sm"
                variant="secondary"
                onClick={() =>
                  void run(
                    () => api.setStatus(id, "pending"),
                    "Marked pending.",
                  )
                }
              >
                Waiting on customer
              </Button>
              <Button
                size="sm"
                variant="secondary"
                icon="clock"
                onClick={() =>
                  void run(
                    () => api.setStatus(id, "snoozed", tomorrow()),
                    "Snoozed until tomorrow.",
                  )
                }
              >
                Snooze
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() =>
                  void run(() => api.setStatus(id, "spam"), "Marked as spam.")
                }
              >
                Spam
              </Button>
            </>
          )}
        </div>
        {conv.status !== "resolved" && conv.status !== "spam" && (
          <Select
            label="Hand over to"
            value=""
            options={[
              { value: "", label: "Choose…" },
              ...(supervisor ? [{ value: "0", label: "The pool" }] : []),
              ...agents
                .filter((a) => a.id !== conv.assigneeUserId)
                .map((a) => ({
                  value: String(a.id),
                  label: a.displayName || a.username,
                })),
            ]}
            onChange={(e) => {
              if (e.target.value !== "") {
                void run(
                  () => api.assign(id, Number(e.target.value)),
                  "Handed over.",
                );
              }
            }}
          />
        )}
        <form
          className="cf-stack"
          onSubmit={(e) => {
            e.preventDefault();
            const list = tags
              .split(",")
              .map((t) => t.trim())
              .filter(Boolean);
            void run(() => api.setTags(id, list), "Tags saved.");
          }}
        >
          <Input
            label="Tags"
            hint="Separated by commas."
            value={tags}
            onChange={(e) => setTags(e.target.value)}
          />
          <Button type="submit" size="sm" variant="secondary">
            Save tags
          </Button>
        </form>
      </div>
      {toast.node}
    </>
  );
}

const VIEWS = [
  { value: "all", label: "All" },
  { value: "mine", label: "Mine" },
  { value: "unassigned", label: "Unassigned" },
];

const STATUSES = [
  { value: "", label: "Active" },
  { value: "resolved", label: "Resolved" },
  { value: "spam", label: "Spam" },
  { value: "any", label: "Everything" },
];

interface ViewProps {
  api: InboxApi;
  supervisor: boolean;
}

function InboxView({ api, supervisor }: ViewProps) {
  const [view, setView] = useState<"all" | "mine" | "unassigned">(
    supervisor ? "all" : "mine",
  );
  const [status, setStatus] = useState<StatusFilter>("");
  const [search, setSearch] = useState("");
  const [q, setQ] = useState("");
  const [selected, setSelected] = useState<number | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);
  const [agents, setAgents] = useState<InboxAgent[]>([]);
  const [sound, setSound] = useState(
    () => window.localStorage.getItem(SOUND_KEY) === "on",
  );
  const [permission, setPermission] = useState<NotificationPermission | "none">(
    typeof Notification === "undefined" ? "none" : Notification.permission,
  );
  const selectedRef = useRef<number | null>(null);
  useEffect(() => {
    selectedRef.current = selected;
  }, [selected]);

  const load = useCallback(
    (signal: AbortSignal) =>
      api.conversations(
        { view: supervisor ? view : "all", status, q: q || undefined },
        signal,
      ),
    [api, supervisor, view, status, q],
  );
  const convs = usePolling(load, REFRESH_MS);
  const reload = convs.reload;

  useEffect(() => {
    const controller = new AbortController();
    listInboxAgents(controller.signal)
      .then(setAgents)
      .catch(() => undefined);
    return () => controller.abort();
  }, []);

  const onEvent = useCallback(
    (e: InboxEvent) => {
      reload();
      if (e.conversationId === selectedRef.current) {
        setRefreshKey((k) => k + 1);
        return;
      }
      if (e.type !== "message.in") return;
      if (sound) beep();
      if (
        typeof Notification !== "undefined" &&
        Notification.permission === "granted"
      ) {
        // The text itself is never in a notification.
        void api
          .conversation(e.conversationId)
          .then((c) => {
            new Notification(`New message from ${peerLabel(c)}`, {
              tag: `conversation-${c.id}`,
            });
          })
          .catch(() => undefined);
      }
    },
    [api, reload, sound],
  );
  // After a reconnect the page reads what it may have missed.
  useInboxStream(onEvent, reload);

  const items = useMemo(
    () => (convs.status === "loading" ? [] : (convs.data?.items ?? [])),
    [convs],
  );
  const unread = items.reduce((n, c) => n + c.unread, 0);
  useEffect(() => {
    const base = document.title.replace(/^\(\d+\) /, "");
    document.title = unread > 0 ? `(${unread}) ${base}` : base;
    return () => {
      document.title = base;
    };
  }, [unread]);

  const current = items.find((c) => c.id === selected) ?? null;
  const changed = useCallback(() => reload(), [reload]);

  return (
    <div className="inbox-layout">
      <div className="az-card inbox-panel">
        <div className="inbox-filters">
          <form
            onSubmit={(e) => {
              e.preventDefault();
              setQ(search.trim());
            }}
          >
            <Input
              label="Search"
              icon="search"
              placeholder="Number, name or text"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </form>
          {supervisor && (
            <Select
              label="Show"
              value={view}
              options={VIEWS}
              onChange={(e) => setView(e.target.value as typeof view)}
            />
          )}
          <Select
            label="State"
            value={status}
            options={STATUSES}
            onChange={(e) => setStatus(e.target.value as StatusFilter)}
          />
          <Switch
            label="Sound for new texts"
            labelPosition="end"
            checked={sound}
            onChange={(e) => {
              setSound(e.target.checked);
              window.localStorage.setItem(
                SOUND_KEY,
                e.target.checked ? "on" : "off",
              );
            }}
          />
          {permission === "default" && (
            <Button
              size="sm"
              variant="secondary"
              icon="bell"
              onClick={() =>
                void Notification.requestPermission().then(setPermission)
              }
            >
              Enable desktop notifications
            </Button>
          )}
        </div>
        {convs.status === "loading" && (
          <Spinner label="Loading conversations…" />
        )}
        {convs.status === "error" && (
          <Alert tone="bad" title="Could not load the conversations">
            {convs.message}
          </Alert>
        )}
        {convs.status !== "loading" && items.length === 0 && (
          <EmptyState
            icon="inbox"
            title="No conversations"
            description="Customer texts to your numbers appear here."
          />
        )}
        <ul className="inbox-list" aria-label="Conversations">
          {items.map((c) => (
            <li key={c.id}>
              <button
                type="button"
                className="inbox-conv"
                aria-current={c.id === selected}
                onClick={() => setSelected(c.id)}
              >
                <span className="inbox-conv__top">
                  <span>{peerLabel(c)}</span>
                  <span className="az-caption">
                    {formatTime(c.lastMessageAt)}
                  </span>
                </span>
                <span className="inbox-conv__preview">{c.preview}</span>
                <span className="inbox-conv__meta">
                  <Badge tone={statusTone(c.status)}>{c.status}</Badge>
                  {c.unread > 0 && (
                    <Badge tone="pillar">{c.unread} unread</Badge>
                  )}
                  {c.assigneeUserId === null && c.status !== "resolved" && (
                    <Badge tone="warn">Unassigned</Badge>
                  )}
                  {c.optedOut && <Badge tone="bad">Opted out</Badge>}
                </span>
              </button>
            </li>
          ))}
        </ul>
      </div>
      {current ? (
        <Thread
          key={current.id}
          api={api}
          conversation={current}
          agents={agents}
          supervisor={supervisor}
          onChanged={changed}
          refreshKey={refreshKey}
        />
      ) : (
        <div style={{ gridColumn: "span 2" }}>
          <EmptyState
            icon="messages-square"
            title="Choose a conversation"
            description="Pick one from the list to read it and answer."
          />
        </div>
      )}
    </div>
  );
}

function Shell({ api, supervisor }: ViewProps) {
  const status = usePolling(getInboxStatus, REFRESH_MS);
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow={supervisor ? "Messaging" : "My phone"}
        title={supervisor ? "Inbox" : "My inbox"}
        description={
          supervisor
            ? "Every customer text conversation. Reading one that is not yours is recorded in the audit log."
            : "The customer text conversations assigned to you, and those you answered."
        }
      />
      {status.status === "ready" && !status.data.enabled ? (
        <EmptyState
          icon="inbox"
          title="No messaging channel yet"
          description="The inbox shows once an administrator adds a channel and a number."
          action={
            <Can min="admin">
              <LinkButton to="/messaging/channels">Set up a channel</LinkButton>
            </Can>
          }
        />
      ) : (
        <InboxView api={api} supervisor={supervisor} />
      )}
    </section>
  );
}

/** The supervisors' inbox: every conversation. */
export function Inbox() {
  return <Shell api={supervisorInbox} supervisor />;
}

/** An agent's own inbox: the conversations assigned to them. */
export function MyInbox() {
  return <Shell api={myInbox} supervisor={false} />;
}
