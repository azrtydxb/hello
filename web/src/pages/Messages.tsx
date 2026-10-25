import { useEffect, useState, type FormEvent } from "react";
import { errorMessage, fieldErrors } from "../api";
import {
  getMessageSettings,
  messagesApi,
  purgeMessages,
  putMessageSettings,
  type MessageSettings,
} from "../api/messages";
import {
  Alert,
  Button,
  ConfirmDialog,
  Input,
  Modal,
  PageHeader,
  Spinner,
  Switch,
  useRestoreFocus,
  useToast,
} from "../design/azrty/components";
import { Can, useCan } from "../role";
import { Workspace } from "./messages/Workspace";

const NUMBER_FIELDS: ReadonlyArray<{
  key: "ttlHours" | "retentionDays" | "ratePerMinute" | "maxQueuedPerExtension";
  label: string;
  hint: string;
  min: number;
  max: number;
}> = [
  {
    key: "ttlHours",
    label: "Hold undelivered messages for (hours)",
    hint: "A message for a phone that is not registered waits this long, then is given up on.",
    min: 1,
    max: 720,
  },
  {
    key: "retentionDays",
    label: "Keep history for (days)",
    hint: "Delivered and failed messages older than this are deleted.",
    min: 1,
    max: 3650,
  },
  {
    key: "ratePerMinute",
    label: "Messages per sender per minute",
    hint: "A sender over the limit is refused until the minute passes.",
    min: 1,
    max: 1000,
  },
  {
    key: "maxQueuedPerExtension",
    label: "Waiting messages kept per extension",
    hint: "Beyond this the oldest waiting message is dropped.",
    min: 1,
    max: 10000,
  },
];

/** Retention, TTL and limits; and a purge of the history. Admin only. */
function SettingsDialog({
  onClose,
  onSaved,
}: {
  onClose: () => void;
  onSaved: () => void;
}) {
  useRestoreFocus();
  const [loaded, setLoaded] = useState<MessageSettings | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [enabled, setEnabled] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [purging, setPurging] = useState(false);
  const [purgeDays, setPurgeDays] = useState("0");

  useEffect(() => {
    const controller = new AbortController();
    getMessageSettings(controller.signal)
      .then((s) => {
        setLoaded(s);
        setEnabled(s.enabled);
        setDraft({
          ttlHours: String(s.ttlHours),
          retentionDays: String(s.retentionDays),
          ratePerMinute: String(s.ratePerMinute),
          maxQueuedPerExtension: String(s.maxQueuedPerExtension),
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
    for (const f of NUMBER_FIELDS) {
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
      await putMessageSettings({
        enabled,
        ttlHours: Number(draft.ttlHours),
        retentionDays: Number(draft.retentionDays),
        ratePerMinute: Number(draft.ratePerMinute),
        maxQueuedPerExtension: Number(draft.maxQueuedPerExtension),
      });
      onSaved();
    } catch (err: unknown) {
      const fe = fieldErrors(err);
      if (fe.length > 0) {
        setFields(Object.fromEntries(fe.map((f) => [f.path, f.message])));
      }
      setError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <>
      <Modal
        title="Messaging settings"
        onClose={onClose}
        actions={
          <>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button
              type="submit"
              form="message-settings"
              disabled={busy || loaded === null}
            >
              Save settings
            </Button>
          </>
        }
      >
        {loaded === null && !error && <Spinner label="Loading settings…" />}
        {error && (
          <Alert tone="bad" title="Could not save the settings">
            {error}
          </Alert>
        )}
        {loaded !== null && (
          <form id="message-settings" className="cf-stack" onSubmit={save}>
            <Switch
              label="Messaging"
              hint="Off refuses new messages. Messages already waiting are still delivered."
              labelPosition="end"
              checked={enabled}
              onChange={(e) => setEnabled(e.target.checked)}
            />
            {NUMBER_FIELDS.map((f) => (
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
            <Button
              variant="danger"
              icon="trash-2"
              onClick={() => setPurging(true)}
            >
              Delete old history…
            </Button>
          </form>
        )}
      </Modal>
      {purging && (
        <ConfirmDialog
          title="Delete message history?"
          description={
            <>
              Delivered and failed messages older than{" "}
              <label>
                <span className="visually-hidden">Days</span>
                <input
                  className="az-input"
                  style={{ width: 80 }}
                  aria-label="Days"
                  inputMode="numeric"
                  value={purgeDays}
                  onChange={(e) => setPurgeDays(e.target.value)}
                />
              </label>{" "}
              days are deleted for good (0 deletes every finished message).
              Messages still waiting are kept.
            </>
          }
          confirmLabel="Delete history"
          errorTitle="Could not delete the history"
          onConfirm={async () => {
            const n = Number(purgeDays);
            if (!Number.isInteger(n) || n < 0 || n > 3650) {
              throw new Error("Enter a whole number of days from 0 to 3650.");
            }
            const r = await purgeMessages(n);
            setPurging(false);
            onSaved();
            void r;
          }}
          onClose={() => setPurging(false)}
        />
      )}
    </>
  );
}

/**
 * Text messages (SIP MESSAGE): conversations between phones, to groups and to
 * everyone, with search; and sending to an extension, a group, all phones or
 * an external number. Message text is private to administrators; an operator
 * may send. Retention and limits are administrator settings.
 */
export function Messages() {
  const isAdmin = useCan("admin");
  const canSend = useCan("operator");
  const [settings, setSettings] = useState(false);
  const toast = useToast();

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Activity"
        title="Messages"
        description="Text messages between phones, to groups and to all phones. A phone that is switched off gets its messages when it registers again."
        actions={
          <Can min="admin">
            <Button
              variant="secondary"
              icon="settings"
              onClick={() => setSettings(true)}
            >
              Settings
            </Button>
          </Can>
        }
      />
      <Workspace
        api={messagesApi}
        canRead={isAdmin}
        canSend={canSend}
        allowBroadcast
        heading="Conversations"
      />
      {settings && (
        <SettingsDialog
          onClose={() => setSettings(false)}
          onSaved={() => {
            setSettings(false);
            toast.show("Messaging settings saved.");
          }}
        />
      )}
      {toast.node}
    </section>
  );
}
