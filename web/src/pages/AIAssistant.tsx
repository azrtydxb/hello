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
  postMessage,
  TASK_POLL_MS,
  type AIMessage,
  type AIProposal,
  type AISession,
  type AISessionDetail,
  type AITask,
} from "../api/aiagent";
import { AIGate } from "../components/ai/AIGate";
import { PlainText } from "../components/ai/PlainText";
import { ProposalCard } from "../components/ai/ProposalCard";
import { TaskStatus } from "../components/ai/TaskStatus";
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

function Chat({ id }: { id: string | undefined }) {
  const navigate = useNavigate();
  const [sessions, setSessions] = useState<AISession[]>([]);
  const [detail, setDetail] = useState<AISessionDetail | null>(null);
  const [task, setTask] = useState<AITask | null>(null);
  const [pending, setPending] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState(false);
  const alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
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

  // Poll a running task every 2 s; on completion reload the session.
  const taskId = task?.id;
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

  async function send() {
    const content = draft.trim();
    if (!content) return;
    setError(null);
    try {
      const sid = id ?? (await createSession()).id;
      setPending(content);
      setDraft("");
      const r = await postMessage(sid, content);
      setTask({
        id: r.taskId,
        kind: "assistant",
        status: "queued",
        sessionId: sid,
        errorCode: null,
        errorMessage: null,
        createdAt: new Date().toISOString(),
      });
      if (!id) navigate(`/ai/assistant/${sid}`, { replace: true });
      else void loadSession(sid);
    } catch (e) {
      setPending(null);
      setError(errorMessage(e));
    }
  }

  const busy = task?.status === "queued" || task?.status === "running";
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
