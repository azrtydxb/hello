import { useCallback, useEffect, useRef, useState } from "react";
import { Link, NavLink, useNavigate, useParams } from "react-router";
import { errorMessage } from "../api";
import {
  createSession,
  deleteSession,
  getProposal,
  getSession,
  getTask,
  listSessions,
  MESSAGE_LIMIT,
  TASK_POLL_MS,
  type AIMessage,
  type AIProposal,
  type AISession,
  type AISessionDetail,
  type AITask,
} from "../api/aiagent";
import { streamMessage, type StreamEvent } from "../api/aistream";
import { AIGate } from "../components/ai/AIGate";
import { PlainText } from "../components/ai/PlainText";
import { ProposalCard } from "../components/ai/ProposalCard";
import { TaskStatus } from "../components/ai/TaskStatus";
import { TypingIndicator } from "../components/ai/TypingIndicator";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  PageHeader,
  Spinner,
} from "../design/azrty/components";
import { useCan } from "../role";
import "../components/ai/ai.css";

function InlineProposal({ id }: { id: string }) {
  const [p, setP] = useState<AIProposal | null>(null);
  useEffect(() => {
    const c = new AbortController();
    getProposal(id, c.signal).then(setP, () => undefined);
    return () => c.abort();
  }, [id]);
  return p ? <ProposalCard proposal={p} /> : null;
}

/** The data each answer used: the cited tool calls (S-9). */
function Sources({ m }: { m: AIMessage }) {
  const calls = m.toolCalls ?? [];
  const cited = (m.citations ?? [])
    .map((i) => calls[i])
    .filter((c) => c !== undefined);
  return (
    <div>
      <p className="aix-muted">Data used</p>
      {cited.length === 0 ? (
        <p className="aix-muted">No data was read for this answer.</p>
      ) : (
        <ul className="aix-sources">
          {cited.map((c, i) => (
            <li key={i}>
              {c.operationId} ({c.status})
              {c.truncated ? " — result truncated" : ""}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Message({ m }: { m: AIMessage }) {
  const user = m.role === "user";
  return (
    <div className={`aix-msg${user ? " aix-msg--user" : ""}`}>
      <p className="aix-msg__role">{user ? "You" : "Assistant"}</p>
      <PlainText text={m.content} />
      {!user && <Sources m={m} />}
      {m.proposalId && <InlineProposal id={m.proposalId} />}
    </div>
  );
}

/** What the indicator says before the assistant reports a step. */
const WORKING = "The assistant is working…";
const THINKING = "Thinking…";

/** The answer being generated: the step it is on and the text so far. */
interface Live {
  step: string;
  text: string;
}

function Chat({ id }: { id: string | undefined }) {
  const navigate = useNavigate();
  const [sessions, setSessions] = useState<AISession[]>([]);
  const [detail, setDetail] = useState<AISessionDetail | null>(null);
  const [task, setTask] = useState<AITask | null>(null);
  const [pending, setPending] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [live, setLive] = useState<Live | null>(null);
  const [stopped, setStopped] = useState(false);
  const alive = useRef(true);
  const streaming = useRef<AbortController | null>(null);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      // Leaving the page stops the answer being generated.
      streaming.current?.abort();
    };
  }, []);

  const refreshSessions = useCallback(
    () =>
      listSessions().then(
        (s) => alive.current && setSessions(s),
        () => undefined,
      ),
    [],
  );
  useEffect(() => void refreshSessions(), [refreshSessions]);

  const loadSession = useCallback(async (sid: string) => {
    try {
      const d = await getSession(sid);
      if (!alive.current) return;
      setDetail(d);
      if (d.task && (d.task.status === "queued" || d.task.status === "running"))
        setTask(d.task);
      setPending(null);
    } catch (e) {
      if (alive.current) setError(errorMessage(e));
    }
  }, []);
  useEffect(() => {
    if (!id) return;
    const c = new AbortController();
    getSession(id, c.signal).then(
      (d) => {
        setDetail(d);
        if (
          d.task &&
          (d.task.status === "queued" || d.task.status === "running")
        )
          setTask(d.task);
      },
      (e: unknown) => {
        if (!c.signal.aborted) setError(errorMessage(e));
      },
    );
    return () => c.abort();
  }, [id]);

  // Poll a running task every 2 s; on completion reload the session. Only
  // for a server that does not stream: a streamed answer reports itself.
  const taskId =
    task?.status === "queued" || task?.status === "running"
      ? task.id
      : undefined;
  useEffect(() => {
    if (!taskId || !id) return;
    const timer = setInterval(() => {
      getTask(taskId).then(
        (t) => {
          if (!alive.current) return;
          if (t.status === "succeeded" || t.status === "failed") {
            clearInterval(timer);
            setTask(t.status === "failed" ? t : null);
            void loadSession(id);
            void refreshSessions();
          } else setTask(t);
        },
        (e: unknown) => alive.current && setError(errorMessage(e)),
      );
    }, TASK_POLL_MS);
    return () => clearInterval(timer);
  }, [taskId, id, loadSession, refreshSessions]);

  /** Applies one event of the streamed answer. */
  function onEvent(ev: StreamEvent) {
    if (!alive.current) return;
    switch (ev.type) {
      case "started":
        setLive((l) => l && { ...l, step: WORKING });
        break;
      case "thinking":
        setLive((l) => l && { ...l, step: THINKING });
        break;
      case "tool":
        setLive((l) => l && { ...l, step: ev.label });
        break;
      case "delta":
        setLive((l) => l && { ...l, text: l.text + ev.text });
        break;
      case "reset":
        setLive((l) => l && { step: THINKING, text: "" });
        break;
      default:
        break;
    }
  }

  /** After a stream ends without an answer: show the stored question. */
  function showStored(sid: string) {
    if (!id) navigate(`/ai/assistant/${sid}`, { replace: true });
    else void loadSession(sid);
    void refreshSessions();
  }

  async function send() {
    const content = draft.trim();
    if (!content) return;
    setError(null);
    setTask(null);
    setStopped(false);
    let sid = id;
    try {
      sid ??= (await createSession()).id;
    } catch (e) {
      setError(errorMessage(e));
      return;
    }
    const ctl = new AbortController();
    streaming.current = ctl;
    setPending(content);
    setDraft("");
    setLive({ step: WORKING, text: "" });
    try {
      const out = await streamMessage(sid, content, onEvent, ctl.signal);
      if (!alive.current) return;
      switch (out.kind) {
        case "done": {
          // Swap the growing text for the stored message, with its sources
          // and proposal; then reconcile with the server.
          const m = out.event;
          const asked: AIMessage = {
            id: m.messageId,
            role: "user",
            content,
            toolCalls: [],
            citations: [],
            proposalId: null,
            taskId: m.taskId,
            createdAt: new Date().toISOString(),
          };
          setDetail((d) =>
            d ? { ...d, messages: [...d.messages, asked, m.message] } : d,
          );
          setPending(null);
          if (id) void loadSession(sid);
          else navigate(`/ai/assistant/${sid}`, { replace: true });
          void refreshSessions();
          break;
        }
        case "error":
          setTask({
            id: out.event.code,
            kind: "message",
            status: "failed",
            sessionId: sid,
            errorCode: out.event.code,
            errorMessage: out.event.message,
            result: null,
            createdAt: new Date().toISOString(),
            startedAt: null,
            finishedAt: null,
          });
          showStored(sid);
          break;
        case "dropped":
          setError(
            "The connection closed before the answer was complete. Ask again.",
          );
          showStored(sid);
          break;
        case "accepted":
          setTask({
            id: out.taskId,
            kind: "message",
            status: "queued",
            sessionId: sid,
            errorCode: null,
            errorMessage: null,
            result: null,
            createdAt: new Date().toISOString(),
            startedAt: null,
            finishedAt: null,
          });
          if (!id) navigate(`/ai/assistant/${sid}`, { replace: true });
          else void loadSession(sid);
          break;
      }
    } catch (e) {
      if (!alive.current) return;
      if (ctl.signal.aborted) {
        setStopped(true);
        showStored(sid);
      } else {
        setPending(null);
        setError(errorMessage(e));
      }
    } finally {
      if (streaming.current === ctl) streaming.current = null;
      if (alive.current) setLive(null);
    }
  }

  const busy =
    live !== null || task?.status === "queued" || task?.status === "running";
  return (
    <div className="aix-chat">
      <nav aria-label="Chats" className="aix-sessions">
        <Button
          size="sm"
          icon="plus"
          variant="secondary"
          onClick={() => navigate("/ai/assistant")}
        >
          New chat
        </Button>
        {sessions.map((s) => (
          <NavLink key={s.id} to={`/ai/assistant/${s.id}`}>
            {s.title || "New chat"}
          </NavLink>
        ))}
      </nav>
      <div className="aix-stack">
        {error && (
          <Alert tone="bad" title="Something went wrong">
            {error}
          </Alert>
        )}
        {id && !detail && !error && <Spinner label="Loading chat…" />}
        {!id && !pending && (
          <EmptyState
            icon="message-square"
            title="Ask about your PBX"
            description="The assistant reads live state and call history as you, and can propose changes for you to review."
          />
        )}
        <div className="aix-thread" aria-live="polite">
          {detail?.messages.map((m) => (
            <Message key={m.id} m={m} />
          ))}
          {pending && (
            <div className="aix-msg aix-msg--user">
              <p className="aix-msg__role">You</p>
              <PlainText text={pending} />
            </div>
          )}
          {live && live.text === "" && <TypingIndicator label={live.step} />}
          {live && live.text !== "" && (
            // Not announced token by token: the stored message that replaces
            // it is.
            <div
              className="aix-msg aix-msg--live"
              aria-live="off"
              aria-busy="true"
            >
              <p className="aix-msg__role">Assistant</p>
              <PlainText text={live.text} />
            </div>
          )}
          {stopped && (
            <p className="aix-muted">
              Stopped. The answer was discarded; your question was kept.
            </p>
          )}
          {task && <TaskStatus task={task} />}
        </div>
        <form
          className="aix-composer"
          onSubmit={(e) => {
            e.preventDefault();
            void send();
          }}
        >
          <label htmlFor="ai-message" className="aix-muted">
            Message
          </label>
          <textarea
            id="ai-message"
            value={draft}
            maxLength={MESSAGE_LIMIT}
            onChange={(e) => setDraft(e.target.value)}
          />
          <div className="aix-actions">
            <Button
              type="submit"
              icon="send"
              disabled={busy || draft.trim() === ""}
            >
              Send
            </Button>
            {live && (
              <Button
                type="button"
                variant="secondary"
                icon="circle-stop"
                onClick={() => streaming.current?.abort()}
              >
                Stop
              </Button>
            )}
            <Badge tone="outline">
              {draft.length} / {MESSAGE_LIMIT}
            </Badge>
            {id && (
              <Button
                type="button"
                variant="ghost"
                icon="trash-2"
                onClick={() => setDeleting(true)}
              >
                Delete chat
              </Button>
            )}
          </div>
        </form>
      </div>
      {deleting && id && (
        <ConfirmDialog
          title="Delete this chat?"
          description="The messages are removed. Proposals made in it stay in the inbox."
          confirmLabel="Delete chat"
          errorTitle="Could not delete the chat"
          onClose={() => setDeleting(false)}
          onConfirm={async () => {
            await deleteSession(id);
            navigate("/ai/assistant", { replace: true });
            void refreshSessions();
          }}
        />
      )}
    </div>
  );
}

/** The assistant: operators chat; viewers are sent to the read-only pages. */
export function AIAssistant() {
  const { id } = useParams();
  const operator = useCan("operator");
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="AI"
        title="Assistant"
        description="Answers come from your own PBX data and are shown as plain text."
      />
      {!operator ? (
        <EmptyState
          icon="lock"
          title="The assistant is for operators"
          description={
            <>
              Viewers can read <Link to="/ai/findings">findings</Link>,{" "}
              <Link to="/ai/proposals">proposals</Link> and the{" "}
              <Link to="/ai/status">status</Link>.
            </>
          }
        />
      ) : (
        <AIGate>
          <Chat key={id ?? "new"} id={id} />
        </AIGate>
      )}
    </section>
  );
}
