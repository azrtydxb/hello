// Phone templates (spec S-8, S-9, S-16): the built-ins, their editable
// copies and generic templates; an editor whose validation errors land on
// the failing body line; and a preview of what a phone gets now.
import { useEffect, useRef, useState } from "react";
import { ApiError, errorMessage, type FieldError, type Id } from "../api";
import {
  copyTemplate,
  createTemplate,
  deleteTemplate,
  fileNameFor,
  listPhones,
  listTemplates,
  previewPhoneFile,
  TEMPLATE_FUNCTIONS,
  TEMPLATE_VARIABLES,
  updateTemplate,
  validateTemplate,
  VENDORS,
  vendorLabel,
  type Phone,
  type Template,
  type TemplateInput,
  type Vendor,
} from "../api/prov";
import {
  Alert,
  Badge,
  Button,
  CodeBlock,
  ConfirmDialog,
  EmptyState,
  IconButton,
  Input,
  Modal,
  PageHeader,
  Select,
  Spinner,
  Table,
  Tabs,
  type TableColumn,
  useToast,
} from "../design/azrty/components";
import { formatMac, PhonesTabs, vendorModel } from "./phones/ui";

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; templates: Template[] };

/** One file being edited; `key` keeps tabs stable while files come and go. */
interface DraftFile {
  key: string;
  pattern: string;
  contentType: string;
  body: string;
}

interface Draft {
  name: string;
  vendor: Vendor;
  modelGlob: string;
  priority: string;
  files: DraftFile[];
}

/** The editor: `saved` is the stored template, absent for a new one. */
interface Editing {
  saved: Template | null;
  draft: Draft;
}

/** Server validation errors, placed where the editor shows them. */
interface Errors {
  /** Errors with no field of their own. */
  form: FieldError[];
  /** "name", "vendor", "modelGlob", "priority" -> message. */
  fields: Record<string, string>;
  /** Per file index: pattern/contentType messages and body errors (by line). */
  files: Record<
    number,
    { pattern?: string; contentType?: string; body: FieldError[] }
  >;
}

const NO_ERRORS: Errors = { form: [], fields: {}, files: {} };
const TOP_FIELDS = ["name", "vendor", "modelGlob", "priority"];

let nextKey = 0;
const newKey = () => `f${++nextKey}`;

const sameId = (a: Id, b: Id) => String(a) === String(b);

function isVendor(v: string): v is Vendor {
  return (VENDORS as readonly string[]).includes(v);
}

function draftOf(t: Template): Draft {
  return {
    name: t.name,
    vendor: isVendor(t.vendor) ? t.vendor : "generic",
    modelGlob: t.modelGlob,
    priority: String(t.priority),
    files: (t.files ?? []).map((f) => ({ ...f, key: newKey() })),
  };
}

const NEW_DRAFT = (): Draft => ({
  name: "",
  vendor: "generic",
  modelGlob: "*",
  priority: "100",
  files: [
    {
      key: newKey(),
      pattern: "{mac}.cfg",
      contentType: "text/plain",
      body: "",
    },
  ],
});

function inputOf(d: Draft): TemplateInput {
  return {
    name: d.name.trim(),
    vendor: d.vendor,
    modelGlob: d.modelGlob.trim(),
    priority: Number(d.priority),
    files: d.files.map(({ pattern, contentType, body }) => ({
      pattern: pattern.trim(),
      contentType: contentType.trim(),
      body,
    })),
  };
}

/** Place each server FieldError at its file, line or field. */
function placeErrors(fields: readonly FieldError[]): Errors {
  const out: Errors = { form: [], fields: {}, files: {} };
  for (const fe of fields) {
    const m = /^files\[(\d+)\](?:\.(\w+))?/.exec(fe.path.trim());
    if (m) {
      const i = Number(m[1]);
      const at = (out.files[i] ??= { body: [] });
      if (m[2] === "pattern" || m[2] === "contentType") {
        const prev = at[m[2]];
        at[m[2]] = prev ? `${prev} ${fe.message}` : fe.message;
      } else {
        at.body.push(fe);
      }
      continue;
    }
    const top = TOP_FIELDS.find(
      (k) => k.toLowerCase() === fe.path.trim().toLowerCase(),
    );
    if (top) {
      const prev = out.fields[top];
      out.fields[top] = prev ? `${prev} ${fe.message}` : fe.message;
    } else {
      out.form.push(fe);
    }
  }
  return out;
}

/** "Line 3: unknown variable .Foo", or the message alone without a line. */
const lineText = (fe: FieldError) =>
  fe.line ? `Line ${fe.line}: ${fe.message}` : fe.message;

/** Phone templates: list, view, copy, edit, validate and preview. */
export function ProvTemplates() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [actionError, setActionError] = useState<string | null>(null);
  const [viewing, setViewing] = useState<Template | null>(null);
  const [editing, setEditing] = useState<Editing | null>(null);
  const [deleting, setDeleting] = useState<Template | null>(null);
  const [copying, setCopying] = useState(false);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    listTemplates(controller.signal)
      .then((templates) => setList({ status: "ready", templates }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  function updateTemplates(fn: (ts: Template[]) => Template[]) {
    setList((prev) =>
      prev.status === "ready"
        ? { ...prev, templates: fn(prev.templates) }
        : prev,
    );
  }

  function upsert(t: Template) {
    updateTemplates((ts) =>
      ts.some((x) => sameId(x.id, t.id))
        ? ts.map((x) => (sameId(x.id, t.id) ? t : x))
        : [...ts, t],
    );
  }

  async function onCopy(t: Template) {
    setActionError(null);
    setCopying(true);
    try {
      const copy = await copyTemplate(t.id);
      upsert(copy);
      setViewing(null);
      setEditing({ saved: copy, draft: draftOf(copy) });
      toast.show(`Copied ${t.name}; edit the copy.`);
    } catch (err) {
      setActionError(`Could not copy ${t.name}: ${errorMessage(err)}`);
    } finally {
      setCopying(false);
    }
  }

  async function onDelete(t: Template) {
    await deleteTemplate(t.id);
    updateTemplates((ts) => ts.filter((x) => !sameId(x.id, t.id)));
    setDeleting(null);
    toast.show(`Template ${t.name} deleted.`);
  }

  const columns: TableColumn<Template>[] = [
    { key: "name", label: "Name", primary: true },
    {
      key: "vendor",
      label: "Vendor",
      render: (t) => vendorLabel(t.vendor),
    },
    { key: "modelGlob", label: "Model glob", mono: true },
    { key: "priority", label: "Priority", align: "right" },
    {
      key: "files",
      label: "Files",
      align: "right",
      render: (t) => (t.files ?? []).length,
    },
    {
      key: "origin",
      label: "Origin",
      render: (t) =>
        t.builtin ? (
          <Badge tone="info">Built-in</Badge>
        ) : t.builtinRef ? (
          <span className="prov-cell-sm">Copy of {t.builtinRef}</span>
        ) : (
          <span className="prov-cell-sm prov-muted">Custom</span>
        ),
    },
    { key: "version", label: "Version", align: "right" },
    {
      key: "actions",
      label: "Actions",
      align: "right",
      render: (t) =>
        t.builtin ? (
          <div className="prov-row" style={{ justifyContent: "flex-end" }}>
            <Button
              variant="secondary"
              size="sm"
              icon="eye"
              aria-label={`View ${t.name}`}
              onClick={() => setViewing(t)}
            >
              View
            </Button>
            <Button
              variant="secondary"
              size="sm"
              icon="copy"
              disabled={copying}
              aria-label={`Copy ${t.name} to edit`}
              onClick={() => void onCopy(t)}
            >
              Copy to edit
            </Button>
          </div>
        ) : (
          <div className="prov-row" style={{ justifyContent: "flex-end" }}>
            <Button
              variant="secondary"
              size="sm"
              icon="pencil"
              aria-label={`Edit ${t.name}`}
              onClick={() => setEditing({ saved: t, draft: draftOf(t) })}
            >
              Edit
            </Button>
            <IconButton
              icon="trash-2"
              label={`Delete ${t.name}`}
              size={15}
              onClick={() => setDeleting(t)}
            />
          </div>
        ),
    },
  ];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title="Phone templates"
        description="The files each phone fetches. Built-ins are read-only: copy one to change it; deleting the copy restores the built-in."
        actions={
          editing ? undefined : (
            <Button
              icon="plus"
              disabled={list.status !== "ready"}
              onClick={() => setEditing({ saved: null, draft: NEW_DRAFT() })}
            >
              New template
            </Button>
          )
        }
      />
      <PhonesTabs />

      <div className="prov-stack">
        {actionError && <Alert tone="bad">{actionError}</Alert>}
        {editing ? (
          <TemplateEditor
            key={editing.saved ? String(editing.saved.id) : "new"}
            editing={editing}
            onClose={() => setEditing(null)}
            onSaved={(t) => {
              upsert(t);
              setEditing({ saved: t, draft: draftOf(t) });
              toast.show(`Template ${t.name} saved.`);
            }}
          />
        ) : (
          <>
            {list.status === "loading" && (
              <Spinner label="Loading templates…" />
            )}
            {list.status === "error" && (
              <Alert tone="bad" title="Could not load templates">
                {list.message}
              </Alert>
            )}
            {list.status === "ready" && list.templates.length === 0 && (
              <EmptyState
                icon="file-code"
                title="No templates"
                description="A template renders the files a phone fetches."
                action={
                  <Button
                    icon="plus"
                    onClick={() =>
                      setEditing({ saved: null, draft: NEW_DRAFT() })
                    }
                  >
                    New template
                  </Button>
                }
              />
            )}
            {list.status === "ready" && list.templates.length > 0 && (
              <Table
                caption="Phone templates"
                columns={columns}
                rows={list.templates}
                rowKey={(t) => String(t.id)}
              />
            )}
          </>
        )}
      </div>

      {viewing && (
        <Modal
          title={viewing.name}
          description={`${vendorLabel(viewing.vendor)} · ${viewing.modelGlob} · priority ${viewing.priority} · version ${viewing.version}. Built-in templates are read-only.`}
          width={1100}
          onClose={() => setViewing(null)}
          actions={
            <>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => setViewing(null)}
              >
                Close
              </Button>
              <Button
                size="sm"
                icon="copy"
                disabled={copying}
                onClick={() => void onCopy(viewing)}
              >
                Copy to edit
              </Button>
            </>
          }
        >
          <div className="prov-stack">
            {(viewing.files ?? []).map((f, i) => (
              <CodeBlock
                key={i}
                title={`${f.pattern} (${f.contentType})`}
                code={f.body}
                maxHeight={420}
              />
            ))}
          </div>
        </Modal>
      )}

      {deleting && (
        <ConfirmDialog
          title={`Delete ${deleting.name}?`}
          description={
            deleting.builtinRef
              ? `Deleting the copy restores the built-in ${deleting.builtinRef}: phones it matches get the built-in's files again.`
              : "Phones it matches fall back to the next matching template. This cannot be undone."
          }
          confirmLabel="Delete template"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </section>
  );
}

/** The template editor: fields, files with line-numbered bodies, preview. */
function TemplateEditor({
  editing,
  onClose,
  onSaved,
}: {
  editing: Editing;
  onClose: () => void;
  onSaved: (t: Template) => void;
}) {
  const { saved } = editing;
  const [draft, setDraft] = useState<Draft>(editing.draft);
  const [active, setActive] = useState<string>(
    editing.draft.files[0]?.key ?? "",
  );
  const [errors, setErrors] = useState<Errors>(NO_ERRORS);
  const [status, setStatus] = useState<{
    tone: "good" | "bad";
    text: string;
  } | null>(null);
  const [busy, setBusy] = useState<"validate" | "save" | null>(null);
  const headingRef = useRef<HTMLHeadingElement>(null);

  useEffect(() => {
    headingRef.current?.focus();
  }, []);

  const set = <K extends keyof Draft>(k: K, v: Draft[K]) =>
    setDraft((d) => ({ ...d, [k]: v }));

  const setFile = (i: number, patch: Partial<DraftFile>) =>
    setDraft((d) => ({
      ...d,
      files: d.files.map((f, j) => (j === i ? { ...f, ...patch } : f)),
    }));

  function addFile() {
    const f: DraftFile = {
      key: newKey(),
      pattern: "",
      contentType: "text/plain",
      body: "",
    };
    setDraft((d) => ({ ...d, files: [...d.files, f] }));
    setActive(f.key);
    // Errors are indexed by file; a new list invalidates them.
    setErrors(NO_ERRORS);
  }

  function removeFile(i: number) {
    const files = draft.files.filter((_, j) => j !== i);
    setDraft((d) => ({ ...d, files }));
    setActive(files[Math.min(i, files.length - 1)]?.key ?? "");
    setErrors(NO_ERRORS);
  }

  /** Show a failure: field errors at their place, the rest at form level. */
  function showFailure(err: unknown) {
    const fields = err instanceof ApiError ? err.fields : [];
    if (fields.length === 0) {
      setErrors(NO_ERRORS);
      setStatus({ tone: "bad", text: errorMessage(err) });
      return;
    }
    const placed = placeErrors(fields);
    setErrors(placed);
    setStatus({ tone: "bad", text: errorMessage(err) });
    const first = Object.keys(placed.files)
      .map(Number)
      .sort((a, b) => a - b)[0];
    const file = first !== undefined ? draft.files[first] : undefined;
    if (file) setActive(file.key);
  }

  async function run(kind: "validate" | "save") {
    setBusy(kind);
    setStatus(null);
    const input = inputOf(draft);
    try {
      if (kind === "validate") {
        await validateTemplate(input);
        setErrors(NO_ERRORS);
        setStatus({ tone: "good", text: "Template is valid" });
      } else {
        const t = saved
          ? await updateTemplate(saved.id, input)
          : await createTemplate(input);
        setErrors(NO_ERRORS);
        onSaved(t);
      }
    } catch (err) {
      showFailure(err);
    } finally {
      setBusy(null);
    }
  }

  const activeIndex = Math.max(
    0,
    draft.files.findIndex((f) => f.key === active),
  );
  const file = draft.files[activeIndex];
  const fileErr = errors.files[activeIndex];

  return (
    <div className="prov-card">
      <div className="prov-card__head">
        <h2 className="prov-card__title" ref={headingRef} tabIndex={-1}>
          {saved ? `Edit ${saved.name}` : "New template"}
        </h2>
        <div className="prov-row">
          {saved && (
            <span className="prov-muted prov-cell-sm">
              {saved.builtinRef ? `Copy of ${saved.builtinRef} · ` : ""}
              version {saved.version}
            </span>
          )}
          <Button
            variant="secondary"
            size="sm"
            icon="arrow-left"
            onClick={onClose}
          >
            Back to templates
          </Button>
        </div>
      </div>

      <form
        className="prov-stack"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          void run("save");
        }}
      >
        {status && status.tone === "good" && (
          <Alert tone="good">{status.text}</Alert>
        )}
        {status && status.tone === "bad" && (
          <Alert
            tone="bad"
            title={
              errors.form.length > 0 || Object.keys(errors.files).length > 0
                ? "The template has errors"
                : "Could not save"
            }
          >
            {errors.form.length > 0 ? (
              <ul>
                {errors.form.map((fe, i) => (
                  <li key={i}>
                    {fe.path ? `${fe.path}: ` : ""}
                    {lineText(fe)}
                  </li>
                ))}
              </ul>
            ) : (
              status.text
            )}
          </Alert>
        )}

        <div className="prov-grid-2">
          <Input
            label="Name"
            value={draft.name}
            error={errors.fields.name}
            onChange={(e) => set("name", e.target.value)}
          />
          <Select
            label="Vendor"
            value={draft.vendor}
            error={errors.fields.vendor}
            options={VENDORS.map((v) => ({ value: v, label: vendorLabel(v) }))}
            onChange={(e) => set("vendor", e.target.value as Vendor)}
          />
          <Input
            label="Model glob"
            mono
            value={draft.modelGlob}
            hint="T5*, GXP21??, or * for every model"
            error={errors.fields.modelGlob}
            onChange={(e) => set("modelGlob", e.target.value)}
          />
          <Input
            label="Priority"
            type="number"
            value={draft.priority}
            hint="The highest matching priority wins."
            error={errors.fields.priority}
            onChange={(e) => set("priority", e.target.value)}
          />
        </div>

        <div className="prov-row" style={{ justifyContent: "space-between" }}>
          {draft.files.length > 0 ? (
            <Tabs
              aria-label="Files"
              idPrefix="tpl-file"
              value={file?.key}
              onChange={setActive}
              items={draft.files.map((f, i) => ({
                id: f.key,
                label:
                  (f.pattern || `File ${i + 1}`) +
                  (errors.files[i] ? " (errors)" : ""),
              }))}
            />
          ) : (
            <span className="prov-muted">No files yet.</span>
          )}
          <Button variant="secondary" size="sm" icon="plus" onClick={addFile}>
            Add file
          </Button>
        </div>

        {file && (
          <div
            className="prov-editor-layout"
            role="tabpanel"
            id={`tpl-file-panel-${file.key}`}
            aria-labelledby={`tpl-file-tab-${file.key}`}
          >
            <div className="prov-stack">
              <div className="prov-grid-2">
                <Input
                  label="File name pattern"
                  mono
                  value={file.pattern}
                  hint="{mac}, {MAC} and {model} are filled in per phone."
                  error={fileErr?.pattern}
                  onChange={(e) =>
                    setFile(activeIndex, { pattern: e.target.value })
                  }
                />
                <Input
                  label="Content type"
                  mono
                  value={file.contentType}
                  hint="text/plain, application/xml, …"
                  error={fileErr?.contentType}
                  onChange={(e) =>
                    setFile(activeIndex, { contentType: e.target.value })
                  }
                />
              </div>
              <BodyEditor
                label={`Body of ${file.pattern || `file ${activeIndex + 1}`}`}
                value={file.body}
                errors={fileErr?.body ?? []}
                onChange={(body) => setFile(activeIndex, { body })}
              />
              <div className="prov-row">
                <Button
                  variant="ghost"
                  size="sm"
                  icon="trash-2"
                  onClick={() => removeFile(activeIndex)}
                >
                  Remove file
                </Button>
              </div>
            </div>
            <VariableReference />
          </div>
        )}

        <div className="prov-row">
          <Button
            variant="secondary"
            icon="check"
            disabled={busy !== null}
            onClick={() => void run("validate")}
          >
            {busy === "validate" ? "Validating…" : "Validate"}
          </Button>
          <Button type="submit" icon="save" disabled={busy !== null}>
            {busy === "save" ? "Saving…" : "Save"}
          </Button>
        </div>
      </form>

      {saved && <Preview template={saved} />}
    </div>
  );
}

/** A textarea with a line-number gutter; error lines are marked and listed. */
function BodyEditor({
  label,
  value,
  errors,
  onChange,
}: {
  label: string;
  value: string;
  errors: FieldError[];
  onChange: (body: string) => void;
}) {
  const lines = value.split("\n").length;
  const byLine = new Map<number, string[]>();
  for (const fe of errors) {
    if (fe.line)
      byLine.set(fe.line, [...(byLine.get(fe.line) ?? []), fe.message]);
  }
  return (
    <div className="prov-stack" style={{ gap: 8 }}>
      <div className="prov-editor">
        <ol className="prov-editor__gutter" aria-hidden={byLine.size === 0}>
          {Array.from({ length: lines }, (_, i) => {
            const n = i + 1;
            const msgs = byLine.get(n);
            const text = msgs ? `Line ${n}: ${msgs.join("; ")}` : undefined;
            return (
              <li
                key={n}
                data-line={n}
                data-error={msgs ? "" : undefined}
                aria-label={text}
                title={text}
              >
                {n}
              </li>
            );
          })}
        </ol>
        <textarea
          className="az-input prov-textarea"
          aria-label={label}
          aria-invalid={errors.length > 0 ? true : undefined}
          spellCheck={false}
          wrap="off"
          rows={Math.max(lines, 16)}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      </div>
      {errors.length > 0 && (
        <ul
          className="prov-cell-sm"
          aria-label={`Errors in ${label.toLowerCase()}`}
        >
          {errors.map((fe, i) => (
            <li key={i} style={{ color: "var(--az-bad-strong)" }}>
              {lineText(fe)}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function VariableReference() {
  return (
    <aside className="prov-varref" aria-label="Template variables">
      <strong>Variables</strong>
      {TEMPLATE_VARIABLES.map((v) => (
        <div key={v.name}>
          <code>{`{{${v.name}}}`}</code>{" "}
          <span className="prov-muted">{v.doc}</span>
        </div>
      ))}
      <strong>Functions</strong>
      {TEMPLATE_FUNCTIONS.map((f) => (
        <div key={f.name}>
          <code>{f.name}</code> <span className="prov-muted">{f.doc}</span>
        </div>
      ))}
    </aside>
  );
}

type PhonesState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; phones: Phone[] };

/**
 * What a phone gets now from the saved template (spec S-9). The API renders
 * stored templates only, so unsaved edits are not in the preview.
 */
function Preview({ template }: { template: Template }) {
  const [phones, setPhones] = useState<PhonesState>({ status: "loading" });
  const [phoneId, setPhoneId] = useState("");
  const [pattern, setPattern] = useState(template.files[0]?.pattern ?? "");
  const [result, setResult] = useState<
    | { status: "idle" }
    | { status: "loading" }
    | { status: "error"; message: string }
    | { status: "ready"; name: string; text: string }
  >({ status: "idle" });

  useEffect(() => {
    const controller = new AbortController();
    listPhones(controller.signal)
      .then((all) => setPhones({ status: "ready", phones: all }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setPhones({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  const candidates =
    phones.status === "ready"
      ? template.vendor === "generic"
        ? phones.phones
        : phones.phones.filter((p) => p.vendor === template.vendor)
      : [];
  const phone =
    candidates.find((p) => String(p.id) === phoneId) ?? candidates[0];
  const patterns = (template.files ?? []).map((f) => f.pattern);
  const chosen = patterns.includes(pattern) ? pattern : patterns[0];
  const fileName = phone && chosen ? fileNameFor(chosen, phone) : "";

  async function preview() {
    if (!phone || !fileName) return;
    setResult({ status: "loading" });
    try {
      const text = await previewPhoneFile(phone.id, fileName);
      setResult({ status: "ready", name: fileName, text });
    } catch (err) {
      setResult({ status: "error", message: errorMessage(err) });
    }
  }

  return (
    <section className="prov-stack" aria-labelledby="tpl-preview-title">
      <h3 className="prov-card__title" id="tpl-preview-title">
        Preview
      </h3>
      <p className="prov-muted prov-cell-sm" style={{ margin: 0 }}>
        Renders what this phone gets now, with secrets masked by the server;
        save first to preview your changes. The phone must resolve to this
        template.
      </p>
      {phones.status === "loading" && <Spinner label="Loading phones…" />}
      {phones.status === "error" && (
        <Alert tone="bad" title="Could not load phones">
          {phones.message}
        </Alert>
      )}
      {phones.status === "ready" && candidates.length === 0 && (
        <p className="prov-muted" style={{ margin: 0 }}>
          No{" "}
          {template.vendor === "generic"
            ? ""
            : `${vendorLabel(template.vendor)} `}
          phones to preview with.
        </p>
      )}
      {phones.status === "ready" && candidates.length > 0 && (
        <div className="prov-row" style={{ alignItems: "flex-end" }}>
          <Select
            label="Phone"
            value={phone ? String(phone.id) : ""}
            options={candidates.map((p) => ({
              value: String(p.id),
              label: `${p.label || formatMac(p.mac)} · ${vendorModel(p)}`,
            }))}
            onChange={(e) => {
              setPhoneId(e.target.value);
              setResult({ status: "idle" });
            }}
          />
          <Select
            label="File"
            value={chosen ?? ""}
            options={patterns.map((p) => ({
              value: p,
              label: phone ? fileNameFor(p, phone) : p,
            }))}
            onChange={(e) => {
              setPattern(e.target.value);
              setResult({ status: "idle" });
            }}
          />
          <Button
            variant="secondary"
            icon="eye"
            disabled={!fileName || result.status === "loading"}
            onClick={() => void preview()}
          >
            Preview
          </Button>
        </div>
      )}
      {result.status === "loading" && <Spinner label="Rendering…" />}
      {result.status === "error" && (
        <Alert tone="bad" title="Could not preview">
          {result.message}
        </Alert>
      )}
      {result.status === "ready" && (
        <CodeBlock title={result.name} code={result.text} maxHeight={480} />
      )}
    </section>
  );
}
