import { useEffect, useState, type FormEvent } from "react";
import { errorMessage, fieldErrors } from "../api";
import {
  erasePeer,
  getMessagingSettings,
  putMessagingSettings,
  type EraseReport,
  type MessagingSettings as Settings,
} from "../api/inbox";
import {
  Alert,
  Button,
  ConfirmDialog,
  Input,
  Modal,
  PageHeader,
  Spinner,
  Switch,
  useToast,
} from "../design/azrty/components";

const FIELDS: ReadonlyArray<{
  key: "retentionSmsDays" | "retentionChatDays" | "reopenDays";
  label: string;
  hint: string;
  min: number;
  max: number;
}> = [
  {
    key: "retentionSmsDays",
    label: "Keep text conversations for (days)",
    hint: "A resolved conversation is deleted this long after it was resolved. An open one is never deleted by age.",
    min: 1,
    max: 3650,
  },
  {
    key: "retentionChatDays",
    label: "Keep chat conversations for (days)",
    hint: "The same for chat conversations.",
    min: 1,
    max: 3650,
  },
  {
    key: "reopenDays",
    label: "Reopen a resolved conversation within (days)",
    hint: "A new text this soon after it was resolved reopens it; later it starts a new one. 0 always starts a new one.",
    min: 0,
    max: 365,
  },
];

/**
 * The inbox's retention and notification settings, and the tool that erases
 * everything about one customer. Administrators only.
 */
export function MessagingSettings() {
  const [loaded, setLoaded] = useState<Settings | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [emailNotify, setEmailNotify] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [erasing, setErasing] = useState(false);
  const [peer, setPeer] = useState("");
  const [report, setReport] = useState<EraseReport | null>(null);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    getMessagingSettings(controller.signal)
      .then((s) => {
        setLoaded(s);
        setEmailNotify(s.emailNotify);
        setDraft({
          retentionSmsDays: String(s.retentionSmsDays),
          retentionChatDays: String(s.retentionChatDays),
          reopenDays: String(s.reopenDays),
        });
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      });
    return () => controller.abort();
  }, []);

  async function save(e: FormEvent) {
    e.preventDefault();
    const next: Record<string, string> = {};
    for (const f of FIELDS) {
      const n = Number(draft[f.key]);
      if (!Number.isInteger(n) || n < f.min || n > f.max) {
        next[f.key] = `Enter a whole number from ${f.min} to ${f.max}.`;
      }
    }
    setFields(next);
    if (Object.keys(next).length > 0) return;
    setBusy(true);
    setError(null);
    try {
      await putMessagingSettings({
        retentionSmsDays: Number(draft.retentionSmsDays),
        retentionChatDays: Number(draft.retentionChatDays),
        reopenDays: Number(draft.reopenDays),
        emailNotify,
      });
      toast.show("Messaging settings saved.");
    } catch (err: unknown) {
      setFields(
        Object.fromEntries(fieldErrors(err).map((f) => [f.path, f.message])),
      );
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Messaging"
        title="Messaging settings"
        description="How long customer conversations are kept, when agents are emailed, and the tool that erases a customer."
      />
      {!loaded && !error && <Spinner label="Loading settings…" />}
      {error && (
        <Alert tone="bad" title="Messaging settings">
          {error}
        </Alert>
      )}
      {loaded && (
        <form className="cf-stack" onSubmit={save}>
          {FIELDS.map((f) => (
            <Input
              key={f.key}
              label={f.label}
              hint={f.hint}
              error={fields[f.key]}
              inputMode="numeric"
              value={draft[f.key] ?? ""}
              onChange={(e) =>
                setDraft((d) => ({ ...d, [f.key]: e.target.value }))
              }
            />
          ))}
          <Switch
            label="Email agents who have no inbox open"
            hint="A customer text that waits two minutes sends one email per conversation every 15 minutes. The email never carries the text or the number."
            labelPosition="end"
            checked={emailNotify}
            onChange={(e) => setEmailNotify(e.target.checked)}
          />
          <div>
            <Button type="submit" disabled={busy}>
              Save settings
            </Button>
          </div>
        </form>
      )}
      <h2 className="az-h3" style={{ marginTop: 32 }}>
        Erase a customer
      </h2>
      <p>
        Deletes every conversation, message and note with a phone number, on
        every channel, and keeps only a hash of the number on the opt-out list
        so the customer is not texted again. The carrier keeps its own logs, and
        database backups keep the rows until they expire.
      </p>
      <form
        className="msg-toolbar"
        onSubmit={(e) => {
          e.preventDefault();
          if (peer.trim()) setErasing(true);
        }}
      >
        <Input
          label="Customer's number"
          value={peer}
          onChange={(e) => setPeer(e.target.value)}
        />
        <Button
          type="submit"
          variant="danger"
          icon="trash-2"
          disabled={peer.trim() === ""}
        >
          Erase this customer…
        </Button>
      </form>
      {erasing && (
        <ConfirmDialog
          title={`Erase ${peer.trim()}?`}
          description="Every conversation, message and note with this number is deleted for good."
          confirmLabel="Erase customer"
          errorTitle="Could not erase the customer"
          onConfirm={async () => {
            setReport(await erasePeer(peer.trim()));
            setErasing(false);
            setPeer("");
          }}
          onClose={() => setErasing(false)}
        />
      )}
      {report && (
        <Modal
          title="Customer erased"
          onClose={() => setReport(null)}
          actions={<Button onClick={() => setReport(null)}>Close</Button>}
        >
          <p>
            Deleted {report.conversations} conversations and {report.messages}{" "}
            messages. {report.optOutsKept} opt-outs were kept as a hash.
          </p>
          <ul>
            {report.providers.map((p) => (
              <li key={p.provider}>
                {p.provider}: {p.detail}
              </li>
            ))}
          </ul>
        </Modal>
      )}
      {toast.node}
    </section>
  );
}
