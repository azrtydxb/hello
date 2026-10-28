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
import { CREATE_FORMS, canCreateKind } from "./objects";

export function creatableKinds(catalogue: FlowCatalogue): string[] {
  return catalogue.nodes.map((n) => n.kind).filter(canCreateKind);
}

export function CreateDialog({
  ctx,
  onCreate,
  onClose,
}: {
  ctx: EditCtx;
  /** Stages the object; returns the reason it could not. */
  onCreate: (kind: string, values: FormValues) => Promise<string | null>;
  onClose: () => void;
}) {
  const kinds = creatableKinds(ctx.catalogue);
  const [kind, setKind] = useState(kinds[0] ?? "");
  const [values, setValues] = useState<FormValues>({});
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const form = CREATE_FORMS[kind];

  const set = (key: string, value: string) =>
    setValues((v) => ({ ...v, [key]: value }));

  const submit = async () => {
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
          <Button
            size="sm"
            icon="plus"
            disabled={busy}
            onClick={() => void submit()}
          >
            Add to draft
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
