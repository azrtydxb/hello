import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type FormEvent,
} from "react";
import { errorMessage, fieldErrors } from "../api";
import {
  getInboxStatus,
  listInboxAgents,
  myInbox,
  supervisorInbox,
  type Conversation,
  type ConversationStatus,
  type InboxAgent,
  type InboxApi,
  type InboxCall,
  type InboxMessage,
  type StatusFilter,
} from "../api/inbox";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
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
import { LinkedText } from "./inbox/Linkified";
import { peerLabel, smsSegments } from "./inbox/sms";
import { StartConversation } from "./inbox/StartConversation";
import { useInboxStream, type InboxEvent } from "./inbox/useInboxStream";
import "./inbox/inbox.css";

const REFRESH_MS = 30_000;
const SOUND_KEY = "hello.inbox.sound";

/** What a reply's delivery state says, in words (never colour alone). */
function deliveryText(m: InboxMessage, chat: boolean): string {
  switch (m.status) {
    case "queued":
      return "Queued";
    case "sending":
      return chat ? "Sending the email" : "Sending";
    case "sent":
      return chat ? "Sent" : "Sent to the carrier";
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
  /** Whether email is configured, so an offline chat can be answered by it. */
  emailReady: boolean;
  /** The visitor of this chat is typing. */
  visitorTyping: boolean;
}

/** The thread, its composer and the details of one conversation. */
function Thread({
  api,
  conversation,
  agents,
  supervisor,
  onChanged,
  refreshKey,
  emailReady,
  visitorTyping,
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
  const [calls, setCalls] = useState<InboxCall[] | null>(null);
  const [callsFailed, setCallsFailed] = useState(false);
  const clientId = useRef(newClientId());
  const lastTyping = useRef(0);
  const trusted = useRef(new Set<string>());
  const [leaving, setLeaving] = useState<URL | null>(null);
  const [blocking, setBlocking] = useState(false);
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

  // The customer's recent calls, for a number (a chat visitor has none).
  const isPhone = conv.peerKind === "phone";
  useEffect(() => {
    if (!isPhone) return;
    const controller = new AbortController();
    api
      .calls(id, controller.signal)
      .then((r) => {
        if (controller.signal.aborted) return;
        setCalls(r.items);
        setCallsFailed(false);
      })
      .catch(() => {
        if (!controller.signal.aborted) setCallsFailed(true);
      });
    return () => controller.abort();
  }, [api, id, isPhone, refreshKey]);

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

  const chat = conv.channelKind === "webchat";
  const sms = smsSegments(draft);
  const canReply = !conv.optedOut && conv.status !== "spam";

  /** A link in a visitor's message leaves Hello: ask once per host. */
  function openLink(url: URL) {
    if (trusted.current.has(url.host)) {
      window.open(url.href, "_blank", "noopener,noreferrer");
    } else {
      setLeaving(url);
    }
  }

  /** Tells the visitor an agent is typing, at most every three seconds. */
  function typingPing() {
    const now = Date.now();
    if (!chat || mode !== "reply" || now - lastTyping.current < 3000) return;
    lastTyping.current = now;
    void api.typing(id).catch(() => undefined);
  }

  async function sendByEmail() {
    const text = draft.trim();
    if (!text) return;
    setBusy(true);
    setSendError(null);
    try {
      await api.emailReply(id, text, clientId.current);
      clientId.current = newClientId();
      setDraft("");
      onChanged();
      await load();
      toast.show("Emailed.");
    } catch (err: unknown) {
      setSendError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

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
              {chat
                ? `Website chat · ${conv.channelName}`
                : `to ${conv.address} · ${conv.channelName}`}
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
        {conv.chat?.offline && (
          <Alert tone="info" title="Left while nobody was available">
            {conv.chat.email
              ? "Answer by email: the visitor sees the reply in the widget when they return."
              : "The visitor left no email address, so there is nobody to email."}
          </Alert>
        )}
        {conv.chat?.blocked && (
          <Alert tone="warn" title="This visitor is blocked">
            They can no longer chat. A supervisor can lift the block on the Chat
            sites page.
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
                        {deliveryText(m, chat)}
                      </Badge>
                    )}
                  </div>
                )}
                {/* Customer text is shown as text, never as markup; in a chat
                    its links are made clickable, asking before one leaves Hello. */}
                <p className="inbox-msg__body">
                  {chat && m.kind === "message" && m.direction === "in" ? (
                    <LinkedText text={m.body} onOpen={openLink} />
                  ) : (
                    m.body
                  )}
                </p>
              </li>
            );
          })}
        </ol>
        {visitorTyping && (
          <p className="az-caption" role="status" style={{ margin: "0 16px" }}>
            The visitor is typing…
          </p>
        )}
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
            onChange={(e) => {
              setDraft(e.target.value);
              typingPing();
            }}
          />
          {sendError && (
            <Alert tone="bad" title="Not sent">
              {sendError}
            </Alert>
          )}
          <div className="inbox-composer__foot">
            <span className="az-caption">
              {mode === "reply"
                ? chat
                  ? `${[...draft].length} characters`
                  : `${sms.chars} characters, ${sms.segments} ${sms.segments === 1 ? "text" : "texts"}${sms.unicode ? " (non-GSM characters)" : ""}`
                : "Never sent to the customer"}
            </span>
            {chat && conv.chat?.offline && mode === "reply" && (
              <Button
                type="button"
                variant="secondary"
                icon="mail"
                disabled={
                  busy ||
                  draft.trim() === "" ||
                  !emailReady ||
                  !conv.chat.email ||
                  !canReply
                }
                title={
                  emailReady
                    ? undefined
                    : "Email is not set up: an administrator configures it under Settings, Email."
                }
                onClick={() => void sendByEmail()}
              >
                Reply by email
              </Button>
            )}
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
          <dt className="az-caption">{chat ? "Visitor" : "Customer"}</dt>
          <dd style={{ margin: 0 }}>
            {chat ? peerLabel({ ...conv, peerName: "" }) : conv.peer}
          </dd>
          {chat && conv.chat?.email && (
            <>
              <dt className="az-caption">Email</dt>
              <dd style={{ margin: 0 }}>{conv.chat.email}</dd>
            </>
          )}
          {chat && conv.chat?.page && (
            <>
              <dt className="az-caption">Started on</dt>
              <dd style={{ margin: 0 }}>{conv.chat.page}</dd>
            </>
          )}
          {chat && conv.chat?.browser && (
            <>
              <dt className="az-caption">Browser</dt>
              <dd style={{ margin: 0 }}>{conv.chat.browser}</dd>
            </>
          )}
          {conv.peerName && (
            <>
              <dt className="az-caption">Name</dt>
              <dd style={{ margin: 0 }}>{conv.peerName}</dd>
            </>
          )}
          <dt className="az-caption">Contact</dt>
          <dd style={{ margin: 0 }}>
            <AddContact
              api={api}
              id={id}
              supervisor={supervisor}
              named={conv.peerName !== ""}
              onSaved={(c) => {
                setConv(c);
                toast.show("Saved to contacts.");
                onChanged();
              }}
            />
          </dd>
          {isPhone && (
            <>
              <dt className="az-caption">Call</dt>
              <dd style={{ margin: 0 }}>
                {/* The existing click-to-call page: it rings your phone first. */}
                <LinkButton
                  to={`/dial?n=${encodeURIComponent(conv.peer)}`}
                  size="sm"
                  icon="phone-outgoing"
                >
                  Call {conv.peer}
                </LinkButton>
              </dd>
              <dt className="az-caption">Recent calls</dt>
              <dd style={{ margin: 0 }}>
                <RecentCalls calls={calls} failed={callsFailed} />
              </dd>
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
        {chat && conv.chat && !conv.chat.blocked && (
          <div className="inbox-actions">
            <Button
              size="sm"
              variant="ghost"
              icon="ban"
              onClick={() => setBlocking(true)}
            >
              Block visitor
            </Button>
          </div>
        )}
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
      {leaving && (
        <ConfirmDialog
          title="This link leaves Hello"
          description={`The visitor wrote a link to ${leaving.host}. Open it only if you trust it.`}
          confirmLabel="Open the link"
          confirmIcon="external-link"
          confirmVariant="primary"
          errorTitle="Could not open the link"
          onClose={() => setLeaving(null)}
          onConfirm={() => {
            trusted.current.add(leaving.host);
            window.open(leaving.href, "_blank", "noopener,noreferrer");
            setLeaving(null);
            return Promise.resolve();
          }}
        >
          <p className="az-caption" style={{ overflowWrap: "anywhere" }}>
            {leaving.href}
          </p>
        </ConfirmDialog>
      )}
      {blocking && (
        <ConfirmDialog
          title="Block this visitor?"
          description="They can no longer chat for 24 hours, from this device or, today, from this network address. They are not told, and the conversation is resolved."
          confirmLabel="Block visitor"
          confirmIcon="ban"
          errorTitle="Could not block the visitor"
          onClose={() => setBlocking(false)}
          onConfirm={async () => {
            await api.block(id);
            setBlocking(false);
            toast.show("Visitor blocked.");
            onChanged();
            await load();
          }}
        />
      )}
      {toast.node}
    </>
  );
}

/** The customer's recent calls, newest first. */
function RecentCalls({
  calls,
  failed,
}: {
  calls: InboxCall[] | null;
  failed: boolean;
}) {
  if (failed) return <span className="az-caption">Calls unavailable.</span>;
  if (calls === null) return <Spinner />;
  if (calls.length === 0)
    return <span className="az-caption">No recent calls.</span>;
  return (
    <ul style={{ margin: 0, paddingLeft: "1.1em" }} aria-label="Recent calls">
      {calls.map((c) => (
        <li key={c.id}>
          {c.direction === "inbound" ? "Incoming" : "Outgoing"},{" "}
          {c.answered
            ? `${Math.max(1, Math.round(c.durationMs / 1000))} s`
            : "not answered"}
          , {formatTime(c.startTime)}
        </li>
      ))}
    </ul>
  );
}

/**
 * "Add to contacts": names the customer's number. A personal contact names it
 * for the person who saves it; a supervisor may save it to the shared company
 * book instead.
 */
function AddContact({
  api,
  id,
  supervisor,
  named,
  onSaved,
}: {
  api: InboxApi;
  id: number;
  supervisor: boolean;
  named: boolean;
  onSaved: (c: Conversation) => void;
}) {
  const [open, setOpen] = useState(false);
  const [first, setFirst] = useState("");
  const [last, setLast] = useState("");
  const [company, setCompany] = useState("");
  const [book, setBook] = useState<"personal" | "company">("personal");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (!open) {
    return (
      <Button
        size="sm"
        variant="secondary"
        icon="user-plus"
        onClick={() => setOpen(true)}
      >
        {named ? "Add another contact" : "Add to contacts"}
      </Button>
    );
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      const c = await api.addContact(id, {
        firstName: first.trim(),
        lastName: last.trim(),
        company: company.trim(),
        book,
      });
      setOpen(false);
      setFirst("");
      setLast("");
      setCompany("");
      onSaved(c);
    } catch (err: unknown) {
      const f = fieldErrors(err)[0];
      setError(f ? `${f.path}: ${f.message}` : errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      className="cf-stack"
      onSubmit={(e) => void save(e)}
      aria-label="Add to contacts"
    >
      {error && <Alert tone="bad">{error}</Alert>}
      <Input
        label="First name"
        value={first}
        onChange={(e) => setFirst(e.target.value)}
      />
      <Input
        label="Last name"
        value={last}
        onChange={(e) => setLast(e.target.value)}
      />
      <Input
        label="Company"
        value={company}
        onChange={(e) => setCompany(e.target.value)}
      />
      {supervisor && (
        <Select
          label="Save to"
          value={book}
          options={[
            { value: "personal", label: "My contacts" },
            { value: "company", label: "Company book (everyone)" },
          ]}
          onChange={(e) => setBook(e.target.value as "personal" | "company")}
        />
      )}
      <div className="inbox-actions">
        <Button type="submit" size="sm" disabled={busy}>
          Save contact
        </Button>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          onClick={() => setOpen(false)}
        >
          Cancel
        </Button>
      </div>
    </form>
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

function InboxView({
  api,
  supervisor,
  emailReady,
}: ViewProps & { emailReady: boolean }) {
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
  const [typing, setTyping] = useState<number | null>(null);
  const typingTimer = useRef<ReturnType<typeof setTimeout>>(undefined);
  useEffect(() => {
    selectedRef.current = selected;
  }, [selected]);
  useEffect(() => () => clearTimeout(typingTimer.current), []);

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
      if (e.type === "typing") {
        // A visitor typing changes nothing to read: show it for a moment.
        setTyping(e.conversationId);
        clearTimeout(typingTimer.current);
        typingTimer.current = setTimeout(() => setTyping(null), 4000);
        return;
      }
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
                  {c.channelKind === "webchat" && (
                    <Badge tone="neutral">Chat</Badge>
                  )}
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
          emailReady={emailReady}
          visitorTyping={typing === current.id}
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
  const toast = useToast();
  const [starting, setStarting] = useState(false);
  const senders = status.status === "ready" ? (status.data.senders ?? []) : [];
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
        <>
          {supervisor && senders.length > 0 && (
            <div className="msg-toolbar">
              <Button icon="send" onClick={() => setStarting(true)}>
                Text a customer
              </Button>
            </div>
          )}
          <InboxView
            api={api}
            supervisor={supervisor}
            emailReady={
              status.status === "ready" && status.data.emailReply === true
            }
          />
        </>
      )}
      {starting && (
        <StartConversation
          senders={senders}
          onClose={() => setStarting(false)}
          onStarted={() => {
            setStarting(false);
            toast.show("The text is queued and will be sent in a moment.");
          }}
        />
      )}
      {toast.node}
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
