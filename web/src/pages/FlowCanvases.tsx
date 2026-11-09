import { useState, type FormEvent } from "react";
import { Link, useNavigate } from "react-router";
import { errorMessage, fieldErrors, type FieldError } from "../api";
import {
  createFlowCanvas,
  deleteFlowCanvas,
  listFlowCanvases,
  updateFlowCanvas,
  type FlowCanvas,
} from "../api/flow";
import {
  Alert,
  Button,
  ConfirmDialog,
  Drawer,
  EmptyState,
  Input,
  PageHeader,
  Spinner,
  useRestoreFocus,
  useToast,
} from "../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { Can } from "../role";
import { usePolling } from "../usePolling";
import { FormAlert } from "./callflow/ui";

const KEYS = ["name", "description"] as const;

/** The call flow canvases: named, shared views of the PBX's call flow. */
export function FlowCanvases() {
  const list = usePolling(listFlowCanvases, 30_000);
  const toast = useToast();
  const navigate = useNavigate();
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; canvas: FlowCanvas } | null
  >(null);
  const [deleting, setDeleting] = useState<FlowCanvas | null>(null);
  const canvases =
    list.status === "ready"
      ? list.data
      : list.status === "error"
        ? list.data
        : undefined;

  const add = (
    <Can min="operator">
      <Button
        icon="plus"
        disabled={list.status === "loading"}
        onClick={() => setEditing({ kind: "new" })}
      >
        New canvas
      </Button>
    </Can>
  );

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Call flow"
        title="Call flows"
        description="See how calls move through the PBX: trunks, numbers, extensions, ring groups, menus and what they reach. A canvas is a view; the configuration is changed on each object's page."
        actions={add}
      />
      {list.status === "loading" && <Spinner label="Loading canvases…" />}
      {list.status === "error" && (
        <Alert tone="bad" title="Could not load the canvases">
          {list.message}
        </Alert>
      )}
      {canvases && canvases.length === 0 && (
        <EmptyState
          icon="workflow"
          title="No canvases yet"
          description="Create a canvas, then import a flow from a trunk, a number or a feature code. Every user sees the same canvases."
          action={add}
        />
      )}
      {canvases && canvases.length > 0 && (
        <ul className="cf-grid cf-grid--groups cf-plain" aria-label="Canvases">
          {canvases.map((c) => (
            <li
              className="az-card cf-card"
              key={String(c.id)}
              aria-labelledby={`canvas-${String(c.id)}`}
            >
              <div className="cf-card__head cf-card__head--top">
                <div style={{ minWidth: 0 }}>
                  <h3
                    className="cf-card__group-name"
                    id={`canvas-${String(c.id)}`}
                  >
                    <Link to={`/flow/${encodeURIComponent(String(c.id))}`}>
                      {c.name}
                    </Link>
                  </h3>
                  <div className="cf-card__sub">
                    {c.description ? `${c.description} · ` : ""}
                    {c.nodeCount} {c.nodeCount === 1 ? "node" : "nodes"} placed
                  </div>
                </div>
              </div>
              <div className="cf-card__foot">
                <Can min="operator">
                  <Button
                    variant="secondary"
                    size="sm"
                    icon="pencil"
                    aria-label={`Edit canvas ${c.name}`}
                    onClick={() => setEditing({ kind: "edit", canvas: c })}
                  >
                    Edit
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    icon="trash-2"
                    className="cf-danger"
                    aria-label={`Delete canvas ${c.name}`}
                    onClick={() => setDeleting(c)}
                  >
                    Delete
                  </Button>
                </Can>
              </div>
            </li>
          ))}
        </ul>
      )}
      {editing && (
        <CanvasDrawer
          key={editing.kind === "edit" ? String(editing.canvas.id) : "new"}
          canvas={editing.kind === "edit" ? editing.canvas : null}
          onClose={() => setEditing(null)}
          onSaved={(c, created) => {
            setEditing(null);
            // A new canvas opens at once: that is where it is filled.
            if (created) {
              void navigate(`/flow/${encodeURIComponent(String(c.id))}`);
              return;
            }
            list.reload();
            toast.show(
              created ? `Canvas ${c.name} created.` : `Canvas ${c.name} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete canvas ${deleting.name}?`}
          description="The canvas and where its nodes sit are removed. The configuration it shows is not changed."
          confirmLabel="Delete canvas"
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            try {
              await deleteFlowCanvas(deleting.id);
            } catch (err: unknown) {
              throw new Error(
                `Could not delete ${deleting.name}: ${errorMessage(err)}`,
                { cause: err },
              );
            }
            setDeleting(null);
            list.reload();
            toast.show(`Canvas ${deleting.name} deleted.`);
          }}
        />
      )}
      {toast.node}
    </section>
  );
}

function CanvasDrawer({
  canvas,
  onClose,
  onSaved,
}: {
  canvas: FlowCanvas | null;
  onClose: () => void;
  onSaved: (c: FlowCanvas, created: boolean) => void;
}) {
  useRestoreFocus();
  const [name, setName] = useState(canvas?.name ?? "");
  const [description, setDescription] = useState(canvas?.description ?? "");
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setUnmatched([]);
    if (name.trim() === "" || name !== name.trim() || name.length > 64) {
      setErrors({
        name: "1-64 characters, without leading or trailing spaces.",
      });
      setFormError("Fix the highlighted fields.");
      return;
    }
    setErrors({});
    setBusy(true);
    try {
      const saved = canvas
        ? await updateFlowCanvas(canvas.id, { name, description })
        : await createFlowCanvas({ name, description });
      onSaved(saved, canvas === null);
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), KEYS);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Drawer
      title={canvas ? `Canvas ${canvas.name}` : "New canvas"}
      description="A canvas is shared by everyone. Naming it after what it shows, such as Main office or After hours, helps."
      onClose={busy ? undefined : onClose}
      width={480}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="flow-canvas-form" disabled={busy}>
            {canvas ? "Save canvas" : "Create canvas"}
          </Button>
        </>
      }
    >
      <form
        id="flow-canvas-form"
        className="cf-form"
        aria-label={canvas ? `Edit canvas ${canvas.name}` : "New canvas"}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <Input
          id="canvas-name"
          label="Name"
          autoFocus
          value={name}
          error={errors.name}
          onChange={(e) => setName(e.target.value)}
        />
        <Input
          id="canvas-description"
          label="Description"
          value={description}
          error={errors.description}
          onChange={(e) => setDescription(e.target.value)}
        />
        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Drawer>
  );
}
