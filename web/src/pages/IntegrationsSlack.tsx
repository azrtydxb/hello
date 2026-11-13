import { useCallback, useState, type FormEvent } from "react";
import { errorMessage } from "../api";
import {
  createSlack,
  deleteSlack,
  listSlack,
  listSlackPosts,
  retrySlackPost,
  SLACK_EVENTS,
  testSlack,
  updateSlack,
  type SlackDestination,
  type SlackEvent,
  type SlackMode,
  type SlackPost,
  type SlackPostStatus,
} from "../api/slack";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  ConfirmDialog,
  EmptyState,
  Input,
  PageHeader,
  PropertyList,
  Select,
  Spinner,
  Switch,
  Table,
  Tabs,
  useToast,
  type BadgeTone,
} from "../design/azrty/components";
import { formatTime } from "../format";
import { usePolling } from "../usePolling";
import "./platform/platform.css";

const REASONS: Record<string, string> = {
  manual: "Switched off.",
  consecutive_failures: "Switched off after 50 failed posts in a row.",
  auth_failed:
    "Slack refused the credential. Enter a new webhook URL or bot token to switch it on again.",
  destination_gone:
    "Slack says the webhook no longer exists. Create a new one in Slack and enter it here.",
};

const POST_TONE: Record<SlackPostStatus, BadgeTone> = {
  pending: "info",
  retrying: "warn",
  succeeded: "good",
  dead: "bad",
};

function splitExtensions(v: string): string[] {
  return v
    .split(/[\s,]+/)
    .map((x) => x.trim())
    .filter(Boolean);
}

/**
 * Slack notifications (/integrations/slack): missed calls and new voicemail
 * messages posted to a channel through an incoming webhook or a bot token.
 * The credential is sealed and never shown again.
 */
export function IntegrationsSlack() {
  const list = usePolling(listSlack, 15_000);
  const toast = useToast();
  const items = list.status === "loading" ? null : (list.data ?? []);
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Integrations"
        title="Slack"
        description="Post missed calls and new voicemail messages to a Slack channel. You create the Slack app in your own workspace; Hello keeps its webhook URL or bot token sealed. Posts carry the caller's number and name, never audio."
      />
      <div className="cf-stack">
        {list.status === "loading" && <Spinner label="Loading…" />}
        {list.status === "error" && (
          <Alert tone="bad" title="Could not load the Slack destinations">
            {list.message}
          </Alert>
        )}
        {items && items.length === 0 && (
          <EmptyState
            icon="message-square"
            title="No Slack channel yet"
            description="Add an incoming webhook, or a bot token and a channel."
          />
        )}
        {items?.map((d) => (
          <DestinationPanel
            key={d.id}
            dest={d}
            reload={list.reload}
            say={toast.show}
          />
        ))}
        {items && <NewDestination reload={list.reload} say={toast.show} />}
      </div>
      {toast.node}
    </section>
  );
}

function EventPicker({
  value,
  onChange,
}: {
  value: SlackEvent[];
  onChange: (v: SlackEvent[]) => void;
}) {
  return (
    <fieldset>
      <legend>Post</legend>
      {SLACK_EVENTS.map((e) => (
        <Checkbox
          key={e.id}
          label={e.label}
          checked={value.includes(e.id)}
          onChange={(x) =>
            onChange(
              x.target.checked
                ? [...value, e.id]
                : value.filter((v) => v !== e.id),
            )
          }
        />
      ))}
    </fieldset>
  );
}

function NewDestination({
  reload,
  say,
}: {
  reload: () => void;
  say: (m: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [mode, setMode] = useState<SlackMode>("webhook");
  const [name, setName] = useState("Phone");
  const [secret, setSecret] = useState("");
  const [channel, setChannel] = useState("");
  const [events, setEvents] = useState<SlackEvent[]>([
    "call.missed",
    "voicemail.received",
  ]);
  const [exts, setExts] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await createSlack({
        name,
        mode,
        secret,
        events,
        extensions: splitExtensions(exts),
        ...(mode === "bot" ? { channel } : {}),
      });
      setOpen(false);
      setSecret("");
      reload();
      say("Slack destination added. Send a test message next.");
    } catch (x: unknown) {
      setErr(errorMessage(x));
    } finally {
      setBusy(false);
    }
  }

  if (!open) {
    return (
      <div>
        <Button icon="plus" variant="secondary" onClick={() => setOpen(true)}>
          Add a Slack channel
        </Button>
      </div>
    );
  }
  return (
    <form onSubmit={(e) => void submit(e)} aria-label="New Slack destination">
      <h2>Add a Slack channel</h2>
      <Select
        label="How Hello posts"
        value={mode}
        options={[
          { value: "webhook", label: "Incoming webhook (one channel)" },
          { value: "bot", label: "Bot token (chat.postMessage)" },
        ]}
        onChange={(e) => setMode(e.target.value as SlackMode)}
      />
      <Input
        label="Name"
        value={name}
        maxLength={64}
        required
        onChange={(e) => setName(e.target.value)}
      />
      <Input
        label={mode === "webhook" ? "Webhook URL" : "Bot token"}
        type="password"
        autoComplete="off"
        value={secret}
        required
        placeholder={
          mode === "webhook" ? "https://hooks.slack.com/services/…" : "xoxb-…"
        }
        onChange={(e) => setSecret(e.target.value)}
        hint="Kept sealed; it is never shown again."
      />
      {mode === "bot" && (
        <Input
          label="Channel"
          value={channel}
          required
          placeholder="#phone or C0123456789"
          onChange={(e) => setChannel(e.target.value)}
          hint="Invite the bot to the channel first."
        />
      )}
      <EventPicker value={events} onChange={setEvents} />
      <Input
        label="Only these extensions"
        value={exts}
        onChange={(e) => setExts(e.target.value)}
        hint="Extension numbers separated by commas. Empty posts for every extension."
      />
      {err && (
        <Alert tone="bad" title="Could not add the channel">
          {err}
        </Alert>
      )}
      <Button type="submit" disabled={busy}>
        Add channel
      </Button>{" "}
      <Button variant="ghost" type="button" onClick={() => setOpen(false)}>
        Cancel
      </Button>
    </form>
  );
}

function DestinationPanel({
  dest: d,
  reload,
  say,
}: {
  dest: SlackDestination;
  reload: () => void;
  say: (m: string) => void;
}) {
  const [tab, setTab] = useState<"settings" | "posts">("settings");
  return (
    <article aria-label={`${d.name} Slack destination`}>
      <h2>
        {d.name}{" "}
        {d.enabled ? (
          <Badge tone="good">On</Badge>
        ) : (
          <Badge tone="bad">Off</Badge>
        )}
        {d.dead > 0 && <Badge tone="bad">{d.dead} failed</Badge>}
      </h2>
      {!d.enabled && d.disabledReason && d.disabledReason !== "manual" && (
        <Alert tone="bad" title="This channel is switched off">
          {REASONS[d.disabledReason] ?? d.disabledReason}{" "}
          {d.lastError && <>Last error: {d.lastError}.</>}
        </Alert>
      )}
      <Tabs
        aria-label={`${d.name} sections`}
        items={[
          { id: "settings", label: "Settings" },
          { id: "posts", label: "Posts" },
        ]}
        value={tab}
        onChange={setTab}
      />
      {tab === "settings" && <SettingsTab dest={d} reload={reload} say={say} />}
      {tab === "posts" && <PostsTab dest={d} say={say} />}
    </article>
  );
}

function SettingsTab({
  dest: d,
  reload,
  say,
}: {
  dest: SlackDestination;
  reload: () => void;
  say: (m: string) => void;
}) {
  const [name, setName] = useState(d.name);
  const [secret, setSecret] = useState("");
  const [channel, setChannel] = useState(d.channel);
  const [events, setEvents] = useState<SlackEvent[]>(d.events);
  const [exts, setExts] = useState(d.extensions.join(", "));
  const [enabled, setEnabled] = useState(d.enabled);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState(false);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await updateSlack(d.id, {
        name,
        enabled,
        events,
        extensions: splitExtensions(exts),
        ...(d.mode === "bot" ? { channel } : {}),
        ...(secret !== "" ? { secret } : {}),
      });
      setSecret("");
      reload();
      say("Saved.");
    } catch (x: unknown) {
      setErr(errorMessage(x));
    } finally {
      setBusy(false);
    }
  }

  async function test() {
    setErr(null);
    try {
      const r = await testSlack(d.id);
      say(
        r.delivered
          ? "Slack accepted the test message."
          : `Slack refused the test message: ${r.error || `http ${r.status}`}`,
      );
    } catch (x: unknown) {
      setErr(errorMessage(x));
    }
  }

  return (
    <div>
      <PropertyList
        items={[
          {
            label: "How",
            value: d.mode === "webhook" ? "Incoming webhook" : "Bot token",
          },
          {
            label: "Last success",
            value: d.lastSuccessAt ? formatTime(d.lastSuccessAt) : "—",
          },
          {
            label: "Waiting",
            value: String(d.pending),
          },
        ]}
      />
      <p>
        <Button icon="send" variant="secondary" onClick={() => void test()}>
          Send a test message
        </Button>{" "}
        <Button
          variant="danger"
          icon="trash-2"
          onClick={() => setConfirm(true)}
        >
          Delete
        </Button>
      </p>
      <form onSubmit={(e) => void save(e)} aria-label={`${d.name} settings`}>
        <Input
          label="Name"
          value={name}
          maxLength={64}
          required
          onChange={(e) => setName(e.target.value)}
        />
        <Input
          label={d.mode === "webhook" ? "Webhook URL" : "Bot token"}
          type="password"
          autoComplete="off"
          value={secret}
          placeholder="set; leave empty to keep it"
          onChange={(e) => setSecret(e.target.value)}
          hint="Write-only. A new one also switches the channel on again after Slack refused the old one."
        />
        {d.mode === "bot" && (
          <Input
            label="Channel"
            value={channel}
            required
            onChange={(e) => setChannel(e.target.value)}
          />
        )}
        <EventPicker value={events} onChange={setEvents} />
        <Input
          label="Only these extensions"
          value={exts}
          onChange={(e) => setExts(e.target.value)}
          hint="Extension numbers separated by commas. Empty posts for every extension."
        />
        <Switch
          label="Enabled"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        {err && (
          <Alert tone="bad" title="Not saved">
            {err}
          </Alert>
        )}
        <Button type="submit" disabled={busy}>
          Save settings
        </Button>
      </form>
      {confirm && (
        <ConfirmDialog
          title={`Delete ${d.name}?`}
          description="The channel and the posts still waiting for it are deleted. Messages already in Slack stay there."
          confirmLabel="Delete channel"
          onClose={() => setConfirm(false)}
          onConfirm={async () => {
            await deleteSlack(d.id);
            reload();
            setConfirm(false);
            say("Slack channel deleted.");
          }}
        />
      )}
    </div>
  );
}

function PostsTab({
  dest: d,
  say,
}: {
  dest: SlackDestination;
  say: (m: string) => void;
}) {
  const [status, setStatus] = useState<SlackPostStatus | "">("");
  const load = useCallback(
    (s: AbortSignal) => listSlackPosts(d.id, status, s),
    [d.id, status],
  );
  const posts = usePolling(load, 10_000);
  const [err, setErr] = useState<string | null>(null);

  async function retry(p: SlackPost) {
    setErr(null);
    try {
      await retrySlackPost(p.id);
      posts.reload();
      say("Queued again.");
    } catch (x: unknown) {
      setErr(errorMessage(x));
    }
  }

  return (
    <div>
      <Select
        label="Show"
        value={status}
        options={[
          { value: "", label: "All posts" },
          { value: "pending", label: "pending" },
          { value: "retrying", label: "retrying" },
          { value: "succeeded", label: "succeeded" },
          { value: "dead", label: "dead (failed for good)" },
        ]}
        onChange={(e) => setStatus(e.target.value as SlackPostStatus | "")}
      />
      {err && (
        <Alert tone="bad" title="Could not retry">
          {err}
        </Alert>
      )}
      {posts.status === "loading" && <Spinner label="Loading posts…" />}
      {posts.status === "error" && (
        <Alert tone="bad" title="Could not load the posts">
          {posts.message}
        </Alert>
      )}
      {posts.status === "ready" && posts.data.items.length === 0 && (
        <EmptyState
          icon="inbox"
          title="No posts"
          description="Missed calls and voicemail messages appear here once they are posted."
        />
      )}
      {posts.status === "ready" && posts.data.items.length > 0 && (
        <Table
          caption={`${d.name} posts`}
          rows={posts.data.items}
          rowKey={(p) => p.id}
          columns={[
            {
              key: "when",
              label: "When",
              render: (p) => formatTime(p.createdAt),
            },
            {
              key: "type",
              label: "Event",
              mono: true,
              render: (p) => p.eventType,
            },
            {
              key: "status",
              label: "Status",
              render: (p) => (
                <Badge tone={POST_TONE[p.status]}>{p.status}</Badge>
              ),
            },
            { key: "tries", label: "Tries", render: (p) => String(p.attempts) },
            {
              key: "err",
              label: "Last error",
              render: (p) => p.lastError || "—",
            },
            {
              key: "act",
              label: "",
              render: (p) =>
                p.status === "dead" ? (
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => void retry(p)}
                  >
                    Retry
                  </Button>
                ) : null,
            },
          ]}
        />
      )}
    </div>
  );
}
