import { useEffect, useState } from "react";
import { errorMessage } from "../api";
import {
  createVoiceRuntimeAccount,
  getVoiceStatus,
  listVoiceAgents,
  rotateVoiceSIPSecret,
  type VoiceAgent,
  type VoiceAgentRuntimeState,
  type VoiceSecretRotation,
  type VoiceRuntimeAccount,
} from "../api/voice";
import {
  Alert,
  Badge,
  Button,
  EmptyState,
  Icon,
  Input,
  Modal,
  PageHeader,
  Spinner,
  Table,
  type TableColumn,
  useToast,
} from "../design/azrty/components";
import { Can } from "../role";
import { usePolling } from "../usePolling";
import { ago } from "./platform/health";

/** The runtime service account and the calls it reports (spec S-30). */
export function VoiceRuntime() {
  const status = usePolling(getVoiceStatus, 15_000);
  const toast = useToast();
  const [agents, setAgents] = useState<VoiceAgent[] | null>(null);
  const [rotated, setRotated] = useState<VoiceSecretRotation | null>(null);
  const [account, setAccount] = useState<VoiceRuntimeAccount | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    listVoiceAgents(controller.signal)
      .then(setAgents)
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      });
    return () => controller.abort();
  }, []);

  const data = status.status === "loading" ? undefined : status.data;
  const live = data?.healthy ?? false;

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="AI"
        title="Voice runtime"
        description="talking-agent polls the runtime API for the personas it serves; this is when it last did, and how its agents are loaded."
      />
      {status.status === "loading" && (
        <Spinner label="Loading runtime status…" />
      )}
      {status.status === "error" && (
        <Alert tone="bad" title="Could not load the runtime status">
          {status.message}
        </Alert>
      )}
      {data && (
        <div className="cf-stack">
          {!live && (
            <Alert tone="bad" title="The runtime has not reported recently">
              Calls still route and agents still answer; only this status is
              affected. Check that talking-agent is running and its service
              account still works.
            </Alert>
          )}
          <div className="az-card pf-card pf-card--tight">
            <h2 className="pf-h3" id="runtime-state">
              talking-agent
            </h2>
            <ul className="pf-tokens" aria-labelledby="runtime-state">
              <li>
                <Icon name="activity" size={15} />
                <div className="pf-tokens__meta">
                  <div className="pf-tokens__name">
                    <Badge tone={live ? "good" : "bad"} dot>
                      {data.lastSeenAt
                        ? live
                          ? "Live"
                          : "Stale"
                        : "Never seen"}
                    </Badge>{" "}
                    {data.version && (
                      <span className="pf-mono">{data.version}</span>
                    )}
                  </div>
                  <div className="pf-tokens__when">
                    {data.lastSeenAt
                      ? `last ack ${ago(data.lastSeenAt, new Date())}`
                      : "no acknowledgement yet"}{" "}
                    · revision {data.revision}
                  </div>
                </div>
                {data.lastSeenAt && (
                  <Badge tone={live ? "good" : "warn"}>
                    {live ? "under 2 minutes" : "over 2 minutes"}
                  </Badge>
                )}
              </li>
            </ul>
            <Can min="admin">
              <div className="cf-row__actions" style={{ marginTop: 12 }}>
                <Button
                  variant="secondary"
                  size="sm"
                  icon="key-round"
                  onClick={async () => {
                    try {
                      setRotated(await rotateVoiceSIPSecret());
                    } catch (err: unknown) {
                      setError(errorMessage(err));
                    }
                  }}
                >
                  Rotate the SIP secret
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  icon="bot"
                  onClick={async () => {
                    try {
                      setAccount(await createVoiceRuntimeAccount());
                    } catch (err: unknown) {
                      setError(errorMessage(err));
                    }
                  }}
                >
                  Create the runtime account
                </Button>
              </div>
            </Can>
            {error && (
              <Alert tone="bad" title="The operation failed">
                {error}
              </Alert>
            )}
          </div>
          <AgentStates agents={agents} states={data.agents} />
        </div>
      )}
      {rotated && (
        <ShownOnce
          title="SIP secret rotated"
          label="New HELLO_VOICE_SIP_SECRET"
          value={rotated.secret}
          onClose={() => setRotated(null)}
        />
      )}
      {account && (
        <ShownOnce
          title="Runtime account created"
          label="Client secret of the runtime service account"
          value={account.secret}
          onClose={() => setAccount(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

/** Per-agent load state: live, stale (revision n), not loaded (S-20). */
function AgentStates({
  agents,
  states,
}: {
  agents: VoiceAgent[] | null;
  states: VoiceAgentRuntimeState[];
}) {
  const columns: TableColumn<VoiceAgentRuntimeState>[] = [
    { key: "name", label: "Agent" },
    {
      key: "state",
      label: "State",
      render: (s) => {
        const agent = agents?.find((a) => a.name === s.name);
        const current = agent?.revision === s.revision;
        const tone =
          s.state === "loaded"
            ? current
              ? "good"
              : "warn"
            : s.state === "failed"
              ? "bad"
              : "outline";
        const label =
          s.state === "loaded"
            ? current
              ? "Live"
              : `Stale (revision ${s.revision})`
            : s.state === "failed"
              ? "Failed"
              : "Not loaded";
        return (
          <Badge tone={tone} dot>
            {label}
          </Badge>
        );
      },
    },
    { key: "revision", label: "Loaded revision", align: "right", mono: true },
  ];
  return (
    <div className="az-card pf-card pf-card--tight">
      <h2 className="pf-h3" id="agent-states">
        Agents
      </h2>
      {states.length === 0 ? (
        <EmptyState
          icon="bot"
          title="Nothing loaded yet"
          description="The runtime lists the agents it loaded once it has acked."
        />
      ) : (
        <Table
          caption="Loaded agents"
          columns={columns}
          rows={states}
          rowKey={(s) => s.name}
        />
      )}
    </div>
  );
}

/** A just-issued secret, shown once; closing the dialog drops it. */
function ShownOnce({
  title,
  label,
  value,
  onClose,
}: {
  title: string;
  label: string;
  value: string;
  onClose: () => void;
}) {
  return (
    <Modal
      title={title}
      description={label}
      onClose={onClose}
      actions={<Button onClick={onClose}>Done</Button>}
    >
      <Alert tone="warn" title="Shown once">
        Copy it now. It is stored sealed and cannot be shown again; rotate again
        if it is lost.
      </Alert>
      <Input
        label={label}
        id="shown-once-secret"
        mono
        readOnly
        autoFocus
        value={value}
        onFocus={(e) => e.currentTarget.select()}
      />
    </Modal>
  );
}
