import { useEffect, useState } from "react";
import { Link, useNavigate } from "react-router";
import { errorMessage, fieldErrors, type FieldError } from "../api";
import { mapFieldErrors, type ErrorMap } from "../forms";
import {
  agentStateBadge,
  createVoiceAgent,
  deleteVoiceAgent,
  getVoiceStatus,
  listVoiceAgents,
  type VoiceAgent,
} from "../api/voice";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  Icon,
  Input,
  Modal,
  PageHeader,
  Select,
  Spinner,
  Table,
  type TableColumn,
  useRestoreFocus,
  useToast,
} from "../design/azrty/components";
import { Can } from "../role";
import { usePolling } from "../usePolling";

/** The starter prompt of the create wizard (spec S-25: no JSON anywhere). */
const STARTER_PROMPT = `You are a friendly assistant who answers this company's phone. Find out what the caller needs, answer from what they tell you, and keep replies short.

Never invent facts, never give legal or medical advice, and hand over to a person when the caller asks for one.`;

/**
 * Voice agents (/voice/agents): the list with each agent's state, and the
 * create wizard (spec S-25).
 */
export function VoiceAgents() {
  const [agents, setAgents] = useState<VoiceAgent[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [deleting, setDeleting] = useState<VoiceAgent | null>(null);
  const toast = useToast();
  const navigate = useNavigate();
  const status = usePolling(getVoiceStatus, 30_000);
  const live = status.status === "loading" ? undefined : status.data;

  useEffect(() => {
    const controller = new AbortController();
    listVoiceAgents(controller.signal)
      .then(setAgents)
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      });
    return () => controller.abort();
  }, [creating, deleting]);

  async function onDelete(agent: VoiceAgent) {
    try {
      await deleteVoiceAgent(agent.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${agent.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    setAgents((items) => items?.filter((a) => a.id !== agent.id) ?? null);
    setDeleting(null);
    toast.show(`Voice agent ${agent.name} deleted.`);
  }

  const newAgent = (
    <Can>
      <Button icon="plus" onClick={() => setCreating(true)}>
        New voice agent
      </Button>
    </Can>
  );

  const columns: TableColumn<VoiceAgent>[] = [
    {
      key: "name",
      label: "Agent",
      render: (a) => (
        <>
          <Link
            className="calls-row-link calls-strong"
            to={`/voice/agents/${encodeURIComponent(String(a.id))}`}
          >
            {a.name}
          </Link>
          {a.description && <small>{a.description}</small>}
        </>
      ),
    },
    {
      key: "extension",
      label: "Extension",
      mono: true,
      render: (a) => a.extension || "—",
    },
    { key: "sipUser", label: "SIP user", mono: true, render: (a) => a.sipUser },
    {
      key: "state",
      label: "State",
      render: (a) => {
        const s = agentStateBadge(a, live);
        return (
          <Badge tone={s.tone} dot>
            {s.label}
          </Badge>
        );
      },
    },
    { key: "revision", label: "Revision", mono: true, align: "right" },
    {
      key: "actions",
      label: "",
      render: (a) => (
        <Can>
          <Button
            variant="ghost"
            size="sm"
            icon="trash-2"
            aria-label={`Delete voice agent ${a.name}`}
            onClick={(e) => {
              // The row click opens the editor; a delete stays put.
              e.stopPropagation();
              setDeleting(a);
            }}
          >
            Delete
          </Button>
        </Can>
      ),
    },
  ];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="AI"
        title="Voice agents"
        description="Automated assistants that answer calls, with a persona and tools. Route a DID, an extension or a ring group's last resort to one."
        actions={newAgent}
      />
      {error && (
        <Alert tone="bad" title="Could not load the voice agents">
          {error}
        </Alert>
      )}
      {agents === null && !error && <Spinner label="Loading voice agents…" />}
      {agents !== null && agents.length === 0 && (
        <EmptyState
          icon="bot"
          title="No voice agents yet"
          description="Create one, give it a persona and tools, then route calls to it."
          action={newAgent}
        />
      )}
      {agents !== null && agents.length > 0 && (
        <Table
          caption="Voice agents"
          columns={columns}
          rows={agents}
          rowKey={(a) => String(a.id)}
          onRowClick={(a) =>
            void navigate(`/voice/agents/${encodeURIComponent(String(a.id))}`)
          }
        />
      )}
      {creating && (
        <NewAgentModal
          onClose={() => setCreating(false)}
          onCreated={(a) => {
            setCreating(false);
            toast.show(`Voice agent ${a.name} created.`);
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete voice agent ${deleting.name}?`}
          description="Routes, ring groups and feature codes that name it must be changed first; its past calls keep the agent's name."
          confirmLabel="Delete agent"
          confirmIcon="trash-2"
          errorTitle="Could not delete the voice agent"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

/** The short create wizard: name, persona starter, language, greeting, extension. */
function NewAgentModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (a: VoiceAgent) => void;
}) {
  useRestoreFocus();
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [language, setLanguage] = useState("en-US");
  const [greeting, setGreeting] = useState("");
  const [extension, setExtension] = useState("");
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit() {
    const found: Record<string, string> = {};
    if (name.trim() === "") found.name = "Enter a name.";
    else if (!/^[A-Za-z0-9._-]{1,64}$/.test(name.trim())) {
      found.name = "Use 1-64 of A-Z a-z 0-9 . _ -.";
    }
    if (extension.trim() !== "" && !/^[0-9]{2,10}$/.test(extension.trim())) {
      found.extension = "Use 2 to 10 digits.";
    }
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    setFormError(null);
    setUnmatched([]);
    try {
      onCreated(
        await createVoiceAgent({
          name: name.trim(),
          description: description.trim(),
          language,
          greeting: greeting.trim(),
          prompt: STARTER_PROMPT,
          extension: extension.trim() || undefined,
        }),
      );
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), [
        "name",
        "description",
        "language",
        "greeting",
        "extension",
      ]);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Modal
      title="New voice agent"
      description="A persona and a number; tools and limits come next, in the editor."
      onClose={busy ? undefined : onClose}
      actions={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={() => void submit()} disabled={busy}>
            Create agent
          </Button>
        </>
      }
    >
      <div className="pf-stack">
        {formError && (
          <Alert tone="bad" title="Could not create the voice agent">
            {formError}
          </Alert>
        )}
        {unmatched.length > 0 && (
          <Alert
            tone="warn"
            title="Some problems could not be shown on a field"
          >
            <ul className="cf-plain">
              {unmatched.map((f, i) => (
                <li key={i}>{f.message}</li>
              ))}
            </ul>
          </Alert>
        )}
        <Input
          id="new-agent-name"
          label="Name"
          mono
          autoFocus
          placeholder="support"
          hint="Shown in routes and call records."
          value={name}
          error={errors.name}
          onChange={(e) => setName(e.target.value)}
        />
        <Input
          id="new-agent-description"
          label="What it is for"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
        <div className="cf-two">
          <Select
            id="new-agent-language"
            label="Language"
            value={language}
            options={[
              { value: "en-US", label: "English (US)" },
              { value: "en-GB", label: "English (UK)" },
              { value: "nl-NL", label: "Nederlands" },
              { value: "fr-FR", label: "Français" },
              { value: "de-DE", label: "Deutsch" },
              { value: "ar-AE", label: "العربية" },
            ]}
            onChange={(e) => setLanguage(e.target.value)}
          />
          <Input
            id="new-agent-extension"
            label="Extension (optional)"
            mono
            inputMode="numeric"
            placeholder="3000"
            hint="Dial it from any phone to reach the agent."
            value={extension}
            error={errors.extension}
            onChange={(e) => setExtension(e.target.value)}
          />
        </div>
        <Input
          id="new-agent-greeting"
          label="Greeting"
          hint="Spoken first. Say the call is handled by an automated assistant (spec: recording consent, S-24)."
          value={greeting}
          error={errors.greeting}
          onChange={(e) => setGreeting(e.target.value)}
        />
        <p className="cf-form__note">
          <Icon name="info" size={13} /> A starter prompt is filled in; edit it
          on the Persona tab.
        </p>
      </div>
    </Modal>
  );
}
