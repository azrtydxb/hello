import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router";
import { errorMessage, fieldErrors, type Id } from "../api";
import {
  createGroup,
  deleteGroup,
  listBooks,
  listGroups,
  setGroupBooks,
  updateGroup,
  type Book,
  type Group,
} from "../api/phonebook";
import { listPhones, type Phone } from "../api/prov";
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
import { formatMac, PhonesTabs } from "./phones/ui";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { Can } from "../role";
import "./phones/phones.css";
import "./phonebooks/phonebooks.css";

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | {
      status: "ready";
      groups: Group[];
      books: Book[];
      phones: Phone[];
    };

const sameId = (a: Id, b: Id) => String(a) === String(b);

/** Phone groups: which address books a set of phones is delivered. */
export function PhoneGroups() {
  const [state, setState] = useState<State>({ status: "loading" });
  const [editing, setEditing] = useState<Group | "new" | null>(null);
  const [deleting, setDeleting] = useState<Group | null>(null);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    Promise.all([
      listGroups(controller.signal),
      listBooks(controller.signal),
      // Member phones are shown for context; the groups work without them.
      listPhones(controller.signal).catch(() => [] as Phone[]),
    ])
      .then(([groups, books, phones]) =>
        setState({ status: "ready", groups, books, phones }),
      )
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  const ready = state.status === "ready" ? state : null;

  function replaceGroups(fn: (groups: Group[]) => Group[]) {
    setState((prev) =>
      prev.status === "ready" ? { ...prev, groups: fn(prev.groups) } : prev,
    );
  }

  async function onDelete(g: Group) {
    try {
      await deleteGroup(g.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${g.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    replaceGroups((gs) => gs.filter((x) => !sameId(x.id, g.id)));
    setDeleting(null);
    toast.show(`Phone group ${g.name} deleted.`);
  }

  const bookName = (id: Id) =>
    ready?.books.find((b) => sameId(b.id, id))?.name ?? `#${String(id)}`;

  const columns: TableColumn<Group>[] = [
    { key: "name", label: "Group", render: (g) => g.name },
    {
      key: "description",
      label: "Description",
      render: (g) => g.description || "—",
    },
    {
      key: "books",
      label: "Delivers",
      render: (g) => (
        <span className="prov-flags">
          {g.includeExtensions && <Badge tone="outline">Extensions</Badge>}
          {g.includeCompany && <Badge tone="outline">Company</Badge>}
          {g.bookIds.map((id) => (
            <Badge key={String(id)} tone="info">
              {bookName(id)}
            </Badge>
          ))}
          {!g.includeExtensions &&
            !g.includeCompany &&
            g.bookIds.length === 0 && (
              <span className="prov-muted">No books</span>
            )}
        </span>
      ),
    },
    {
      key: "phones",
      label: "Phones",
      mono: true,
      render: (g) => String(g.phoneCount),
    },
    {
      key: "actions",
      label: "Actions",
      align: "right",
      render: (g) => (
        <div className="inv-actions">
          <IconButton
            icon="pencil"
            label={`Edit ${g.name}`}
            size={15}
            onClick={() => setEditing(g)}
          />
          <Can min="admin">
            <IconButton
              icon="trash-2"
              label={`Delete ${g.name}`}
              size={15}
              onClick={() => setDeleting(g)}
            />
          </Can>
        </div>
      ),
    },
  ];

  const newGroup = (
    <Can min="admin">
      <Button icon="plus" disabled={!ready} onClick={() => setEditing("new")}>
        New group
      </Button>
    </Can>
  );

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title="Phone groups"
        description="A group picks the address books its phones show: the extensions book, the company book and named books in a set order. Assign a phone to a group in the phone's form."
        actions={newGroup}
      />
      <PhonesTabs />
      <div className="prov-stack">
        {state.status === "loading" && (
          <Spinner label="Loading phone groups…" />
        )}
        {state.status === "error" && (
          <Alert tone="bad" title="Could not load the phone groups">
            {state.message}
          </Alert>
        )}
        {ready && ready.groups.length === 0 && (
          <EmptyState
            icon="users-round"
            title="No phone groups yet"
            description="Phones without a group show the extensions and company books. Create a group to give some phones other books."
            action={newGroup}
          />
        )}
        {ready && ready.groups.length > 0 && (
          <Table
            caption="Phone groups"
            columns={columns}
            rows={ready.groups}
            rowKey={(g) => String(g.id)}
          />
        )}
      </div>

      {editing && ready && (
        <GroupModal
          group={editing === "new" ? null : editing}
          books={ready.books}
          phones={ready.phones}
          onClose={() => setEditing(null)}
          onSaved={(saved, created) => {
            replaceGroups((gs) =>
              gs.some((x) => sameId(x.id, saved.id))
                ? gs.map((x) => (sameId(x.id, saved.id) ? saved : x))
                : [...gs, saved],
            );
            setEditing(null);
            toast.show(
              created
                ? `Phone group ${saved.name} created.`
                : `Phone group ${saved.name} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete phone group ${deleting.name}?`}
          description="Its phones stay but fall back to no group, which delivers the extensions and company books."
          confirmLabel="Delete group"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

const GROUP_FIELDS = [
  "name",
  "description",
  "includeExtensions",
  "includeCompany",
  "bookIds",
] as const;

function GroupModal({
  group,
  books,
  phones,
  onClose,
  onSaved,
}: {
  group: Group | null;
  books: Book[];
  phones: Phone[];
  onClose: () => void;
  onSaved: (group: Group, created: boolean) => void;
}) {
  const [name, setName] = useState(group?.name ?? "");
  const [description, setDescription] = useState(group?.description ?? "");
  const [includeExtensions, setIncludeExtensions] = useState(
    group?.includeExtensions ?? true,
  );
  const [includeCompany, setIncludeCompany] = useState(
    group?.includeCompany ?? true,
  );
  const [bookIds, setBookIds] = useState<Id[]>(group?.bookIds ?? []);
  const [pick, setPick] = useState("");
  const [errors, setErrors] = useState<ErrorMap>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Only named books can be assigned: the company and extensions books have
  // their own switches.
  const choices = books.filter(
    (b) => b.kind === "named" && !bookIds.some((id) => sameId(id, b.id)),
  );
  const nameOf = (id: Id) =>
    books.find((b) => sameId(b.id, id))?.name ?? `#${String(id)}`;
  const members = group
    ? phones.filter((p) => p.groupId != null && sameId(p.groupId, group.id))
    : [];

  function move(i: number, by: number) {
    setBookIds((ids) => {
      const j = i + by;
      if (j < 0 || j >= ids.length) return ids;
      const next = [...ids];
      [next[i], next[j]] = [next[j]!, next[i]!];
      return next;
    });
  }

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setErrors({});
    setBusy(true);
    const input = {
      name: name.trim(),
      description: description.trim(),
      includeExtensions,
      includeCompany,
    };
    try {
      let saved = group
        ? await updateGroup(group.id, input)
        : await createGroup(input);
      if (JSON.stringify(saved.bookIds) !== JSON.stringify(bookIds)) {
        saved = await setGroupBooks(saved.id, bookIds);
      }
      onSaved(saved, group === null);
    } catch (err) {
      const { byKey, unmatched } = mapFieldErrors(fieldErrors(err), [
        ...GROUP_FIELDS,
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

  const title = group ? `Edit ${group.name}` : "New phone group";
  return (
    <Modal
      title={title}
      onClose={onClose}
      width={640}
      actions={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Can min="admin">
            <Button type="submit" form="group-form" disabled={busy}>
              {group ? "Save" : "Create group"}
            </Button>
          </Can>
        </>
      }
    >
      <form
        id="group-form"
        className="prov-stack"
        aria-label={title}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        {formError && (
          <Alert tone="bad" title="Could not save the group">
            {formError}
          </Alert>
        )}
        <Input
          id="group-name"
          label="Name"
          autoFocus
          error={errors.name}
          value={name}
          onChange={(e) => setName(e.target.value)}
        />
        <Input
          id="group-description"
          label="Description"
          error={errors.description}
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
        <Switch
          label="Extensions book"
          hint="Every extension by name and number"
          labelPosition="end"
          checked={includeExtensions}
          onChange={(e) => setIncludeExtensions(e.target.checked)}
        />
        <Switch
          label="Company book"
          hint="The shared company address book"
          labelPosition="end"
          checked={includeCompany}
          onChange={(e) => setIncludeCompany(e.target.checked)}
        />

        <fieldset className="pb-numbers-editor">
          <legend className="az-field__label">Named books, in order</legend>
          {bookIds.length === 0 ? (
            <p className="prov-muted">No named books.</p>
          ) : (
            <ol
              className="pb-books"
              aria-label="Named books, in delivery order"
            >
              {bookIds.map((id, i) => (
                <li key={String(id)} className="pb-books__item">
                  <span>{nameOf(id)}</span>
                  <span className="pb-books__tools">
                    <IconButton
                      icon="arrow-up"
                      label={`Move ${nameOf(id)} up`}
                      size={14}
                      disabled={i === 0}
                      onClick={() => move(i, -1)}
                    />
                    <IconButton
                      icon="arrow-down"
                      label={`Move ${nameOf(id)} down`}
                      size={14}
                      disabled={i === bookIds.length - 1}
                      onClick={() => move(i, 1)}
                    />
                    <IconButton
                      icon="x"
                      label={`Remove ${nameOf(id)}`}
                      size={14}
                      onClick={() =>
                        setBookIds((ids) => ids.filter((x) => !sameId(x, id)))
                      }
                    />
                  </span>
                </li>
              ))}
            </ol>
          )}
          {errors.bookIds && (
            <p className="az-field__error" role="alert">
              {errors.bookIds}
            </p>
          )}
          <div className="prov-row">
            <Select
              id="group-book-add"
              label="Add a book"
              value={pick}
              onChange={(e) => setPick(e.target.value)}
              options={[
                { value: "", label: "Choose…" },
                ...choices.map((b) => ({ value: String(b.id), label: b.name })),
              ]}
            />
            <Button
              variant="secondary"
              size="sm"
              icon="plus"
              disabled={pick === ""}
              onClick={() => {
                const book = choices.find((b) => String(b.id) === pick);
                if (book) setBookIds((ids) => [...ids, book.id]);
                setPick("");
              }}
            >
              Add book
            </Button>
          </div>
          <p className="prov-muted">
            Earlier books win when two hold the same number. A phone shows the
            extensions book, the company book, then these, in this order.
          </p>
        </fieldset>

        {group && (
          <div className="prov-stack">
            <span className="az-field__label">Member phones</span>
            {members.length === 0 ? (
              <p className="prov-muted">No phone is in this group yet.</p>
            ) : (
              <ul className="pb-books" aria-label="Member phones">
                {members.map((p) => (
                  <li key={String(p.id)} className="pb-books__item">
                    <Link to={`/phones/${p.id}`}>{formatMac(p.mac)}</Link>
                    <span className="prov-muted">
                      {p.extensionNumber || "Unbound"}
                      {p.label ? ` · ${p.label}` : ""}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </form>
    </Modal>
  );
}
