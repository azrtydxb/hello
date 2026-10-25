import { useCallback, useEffect, useState, type FormEvent } from "react";
import {
  ApiError,
  errorMessage,
  listExtensions,
  listPagingGroups,
  listRingGroups,
  type Extension,
  type PagingGroup,
  type RingGroup,
} from "../../api";
import {
  type Conversation,
  type Message,
  type MessageStatus,
  type MessagesApi,
  type TargetKind,
} from "../../api/messages";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  EmptyState,
  Input,
  Modal,
  Select,
  Spinner,
  useRestoreFocus,
  useToast,
} from "../../design/azrty/components";
import { formatTime } from "../../format";
import "./messages.css";

const MAX_CHARS = 700;

/** What a status says, in words: it is never colour alone. */
function statusBadge(status: MessageStatus) {
  switch (status) {
    case "delivered":
      return <Badge tone="good">Delivered</Badge>;
    case "failed":
      return <Badge tone="bad">Failed</Badge>;
    default:
      return <Badge tone="warn">Waiting</Badge>;
  }
}

/** Who a conversation is with, for its title. */
export function conversationTitle(c: Conversation): string {
  switch (c.scope) {
    case "direct":
      return c.parties.join(" and ");
    case "broadcast":
      return "Everyone";
    case "ring_group":
      return `Ring group ${c.scopeRef}`;
    default:
      return `Paging group ${c.scopeRef}`;
  }
}

function whoLabel(m: Message): string {
  const kind =
    m.senderKind === "system"
      ? "Admin"
      : m.senderKind === "external"
        ? `SMS ${m.sender}`
        : `Ext ${m.sender}`;
  return m.senderName && m.senderKind !== "system"
    ? `${kind} (${m.senderName})`
    : kind;
}

function toLabel(m: Message): string {
  switch (m.scope) {
    case "ring_group":
      return `ring group ${m.scopeRef}`;
    case "paging_group":
      return `paging group ${m.scopeRef}`;
    case "broadcast":
      return "everyone";
    default:
      return m.recipientKind === "external" ? m.recipient : `ext ${m.recipient}`;
  }
}

function deliveryText(m: Message): string {
  if (m.recipients <= 1) {
    if (m.status === "delivered") return `Delivered ${formatTime(m.deliveredAt)}`;
    if (m.status === "failed") return m.detail || "Not delivered";
    return m.detail
      ? `Waiting: ${m.detail}`
      : `Waiting until ${formatTime(m.expiresAt)}`;
  }
  const parts = [`${m.delivered} of ${m.recipients} delivered`];
  if (m.pending > 0) parts.push(`${m.pending} waiting`);
  if (m.failed > 0) parts.push(`${m.failed} failed`);
  return parts.join(", ");
}

type Directory = {
  extensions: Extension[];
  ringGroups: RingGroup[];
  pagingGroups: PagingGroup[];
  error: string | null;
};

/** The extensions and groups a message can be addressed to. */
function useDirectory(): Directory {
  const [dir, setDir] = useState<Directory>({
    extensions: [],
    ringGroups: [],
    pagingGroups: [],
    error: null,
  });
  useEffect(() => {
    const controller = new AbortController();
    Promise.all([
      listExtensions(controller.signal),
      listRingGroups(controller.signal),
      listPagingGroups(controller.signal),
    ])
      .then(([extensions, ringGroups, pagingGroups]) =>
        setDir({ extensions, ringGroups, pagingGroups, error: null }),
      )
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setDir((d) => ({ ...d, error: errorMessage(err) }));
        }
      });
    return () => controller.abort();
  }, []);
  return dir;
}

const KIND_LABELS: Record<TargetKind, string> = {
  extension: "An extension",
  ring_group: "A ring group",
  paging_group: "A paging group",
  broadcast: "All phones",
  external: "An external number (SMS)",
};

/** Write a message and send it. */
export function ComposeDialog({
  api,
  allowBroadcast,
  onClose,
  onSent,
}: {
  api: MessagesApi;
  allowBroadcast: boolean;
  onClose: () => void;
  onSent: (recipients: number) => void;
}) {
  useRestoreFocus();
  const dir = useDirectory();
  const [kind, setKind] = useState<TargetKind>("extension");
  const [target, setTarget] = useState("");
  const [body, setBody] = useState("");
  const [confirm, setConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const kinds = (Object.keys(KIND_LABELS) as TargetKind[]).filter(
    (k) => allowBroadcast || k !== "broadcast",
  );
  const options = (() => {
    switch (kind) {
      case "extension":
        return dir.extensions.map((e) => ({
          value: e.number,
          label: `${e.number} ${e.name}`,
        }));
      case "ring_group":
        return dir.ringGroups.map((g) => ({ value: g.name, label: g.name }));
      case "paging_group":
        return dir.pagingGroups.map((g) => ({
          value: g.name,
          label: `${g.name} (${g.code})`,
        }));
      default:
        return [];
    }
  })();

  function pickKind(k: TargetKind) {
    setKind(k);
    setTarget("");
    setConfirm(false);
    setError(null);
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    if (kind !== "broadcast" && target.trim() === "") {
      setError("Choose who the message is for.");
      return;
    }
    if (body.trim() === "") {
      setError("Write a message.");
      return;
    }
    setBusy(true);
    try {
      const sent = await api.send({
        targetKind: kind,
        target: kind === "broadcast" ? "" : target.trim(),
        body,
        ...(kind === "broadcast" ? { confirm: true } : {}),
      });
      onSent(sent.recipients);
    } catch (err: unknown) {
      setError(
        err instanceof ApiError && err.status === 404
          ? "That extension or group does not exist, or has no members."
          : errorMessage(err),
      );
      setBusy(false);
    }
  }

  return (
    <Modal
      title="New message"
      description="Delivered to the phones of the recipient. A phone that is switched off gets it when it registers again."
      onClose={onClose}
      actions={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="message-form"
            icon="send"
            disabled={busy || (kind === "broadcast" && !confirm)}
          >
            Send message
          </Button>
        </>
      }
    >
      <form id="message-form" className="cf-stack" onSubmit={submit}>
        {dir.error && (
          <Alert tone="warn" title="Could not load extensions and groups">
            {dir.error}
          </Alert>
        )}
        <Select
          label="Send to"
          value={kind}
          options={kinds.map((k) => ({ value: k, label: KIND_LABELS[k] }))}
          onChange={(e) => pickKind(e.target.value as TargetKind)}
        />
        {options.length > 0 || kind === "extension" ? (
          kind !== "broadcast" &&
          kind !== "external" && (
            <Select
              label="Recipient"
              value={target}
              options={[{ value: "", label: "Choose…" }, ...options]}
              onChange={(e) => setTarget(e.target.value)}
            />
          )
        ) : (
          kind !== "broadcast" &&
          kind !== "external" && (
            <Alert tone="info" title="Nothing to choose from">
              There is no {kind === "ring_group" ? "ring" : "paging"} group yet.
            </Alert>
          )
        )}
        {kind === "external" && (
          <Input
            label="Phone number"
            hint="With the country code, for example +32475000000. Goes out through a trunk that has SMS over SIP turned on."
            value={target}
            onChange={(e) => setTarget(e.target.value)}
            mono
          />
        )}
        {kind === "broadcast" && (
          <Checkbox
            label="Send to every phone"
            hint="Every extension that has a device gets this message."
            checked={confirm}
            onChange={(e) => setConfirm(e.target.checked)}
          />
        )}
        <div className="az-field">
          <label className="az-field__label" htmlFor="message-body">
            Message
          </label>
          <textarea
            id="message-body"
            className="az-input az-textarea"
            rows={4}
            maxLength={MAX_CHARS}
            value={body}
            onChange={(e) => setBody(e.target.value)}
          />
          <span className="az-field__hint msg-count">
            {body.length} / {MAX_CHARS}
          </span>
        </div>
        {error && (
          <Alert tone="bad" title="Could not send">
            {error}
          </Alert>
        )}
      </form>
    </Modal>
  );
}

/** One message in a thread. */
function MessageItem({ m }: { m: Message }) {
  const text = m.contentType.toLowerCase().startsWith("text/") || m.body !== "";
  return (
    <li className="msg-item">
      <div className="msg-item__meta">
        <strong>{whoLabel(m)}</strong>
        <span>to {toLabel(m)}</span>
        <span>{formatTime(m.createdAt)}</span>
        {m.trunk && <Badge tone="outline">via {m.trunk}</Badge>}
        {statusBadge(m.status)}
      </div>
      <p className="msg-item__body">
        {text ? m.body : `(${m.contentType}, ${m.bodyBytes} bytes)`}
      </p>
      <div className="msg-item__meta">{deliveryText(m)}</div>
    </li>
  );
}

type ConvState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: Conversation[] };

type ThreadState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: Message[]; next: string };

/**
 * Conversations beside the open thread, with search and a status filter, and
 * a button to write a new message. `canRead` is false for an operator, who
 * may send but not read other people's messages.
 */
export function Workspace({
  api,
  canRead,
  canSend,
  allowBroadcast,
  heading,
}: {
  api: MessagesApi;
  canRead: boolean;
  canSend: boolean;
  allowBroadcast: boolean;
  heading: string;
}) {
  const [conv, setConv] = useState<ConvState>({ status: "loading" });
  const [thread, setThread] = useState<ThreadState>({ status: "idle" });
  const [selected, setSelected] = useState<string | null>(null);
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState<MessageStatus | "">("");
  const [applied, setApplied] = useState<{
    q: string;
    status: MessageStatus | "";
  }>({ q: "", status: "" });
  const [composing, setComposing] = useState(false);
  const [reload, setReload] = useState(0);
  const toast = useToast();

  useEffect(() => {
    if (!canRead) return;
    const controller = new AbortController();
    api
      .conversations(applied, controller.signal)
      .then((items) => setConv({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setConv({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [api, canRead, applied, reload]);

  useEffect(() => {
    if (!canRead || selected === null) return;
    const controller = new AbortController();
    api
      .list({ conversation: selected, status: applied.status, q: applied.q }, controller.signal)
      .then((p) =>
        setThread({ status: "ready", items: p.items, next: p.next }),
      )
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setThread({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [api, canRead, selected, applied, reload]);

  const loadOlder = useCallback(async () => {
    if (selected === null || thread.status !== "ready" || !thread.next) return;
    try {
      const p = await api.list({
        conversation: selected,
        status: applied.status,
        q: applied.q,
        cursor: thread.next,
      });
      setThread({
        status: "ready",
        items: [...thread.items, ...p.items],
        next: p.next,
      });
    } catch (err: unknown) {
      setThread({ status: "error", message: errorMessage(err) });
    }
  }, [api, selected, thread, applied]);

  function open(key: string) {
    setThread({ status: "loading" });
    setSelected(key);
  }

  function onSearch(e: FormEvent) {
    e.preventDefault();
    setSelected(null);
    setThread({ status: "idle" });
    setConv({ status: "loading" });
    setApplied({ q: search.trim(), status });
  }

  const newButton = canSend && (
    <Button icon="send" onClick={() => setComposing(true)}>
      New message
    </Button>
  );

  return (
    <div className="cf-stack">
      <div className="msg-toolbar">
        {canRead && (
          <form className="msg-toolbar" onSubmit={onSearch} role="search">
            <Input
              label="Search messages"
              icon="search"
              placeholder="Text, extension or group"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
            <Select
              label="Status"
              value={status}
              options={[
                { value: "", label: "Any status" },
                { value: "pending", label: "Waiting" },
                { value: "delivered", label: "Delivered" },
                { value: "failed", label: "Failed" },
              ]}
              onChange={(e) => setStatus(e.target.value as MessageStatus | "")}
            />
            <Button type="submit" variant="secondary" icon="search">
              Search
            </Button>
          </form>
        )}
        {newButton}
      </div>

      {!canRead && (
        <Alert tone="info" title="Message history is private">
          Message text is visible to administrators only. You can send messages
          from here.
        </Alert>
      )}

      {canRead && (
        <div className="msg-layout">
          <section className="az-card msg-panel" aria-label="Conversations">
            <div className="msg-panel__head">
              <strong>{heading}</strong>
            </div>
            {conv.status === "loading" && <Spinner label="Loading conversations…" />}
            {conv.status === "error" && (
              <Alert tone="bad" title="Could not load conversations">
                {conv.message}
              </Alert>
            )}
            {conv.status === "ready" && conv.items.length === 0 && (
              <EmptyState
                icon="message-square"
                title="No messages"
                description={
                  applied.q || applied.status
                    ? "Nothing matches this search."
                    : "Messages sent between phones, to groups and to everyone show here."
                }
              />
            )}
            {conv.status === "ready" && conv.items.length > 0 && (
              <ul className="msg-list">
                {conv.items.map((c) => (
                  <li key={c.key}>
                    <button
                      type="button"
                      className="msg-conv"
                      aria-current={selected === c.key}
                      onClick={() => open(c.key)}
                    >
                      <span className="msg-conv__top">
                        <span>{conversationTitle(c)}</span>
                        <span>{formatTime(c.lastAt)}</span>
                      </span>
                      <span className="msg-conv__snippet">
                        {c.lastSnippet || "(no text)"}
                      </span>
                      <span>
                        {statusBadge(c.lastStatus)} {c.messages}{" "}
                        {c.messages === 1 ? "message" : "messages"}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </section>

          <section className="az-card msg-panel" aria-label="Thread">
            {selected === null && (
              <EmptyState
                icon="messages-square"
                title="Pick a conversation"
                description="Its messages and whether they were delivered show here."
              />
            )}
            {thread.status === "loading" && <Spinner label="Loading messages…" />}
            {thread.status === "error" && (
              <Alert tone="bad" title="Could not load the messages">
                {thread.message}
              </Alert>
            )}
            {thread.status === "ready" && (
              <div className="msg-thread">
                {thread.next && (
                  <Button variant="secondary" onClick={() => void loadOlder()}>
                    Load older messages
                  </Button>
                )}
                <ul className="msg-list cf-stack" aria-label="Messages">
                  {[...thread.items].reverse().map((m) => (
                    <MessageItem key={m.id} m={m} />
                  ))}
                </ul>
              </div>
            )}
          </section>
        </div>
      )}

      {composing && (
        <ComposeDialog
          api={api}
          allowBroadcast={allowBroadcast}
          onClose={() => setComposing(false)}
          onSent={(n) => {
            setComposing(false);
            toast.show(
              n === 1 ? "Message queued for 1 recipient." : `Message queued for ${n} recipients.`,
            );
            setReload((r) => r + 1);
          }}
        />
      )}
      {toast.node}
    </div>
  );
}
