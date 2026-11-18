// "Add object" (spec S-11): a small form per kind of object, built from the
// table in objects.ts. Submitting stages the object in the draft; nothing is
// saved until Apply.

import { useState } from "react";
import type { FlowCatalogue } from "../../api/flow";
import {
  Alert,
  Button,
  Input,
  Modal,
  Select,
} from "../../design/azrty/components";
import {
  connectChoices,
  missingFields,
  nodeChoices,
  type EditCtx,
  type FormValues,
} from "./edits";
import { kindLabel } from "./model";
import {
  CREATE_FORMS,
  canCreateKind,
  createsOwn,
  FORM_KINDS,
  OBJECTS,
} from "./objects";

export function creatableKinds(catalogue: FlowCatalogue): string[] {
  return catalogue.nodes.map((n) => n.kind).filter(canCreateKind);
}

export function CreateDialog({
  ctx,
  onCreate,
  onOpenForm,
  onClose,
}: {
  ctx: EditCtx;
  /** Stages the object; returns the reason it could not. */
  onCreate: (kind: string, values: FormValues) => Promise<string | null>;
  /**
   * Opens the kind's own form (every field, as on its page) for a new one:
   * the only way for a kind without a short form here.
   */
  onOpenForm?: (kind: string) => void;
  onClose: () => void;
}) {
  const kinds = creatableKinds(ctx.catalogue);
  const [kind, setKind] = useState(kinds[0] ?? "");
  const [values, setValues] = useState<FormValues>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const form = CREATE_FORMS[kind];
  const full = onOpenForm && FORM_KINDS.has(kind) ? onOpenForm : undefined;
  const note = createsOwn(kind)
    ? "Its own form creates it at once, not in the draft: the answer is shown only once."
    : OBJECTS[kind]?.listCreate
      ? "Its form edits the whole list; the new row is added to the draft."
      : "Its form has every field; the object is added to the draft.";

  const set = (key: string, value: string) =>
    setValues((v) => ({ ...v, [key]: value }));

  const submit = async () => {
    if (!form) {
      full?.(kind);
      return;
    }
    const missing = missingFields(kind, values);
    if (missing.length > 0) {
      setError(`Fill in: ${missing.join(", ")}.`);
      return;
    }
    setBusy(true);
    const err = await onCreate(kind, values);
    setBusy(false);
    if (err) setError(err);
    else onClose();
  };

  return (
    <Modal
      title="Add an object"
      description="It is added to the draft and becomes real when you apply."
      onClose={onClose}
      actions={
        <>
          <Button variant="secondary" size="sm" onClick={onClose}>
            Cancel
          </Button>
          {form && full && (
            <Button
              variant="secondary"
              size="sm"
              icon="square-pen"
              onClick={() => full(kind)}
            >
              All fields…
            </Button>
          )}
          <Button
            size="sm"
            icon="plus"
            disabled={busy || (!form && !full)}
            onClick={() => void submit()}
          >
            {form ? "Add to draft" : "Open the form"}
          </Button>
        </>
      }
    >
      <form
        className="flow-form"
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        <Select
          label="Kind"
          value={kind}
          onChange={(e) => {
            setKind(e.target.value);
            setValues({});
            setError("");
          }}
          options={kinds.map((k) => ({
            value: k,
            label: kindLabel(ctx.catalogue, k),
          }))}
        />
        {!form && <p>{full ? note : "It is created on its own page."}</p>}
        {form?.fields.map((f) =>
          f.type === "select" ? (
            <Select
              key={`${kind}.${f.key}`}
              label={f.label}
              value={values[f.key] ?? f.default ?? ""}
              onChange={(e) => set(f.key, e.target.value)}
              options={f.options ?? []}
            />
          ) : f.type === "node" ? (
            <Select
              key={`${kind}.${f.key}`}
              label={f.label}
              hint={f.hint}
              value={values[f.key] ?? ""}
              onChange={(e) => set(f.key, e.target.value)}
              options={[
                { value: "", label: "Choose…" },
                ...(f.connectPort
                  ? connectChoices(ctx, kind, f.connectPort)
                  : nodeChoices(ctx, f)),
              ]}
            />
          ) : (
            <Input
              key={`${kind}.${f.key}`}
              label={f.label}
              hint={f.hint}
              value={values[f.key] ?? ""}
              onChange={(e) => set(f.key, e.target.value)}
            />
          ),
        )}
        {error && (
          <Alert tone="bad" title="Cannot add it">
            {error}
          </Alert>
        )}
      </form>
    </Modal>
  );
}
