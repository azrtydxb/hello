import { useState, type FormEvent } from "react";
import {
  errorMessage,
  fieldErrors,
  listExtensions,
  type Extension,
} from "../api";
import {
  adoptCandidate,
  bulkAdopt,
  dismissCandidate,
  listCandidates,
  listIgnored,
  unignoreMac,
  type BulkResult,
  type Candidate,
  type IgnoredMac,
} from "../api/onboarding";
import { vendorLabel } from "../api/prov";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  ConfirmDialog,
  EmptyState,
  Input,
  Modal,
  PageHeader,
  SegmentedControl,
  Select,
  Spinner,
  Table,
  type TableColumn,
  useToast,
} from "../design/azrty/components";
import { formatTime } from "../format";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { Can } from "../role";
import { usePolling } from "../usePolling";
import { consecutiveNumbers } from "./onboarding/helpers";
import { formatMac } from "./phones/ui";
import "./phones/phones.css";
import "./phones/inventory.css";

/** How often the queue reloads. */
export const ONBOARDING_REFRESH_MS = 10_000;

const PICKUP =
  "The phone picks up its configuration on its next retry or after a reboot.";

interface Queue {
  candidates: Candidate[];
  ignored: IgnoredMac[];
}

function loadQueue(signal: AbortSignal): Promise<Queue> {
  return Promise.all([listCandidates(signal), listIgnored(signal)]).then(
    ([candidates, ignored]) => ({ candidates, ignored }),
  );
}

/** "Yealink T54W", or the vendor alone while the phone did not report a model. */
function modelText(c: { vendor: string; model: string }): string {
  const v = c.vendor === "generic" ? "" : vendorLabel(c.vendor);
  return `${v} ${c.model}`.trim() || "Unknown model";
}

const extensionOption = (x: Extension) => ({
  value: String(x.id),
  label: `${x.number} — ${x.name}`,
});

/** The phones that asked for a configuration before they were added. */
export function Onboarding() {
  const queue = usePolling(loadQueue, ONBOARDING_REFRESH_MS);
  const extensions = usePolling(listExtensions, 60_000);
  const toast = useToast();
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [adopting, setAdopting] = useState<Candidate | null>(null);
  const [dismissing, setDismissing] = useState<Candidate | null>(null);
  const [bulk, setBulk] = useState(false);

  const data = queue.status === "loading" ? undefined : queue.data;
  const candidates = data?.candidates ?? [];
  const exts = extensions.status === "loading" ? [] : (extensions.data ?? []);
  // A phone that left the queue (adopted elsewhere, expired) is not selected any more.
  const picked = candidates.filter((c) => selected.has(c.mac));

  function toggle(mac: string, on: boolean) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (on) next.add(mac);
      else next.delete(mac);
      return next;
    });
  }

  const allOn = candidates.length > 0 && picked.length === candidates.length;

  const columns: TableColumn<Candidate>[] = [
    {
      key: "select",
      label: (
        <Checkbox
          label={<span className="visually-hidden">Select all phones</span>}
          checked={allOn}
          onChange={(e) =>
            setSelected(
              e.target.checked
                ? new Set(candidates.map((c) => c.mac))
                : new Set(),
            )
          }
        />
      ),
      width: 44,
      primary: false,
      render: (c) => (
        <Checkbox
          label={
            <span className="visually-hidden">Select {formatMac(c.mac)}</span>
          }
          checked={selected.has(c.mac)}
          onChange={(e) => toggle(c.mac, e.target.checked)}
        />
      ),
    },
    { key: "model", label: "Model", primary: false, render: modelText },
    {
      key: "mac",
      label: "MAC",
      mono: true,
      primary: false,
      render: (c) => formatMac(c.mac),
    },
    {
      key: "ip",
      label: "IP",
      mono: true,
      primary: false,
      render: (c) => c.sourceIp || "—",
    },
    {
      key: "firmware",
      label: "Firmware",
      primary: false,
      render: (c) => c.firmware || "—",
    },
    {
      key: "first",
      label: "First seen",
      primary: false,
      render: (c) => (
        <span className="prov-cell-sm">{formatTime(c.firstSeen)}</span>
      ),
    },
    {
      key: "last",
      label: "Last seen",
      primary: false,
      render: (c) => (
        <span className="prov-cell-sm">{formatTime(c.lastSeen)}</span>
      ),
    },
    {
      key: "count",
      label: "Requests",
      align: "right",
      mono: true,
      primary: false,
      render: (c) => String(c.requestCount),
    },
    {
      key: "actions",
      label: <span className="visually-hidden">Actions</span>,
      align: "right",
      primary: false,
      render: (c) => (
        <Can>
          <div className="inv-actions">
            <Button
              size="sm"
              icon="user-plus"
              aria-label={`Adopt ${formatMac(c.mac)}`}
              onClick={() => setAdopting(c)}
            >
              Adopt
            </Button>
            <Button
              size="sm"
              variant="secondary"
              icon="x"
              aria-label={`Dismiss ${formatMac(c.mac)}`}
              onClick={() => setDismissing(c)}
            >
              Dismiss
            </Button>
          </div>
        </Can>
      ),
    },
  ];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title="Onboarding"
        description={`Phones that asked Hello for a configuration before they were added. Point DHCP option 66 at the provisioning boot URL, join the phone to the network, then adopt it here. ${PICKUP}`}
        actions={
          <Can>
            <Button
              variant="secondary"
              icon="users-round"
              disabled={picked.length === 0}
              onClick={() => setBulk(true)}
            >
              {picked.length > 0
                ? `Bulk adopt (${picked.length})`
                : "Bulk adopt"}
            </Button>
          </Can>
        }
      />

      <div className="prov-stack">
        {queue.status === "loading" && <Spinner label="Loading phones…" />}
        {queue.status === "error" && (
          <Alert tone="bad" title="Could not load the onboarding queue">
            {queue.message}
          </Alert>
        )}
        {data && candidates.length === 0 && (
          <EmptyState
            icon="smartphone"
            title="No phones waiting"
            description="A phone that asks for its configuration before it is added shows up here. This page refreshes by itself."
          />
        )}
        {candidates.length > 0 && (
          <Table
            caption="Phones waiting to be adopted"
            columns={columns}
            rows={candidates}
            rowKey={(c) => c.mac}
          />
        )}
        {data && data.ignored.length > 0 && (
          <IgnoredList
            ignored={data.ignored}
            onChanged={() => queue.reload()}
            onMessage={toast.show}
          />
        )}
      </div>

      {adopting && (
        <AdoptDialog
          candidate={adopting}
          extensions={exts}
          onClose={() => setAdopting(null)}
          onAdopted={(c) => {
            setAdopting(null);
            toggle(c.mac, false);
            queue.reload();
            toast.show(`${formatMac(c.mac)} adopted. ${PICKUP}`);
          }}
        />
      )}
      {dismissing && (
        <DismissDialog
          candidate={dismissing}
          onClose={() => setDismissing(null)}
          onDone={(c, ignored) => {
            setDismissing(null);
            toggle(c.mac, false);
            queue.reload();
            toast.show(
              ignored
                ? `${formatMac(c.mac)} dismissed and ignored.`
                : `${formatMac(c.mac)} dismissed. It returns if it asks again.`,
            );
          }}
        />
      )}
      {bulk && (
        <BulkAdoptDialog
          candidates={picked}
          extensions={exts}
          onClose={() => setBulk(false)}
          onAdopted={(n) => {
            setBulk(false);
            setSelected(new Set());
            queue.reload();
            toast.show(
              `${n} ${n === 1 ? "phone" : "phones"} adopted. ${PICKUP}`,
            );
          }}
        />
      )}
      {toast.node}
    </section>
  );
}

const ADOPT_FIELDS = ["extensionId", "label", "model"] as const;

/** Adopt one phone: the extension it is bound to and its label. */
function AdoptDialog({
  candidate,
  extensions,
  onClose,
  onAdopted,
}: {
  candidate: Candidate;
  extensions: Extension[];
  onClose: () => void;
  onAdopted: (c: Candidate) => void;
}) {
  const [extensionId, setExtensionId] = useState("");
  const [label, setLabel] = useState("");
  const [model, setModel] = useState(candidate.model);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const needsModel = candidate.model === "";

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErrors({});
    setFormError(null);
    try {
      await adoptCandidate(candidate.mac, {
        extensionId,
        label: label.trim() || undefined,
        model: needsModel ? model.trim() : undefined,
      });
      onAdopted(candidate);
    } catch (err) {
      const { byKey, unmatched } = mapFieldErrors(
        fieldErrors(err),
        ADOPT_FIELDS,
      );
      setErrors(byKey);
      setFormError(
        unmatched.length > 0 || Object.keys(byKey).length === 0
          ? errorMessage(err)
          : null,
      );
      setBusy(false);
    }
  }

  return (
    <Modal
      title={`Adopt ${formatMac(candidate.mac)}`}
      description={`${modelText(candidate)} at ${candidate.sourceIp || "an unknown address"}. ${PICKUP}`}
      onClose={busy ? undefined : onClose}
      actions={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="onboarding-adopt"
            disabled={busy || extensionId === ""}
          >
            Adopt phone
          </Button>
        </>
      }
    >
      <form
        id="onboarding-adopt"
        className="prov-stack"
        onSubmit={(e) => void submit(e)}
      >
        {formError && (
          <Alert tone="bad" title="Could not adopt the phone">
            {formError}
          </Alert>
        )}
        <Select
          id="adopt-extension"
          label="Extension"
          value={extensionId}
          error={errors.extensionId}
          onChange={(e) => setExtensionId(e.target.value)}
          options={[
            { value: "", label: "Choose…" },
            ...extensions.map(extensionOption),
          ]}
        />
        <Input
          id="adopt-label"
          label="Label"
          placeholder="Front desk"
          error={errors.label}
          value={label}
          onChange={(e) => setLabel(e.target.value)}
        />
        {needsModel && (
          <Input
            id="adopt-model"
            label="Model"
            hint="The phone did not report its model."
            placeholder="T54W"
            error={errors.model}
            value={model}
            onChange={(e) => setModel(e.target.value)}
          />
        )}
      </form>
    </Modal>
  );
}

/** Dismiss a phone, optionally ignoring its MAC from now on. */
function DismissDialog({
  candidate,
  onClose,
  onDone,
}: {
  candidate: Candidate;
  onClose: () => void;
  onDone: (c: Candidate, ignored: boolean) => void;
}) {
  const [ignore, setIgnore] = useState(false);
  return (
    <ConfirmDialog
      title={`Dismiss ${formatMac(candidate.mac)}?`}
      description="It leaves the queue. Unless you ignore it, it returns the next time it asks for its configuration."
      confirmLabel="Dismiss"
      confirmIcon="x"
      confirmVariant="primary"
      errorTitle="Could not dismiss"
      onConfirm={async () => {
        await dismissCandidate(candidate.mac, ignore);
        onDone(candidate, ignore);
      }}
      onClose={onClose}
    >
      <Checkbox
        label="Ignore this MAC"
        hint="Never queue it again, until it is taken off the ignore list."
        checked={ignore}
        onChange={(e) => setIgnore(e.target.checked)}
      />
    </ConfirmDialog>
  );
}

type Mode = "consecutive" | "perRow";

/** Adopt the selected phones: consecutive extensions from a number, or one per row. */
function BulkAdoptDialog({
  candidates,
  extensions,
  onClose,
  onAdopted,
}: {
  candidates: Candidate[];
  extensions: Extension[];
  onClose: () => void;
  onAdopted: (n: number) => void;
}) {
  const [mode, setMode] = useState<Mode>("consecutive");
  const [start, setStart] = useState("");
  const [picks, setPicks] = useState<Record<string, string>>({});
  const [labels, setLabels] = useState<Record<string, string>>({});
  const [models, setModels] = useState<Record<string, string>>({});
  const [report, setReport] = useState<BulkResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const byNumber = new Map(extensions.map((x) => [x.number, x]));
  const numbers = consecutiveNumbers(start, candidates.length);
  const startBad = start !== "" && numbers === null;
  // The extension each row would get, as a number.
  const numberFor = (i: number, mac: string): string =>
    mode === "consecutive"
      ? (numbers?.[i] ?? "")
      : (extensions.find((x) => String(x.id) === picks[mac])?.number ?? "");
  const missingModel = (c: Candidate) =>
    c.model === "" && (models[c.mac] ?? "").trim() === "";
  const ready =
    candidates.every(
      (c, i) => numberFor(i, c.mac) !== "" && !missingModel(c),
    ) &&
    (mode === "perRow" || numbers !== null);

  async function submit() {
    setBusy(true);
    setError(null);
    setReport(null);
    try {
      const result = await bulkAdopt(
        candidates.map((c, i) => ({
          mac: c.mac,
          extensionNumber: numberFor(i, c.mac),
          label: labels[c.mac]?.trim() || undefined,
          model: c.model === "" ? models[c.mac]?.trim() : undefined,
        })),
      );
      if (result.ok) onAdopted(result.adopted);
      else {
        setReport(result);
        setBusy(false);
      }
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  }

  const problems = new Map(
    (report?.rows ?? [])
      .filter((r) => r.errors.length > 0)
      .map((r) => [r.mac, r.errors.join("; ")]),
  );

  const columns: TableColumn<Candidate>[] = [
    {
      key: "mac",
      label: "MAC",
      mono: true,
      primary: false,
      render: (c) => (
        <>
          {formatMac(c.mac)}
          <small className="prov-muted">{modelText(c)}</small>
        </>
      ),
    },
    {
      key: "extension",
      label: "Extension",
      primary: false,
      render: (c) => {
        const i = candidates.indexOf(c);
        if (mode === "perRow") {
          return (
            <Select
              aria-label={`Extension for ${formatMac(c.mac)}`}
              value={picks[c.mac] ?? ""}
              onChange={(e) =>
                setPicks((p) => ({ ...p, [c.mac]: e.target.value }))
              }
              options={[
                { value: "", label: "Choose…" },
                ...extensions.map(extensionOption),
              ]}
            />
          );
        }
        const n = numberFor(i, c.mac);
        const ext = byNumber.get(n);
        return n === "" ? (
          "—"
        ) : (
          <>
            <span className="az-table__mono">{n}</span>
            <small className="prov-muted">
              {ext ? ext.name : "No such extension"}
            </small>
          </>
        );
      },
    },
    {
      key: "label",
      label: "Label",
      primary: false,
      render: (c) => (
        <div className="prov-stack">
          <Input
            aria-label={`Label for ${formatMac(c.mac)}`}
            size="sm"
            value={labels[c.mac] ?? ""}
            onChange={(e) =>
              setLabels((l) => ({ ...l, [c.mac]: e.target.value }))
            }
          />
          {c.model === "" && (
            <Input
              aria-label={`Model for ${formatMac(c.mac)}`}
              size="sm"
              placeholder="Model (not reported)"
              value={models[c.mac] ?? ""}
              onChange={(e) =>
                setModels((m) => ({ ...m, [c.mac]: e.target.value }))
              }
            />
          )}
          {problems.has(c.mac) && (
            <span className="inv-import__error">{problems.get(c.mac)}</span>
          )}
        </div>
      ),
    },
  ];

  return (
    <Modal
      title={`Adopt ${candidates.length} ${candidates.length === 1 ? "phone" : "phones"}`}
      description={`Every phone is adopted, or none. ${PICKUP}`}
      onClose={busy ? undefined : onClose}
      width={760}
      actions={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button disabled={busy || !ready} onClick={() => void submit()}>
            Adopt phones
          </Button>
        </>
      }
    >
      <div className="prov-stack">
        {error && (
          <Alert tone="bad" title="Could not adopt the phones">
            {error}
          </Alert>
        )}
        {report && (
          <Alert tone="warn" title="Nothing was adopted">
            Fix the rows marked below and try again.
          </Alert>
        )}
        <SegmentedControl
          aria-label="Extension assignment"
          options={[
            { value: "consecutive", label: "Consecutive numbers" },
            { value: "perRow", label: "Pick per phone" },
          ]}
          value={mode}
          onChange={setMode}
        />
        {mode === "consecutive" && (
          <Input
            id="bulk-start"
            label="Starting extension"
            mono
            placeholder="2001"
            hint="Phones get consecutive extensions in the order listed. The extensions must exist."
            error={
              startBad
                ? "Use 2 to 10 digits, with room for the whole range."
                : undefined
            }
            value={start}
            onChange={(e) => setStart(e.target.value.trim())}
          />
        )}
        <Table
          caption="Phones to adopt"
          columns={columns}
          rows={candidates}
          rowKey={(c) => c.mac}
        />
      </div>
    </Modal>
  );
}

/** The ignore list, with a way back. */
function IgnoredList({
  ignored,
  onChanged,
  onMessage,
}: {
  ignored: IgnoredMac[];
  onChanged: () => void;
  onMessage: (m: string) => void;
}) {
  async function remove(m: IgnoredMac) {
    try {
      await unignoreMac(m.mac);
      onChanged();
      onMessage(`${formatMac(m.mac)} is no longer ignored.`);
    } catch (err) {
      onMessage(`Could not stop ignoring it: ${errorMessage(err)}`);
    }
  }
  const columns: TableColumn<IgnoredMac>[] = [
    {
      key: "mac",
      label: "MAC",
      mono: true,
      primary: false,
      render: (m) => formatMac(m.mac),
    },
    {
      key: "model",
      label: "Model",
      primary: false,
      render: (m) => modelText(m),
    },
    {
      key: "by",
      label: "Ignored by",
      primary: false,
      render: (m) => m.createdBy || "—",
    },
    {
      key: "at",
      label: "When",
      primary: false,
      render: (m) => (
        <span className="prov-cell-sm">{formatTime(m.createdAt)}</span>
      ),
    },
    {
      key: "actions",
      label: <span className="visually-hidden">Actions</span>,
      align: "right",
      primary: false,
      render: (m) => (
        <Can>
          <Button
            size="sm"
            variant="secondary"
            aria-label={`Stop ignoring ${formatMac(m.mac)}`}
            onClick={() => void remove(m)}
          >
            Stop ignoring
          </Button>
        </Can>
      ),
    },
  ];
  return (
    <section aria-labelledby="ignored-title" className="prov-stack">
      <h2 id="ignored-title" className="inv-section-title">
        Ignored <Badge tone="neutral">{ignored.length}</Badge>
      </h2>
      <Table
        caption="Ignored MACs"
        columns={columns}
        rows={ignored}
        rowKey={(m) => m.mac}
      />
    </section>
  );
}
