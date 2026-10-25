import {
  useEffect,
  useMemo,
  useState,
  type FormEvent,
  type ReactNode,
} from "react";
import {
  errorMessage,
  fieldErrors,
  listExtensions,
  updateExtension,
  type Extension,
  type Id,
} from "../api";
import {
  BOOK_KIND_LABEL,
  bookEntriesApi,
  createBook,
  deleteBook,
  entryName,
  listBooks,
  NUMBER_LABELS,
  renameBook,
  type Book,
  type Entry,
  type EntriesApi,
  type EntryInput,
  type ImportResult,
} from "../api/phonebook";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  IconButton,
  Input,
  Modal,
  PageHeader,
  Select,
  Spinner,
  Switch,
  Table,
  type TableColumn,
  useToast,
} from "../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { Can } from "../role";
import "./phones/phones.css";
import "./phonebooks/phonebooks.css";

type Load<T> =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; value: T };

const sameId = (a: Id, b: Id) => String(a) === String(b);

/** Address books: the company book, named books, and the automatic extensions book. */
export function Phonebooks() {
  const [books, setBooks] = useState<Load<Book[]>>({ status: "loading" });
  const [selected, setSelected] = useState<Id | null>(null);
  const [naming, setNaming] = useState<"new" | "rename" | null>(null);
  const [deleting, setDeleting] = useState(false);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    listBooks(controller.signal)
      .then((value) => {
        setBooks({ status: "ready", value });
        setSelected(
          (current) =>
            current ??
            value.find((b) => b.kind === "company")?.id ??
            value[0]?.id ??
            null,
        );
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setBooks({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  const list = books.status === "ready" ? books.value : [];
  const book = list.find((b) => selected !== null && sameId(b.id, selected));

  function replaceBook(next: Book) {
    setBooks((prev) =>
      prev.status === "ready"
        ? {
            status: "ready",
            value: prev.value.some((b) => sameId(b.id, next.id))
              ? prev.value.map((b) => (sameId(b.id, next.id) ? next : b))
              : [...prev.value, next],
          }
        : prev,
    );
  }

  async function onDelete(b: Book) {
    try {
      await deleteBook(b.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${b.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    setBooks((prev) =>
      prev.status === "ready"
        ? {
            status: "ready",
            value: prev.value.filter((x) => !sameId(x.id, b.id)),
          }
        : prev,
    );
    setSelected(list.find((x) => x.kind === "company")?.id ?? null);
    setDeleting(false);
    toast.show(`Phonebook ${b.name} deleted.`);
  }

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title="Phonebooks"
        description="Address books delivered to phones as their remote phonebook. Phone groups pick which named books a phone shows."
        actions={
          <Can min="admin">
            <Button
              icon="plus"
              disabled={books.status !== "ready"}
              onClick={() => setNaming("new")}
            >
              New book
            </Button>
          </Can>
        }
      />
      <div className="prov-stack">
        {books.status === "loading" && <Spinner label="Loading phonebooks…" />}
        {books.status === "error" && (
          <Alert tone="bad" title="Could not load the phonebooks">
            {books.message}
          </Alert>
        )}
        {books.status === "ready" && (
          <div className="prov-row pb-bookbar">
            <Select
              id="phonebook-select"
              label="Book"
              value={selected === null ? "" : String(selected)}
              onChange={(e) => {
                const next = list.find((b) => String(b.id) === e.target.value);
                if (next) setSelected(next.id);
              }}
              options={list.map((b) => ({
                value: String(b.id),
                label: `${b.name} (${BOOK_KIND_LABEL[b.kind].toLowerCase()}, ${b.entryCount})`,
              }))}
            />
            {book?.kind === "named" && (
              <Can min="admin">
                <Button
                  variant="secondary"
                  icon="pencil"
                  onClick={() => setNaming("rename")}
                >
                  Rename
                </Button>
                <Button
                  variant="ghost"
                  icon="trash-2"
                  onClick={() => setDeleting(true)}
                >
                  Delete book
                </Button>
              </Can>
            )}
          </div>
        )}
        {book?.kind === "extensions" && <ExtensionsBook />}
        {book && book.kind !== "extensions" && (
          <EntriesView
            key={String(book.id)}
            book={book}
            onCount={(entryCount) => replaceBook({ ...book, entryCount })}
          />
        )}
      </div>

      {naming && (
        <NameModal
          book={naming === "rename" ? (book ?? null) : null}
          onClose={() => setNaming(null)}
          onSaved={(saved, created) => {
            replaceBook(saved);
            if (created) setSelected(saved.id);
            setNaming(null);
            toast.show(
              created
                ? `Phonebook ${saved.name} created.`
                : `Phonebook renamed to ${saved.name}.`,
            );
          }}
        />
      )}
      {deleting && book && (
        <ConfirmDialog
          title={`Delete phonebook ${book.name}?`}
          description="Its entries are deleted, and phone groups that use it stop delivering it."
          confirmLabel="Delete book"
          onConfirm={() => onDelete(book)}
          onClose={() => setDeleting(false)}
        />
      )}
      {toast.node}
    </section>
  );
}

function NameModal({
  book,
  onClose,
  onSaved,
}: {
  book: Book | null;
  onClose: () => void;
  onSaved: (book: Book, created: boolean) => void;
}) {
  const [name, setName] = useState(book?.name ?? "");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      const saved = book
        ? await renameBook(book.id, name.trim())
        : await createBook(name.trim());
      onSaved(saved, book === null);
    } catch (err) {
      const fields = fieldErrors(err);
      setError(
        fields.length > 0
          ? fields.map((f) => f.message).join(" ")
          : errorMessage(err),
      );
      setBusy(false);
    }
  }

  const title = book ? "Rename phonebook" : "New phonebook";
  return (
    <Modal
      title={title}
      onClose={onClose}
      width={460}
      actions={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="book-form" disabled={busy}>
            {book ? "Save" : "Create book"}
          </Button>
        </>
      }
    >
      <form
        id="book-form"
        className="prov-stack"
        aria-label={title}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        {error && (
          <Alert tone="bad" title="Could not save the phonebook">
            {error}
          </Alert>
        )}
        <Input
          id="book-name"
          label="Name"
          autoFocus
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
      </form>
    </Modal>
  );
}

/** The automatic extensions book: read-only, with a hide toggle per extension. */
function ExtensionsBook() {
  const [state, setState] = useState<Load<Extension[]>>({ status: "loading" });
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    listExtensions(controller.signal)
      .then((value) => setState({ status: "ready", value }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  async function onToggle(ext: Extension, hidden: boolean) {
    setError(null);
    try {
      const saved = await updateExtension(ext.id, { directoryHidden: hidden });
      setState((prev) =>
        prev.status === "ready"
          ? {
              status: "ready",
              value: prev.value.map((x) =>
                sameId(x.id, ext.id) ? { ...x, ...saved } : x,
              ),
            }
          : prev,
      );
    } catch (err) {
      setError(`Could not change ${ext.number}: ${errorMessage(err)}`);
    }
  }

  const columns: TableColumn<Extension>[] = [
    { key: "number", label: "Number", mono: true, render: (x) => x.number },
    { key: "name", label: "Name", render: (x) => x.name },
    {
      key: "shown",
      label: "In the directory",
      render: (x) => (
        <Can
          fallback={
            x.directoryHidden ? (
              <Badge tone="outline">Hidden</Badge>
            ) : (
              <Badge tone="good">Shown</Badge>
            )
          }
        >
          <Switch
            aria-label={`Show ${x.number} in the directory`}
            checked={!x.directoryHidden}
            onChange={(e) => void onToggle(x, !e.target.checked)}
          />
        </Can>
      ),
    },
  ];

  return (
    <div className="prov-stack">
      <Alert tone="info" title="Built from the extensions">
        This book lists every extension by name and number and cannot be edited
        or imported into. Hide an extension to keep it out of the phones&apos;
        directory.
      </Alert>
      {error && (
        <Alert tone="bad" title="Could not save">
          {error}
        </Alert>
      )}
      {state.status === "loading" && <Spinner label="Loading extensions…" />}
      {state.status === "error" && (
        <Alert tone="bad" title="Could not load the extensions">
          {state.message}
        </Alert>
      )}
      {state.status === "ready" && (
        <Table
          caption="Extensions book"
          columns={columns}
          rows={state.value}
          rowKey={(x) => String(x.id)}
        />
      )}
    </div>
  );
}

/**
 * Shows its children to an operator and above, or to everybody for a list
 * its viewer owns (the user's own contacts, spec P-19).
 */
function Gate({ open, children }: { open: boolean; children: ReactNode }) {
  return open ? <>{children}</> : <Can>{children}</Can>;
}

/**
 * A list of entries, with search, edit and CSV: a named or company book's
 * (the default) or, with `api` and `selfService`, the signed-in user's own
 * contacts, which any role may change.
 */
export function EntriesView({
  book,
  onCount,
  api: entriesApi,
  selfService = false,
}: {
  book: Book;
  onCount: (count: number) => void;
  api?: EntriesApi;
  selfService?: boolean;
}) {
  const bookId = book.id;
  const api = useMemo(
    () => entriesApi ?? bookEntriesApi(bookId),
    [entriesApi, bookId],
  );
  const [entries, setEntries] = useState<Load<Entry[]>>({ status: "loading" });
  const [next, setNext] = useState("");
  const [query, setQuery] = useState("");
  const [editing, setEditing] = useState<Entry | "new" | null>(null);
  const [deleting, setDeleting] = useState<Entry | null>(null);
  const [importing, setImporting] = useState(false);
  const [reload, setReload] = useState(0);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState<string | null>(null);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    api
      .list("", controller.signal)
      .then((p) => {
        setEntries({ status: "ready", value: p.items });
        setNext(p.next);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setEntries({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [api, reload]);

  async function onMore() {
    setLoadingMore(true);
    setMoreError(null);
    try {
      const p = await api.list(next);
      setEntries((prev) =>
        prev.status === "ready"
          ? { status: "ready", value: [...prev.value, ...p.items] }
          : prev,
      );
      setNext(p.next);
    } catch (err) {
      setMoreError(errorMessage(err));
    } finally {
      setLoadingMore(false);
    }
  }

  async function onDelete(e: Entry) {
    try {
      await api.remove(e.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete the entry: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    setEntries((prev) =>
      prev.status === "ready"
        ? {
            status: "ready",
            value: prev.value.filter((x) => !sameId(x.id, e.id)),
          }
        : prev,
    );
    onCount(Math.max(book.entryCount - 1, 0));
    setDeleting(null);
    toast.show(`${entryName(e)} deleted.`);
  }

  const all = entries.status === "ready" ? entries.value : [];
  const q = query.trim().toLowerCase();
  const shown = q
    ? all.filter((e) =>
        [entryName(e), e.company, e.note, ...e.numbers.map((n) => n.number)]
          .join("\n")
          .toLowerCase()
          .includes(q),
      )
    : all;

  const columns: TableColumn<Entry>[] = [
    { key: "name", label: "Name", render: (e) => entryName(e) },
    { key: "company", label: "Company", render: (e) => e.company || "—" },
    {
      key: "numbers",
      label: "Numbers",
      render: (e) => (
        <span className="pb-numbers">
          {e.numbers.map((n) => (
            <span key={n.label} className="pb-number">
              <small className="prov-muted">{n.label}</small>{" "}
              <span className="az-table__mono">{n.number}</span>
            </span>
          ))}
        </span>
      ),
    },
    { key: "note", label: "Note", render: (e) => e.note || "—" },
    {
      key: "actions",
      label: "Actions",
      align: "right",
      render: (e) => (
        <Gate open={selfService}>
          <div className="inv-actions">
            <IconButton
              icon="pencil"
              label={`Edit ${entryName(e)}`}
              size={15}
              onClick={() => setEditing(e)}
            />
            <IconButton
              icon="trash-2"
              label={`Delete ${entryName(e)}`}
              size={15}
              onClick={() => setDeleting(e)}
            />
          </div>
        </Gate>
      ),
    },
  ];

  return (
    <div className="prov-stack">
      <div className="prov-row pb-toolbar">
        <Input
          id="entry-search"
          label="Search entries"
          icon="search"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <div className="prov-row pb-toolbar__buttons">
          <Gate open={selfService}>
            <Button
              variant="secondary"
              icon="upload"
              onClick={() => setImporting(true)}
            >
              Import CSV
            </Button>
            <a
              className="az-btn az-btn--secondary"
              href={api.exportUrl}
              download
            >
              Export CSV
            </a>
            <Button icon="plus" onClick={() => setEditing("new")}>
              Add entry
            </Button>
          </Gate>
        </div>
      </div>
      {entries.status === "loading" && <Spinner label="Loading entries…" />}
      {entries.status === "error" && (
        <Alert tone="bad" title="Could not load the entries">
          {entries.message}
        </Alert>
      )}
      {entries.status === "ready" && all.length === 0 && (
        <EmptyState
          icon="book-user"
          title={`${book.name} is empty`}
          description="Add entries one by one, or import a CSV with the columns first_name, last_name, company, work, mobile, home, other and note."
        />
      )}
      {entries.status === "ready" && all.length > 0 && (
        <>
          {shown.length === 0 ? (
            <p className="prov-muted">No entries match the search.</p>
          ) : (
            <Table
              caption={`Entries of ${book.name}`}
              columns={columns}
              rows={shown}
              rowKey={(e) => String(e.id)}
            />
          )}
          {moreError && (
            <Alert tone="bad" title="Could not load more entries">
              {moreError}
            </Alert>
          )}
          {next !== "" && (
            <div>
              <Button
                variant="secondary"
                disabled={loadingMore}
                onClick={() => void onMore()}
              >
                Show more entries
              </Button>
            </div>
          )}
        </>
      )}

      {editing && (
        <EntryModal
          book={book}
          api={api}
          entry={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={(saved, created) => {
            setEntries((prev) =>
              prev.status === "ready"
                ? {
                    status: "ready",
                    value: created
                      ? [...prev.value, saved]
                      : prev.value.map((x) =>
                          sameId(x.id, saved.id) ? saved : x,
                        ),
                  }
                : prev,
            );
            if (created) onCount(book.entryCount + 1);
            setEditing(null);
            toast.show(`${entryName(saved)} ${created ? "added" : "saved"}.`);
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete ${entryName(deleting)}?`}
          description="The entry is removed from the book and from the phones' directory at their next refresh."
          confirmLabel="Delete entry"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {importing && (
        <ImportModal
          book={book}
          api={api}
          onClose={() => setImporting(false)}
          onImported={(imported) => {
            setImporting(false);
            setEntries({ status: "loading" });
            setReload((n) => n + 1);
            // The list is reloaded; the count follows the next book list load.
            onCount(book.entryCount);
            toast.show(`${imported} entries imported.`);
          }}
        />
      )}
      {toast.node}
    </div>
  );
}

const ENTRY_FIELDS = [
  "firstName",
  "lastName",
  "company",
  "note",
  "numbers",
  "numbers[0]",
  "numbers[0].label",
  "numbers[0].number",
  "numbers[1]",
  "numbers[1].label",
  "numbers[1].number",
  "numbers[2]",
  "numbers[2].label",
  "numbers[2].number",
] as const;

const MAX_NUMBERS = 3;

interface NumberDraft {
  label: string;
  number: string;
}

function EntryModal({
  book,
  api,
  entry,
  onClose,
  onSaved,
}: {
  book: Book;
  api: EntriesApi;
  entry: Entry | null;
  onClose: () => void;
  onSaved: (entry: Entry, created: boolean) => void;
}) {
  const [firstName, setFirstName] = useState(entry?.firstName ?? "");
  const [lastName, setLastName] = useState(entry?.lastName ?? "");
  const [company, setCompany] = useState(entry?.company ?? "");
  const [note, setNote] = useState(entry?.note ?? "");
  const [numbers, setNumbers] = useState<NumberDraft[]>(
    entry?.numbers.length
      ? entry.numbers.map((n) => ({ label: n.label, number: n.number }))
      : [{ label: "work", number: "" }],
  );
  const [errors, setErrors] = useState<ErrorMap>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  function setNumber(i: number, patch: Partial<NumberDraft>) {
    setNumbers((ns) => ns.map((n, j) => (j === i ? { ...n, ...patch } : n)));
  }

  function addNumber() {
    setNumbers((ns) => {
      const used = new Set(ns.map((n) => n.label));
      const label = NUMBER_LABELS.find((l) => !used.has(l)) ?? "other";
      return [...ns, { label, number: "" }];
    });
  }

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    const input: EntryInput = {
      firstName: firstName.trim(),
      lastName: lastName.trim(),
      company: company.trim(),
      note: note.trim(),
      numbers: numbers
        .filter((n) => n.number.trim() !== "")
        .map((n) => ({ label: n.label, number: n.number.trim() })),
    };
    setBusy(true);
    try {
      const saved = entry
        ? await api.update(entry.id, input)
        : await api.create(input);
      onSaved(saved, entry === null);
    } catch (err) {
      const { byKey, unmatched } = mapFieldErrors(fieldErrors(err), [
        ...ENTRY_FIELDS,
      ]);
      setErrors(byKey);
      if (Object.keys(byKey).length === 0 || unmatched.length > 0) {
        setFormError(
          unmatched.length > 0
            ? unmatched.map((f) => `${f.path}: ${f.message}`).join(" ")
            : errorMessage(err),
        );
      }
      setBusy(false);
    }
  }

  const title = entry ? `Edit ${entryName(entry)}` : "Add entry";
  return (
    <Modal
      title={title}
      description={`In ${book.name}.`}
      onClose={onClose}
      width={600}
      actions={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="entry-form" disabled={busy}>
            {entry ? "Save" : "Add entry"}
          </Button>
        </>
      }
    >
      <form
        id="entry-form"
        className="prov-stack"
        aria-label={title}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        {formError && (
          <Alert tone="bad" title="Could not save the entry">
            {formError}
          </Alert>
        )}
        <div className="prov-grid-2">
          <Input
            id="entry-first"
            label="First name"
            autoFocus
            error={errors.firstName}
            value={firstName}
            onChange={(e) => setFirstName(e.target.value)}
          />
          <Input
            id="entry-last"
            label="Last name"
            error={errors.lastName}
            value={lastName}
            onChange={(e) => setLastName(e.target.value)}
          />
        </div>
        <Input
          id="entry-company"
          label="Company"
          error={errors.company}
          value={company}
          onChange={(e) => setCompany(e.target.value)}
        />
        <fieldset className="pb-numbers-editor">
          <legend className="az-field__label">Numbers</legend>
          {numbers.map((n, i) => (
            <div className="pb-number-row" key={i}>
              <Select
                id={`entry-label-${i}`}
                label={`Label ${i + 1}`}
                value={n.label}
                error={errors[`numbers[${i}].label`]}
                onChange={(e) => setNumber(i, { label: e.target.value })}
                options={NUMBER_LABELS.map((l) => ({ value: l, label: l }))}
              />
              <Input
                id={`entry-number-${i}`}
                label={`Number ${i + 1}`}
                mono
                placeholder="+32 2 555 01 02"
                error={
                  errors[`numbers[${i}].number`] ?? errors[`numbers[${i}]`]
                }
                value={n.number}
                onChange={(e) => setNumber(i, { number: e.target.value })}
              />
              {numbers.length > 1 && (
                <IconButton
                  icon="x"
                  label={`Remove number ${i + 1}`}
                  size={14}
                  onClick={() =>
                    setNumbers((ns) => ns.filter((_, j) => j !== i))
                  }
                />
              )}
            </div>
          ))}
          {errors.numbers && (
            <p className="az-field__error" role="alert">
              {errors.numbers}
            </p>
          )}
          {numbers.length < MAX_NUMBERS && (
            <div>
              <Button
                variant="secondary"
                size="sm"
                icon="plus"
                onClick={addNumber}
              >
                Add number
              </Button>
            </div>
          )}
        </fieldset>
        <Input
          id="entry-note"
          label="Note"
          error={errors.note}
          value={note}
          onChange={(e) => setNote(e.target.value)}
        />
      </form>
    </Modal>
  );
}

function ImportModal({
  book,
  api,
  onClose,
  onImported,
}: {
  book: Book;
  api: EntriesApi;
  onClose: () => void;
  onImported: (imported: number) => void;
}) {
  const [csv, setCsv] = useState("");
  const [mode, setMode] = useState<"append" | "replace">("append");
  const [checked, setChecked] = useState<{
    csv: string;
    mode: string;
    result: ImportResult;
  } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const current =
    checked !== null && checked.csv === csv && checked.mode === mode
      ? checked.result
      : null;
  const canImport = current !== null && current.ok && !busy;

  async function onCheck() {
    setError(null);
    setBusy(true);
    try {
      const result = await api.importCsv(csv, { dryRun: true, mode });
      setChecked({ csv, mode, result });
    } catch (err) {
      setChecked(null);
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function onImport() {
    setError(null);
    setBusy(true);
    try {
      const result = await api.importCsv(csv, { dryRun: false, mode });
      onImported(result.imported);
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  }

  const errorColumns: TableColumn<ImportResult["errors"][number]>[] = [
    { key: "row", label: "Row", mono: true, render: (r) => String(r.row) },
    {
      key: "column",
      label: "Column",
      mono: true,
      render: (r) => r.column || "—",
    },
    { key: "reason", label: "Problem", render: (r) => r.reason },
  ];

  return (
    <Modal
      title={`Import into ${book.name}`}
      description="A CSV with a header row: first_name, last_name, company, work, mobile, home, other, note. Check the file first; the import adds every row or none."
      onClose={busy ? undefined : onClose}
      width={720}
      actions={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="secondary"
            disabled={busy || csv.trim() === ""}
            onClick={() => void onCheck()}
          >
            Check
          </Button>
          <Button disabled={!canImport} onClick={() => void onImport()}>
            Import
          </Button>
        </>
      }
    >
      <div className="prov-stack">
        {error && (
          <Alert tone="bad" title="Import failed">
            {error}
          </Alert>
        )}
        <Select
          id="import-mode"
          label="Mode"
          value={mode}
          onChange={(e) => setMode(e.target.value as "append" | "replace")}
          options={[
            {
              value: "append",
              label: "Add to the entries already in the book",
            },
            { value: "replace", label: "Replace every entry in the book" },
          ]}
        />
        {mode === "replace" && (
          <Alert tone="warn" title="Replace deletes the current entries">
            Every entry now in {book.name} is removed when the import runs.
          </Alert>
        )}
        <div className="az-field">
          <label className="az-field__label" htmlFor="import-csv">
            CSV
          </label>
          <textarea
            id="import-csv"
            className="az-input prov-textarea"
            rows={8}
            spellCheck={false}
            placeholder={
              "first_name,last_name,company,work\nAnn,Smith,Acme,+32 2 555 01 02"
            }
            value={csv}
            onChange={(e) => setCsv(e.target.value)}
          />
        </div>
        <div className="az-field">
          <label className="az-field__label" htmlFor="import-file">
            Or load a file
          </label>
          <input
            id="import-file"
            type="file"
            accept=".csv,text/csv,text/plain"
            onChange={(e) => {
              const file = e.target.files?.[0];
              if (file) void file.text().then(setCsv);
            }}
          />
        </div>
        {current && (
          <>
            {current.ok ? (
              <Alert tone="good" title="Ready to import">
                {current.rows} {current.rows === 1 ? "row" : "rows"} can be
                imported.
              </Alert>
            ) : (
              <Alert tone="warn" title="Fix the rows with errors">
                Nothing is imported until every row is valid.
              </Alert>
            )}
            {current.errors.length > 0 && (
              <Table
                caption="Rows with errors"
                columns={errorColumns}
                rows={current.errors}
                rowKey={(r, i) => `${r.row}-${r.column}-${i}`}
              />
            )}
            {current.warnings.length > 0 && (
              <Alert tone="info" title="Worth a look">
                <ul className="pb-warnings">
                  {current.warnings.map((w, i) => (
                    <li key={i}>
                      Row {w.row}: {w.reason}
                    </li>
                  ))}
                </ul>
              </Alert>
            )}
          </>
        )}
      </div>
    </Modal>
  );
}
