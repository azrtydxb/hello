import { useEffect, useState, type FormEvent } from "react";
import { errorMessage, fieldErrors } from "../api";
import {
  createChatSite,
  deleteChatBlock,
  deleteChatSite,
  listChatBlocks,
  listChatSites,
  updateChatSite,
  type ChatBlock,
  type ChatHours,
  type ChatSite,
  type ChatSiteInput,
} from "../api/inbox";
import {
  Alert,
  Badge,
  Button,
  CodeBlock,
  ConfirmDialog,
  EmptyState,
  Input,
  Modal,
  PageHeader,
  Select,
  Spinner,
  Switch,
  Table,
  useRestoreFocus,
  useToast,
} from "../design/azrty/components";
import { formatTime } from "../format";

const DAYS = [
  "Sunday",
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
];
const ADDRESS_KEY = "hello.chat.embedAddress";

/** One weekday's opening window as the form edits it: blank means closed. */
type Window = { from: string; to: string };

function windowsOf(h: ChatHours | undefined): Window[] {
  const w: Window[] = DAYS.map(() => ({ from: "", to: "" }));
  for (const d of h?.days ?? []) w[d.day] = { from: d.from, to: d.to };
  return w;
}

function hoursOf(always: boolean, zone: string, w: Window[]): ChatHours {
  if (always) return { zone: "", days: [] };
  return {
    zone: zone.trim(),
    days: w.flatMap((x, day) =>
      x.from && x.to ? [{ day, from: x.from, to: x.to }] : [],
    ),
  };
}

function SiteDialog({
  site,
  onClose,
  onSaved,
}: {
  site: ChatSite | null;
  onClose: () => void;
  onSaved: (s: ChatSite) => void;
}) {
  useRestoreFocus();
  const [name, setName] = useState(site?.name ?? "");
  const [enabled, setEnabled] = useState(site?.enabled ?? true);
  const [origins, setOrigins] = useState(site?.allowedOrigins.join(", ") ?? "");
  const [greeting, setGreeting] = useState(site?.greeting ?? "");
  const [accent, setAccent] = useState(site?.accent ?? "#2563eb");
  const [position, setPosition] = useState(site?.position ?? "right");
  const [collect, setCollect] = useState(site?.collect ?? "off");
  const [offlineMode, setOfflineMode] = useState(
    site?.offlineMode ?? "collect",
  );
  const [privacy, setPrivacy] = useState(site?.privacyUrl ?? "");
  const [challenge, setChallenge] = useState(site?.challenge ?? "pow");
  const [bits, setBits] = useState(String(site?.powBits ?? 16));
  const [tsKey, setTsKey] = useState(site?.turnstileSiteKey ?? "");
  const [tsSecret, setTsSecret] = useState("");
  const [stripLinks, setStripLinks] = useState(site?.stripLinks ?? false);
  const [transcripts, setTranscripts] = useState(site?.transcripts ?? false);
  const [cap, setCap] = useState(String(site?.maxOpenUnassigned ?? 200));
  const [retention, setRetention] = useState(
    site?.retentionDays ? String(site.retentionDays) : "",
  );
  const [always, setAlways] = useState((site?.hours.days.length ?? 0) === 0);
  const [zone, setZone] = useState(
    site?.hours.zone ||
      Intl.DateTimeFormat().resolvedOptions().timeZone ||
      "UTC",
  );
  const [windows, setWindows] = useState(windowsOf(site?.hours));
  const [error, setError] = useState<string | null>(null);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setFields({});
    const body: ChatSiteInput = {
      name: name.trim(),
      enabled,
      allowedOrigins: origins
        .split(",")
        .map((o) => o.trim())
        .filter(Boolean),
      greeting: greeting.trim(),
      accent: accent.trim(),
      position,
      collect,
      offlineMode,
      privacyUrl: privacy.trim(),
      challenge,
      powBits: Number(bits),
      turnstileSiteKey: tsKey.trim(),
      stripLinks,
      transcripts,
      maxOpenUnassigned: Number(cap),
      retentionDays: retention.trim() === "" ? 0 : Number(retention),
      hours: hoursOf(always, zone, windows),
    };
    // The secret is only ever sent when typed: leaving it empty keeps the
    // stored one.
    if (tsSecret !== "") body.turnstileSecret = tsSecret;
    try {
      onSaved(
        site ? await updateChatSite(site.id, body) : await createChatSite(body),
      );
    } catch (err: unknown) {
      setFields(
        Object.fromEntries(fieldErrors(err).map((f) => [f.path, f.message])),
      );
      setError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Modal
      title={site ? `Edit ${site.name}` : "Add a chat site"}
      description="A chat site is a website chat widget for your intranet pages. Visitors are anonymous; their conversations arrive in the inbox."
      onClose={onClose}
      actions={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="chat-site-form" disabled={busy}>
            {site ? "Save site" : "Add site"}
          </Button>
        </>
      }
    >
      {error && (
        <Alert tone="bad" title="Could not save the site">
          {error}
        </Alert>
      )}
      <form id="chat-site-form" className="cf-stack" onSubmit={save}>
        <Input
          label="Name"
          hint="Shown in the widget's header and in the inbox."
          value={name}
          error={fields.name}
          autoFocus
          onChange={(e) => setName(e.target.value)}
        />
        <Switch
          label="Site on"
          hint="Off stops new chats and messages at once."
          labelPosition="end"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        <Input
          label="Allowed origins"
          hint="The intranet pages that may embed the widget, as scheme://host[:port], separated by commas. Empty means no page can embed it yet."
          value={origins}
          error={fields.allowedOrigins}
          onChange={(e) => setOrigins(e.target.value)}
        />
        <Input
          label="Greeting"
          value={greeting}
          error={fields.greeting}
          onChange={(e) => setGreeting(e.target.value)}
        />
        <Input
          label="Accent colour"
          hint="Like #2563eb."
          value={accent}
          error={fields.accent}
          onChange={(e) => setAccent(e.target.value)}
        />
        <Select
          label="Position"
          value={position}
          options={[
            { value: "right", label: "Bottom right" },
            { value: "left", label: "Bottom left" },
          ]}
          onChange={(e) => setPosition(e.target.value as typeof position)}
        />
        <Select
          label="Ask for a name and email"
          value={collect}
          error={fields.collect}
          options={[
            { value: "off", label: "No" },
            { value: "optional", label: "Optional" },
            { value: "required", label: "Required" },
          ]}
          onChange={(e) => setCollect(e.target.value as typeof collect)}
        />
        <Input
          label="Privacy policy address"
          hint="Linked from the widget; empty for none."
          value={privacy}
          error={fields.privacyUrl}
          onChange={(e) => setPrivacy(e.target.value)}
        />
        <Switch
          label="Always open"
          hint="Off lets you set opening hours; outside them visitors can leave a message."
          labelPosition="end"
          checked={always}
          onChange={(e) => setAlways(e.target.checked)}
        />
        {!always && (
          <fieldset className="cf-stack">
            <legend className="az-caption">Opening hours</legend>
            <Input
              label="Time zone"
              hint="For example Europe/Brussels."
              value={zone}
              error={fields.hours}
              onChange={(e) => setZone(e.target.value)}
            />
            {DAYS.map((d, i) => (
              <div key={d} className="inbox-actions">
                <Input
                  label={`${d} from`}
                  type="time"
                  value={windows[i]?.from ?? ""}
                  onChange={(e) =>
                    setWindows((w) =>
                      w.map((x, j) =>
                        j === i ? { ...x, from: e.target.value } : x,
                      ),
                    )
                  }
                />
                <Input
                  label={`${d} to`}
                  type="time"
                  value={windows[i]?.to ?? ""}
                  onChange={(e) =>
                    setWindows((w) =>
                      w.map((x, j) =>
                        j === i ? { ...x, to: e.target.value } : x,
                      ),
                    )
                  }
                />
              </div>
            ))}
          </fieldset>
        )}
        <Select
          label="When nobody is available"
          value={offlineMode}
          options={[
            { value: "collect", label: "Take a message and an email address" },
            { value: "hidden", label: "Hide the widget" },
          ]}
          onChange={(e) => setOfflineMode(e.target.value as typeof offlineMode)}
        />
        <Select
          label="Check for new visitors"
          value={challenge}
          error={fields.challenge}
          options={[
            { value: "pow", label: "Proof of work (no third party)" },
            { value: "turnstile", label: "Cloudflare Turnstile (your keys)" },
            { value: "none", label: "None" },
          ]}
          onChange={(e) => setChallenge(e.target.value as typeof challenge)}
        />
        {challenge === "pow" && (
          <Input
            label="Proof-of-work difficulty"
            hint="14 to 22 bits. Each bit doubles the work a visitor's browser does; it rises by itself while the site is flooded."
            type="number"
            min={14}
            max={22}
            value={bits}
            error={fields.powBits}
            onChange={(e) => setBits(e.target.value)}
          />
        )}
        {challenge === "turnstile" && (
          <>
            <Input
              label="Turnstile site key"
              value={tsKey}
              error={fields.turnstileSiteKey}
              onChange={(e) => setTsKey(e.target.value)}
            />
            <Input
              label="Turnstile secret key"
              hint={
                site?.hasTurnstileSecret
                  ? "A secret is stored. Leave this empty to keep it."
                  : "Stored sealed and never shown again."
              }
              type="password"
              autoComplete="off"
              value={tsSecret}
              error={fields.turnstileSecret}
              onChange={(e) => setTsSecret(e.target.value)}
            />
          </>
        )}
        <Switch
          label="Remove links from visitors' messages"
          hint="Replaces each address with [link removed]."
          labelPosition="end"
          checked={stripLinks}
          onChange={(e) => setStripLinks(e.target.checked)}
        />
        <Switch
          label="Let visitors email themselves a transcript"
          hint="Sent to the address they typed, at the end of a chat. Needs email to be configured."
          labelPosition="end"
          checked={transcripts}
          onChange={(e) => setTranscripts(e.target.checked)}
        />
        <Input
          label="Live chats that may wait"
          hint="Beyond this many unassigned live chats the widget switches to leaving a message."
          type="number"
          min={1}
          value={cap}
          error={fields.maxOpenUnassigned}
          onChange={(e) => setCap(e.target.value)}
        />
        <Input
          label="Keep finished chats for (days)"
          hint="Empty follows the messaging settings."
          type="number"
          min={1}
          max={3650}
          value={retention}
          error={fields.retentionDays}
          onChange={(e) => setRetention(e.target.value)}
        />
      </form>
    </Modal>
  );
}

function EmbedDialog({
  site,
  onClose,
}: {
  site: ChatSite;
  onClose: () => void;
}) {
  const [address, setAddress] = useState(
    () => window.localStorage.getItem(ADDRESS_KEY) ?? window.location.origin,
  );
  const base = address.trim().replace(/\/+$/, "");
  return (
    <Modal
      title={`Embed ${site.name}`}
      description="Put this on an intranet page. The widget runs in a frame on Hello's own address, so the page cannot read or break it."
      onClose={onClose}
      actions={<Button onClick={onClose}>Done</Button>}
    >
      <Input
        label="Chat address"
        hint="Where visitors' browsers reach this Hello on your network, such as https://hello.lan."
        value={address}
        onChange={(e) => {
          setAddress(e.target.value);
          window.localStorage.setItem(ADDRESS_KEY, e.target.value);
        }}
      />
      <CodeBlock
        title="Embed code"
        code={`<script async src="${base}/chat/v1/widget.js" data-site="${site.siteKey}"></script>`}
      />
      <p className="az-caption">
        The chat listener must be on (HELLO_MSG_CHAT_LAN_ADDR) and the page's
        origin must be one of the site's allowed origins.
      </p>
    </Modal>
  );
}

function BlocksDialog({
  site,
  onClose,
}: {
  site: ChatSite;
  onClose: () => void;
}) {
  const [blocks, setBlocks] = useState<ChatBlock[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [reload, setReload] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    listChatBlocks(site.id, controller.signal)
      .then((b) => {
        setBlocks(b);
        setError(null);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      });
    return () => controller.abort();
  }, [site.id, reload]);
  return (
    <Modal
      title={`Blocked visitors of ${site.name}`}
      description="A block stops a visitor, and for the rest of the UTC day their network address. Lifting it lets them chat again."
      onClose={onClose}
      actions={<Button onClick={onClose}>Close</Button>}
    >
      {error && (
        <Alert tone="bad" title="Could not load the blocks">
          {error}
        </Alert>
      )}
      {!blocks && !error && <Spinner label="Loading blocks…" />}
      {blocks && blocks.length === 0 && (
        <EmptyState
          icon="ban"
          title="Nobody is blocked"
          description="An agent blocks a visitor from their conversation."
        />
      )}
      {blocks && blocks.length > 0 && (
        <Table
          caption="Blocks"
          rows={blocks}
          rowKey={(b) => b.id}
          columns={[
            {
              key: "what",
              label: "Blocked",
              render: (b) =>
                b.visitorId !== null
                  ? `Visitor ${b.visitorId}`
                  : "Network address (today)",
            },
            {
              key: "until",
              label: "Until",
              render: (b) => formatTime(b.until),
            },
            {
              key: "actions",
              label: "",
              align: "right",
              render: (b) => (
                <Button
                  size="sm"
                  variant="secondary"
                  onClick={() =>
                    void deleteChatBlock(b.id).then(
                      () => setReload((n) => n + 1),
                      (err: unknown) => setError(errorMessage(err)),
                    )
                  }
                >
                  Lift block {b.id}
                </Button>
              ),
            },
          ]}
        />
      )}
    </Modal>
  );
}

/**
 * The chat sites of the LAN website chat widget: the settings, the embed code
 * and the blocked visitors. Administrators only.
 */
export function MessagingWebchat() {
  const [sites, setSites] = useState<ChatSite[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [edit, setEdit] = useState<ChatSite | "new" | null>(null);
  const [embed, setEmbed] = useState<ChatSite | null>(null);
  const [blocks, setBlocks] = useState<ChatSite | null>(null);
  const [del, setDel] = useState<ChatSite | null>(null);
  const [reload, setReload] = useState(0);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    listChatSites(controller.signal)
      .then((s) => {
        setSites(s);
        setError(null);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      });
    return () => controller.abort();
  }, [reload]);
  const refresh = () => setReload((n) => n + 1);

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Messaging"
        title="Chat sites"
        description="A website chat widget for intranet pages and VPN users. A visitor chats anonymously; the conversation arrives in the inbox like a text. The widget is served on your network only: nothing here is reachable from the Internet."
        actions={
          <Button icon="plus" onClick={() => setEdit("new")}>
            Add chat site
          </Button>
        }
      />
      {error && (
        <Alert tone="bad" title="Could not load the chat sites">
          {error}
        </Alert>
      )}
      {!sites && !error && <Spinner label="Loading chat sites…" />}
      {sites && sites.length === 0 && (
        <EmptyState
          icon="messages-square"
          title="No chat sites yet"
          description="Add a site, list the intranet pages that may embed it, and paste its embed code into them."
        />
      )}
      {sites?.map(
        (s) =>
          s.warnings.length > 0 && (
            <Alert key={s.id} tone="warn" title={`Check ${s.name}`}>
              <ul>
                {s.warnings.map((w) => (
                  <li key={w}>{w}</li>
                ))}
              </ul>
            </Alert>
          ),
      )}
      {sites && sites.length > 0 && (
        <Table
          caption="Chat sites"
          rows={sites}
          rowKey={(s) => s.id}
          columns={[
            { key: "name", label: "Site", render: (s) => s.name },
            {
              key: "state",
              label: "State",
              render: (s) =>
                s.enabled ? (
                  <Badge tone="good">On</Badge>
                ) : (
                  <Badge tone="neutral">Off</Badge>
                ),
            },
            {
              key: "origins",
              label: "Allowed origins",
              render: (s) =>
                s.allowedOrigins.length > 0
                  ? s.allowedOrigins.join(", ")
                  : "None yet",
            },
            {
              key: "challenge",
              label: "Check",
              render: (s) =>
                s.challenge === "pow"
                  ? "Proof of work"
                  : s.challenge === "turnstile"
                    ? "Turnstile"
                    : "None",
            },
            {
              key: "chats",
              label: "Chats",
              align: "right",
              render: (s) => s.conversations,
            },
            {
              key: "actions",
              label: "",
              align: "right",
              render: (s) => (
                <span className="inbox-actions">
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => setEmbed(s)}
                  >
                    Embed {s.name}
                  </Button>
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => setBlocks(s)}
                  >
                    Blocked visitors of {s.name}
                  </Button>
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => setEdit(s)}
                  >
                    Edit {s.name}
                  </Button>
                  <Button size="sm" variant="ghost" onClick={() => setDel(s)}>
                    Delete {s.name}
                  </Button>
                </span>
              ),
            },
          ]}
        />
      )}
      {edit && (
        <SiteDialog
          site={edit === "new" ? null : edit}
          onClose={() => setEdit(null)}
          onSaved={(s) => {
            setEdit(null);
            toast.show(`Chat site ${s.name} saved.`);
            refresh();
          }}
        />
      )}
      {embed && <EmbedDialog site={embed} onClose={() => setEmbed(null)} />}
      {blocks && <BlocksDialog site={blocks} onClose={() => setBlocks(null)} />}
      {del && (
        <ConfirmDialog
          title={`Delete chat site ${del.name}?`}
          description="Pages that embed it stop showing the widget. A site that has conversations cannot be deleted: retention or an erase request removes them first."
          confirmLabel="Delete site"
          onConfirm={async () => {
            await deleteChatSite(del.id);
            setDel(null);
            toast.show(`Chat site ${del.name} deleted.`);
            refresh();
          }}
          onClose={() => setDel(null)}
        />
      )}
      {toast.node}
    </section>
  );
}
