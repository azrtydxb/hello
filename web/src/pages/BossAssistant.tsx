import { useEffect, useMemo, useState } from "react";
import {
  errorMessage,
  fieldErrors,
  listBossLinks,
  listExtensions,
  putBossLinks,
  type BossLink,
  type BossLinkInput,
  type BossRingMode,
  type Extension,
} from "../api";
import {
  Alert,
  Button,
  EmptyState,
  IconButton,
  PageHeader,
  Select,
  Spinner,
  Switch,
  useToast,
} from "../design/azrty/components";
import { useCan } from "../role";
import "./platform/platform.css";

const RING_MODES = [
  { value: "both", label: "Both ring together" },
  { value: "assistant_first", label: "Assistant first, then both" },
];

const idOf = (v: string) => (v === "" ? "" : Number(v));

/** A link being edited: the writable fields plus a stable key for the row. */
interface Row extends BossLinkInput {
  key: number;
}

const toRow = (l: BossLink, key: number): Row => ({
  key,
  bossExtensionId: l.bossExtensionId,
  assistantExtensionId: l.assistantExtensionId,
  ringMode: l.ringMode,
  screening: l.screening,
  enabled: l.enabled,
  divertOn: l.divertOn,
  outboundAsBoss: l.outboundAsBoss,
});

const toInput = (r: Row): BossLinkInput => ({
  bossExtensionId: r.bossExtensionId,
  assistantExtensionId: r.assistantExtensionId,
  ringMode: r.ringMode,
  screening: r.screening,
  enabled: r.enabled,
  divertOn: r.divertOn,
  outboundAsBoss: r.outboundAsBoss,
});

/** What is wrong with the rows, by row key; the same rules the API checks. */
function problems(rows: readonly Row[]): Map<number, string> {
  const out = new Map<number, string>();
  const bosses = new Set<string>();
  const outbound = new Set<string>();
  for (const r of rows) {
    const boss = String(r.bossExtensionId);
    const assistant = String(r.assistantExtensionId);
    if (boss === "" || assistant === "") {
      out.set(r.key, "Choose a boss and an assistant.");
    } else if (boss === assistant) {
      out.set(r.key, "The assistant must be a different extension.");
    } else if (bosses.has(boss)) {
      out.set(r.key, "A boss has one assistant.");
    } else if (r.outboundAsBoss && outbound.has(assistant)) {
      out.set(
        r.key,
        "An assistant can present the caller ID of one boss only.",
      );
    }
    bosses.add(boss);
    if (r.outboundAsBoss) outbound.add(assistant);
  }
  return out;
}

/** Boss and assistant: who answers for whom (operator and above edit). */
export function BossAssistant() {
  const canEdit = useCan("operator");
  const toast = useToast();
  const [state, setState] = useState<
    | { status: "loading" }
    | { status: "error"; message: string }
    | { status: "ready"; extensions: Extension[] }
  >({ status: "loading" });
  const [rows, setRows] = useState<Row[]>([]);
  const [nextKey, setNextKey] = useState(1);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  useEffect(() => {
    const ctl = new AbortController();
    Promise.all([listBossLinks(ctl.signal), listExtensions(ctl.signal)])
      .then(([links, extensions]) => {
        setRows(links.map((l, i) => toRow(l, i + 1)));
        setNextKey(links.length + 1);
        setState({ status: "ready", extensions });
      })
      .catch((err: unknown) => {
        if (!ctl.signal.aborted)
          setState({ status: "error", message: errorMessage(err) });
      });
    return () => ctl.abort();
  }, []);

  const wrong = useMemo(() => problems(rows), [rows]);

  if (state.status === "loading") return <Spinner label="Loading links…" />;
  if (state.status === "error")
    return (
      <Alert tone="bad" title="Could not load the boss links">
        {state.message}
      </Alert>
    );
  const extOptions = [
    { value: "", label: "Choose an extension" },
    ...state.extensions.map((e) => ({
      value: String(e.id),
      label: e.name ? `${e.number} ${e.name}` : e.number,
    })),
  ];

  const change = (key: number, patch: Partial<BossLinkInput>) =>
    setRows((rs) => rs.map((r) => (r.key === key ? { ...r, ...patch } : r)));

  async function save() {
    if (busy || wrong.size > 0) return;
    setBusy(true);
    setFormError(null);
    try {
      const stored = await putBossLinks(rows.map(toInput));
      setRows(stored.map((l, i) => toRow(l, i + 1)));
      setNextKey(stored.length + 1);
      toast.show("Boss links saved.");
    } catch (err) {
      const fe = fieldErrors(err);
      setFormError(
        fe.length > 0
          ? fe.map((f) => `${f.path}: ${f.message}`).join("; ")
          : errorMessage(err),
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Call flow"
        title="Boss and assistant"
        description="Calls to a boss ring the boss's phones and the assistant's together, and whoever answers first takes the call."
        actions={
          canEdit && (
            <>
              <Button
                variant="secondary"
                icon="plus"
                onClick={() => {
                  setRows((rs) => [
                    ...rs,
                    {
                      key: nextKey,
                      bossExtensionId: "",
                      assistantExtensionId: "",
                      ringMode: "both",
                      screening: false,
                      enabled: true,
                      divertOn: false,
                      outboundAsBoss: false,
                    },
                  ]);
                  setNextKey(nextKey + 1);
                }}
              >
                Add link
              </Button>
              <Button
                disabled={busy || wrong.size > 0}
                onClick={() => void save()}
              >
                Save
              </Button>
            </>
          )
        }
      />
      {formError && (
        <Alert tone="bad" title="Not saved">
          {formError}
        </Alert>
      )}
      {rows.length === 0 ? (
        <EmptyState
          icon="briefcase-business"
          title="No boss links"
          description="Link a boss to an assistant so that calls to the boss ring both."
        />
      ) : (
        <div className="pf-stack">
          {rows.map((r) => (
            <div
              key={r.key}
              className="az-card pf-stack"
              role="group"
              aria-label={`Link ${r.key}`}
            >
              <div className="pf-grid2">
                <Select
                  label="Boss"
                  id={`boss-${r.key}`}
                  value={String(r.bossExtensionId)}
                  options={extOptions}
                  disabled={!canEdit}
                  onChange={(e) =>
                    change(r.key, { bossExtensionId: idOf(e.target.value) })
                  }
                />
                <Select
                  label="Assistant"
                  id={`assistant-${r.key}`}
                  value={String(r.assistantExtensionId)}
                  options={extOptions}
                  disabled={!canEdit}
                  onChange={(e) =>
                    change(r.key, {
                      assistantExtensionId: idOf(e.target.value),
                    })
                  }
                />
              </div>
              <Select
                label="Ring mode"
                id={`mode-${r.key}`}
                hint="Assistant first rings the assistant alone for 10 seconds."
                value={r.ringMode}
                options={RING_MODES}
                disabled={!canEdit}
                onChange={(e) =>
                  change(r.key, { ringMode: e.target.value as BossRingMode })
                }
              />
              <Switch
                label="Enabled"
                checked={r.enabled}
                disabled={!canEdit}
                onChange={(e) => change(r.key, { enabled: e.target.checked })}
              />
              <Switch
                label="Screening"
                hint="The assistant who answers hears who the call is for, then presses 1 to put it through or 2 to take a message."
                checked={r.screening}
                disabled={!canEdit}
                onChange={(e) => change(r.key, { screening: e.target.checked })}
              />
              <Switch
                label="Divert"
                hint="Only the assistant rings. The boss toggles it with the divert code on their phone."
                checked={r.divertOn}
                disabled={!canEdit}
                onChange={(e) => change(r.key, { divertOn: e.target.checked })}
              />
              <Switch
                label="Assistant calls out as the boss"
                hint="The assistant's outbound calls present the boss's caller ID."
                checked={r.outboundAsBoss}
                disabled={!canEdit}
                onChange={(e) =>
                  change(r.key, { outboundAsBoss: e.target.checked })
                }
              />
              {wrong.get(r.key) && (
                <Alert tone="warn" title="Check this link">
                  {wrong.get(r.key)}
                </Alert>
              )}
              {canEdit && (
                <IconButton
                  icon="trash-2"
                  label={`Remove link ${r.key}`}
                  onClick={() =>
                    setRows((rs) => rs.filter((x) => x.key !== r.key))
                  }
                />
              )}
            </div>
          ))}
        </div>
      )}
      {toast.node}
    </section>
  );
}
