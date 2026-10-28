import { useState, type FormEvent } from "react";
import {
  createExtension,
  deleteExtension,
  errorMessage,
  EXTENSION_NUMBER_PATTERN,
  fieldErrors,
  updateExtension,
  type Device,
  type Extension,
  type Id,
} from "../api";
import { bindingsOf, type LiveDirectory } from "../api/directory";
import {
  Alert,
  Button,
  ConfirmDialog,
  Drawer,
  Input,
  LinkButton,
  Modal,
  Switch,
} from "../design/azrty/components";
import { mapFieldErrors } from "../forms";
import "../pages/directory/directory.css";
import { Can } from "../role";
import { MOHSelect } from "../pages/media/MohSelect";

export const NUMBER_HINT = "2 to 10 digits.";
export const EXTERNAL_PATTERN = /^(\+?[0-9]{2,20})?$/;
/** A forward target: empty = off, or 2–20 digits with an optional +. */
export const FORWARD_PATTERN = /^(\+?[0-9]{2,20})?$/;
export const FORWARD_HINT =
  "Empty = off; or 2 to 20 digits, optionally starting with +.";

/** Rings before the no-answer forward: empty or 0 = the system default. */
export const RINGS_PATTERN = /^([0-9]|1[0-9]|20)?$/;
export const RINGS_HINT =
  "Empty or 0 = the system default; otherwise 1 to 20 rings.";

export const FIELDS = [
  "number",
  "name",
  "externalNumber",
  "dnd",
  "forwardAlways",
  "forwardBusy",
  "forwardNoAnswer",
  "noAnswerRings",
  "voicemailEnabled",
  "recordDefault",
] as const;
export type Field = (typeof FIELDS)[number];
export type Errors = Partial<Record<Field, string>>;

export interface Draft {
  number: string;
  name: string;
  externalNumber: string;
  forwardAlways: string;
  forwardBusy: string;
  forwardNoAnswer: string;
  noAnswerRings: string;
}

export function validate(d: Draft): Errors {
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
  if (!RINGS_PATTERN.test(d.noAnswerRings.trim())) {
    errors.noAnswerRings = RINGS_HINT;
  }
  return errors;
}

export const hasErrors = (e: Errors) => Object.values(e).some(Boolean);

/** Server field errors on the extension's editable fields. */
export function serverFieldErrors(err: unknown): Errors {
  return mapFieldErrors(fieldErrors(err), FIELDS).byKey as Errors;
}

export function NewExtension({
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
      noAnswerRings: "",
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

export function ExtensionDrawer({
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
    noAnswerRings: ext.noAnswerRings ? String(ext.noAnswerRings) : "",
  });
  const [dnd, setDnd] = useState(ext.dnd ?? false);
  const [voicemail, setVoicemail] = useState(ext.voicemailEnabled !== false);
  const [record, setRecord] = useState(ext.recordDefault ?? false);
  const [intercom, setIntercom] = useState(ext.allowIntercom !== false);
  const [callWaiting, setCallWaiting] = useState(ext.callWaiting !== false);
  const [mohClass, setMohClass] = useState<Id | null>(ext.mohClassId ?? null);
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
    const rings =
      draft.noAnswerRings.trim() === "" ? 0 : Number(draft.noAnswerRings);
    if (rings !== (ext.noAnswerRings ?? 0)) patch.noAnswerRings = rings;
    if (dnd !== (ext.dnd ?? false)) patch.dnd = dnd;
    if (voicemail !== (ext.voicemailEnabled !== false)) {
      patch.voicemailEnabled = voicemail;
    }
    if (record !== (ext.recordDefault ?? false)) patch.recordDefault = record;
    if (intercom !== (ext.allowIntercom !== false)) {
      patch.allowIntercom = intercom;
    }
    if (callWaiting !== (ext.callWaiting !== false)) {
      patch.callWaiting = callWaiting;
    }
    if (String(mohClass ?? "") !== String(ext.mohClassId ?? "")) {
      patch.mohClassId = mohClass;
    }
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
            <Switch
              label="Allow intercom"
              hint="An intercom call (*80 plus the number) auto-answers; off rings like a normal call"
              labelPosition="end"
              checked={intercom}
              onChange={(e) => setIntercom(e.target.checked)}
            />
            <Switch
              label="Call waiting"
              hint="On: a second call rings the phone while it is busy. Off: the caller gets forward-busy or busy. *71 and *70 toggle it; the phone follows at its next resync"
              labelPosition="end"
              checked={callWaiting}
              onChange={(e) => setCallWaiting(e.target.checked)}
            />
            <MOHSelect
              id="ext-moh"
              label="Music on hold"
              hint="What callers hear while this extension holds them. Default uses the global class."
              value={mohClass}
              onChange={setMohClass}
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
            <Input
              id="ext-no-answer-rings"
              label="Rings before no answer"
              mono
              size="sm"
              inputMode="numeric"
              placeholder="Default"
              hint={RINGS_HINT}
              error={errors.noAnswerRings}
              value={draft.noAnswerRings}
              onChange={set("noAnswerRings")}
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
