import { useCallback, useEffect, useState, type FormEvent } from "react";
import { errorMessage, fieldErrors } from "../api";
import type { AIMode } from "../api/inbox";
import {
  AI_MODE_LABEL,
  createFAQ,
  deleteFAQ,
  getAddressAI,
  getMessagingAI,
  listFAQ,
  putAddressAI,
  putMessagingAI,
  splitList,
  updateFAQ,
  type AddressAI,
  type FAQ,
  type MessagingAI as Settings,
} from "../api/inboxAI";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  Input,
  Modal,
  PageHeader,
  Select,
  Spinner,
  Switch,
  Table,
  useToast,
} from "../design/azrty/components";

const MAX_FAQ = 50;

function fieldMap(err: unknown): Record<string, string> {
  return Object.fromEntries(fieldErrors(err).map((f) => [f.path, f.message]));
}

/**
 * The inbox AI (spec messaging-channels M-30, M-30a): the kill switches, the
 * labels the triage uses, each number's mode and guardrails, and the FAQ an
 * auto-reply may answer from. Administrators only.
 */
export function MessagingAI() {
  const [settings, setSettings] = useState<Settings | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [enabled, setEnabled] = useState(true);
  const [autoEnabled, setAutoEnabled] = useState(true);
  const [business, setBusiness] = useState("");
  const [intents, setIntents] = useState("");
  const [topics, setTopics] = useState("");
  const [channels, setChannels] = useState<Record<number, boolean>>({});
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<Settings["addresses"][number] | null>(
    null,
  );
  const toast = useToast();

  const apply = useCallback((s: Settings) => {
    setSettings(s);
    setEnabled(s.enabled);
    setAutoEnabled(s.autoEnabled);
    setBusiness(s.businessName);
    setIntents(s.intents.join(", "));
    setTopics(s.sensitiveTopics.join(", "));
    setChannels(Object.fromEntries(s.channels.map((c) => [c.id, c.aiEnabled])));
  }, []);

  const load = useCallback(
    (signal?: AbortSignal) =>
      getMessagingAI(signal).then(apply, (err: unknown) => {
        if (!signal?.aborted) setError(errorMessage(err));
      }),
    [apply],
  );

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setFields({});
    try {
      apply(
        await putMessagingAI({
          enabled,
          autoEnabled,
          businessName: business.trim(),
          intents: splitList(intents),
          sensitiveTopics: splitList(topics),
          channels: Object.entries(channels).map(([id, on]) => ({
            id: Number(id),
            aiEnabled: on,
          })),
        }),
      );
      toast.show("AI settings saved.");
    } catch (err: unknown) {
      setFields(fieldMap(err));
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const st = settings?.status;
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Messaging"
        title="Inbox AI"
        description="Triage, reply drafts and auto-replies for customer texts and chats. Everything goes through the in-product AI's model; customer text goes only to that endpoint."
      />
      {!settings && !error && <Spinner label="Loading the AI settings…" />}
      {error && (
        <Alert tone="bad" title="Inbox AI">
          {error}
        </Alert>
      )}
      {st && !st.available && (
        <Alert tone="warn" title="The AI is not available">
          {st.reason || "The in-product AI is off."} Numbers keep their mode;
          nothing is triaged, drafted or answered until it is on.
        </Alert>
      )}
      {st?.available && st.endpointPublic && (
        <Alert tone="warn" title="AI endpoint is public">
          The model's endpoint is not on a private network
          (HELLO_AI_ALLOW_PUBLIC_ENDPOINT): customer texts leave your network
          and that provider's policy applies to them.
        </Alert>
      )}
      {st && !st.autoAllowed && (
        <Alert tone="info" title="Auto-replies are switched off">
          HELLO_MSG_AI_AUTO=false stops every auto-reply on this deployment.
          Triage and drafts go on.
        </Alert>
      )}
      {settings && (
        <form className="cf-stack" onSubmit={save}>
          <Switch
            label="Inbox AI on"
            hint="The kill switch of every triage, draft and auto-reply."
            labelPosition="end"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          <Switch
            label="Auto-replies on"
            hint="The kill switch of the auto-replies only; triage and drafts go on."
            labelPosition="end"
            checked={autoEnabled}
            onChange={(e) => setAutoEnabled(e.target.checked)}
          />
          <Input
            label="Business name"
            hint="The name the AI drafts and answers for."
            value={business}
            maxLength={100}
            error={fields.businessName}
            onChange={(e) => setBusiness(e.target.value)}
          />
          <Input
            label="Intents"
            hint="Comma-separated. The triage picks one of these, or other. An auto-reply answers only these intents (a number can narrow it)."
            value={intents}
            error={fields.intents}
            onChange={(e) => setIntents(e.target.value)}
          />
          <Input
            label="Sensitive topics"
            hint="Comma-separated. A text about one of these always goes to a person."
            value={topics}
            error={fields.sensitiveTopics}
            onChange={(e) => setTopics(e.target.value)}
          />
          {settings.channels.length > 0 && (
            <fieldset className="cf-form__section">
              <legend className="az-eyebrow">Channels</legend>
              {settings.channels.map((c) => (
                <Switch
                  key={c.id}
                  label={`AI on ${c.name}`}
                  labelPosition="end"
                  checked={channels[c.id] ?? true}
                  onChange={(e) =>
                    setChannels((m) => ({ ...m, [c.id]: e.target.checked }))
                  }
                />
              ))}
            </fieldset>
          )}
          <div>
            <Button type="submit" disabled={busy}>
              Save AI settings
            </Button>
          </div>
        </form>
      )}
      {settings && (
        <>
          <h2 className="az-h3" style={{ marginTop: 32 }}>
            Numbers and chat sites
          </h2>
          <Table
            caption="Each number's AI mode"
            rows={settings.addresses}
            rowKey={(a) => a.id}
            columns={[
              {
                key: "address",
                label: "Number",
                primary: true,
                render: (a) =>
                  a.label ? `${a.label} (${a.address})` : a.address,
              },
              {
                key: "mode",
                label: "AI",
                render: (a) => (
                  <Badge tone={a.mode === "off" ? "neutral" : "info"}>
                    {AI_MODE_LABEL[a.mode]}
                  </Badge>
                ),
              },
              { key: "faq", label: "FAQ", render: (a) => String(a.faqCount) },
              {
                key: "edit",
                label: "",
                align: "right",
                render: (a) => (
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => setEditing(a)}
                  >
                    Edit AI
                  </Button>
                ),
              },
            ]}
          />
        </>
      )}
      {editing && (
        <AddressAIDialog
          address={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            void load();
            toast.show("Saved.");
          }}
        />
      )}
      {toast.node}
    </section>
  );
}

/** One number's AI: its mode, guardrails and FAQ. */
function AddressAIDialog({
  address,
  onClose,
  onSaved,
}: {
  address: Settings["addresses"][number];
  onClose: () => void;
  onSaved: () => void;
}) {
  const [ai, setAI] = useState<AddressAI | null>(null);
  const [faq, setFAQ] = useState<FAQ[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [form, setForm] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [snippet, setSnippet] = useState<Partial<FAQ> | null>(null);
  const [removing, setRemoving] = useState<FAQ | null>(null);

  const reloadFAQ = useCallback(
    (signal?: AbortSignal) =>
      listFAQ(address.id, signal).then(setFAQ, (err: unknown) => {
        if (!signal?.aborted) setError(errorMessage(err));
      }),
    [address.id],
  );

  useEffect(() => {
    const controller = new AbortController();
    getAddressAI(address.id, controller.signal)
      .then((a) => {
        setAI(a);
        setForm({
          mode: a.mode,
          disclosureText: a.disclosureText,
          holdingText: a.holdingText,
          maxReplies: String(a.maxReplies),
          hourlyCap: String(a.hourlyCap),
          minConfidence: String(a.minConfidence),
          autoIntents: a.autoIntents.join(", "),
          handoffWords: a.handoffWords.join(", "),
          redact: a.redact ? "1" : "",
          disclosure: a.disclosure ? "1" : "",
        });
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      });
    void reloadFAQ(controller.signal);
    return () => controller.abort();
  }, [address.id, reloadFAQ]);

  const set = (k: string, v: string) => setForm((f) => ({ ...f, [k]: v }));

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setFields({});
    try {
      await putAddressAI(address.id, {
        mode: form.mode as AIMode,
        redact: form.redact === "1",
        disclosure: form.disclosure === "1",
        disclosureText: form.disclosureText,
        holdingText: form.holdingText,
        maxReplies: Number(form.maxReplies),
        hourlyCap: Number(form.hourlyCap),
        minConfidence: Number(form.minConfidence),
        autoIntents: splitList(form.autoIntents ?? ""),
        handoffWords: splitList(form.handoffWords ?? ""),
      });
      onSaved();
      onClose();
    } catch (err: unknown) {
      setFields(fieldMap(err));
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function saveSnippet(e: FormEvent) {
    e.preventDefault();
    if (!snippet) return;
    setBusy(true);
    setFields({});
    try {
      const body = {
        title: snippet.title ?? "",
        body: snippet.body ?? "",
        enabled: snippet.enabled ?? true,
      };
      if (snippet.id) await updateFAQ(snippet.id, body);
      else await createFAQ({ ...body, addressId: address.id });
      setSnippet(null);
      await reloadFAQ();
      onSaved();
    } catch (err: unknown) {
      setFields(fieldMap(err));
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const auto = form.mode === "auto";
  return (
    <Modal
      title={`AI of ${address.label || address.address}`}
      onClose={onClose}
      width={720}
      actions={
        <>
          <Button variant="secondary" onClick={onClose}>
            Close
          </Button>
          <Button type="submit" form="address-ai-form" disabled={busy || !ai}>
            Save
          </Button>
        </>
      }
    >
      {error && (
        <Alert tone="bad" title="Could not save">
          {error}
        </Alert>
      )}
      {!ai && !error && <Spinner label="Loading…" />}
      {ai && (
        <form id="address-ai-form" className="cf-stack" onSubmit={save}>
          <Select
            id="ai-mode"
            label="Mode"
            hint="Triage classifies and summarises; drafts add a reply an agent must click to use; auto-reply lets the AI answer from this number's FAQ, within the limits below. Turning auto-reply on or off is audited."
            value={form.mode}
            error={fields.mode}
            options={(Object.keys(AI_MODE_LABEL) as AIMode[]).map((m) => ({
              value: m,
              label: AI_MODE_LABEL[m],
            }))}
            onChange={(e) => set("mode", e.target.value)}
          />
          <Switch
            label="Remove phone numbers and email addresses before the AI reads a text"
            labelPosition="end"
            checked={form.redact === "1"}
            onChange={(e) => set("redact", e.target.checked ? "1" : "")}
          />
          {auto && (
            <>
              {faq.filter((f) => f.enabled).length === 0 && (
                <Alert tone="warn" title="No FAQ yet">
                  The AI answers only from the FAQ below; with none, every text
                  goes to a person.
                </Alert>
              )}
              <Switch
                label="Start the first AI reply with a disclosure"
                labelPosition="end"
                checked={form.disclosure === "1"}
                onChange={(e) => set("disclosure", e.target.checked ? "1" : "")}
              />
              <Input
                label="Disclosure line"
                value={form.disclosureText ?? ""}
                maxLength={160}
                error={fields.disclosureText}
                onChange={(e) => set("disclosureText", e.target.value)}
              />
              <Input
                label="Holding line"
                hint="Sent once, until a person replies, when the AI hands a conversation to a person. Empty sends nothing."
                value={form.holdingText ?? ""}
                maxLength={480}
                error={fields.holdingText}
                onChange={(e) => set("holdingText", e.target.value)}
              />
              <div className="cf-two">
                <Input
                  label="AI replies before a person must answer"
                  inputMode="numeric"
                  value={form.maxReplies ?? ""}
                  error={fields.maxReplies}
                  onChange={(e) => set("maxReplies", e.target.value)}
                />
                <Input
                  label="AI messages per hour at most"
                  inputMode="numeric"
                  value={form.hourlyCap ?? ""}
                  error={fields.hourlyCap}
                  onChange={(e) => set("hourlyCap", e.target.value)}
                />
              </div>
              <Input
                label="Least confidence (0 to 1)"
                hint="Below it, a person answers."
                inputMode="decimal"
                value={form.minConfidence ?? ""}
                error={fields.minConfidence}
                onChange={(e) => set("minConfidence", e.target.value)}
              />
              <Input
                label="Intents the AI may answer"
                hint="Comma-separated; empty allows every intent but other."
                value={form.autoIntents ?? ""}
                error={fields.autoIntents}
                onChange={(e) => set("autoIntents", e.target.value)}
              />
              <Input
                label="Handoff words"
                hint="Comma-separated. A text with one of these always goes to a person."
                value={form.handoffWords ?? ""}
                error={fields.handoffWords}
                onChange={(e) => set("handoffWords", e.target.value)}
              />
            </>
          )}
        </form>
      )}
      <h3 className="az-h4" style={{ marginTop: 24 }}>
        FAQ ({faq.length} of {MAX_FAQ})
      </h3>
      <p className="az-caption">
        What an auto-reply may say, and the facts a draft uses. A reply may only
        contain a link that is in a snippet.
      </p>
      <ul className="cf-stack" style={{ listStyle: "none", padding: 0 }}>
        {faq.map((f) => (
          <li key={f.id} className="az-card" style={{ padding: 12 }}>
            <strong>{f.title}</strong>{" "}
            {!f.enabled && <Badge tone="neutral">Off</Badge>}
            <p style={{ whiteSpace: "pre-wrap", margin: "4px 0" }}>{f.body}</p>
            <Button size="sm" variant="ghost" onClick={() => setSnippet(f)}>
              Edit
            </Button>
            <Button size="sm" variant="ghost" onClick={() => setRemoving(f)}>
              Delete
            </Button>
          </li>
        ))}
      </ul>
      {snippet ? (
        <form
          className="cf-stack"
          onSubmit={saveSnippet}
          aria-label="FAQ snippet"
        >
          <Input
            label="Question"
            value={snippet.title ?? ""}
            maxLength={200}
            error={fields.title}
            onChange={(e) => setSnippet({ ...snippet, title: e.target.value })}
          />
          <label className="az-field__label" htmlFor="faq-body">
            Answer
          </label>
          <textarea
            id="faq-body"
            className="az-input prov-textarea"
            rows={5}
            maxLength={4096}
            value={snippet.body ?? ""}
            onChange={(e) => setSnippet({ ...snippet, body: e.target.value })}
          />
          {fields.body && <p className="az-caption">{fields.body}</p>}
          <Switch
            label="The AI may use it"
            labelPosition="end"
            checked={snippet.enabled ?? true}
            onChange={(e) =>
              setSnippet({ ...snippet, enabled: e.target.checked })
            }
          />
          <div className="inbox-actions">
            <Button type="submit" disabled={busy}>
              Save snippet
            </Button>
            <Button
              type="button"
              variant="ghost"
              onClick={() => setSnippet(null)}
            >
              Cancel
            </Button>
          </div>
        </form>
      ) : (
        <Button
          variant="secondary"
          icon="plus"
          disabled={faq.length >= MAX_FAQ}
          onClick={() => setSnippet({ enabled: true })}
        >
          Add a snippet
        </Button>
      )}
      {removing && (
        <ConfirmDialog
          title={`Delete "${removing.title}"?`}
          description="The AI no longer answers from it."
          confirmLabel="Delete snippet"
          errorTitle="Could not delete the snippet"
          onConfirm={async () => {
            await deleteFAQ(removing.id);
            setRemoving(null);
            await reloadFAQ();
            onSaved();
          }}
          onClose={() => setRemoving(null)}
        />
      )}
    </Modal>
  );
}
