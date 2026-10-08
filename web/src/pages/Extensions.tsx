import { useEffect, useRef, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router";
import {
  createExtension,
  deleteExtension,
  errorMessage,
  EXTENSION_NUMBER_PATTERN,
  fieldErrors,
  listDevices,
  listExtensions,
  updateExtension,
  type Device,
  type Extension,
} from "../api";
import {
  bindingsOf,
  devicesLabel,
  devicesOf,
  forwardingLabel,
  loadLiveDirectory,
  presenceOf,
  type LiveDirectory,
} from "../api/directory";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  Drawer,
  EmptyState,
  Input,
  LinkButton,
  Modal,
  PageHeader,
  Spinner,
  Switch,
  Table,
  type TableColumn,
  useToast,
} from "../design/azrty/components";
import { mapFieldErrors } from "../forms";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";
import "./directory/directory.css";
import { Can } from "../role";

const NUMBER_HINT = "2 to 10 digits.";
const EXTERNAL_PATTERN = /^(\+?[0-9]{2,20})?$/;
/** A forward target: empty = off, or 2–20 digits with an optional +. */
const FORWARD_PATTERN = /^(\+?[0-9]{2,20})?$/;
const FORWARD_HINT =
  "Empty = off; or 2 to 20 digits, optionally starting with +.";

const FIELDS = [
  "number",
  "name",
  "externalNumber",
  "dnd",
  "forwardAlways",
  "forwardBusy",
  "forwardNoAnswer",
  "voicemailEnabled",
  "recordDefault",
] as const;
type Field = (typeof FIELDS)[number];
type Errors = Partial<Record<Field, string>>;

interface Draft {
  number: string;
  name: string;
  externalNumber: string;
  forwardAlways: string;
  forwardBusy: string;
  forwardNoAnswer: string;
}

function validate(d: Draft): Errors {
  const errors: Errors = {};
  if (!EXTENSION_NUMBER_PATTERN.test(d.number)) {
    errors.number = "The number must be 2 to 10 digits (0–9 only).";
  }
  if (d.name.trim() === "") errors.name = "Enter a name.";
  if (!EXTERNAL_PATTERN.test(d.externalNumber.trim())) {
    errors.externalNumber =
      "Use 2 to 20 digits, optionally starting with +, or leave it empty.";
  }
  for (const key of [
    "forwardAlways",
    "forwardBusy",
    "forwardNoAnswer",
  ] as const) {
    if (!FORWARD_PATTERN.test(d[key].trim())) errors[key] = FORWARD_HINT;
  }
  return errors;
}

const hasErrors = (e: Errors) => Object.values(e).some(Boolean);

/** Server field errors on the extension's editable fields. */
function serverFieldErrors(err: unknown): Errors {
  return mapFieldErrors(fieldErrors(err), FIELDS).byKey as Errors;
}

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; extensions: Extension[]; devices: Device[] };

/** Extensions: the directory list, the extension drawer and "New extension". */
export function Extensions() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [q, setQ] = useState("");
  const [selected, setSelected] = useState<Extension["id"] | null>(null);
  const [params, setParams] = useSearchParams();
  // ?new=1 (the Dashboard's "New extension") opens the New extension modal.
  const [creating, setCreating] = useState(params.get("new") === "1");
  // Extensions created while the list was still loading (from the header or
  // ?new=1): the list response may predate them, so they are merged in when
  // it lands.
  const createdEarly = useRef<Extension[]>([]);
  const toast = useToast();

  useEffect(() => {
    if (params.get("new") !== "1") return;
    // Drop the flag so closing the modal or reloading does not reopen it.
    const next = new URLSearchParams(params);
    next.delete("new");
    setParams(next, { replace: true });
  }, [params, setParams]);
  const liveState = usePolling(loadLiveDirectory, LIVE_REFRESH_MS);
  const live: LiveDirectory =
    liveState.status === "loading" ? {} : (liveState.data ?? {});

  useEffect(() => {
    const controller = new AbortController();
    Promise.all([
      listExtensions(controller.signal),
      listDevices(controller.signal),
    ])
      .then(([extensions, devices]) => {
        const early = createdEarly.current.filter(
          (c) => !extensions.some((e) => e.id === c.id),
        );
        setList({
          status: "ready",
          extensions: [...extensions, ...early],
          devices,
        });
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  function setExtensions(fn: (items: Extension[]) => Extension[]) {
    setList((prev) =>
      prev.status === "ready"
        ? { ...prev, extensions: fn(prev.extensions) }
        : prev,
    );
  }

  const all = list.status === "ready" ? list.extensions : [];
  const devices = list.status === "ready" ? list.devices : [];
  const query = q.trim().toLowerCase();
  const shown = all.filter(
    (e) =>
      !query ||
      e.number.includes(query) ||
      e.name.toLowerCase().includes(query),
  );
  const current = all.find((e) => e.id === selected);

  const columns: TableColumn<Extension>[] = [
    {
      key: "number",
      label: "Number",
      mono: true,
      render: (e) => (
        <button
          type="button"
          className="dir-rowlink dir-number"
          aria-label={`Open extension ${e.number}`}
          onClick={() => setSelected(e.id)}
        >
          {e.number}
        </button>
      ),
    },
    {
      key: "name",
      label: "Name",
      render: (e) => <span className="az-table__primary">{e.name}</span>,
    },
    {
      key: "externalNumber",
      label: "External number",
      mono: true,
      render: (e) => e.externalNumber || "—",
    },
    {
      key: "devices",
      label: "Devices",
      render: (e) => devicesLabel(devicesOf(devices, e), live.bindings),
    },
    {
      key: "presence",
      label: "Presence",
      render: (e) => {
        const p = presenceOf(e, devicesOf(devices, e), live);
        return p ? (
          <Badge tone={p.tone} dot>
            {p.label}
          </Badge>
        ) : (
          "—"
        );
      },
    },
    {
      key: "forwarding",
      label: "Forwarding",
      render: (e) => <span className="dir-cell-sm">{forwardingLabel(e)}</span>,
    },
    {
      key: "voicemail",
      label: "Voicemail",
      render: (e) => (e.voicemailEnabled === false ? "Off" : "On"),
    },
    {
      key: "recording",
      label: "Recording",
      render: (e) => (e.recordDefault ? "Default on" : "Off"),
    },
  ];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title="Extensions"
        description="Dialable numbers, their devices and call features."
        actions={
          <Can>
            <Button icon="plus" onClick={() => setCreating(true)}>
              New extension
            </Button>
          </Can>
        }
      />

      {list.status === "loading" && <Spinner label="Loading extensions…" />}
      {list.status === "error" && (
        <Alert tone="bad" title="Could not load extensions">
          {list.message}
        </Alert>
      )}
      {list.status === "ready" && all.length === 0 && (
        <EmptyState
          icon="user-round"
          title="No extensions yet"
          description="An extension is a dialable number. Add one, then give it devices."
          action={
            <Can>
              <Button icon="plus" onClick={() => setCreating(true)}>
                New extension
              </Button>
            </Can>
          }
        />
      )}
      {list.status === "ready" && all.length > 0 && (
        <>
          <div className="dir-toolbar">
            <Input
              id="ext-search"
              aria-label="Search number or name"
              icon="search"
              size="sm"
              type="search"
              placeholder="Search number or name"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              className="dir-toolbar__search"
            />
            <span className="dir-count" aria-live="polite">
              {shown.length} of {all.length}
            </span>
          </div>
          {shown.length === 0 ? (
            <EmptyState
              icon="search"
              title="No extensions match"
              description={`Nothing matches “${q.trim()}”. Search by number or name.`}
            />
          ) : (
            <Table
              caption="Extensions"
              columns={columns}
              rows={shown}
              rowKey={(e) => String(e.id)}
              onRowClick={(e) => setSelected(e.id)}
            />
          )}
        </>
      )}

      {current && (
        <ExtensionDrawer
          key={String(current.id)}
          ext={current}
          devices={devicesOf(devices, current)}
          live={live}
          onClose={() => setSelected(null)}
          onSaved={(saved) => {
            setExtensions((items) =>
              items.map((i) => (i.id === saved.id ? saved : i)),
            );
            setSelected(null);
            toast.show(`Extension ${saved.number} saved.`);
          }}
          onDeleted={() => {
            setExtensions((items) => items.filter((i) => i.id !== current.id));
            setList((prev) =>
              prev.status === "ready"
                ? {
                    ...prev,
                    devices: prev.devices.filter(
                      (d) => String(d.extensionId) !== String(current.id),
                    ),
                  }
                : prev,
            );
            setSelected(null);
            toast.show(`Extension ${current.number} and its devices deleted.`);
          }}
        />
      )}

      {creating && (
        <NewExtension
          onClose={() => setCreating(false)}
          onCreated={(ext) => {
            if (list.status !== "ready") createdEarly.current.push(ext);
            setExtensions((items) => [...items, ext]);
            setCreating(false);
            toast.show(`Extension ${ext.number} created.`);
          }}
        />
      )}

      {toast.node}
    </section>
  );
}

function NewExtension({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (ext: Extension) => void;
}) {
  const [number, setNumber] = useState("");
  const [name, setName] = useState("");
  const [external, setExternal] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const [serverError, setServerError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setServerError(null);
    const found = validate({
      number,
      name,
      externalNumber: external,
      forwardAlways: "",
      forwardBusy: "",
      forwardNoAnswer: "",
    });
    setErrors(found);
    if (hasErrors(found)) return;
    setBusy(true);
    try {
      onCreated(
        await createExtension({
          number,
          name: name.trim(),
          // Sent only when given, so a create without one stays the Phase 1 shape.
          ...(external.trim() ? { externalNumber: external.trim() } : {}),
        }),
      );
    } catch (err) {
      setErrors(serverFieldErrors(err));
      setServerError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Modal
      title="New extension"
      description="Add devices after the extension exists."
      onClose={onClose}
      actions={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" form="new-extension-form" disabled={busy}>
            Create extension
          </Button>
        </>
      }
    >
      <form
        id="new-extension-form"
        className="dir-stack"
        aria-label="New extension"
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        {serverError && (
          <Alert tone="bad" title="Could not create the extension">
            {serverError}
          </Alert>
        )}
        <div className="dir-grid-2">
          <Input
            id="new-ext-number"
            label="Number"
            mono
            inputMode="numeric"
            placeholder="1030"
            hint={NUMBER_HINT}
            error={errors.number}
            value={number}
            autoFocus
            onChange={(e) => setNumber(e.target.value)}
          />
          <Input
            id="new-ext-name"
            label="Name"
            placeholder="Front desk"
            error={errors.name}
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
        </div>
        <Input
          id="new-ext-external"
          label="External number"
          mono
          inputMode="tel"
          placeholder="+97142000130"
          hint="Optional; presented to carriers."
          error={errors.externalNumber}
          value={external}
          onChange={(e) => setExternal(e.target.value)}
        />
      </form>
    </Modal>
  );
}

function ExtensionDrawer({
  ext,
  devices,
  live,
  onClose,
  onSaved,
  onDeleted,
}: {
  ext: Extension;
  devices: Device[];
  live: LiveDirectory;
  onClose: () => void;
  onSaved: (ext: Extension) => void;
  onDeleted: () => void;
}) {
  const [draft, setDraft] = useState<Draft>({
    number: ext.number,
    name: ext.name,
    externalNumber: ext.externalNumber ?? "",
    forwardAlways: ext.forwardAlways ?? "",
    forwardBusy: ext.forwardBusy ?? "",
    forwardNoAnswer: ext.forwardNoAnswer ?? "",
  });
  const [dnd, setDnd] = useState(ext.dnd ?? false);
  const [voicemail, setVoicemail] = useState(ext.voicemailEnabled !== false);
  const [record, setRecord] = useState(ext.recordDefault ?? false);
  const [errors, setErrors] = useState<Errors>({});
  const [serverError, setServerError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const set = (key: keyof Draft) => (e: { target: { value: string } }) =>
    setDraft((d) => ({ ...d, [key]: e.target.value }));

  async function onSave() {
    setServerError(null);
    const found = validate(draft);
    setErrors(found);
    if (hasErrors(found)) return;
    const patch: Parameters<typeof updateExtension>[1] = {};
    if (draft.number !== ext.number) patch.number = draft.number;
    if (draft.name.trim() !== ext.name) patch.name = draft.name.trim();
    for (const key of [
      "externalNumber",
      "forwardAlways",
      "forwardBusy",
      "forwardNoAnswer",
    ] as const) {
      if (draft[key].trim() !== (ext[key] ?? ""))
        patch[key] = draft[key].trim();
    }
    if (dnd !== (ext.dnd ?? false)) patch.dnd = dnd;
    if (voicemail !== (ext.voicemailEnabled !== false)) {
      patch.voicemailEnabled = voicemail;
    }
    if (record !== (ext.recordDefault ?? false)) patch.recordDefault = record;
    if (Object.keys(patch).length === 0) {
      onClose();
      return;
    }
    setBusy(true);
    try {
      onSaved(await updateExtension(ext.id, patch));
    } catch (err) {
      setErrors(serverFieldErrors(err));
      setServerError(`Could not save: ${errorMessage(err)}`);
      setBusy(false);
    }
  }

  async function onDelete() {
    await deleteExtension(ext.id);
    onDeleted();
  }

  const footer = (
    <>
      <Can>
        <Button
          variant="ghost"
          size="sm"
          className="dir-danger"
          disabled={busy}
          onClick={() => setConfirmDelete(true)}
        >
          Delete
        </Button>
      </Can>
      <Button variant="secondary" disabled={busy} onClick={onClose}>
        Cancel
      </Button>
      <Can>
        <Button type="submit" form="extension-form" disabled={busy}>
          Save
        </Button>
      </Can>
    </>
  );

  return (
    <>
      <Drawer
        title={`Extension ${ext.number}`}
        description={ext.name}
        onClose={confirmDelete ? undefined : onClose}
        width={480}
        footer={footer}
      >
        <form
          id="extension-form"
          className="dir-form"
          aria-label={`Edit extension ${ext.number}`}
          onSubmit={(e) => {
            e.preventDefault();
            void onSave();
          }}
          noValidate
        >
          {serverError && <Alert tone="bad">{serverError}</Alert>}
          <div className="dir-grid-2">
            <Input
              id="ext-number"
              label="Number"
              mono
              inputMode="numeric"
              hint={NUMBER_HINT}
              error={errors.number}
              value={draft.number}
              autoFocus
              onChange={set("number")}
            />
            <Input
              id="ext-name"
              label="Name"
              error={errors.name}
              value={draft.name}
              onChange={set("name")}
            />
          </div>
          <Input
            id="ext-external"
            label="External number"
            mono
            inputMode="tel"
            hint="Optional; presented to carriers, e.g. +97142000101."
            error={errors.externalNumber}
            value={draft.externalNumber}
            onChange={set("externalNumber")}
          />

          <div
            className="dir-section"
            role="group"
            aria-labelledby="ext-features-label"
          >
            <span id="ext-features-label" className="az-eyebrow">
              Call features
            </span>
            <Switch
              label="Do not disturb"
              hint="Calls go straight to forward-busy or voicemail"
              labelPosition="end"
              checked={dnd}
              onChange={(e) => setDnd(e.target.checked)}
            />
            <Switch
              label="Voicemail"
              hint="Unanswered and busy calls go to this box"
              labelPosition="end"
              checked={voicemail}
              onChange={(e) => setVoicemail(e.target.checked)}
            />
            <Switch
              label="Record calls by default"
              hint="*1 toggles recording mid-call either way"
              labelPosition="end"
              checked={record}
              onChange={(e) => setRecord(e.target.checked)}
            />
          </div>

          <div
            className="dir-section"
            role="group"
            aria-labelledby="ext-forwarding-label"
          >
            <span id="ext-forwarding-label" className="az-eyebrow">
              Forwarding
            </span>
            <Input
              id="ext-forward-always"
              label="Always"
              mono
              size="sm"
              inputMode="tel"
              placeholder="Off"
              error={errors.forwardAlways}
              value={draft.forwardAlways}
              onChange={set("forwardAlways")}
            />
            <Input
              id="ext-forward-busy"
              label="When busy"
              mono
              size="sm"
              inputMode="tel"
              placeholder="Off"
              error={errors.forwardBusy}
              value={draft.forwardBusy}
              onChange={set("forwardBusy")}
            />
            <Input
              id="ext-forward-no-answer"
              label="No answer"
              mono
              size="sm"
              inputMode="tel"
              placeholder="Off"
              hint={FORWARD_HINT}
              error={errors.forwardNoAnswer}
              value={draft.forwardNoAnswer}
              onChange={set("forwardNoAnswer")}
            />
          </div>

          <section
            className="dir-section dir-section--tight"
            aria-label="Devices"
          >
            <span className="az-eyebrow" aria-hidden="true">
              Devices
            </span>
            {devices.length === 0 && (
              <p className="dir-device dir-muted">No devices yet.</p>
            )}
            <ul
              className="dir-device-list"
              aria-label={`Devices of ${ext.number}`}
            >
              {devices.map((d) => {
                const contacts = live.bindings
                  ? bindingsOf(live.bindings, d.sipUsername)
                  : undefined;
                const registered = Boolean(contacts && contacts.length > 0);
                return (
                  <li key={String(d.id)} className="dir-device">
                    <span
                      className={`az-dot ${registered ? "dir-dot--good" : "dir-dot--faint"}`}
                      aria-hidden="true"
                    />
                    <span className="dir-device__name">{d.sipUsername}</span>
                    <span className="dir-device__ua">
                      {!contacts
                        ? "—"
                        : registered
                          ? contacts[0]?.userAgent || "Registered"
                          : "Not registered"}
                    </span>
                  </li>
                );
              })}
            </ul>
            <LinkButton
              to="/devices"
              variant="ghost"
              size="sm"
              iconRight="arrow-right"
              className="dir-manage"
            >
              Manage devices
            </LinkButton>
          </section>
        </form>
      </Drawer>
      {confirmDelete && (
        <ConfirmDialog
          title={`Delete extension ${ext.number}?`}
          description="Its devices are deleted with it and stop registering. This cannot be undone."
          confirmLabel="Delete extension"
          onConfirm={onDelete}
          onClose={() => setConfirmDelete(false)}
        />
      )}
    </>
  );
}
