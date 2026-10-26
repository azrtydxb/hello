import { useCallback, useEffect, useState, type FormEvent } from "react";
import {
  errorMessage,
  fieldErrors,
  listAnnouncements,
  listTrunks,
  type Announcement,
  type FieldError,
  type Trunk,
} from "../api";
import {
  createBlocklistEntry,
  deleteBlocklistEntry,
  importBlocklist,
  listBlocklist,
  putBlocklistEntry,
  type BlockAction,
  type BlockMatch,
  type BlocklistEntry,
  type BlocklistImportResult,
  type BlocklistInput,
} from "../api/blocklist";
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
import { Can, useCan } from "../role";

const PAGE = 200;
const REASON_MAX = 120;

type Editing = { kind: "new" } | { kind: "edit"; entry: BlocklistEntry } | null;

interface Draft {
  pattern: string;
  match: BlockMatch;
  action: BlockAction;
  /** "" is every trunk. */
  scope: string;
  reason: string;
  announcementId: string;
  enabled: boolean;
}

function draftOf(e: BlocklistEntry | null): Draft {
  if (!e) {
    return {
      pattern: "",
      match: "exact",
      action: "block",
      scope: "",
      reason: "",
      announcementId: "",
      enabled: true,
    };
  }
  return {
    pattern: e.pattern,
    match: e.match,
    action: e.action,
    scope: e.trunkId === null ? "" : String(e.trunkId),
    reason: e.reason,
    announcementId: e.announcementId === null ? "" : String(e.announcementId),
    enabled: e.enabled,
  };
}

const KEYS = [
  "pattern",
  "match",
  "action",
  "trunkId",
  "reason",
  "announcementId",
  "enabled",
];

/**
 * Blocklist: numbers an operator blocks or allows on inbound trunk calls,
 * for every trunk or one. An allow entry always beats a block entry. The
 * per-trunk anonymous reject and country restriction are on the Trunks page.
 */
export function Blocklist() {
  const [rows, setRows] = useState<BlocklistEntry[]>([]);
  const [next, setNext] = useState("");
  const [total, setTotal] = useState(0);
  const [status, setStatus] = useState<"loading" | "ready" | "error">(
    "loading",
  );
  const [message, setMessage] = useState("");
  const [q, setQ] = useState("");
  const [action, setAction] = useState<BlockAction | "">("");
  const [scope, setScope] = useState("");
  const [trunks, setTrunks] = useState<Trunk[]>([]);
  const [announcements, setAnnouncements] = useState<Announcement[]>([]);
  const [editing, setEditing] = useState<Editing>(null);
  const [deleting, setDeleting] = useState<BlocklistEntry | null>(null);
  const [importing, setImporting] = useState(false);
  const [reload, setReload] = useState(0);
  const toast = useToast();
  const isAdmin = useCan("admin");

  useEffect(() => {
    const c = new AbortController();
    listTrunks(c.signal)
      .then(setTrunks)
      .catch(() => undefined);
    listAnnouncements(c.signal)
      .then(setAnnouncements)
      .catch(() => undefined);
    return () => c.abort();
  }, []);

  // The first page, again whenever a filter or a change asks for it.
  useEffect(() => {
    const c = new AbortController();
    const t = setTimeout(() => {
      listBlocklist({ q: q.trim(), action, scope, limit: PAGE }, c.signal)
        .then((p) => {
          setRows(p.items);
          setNext(p.next);
          setTotal(p.total);
          setStatus("ready");
        })
        .catch((err: unknown) => {
          if (c.signal.aborted) return;
          setMessage(errorMessage(err));
          setStatus("error");
        });
    }, 200);
    return () => {
      clearTimeout(t);
      c.abort();
    };
  }, [q, action, scope, reload]);

  const more = useCallback(async () => {
    try {
      const p = await listBlocklist({
        q: q.trim(),
        action,
        scope,
        limit: PAGE,
        after: next,
      });
      setRows((prev) => [...prev, ...p.items]);
      setNext(p.next);
      setTotal(p.total);
    } catch (err: unknown) {
      toast.show(`Could not load more: ${errorMessage(err)}`);
    }
  }, [q, action, scope, next, toast]);

  const refresh = () => setReload((n) => n + 1);

  async function onDelete(e: BlocklistEntry) {
    await deleteBlocklistEntry(e.id);
    setDeleting(null);
    toast.show("Blocklist entry deleted.");
    refresh();
  }

  const scopeName = (e: BlocklistEntry) =>
    e.trunkId === null ? "All trunks" : e.trunkName || `Trunk ${e.trunkId}`;

  const columns: TableColumn<BlocklistEntry>[] = [
    {
      key: "pattern",
      label: "Number",
      mono: true,
      render: (e) => <span className="az-table__primary">{e.pattern}</span>,
    },
    {
      key: "match",
      label: "Match",
      render: (e) => (e.match === "prefix" ? "Prefix" : "Exact"),
    },
    {
      key: "action",
      label: "Action",
      render: (e) =>
        e.action === "block" ? (
          <Badge tone="bad">Block</Badge>
        ) : (
          <Badge tone="good">Allow</Badge>
        ),
    },
    { key: "scope", label: "Applies to", render: scopeName },
    ...(isAdmin
      ? [
          {
            key: "reason",
            label: "Reason",
            render: (e: BlocklistEntry) => e.reason || "—",
          },
        ]
      : []),
    {
      key: "enabled",
      label: "State",
      render: (e) =>
        e.enabled ? (
          <Badge tone="outline">On</Badge>
        ) : (
          <Badge tone="warn">Off</Badge>
        ),
    },
    {
      key: "actions",
      label: <span className="visually-hidden">Actions</span>,
      align: "right",
      render: (e) => (
        <Can>
          <IconButton
            icon="pencil"
            label={`Edit ${e.pattern}`}
            onClick={() => setEditing({ kind: "edit", entry: e })}
          />
          <IconButton
            icon="trash-2"
            label={`Delete ${e.pattern}`}
            onClick={() => setDeleting(e)}
          />
        </Can>
      ),
    },
  ];

  const actions = (
    <Can>
      <Button
        variant="secondary"
        icon="upload"
        onClick={() => setImporting(true)}
      >
        Import CSV
      </Button>
      <Button icon="plus" onClick={() => setEditing({ kind: "new" })}>
        Add entry
      </Button>
    </Can>
  );
  const filtered = q.trim() !== "" || action !== "" || scope !== "";

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Call flow"
        title="Blocklist"
        description="Numbers turned away on inbound trunk calls. A blocked call is answered 603 Decline (or 486 Busy Here, per trunk) and never rings. An allow entry always beats a block entry; an exact number beats a prefix. Anonymous reject and the country restriction are set per trunk on the Trunks page."
        actions={actions}
      />
      <div className="prov-stack">
        <div className="prov-row">
          <Input
            id="blocklist-search"
            label="Search"
            value={q}
            placeholder="+4122"
            onChange={(e) => setQ(e.target.value)}
          />
          <Select
            id="blocklist-action"
            label="Action"
            value={action}
            onChange={(e) =>
              setAction(
                e.target.value === "block" || e.target.value === "allow"
                  ? e.target.value
                  : "",
              )
            }
            options={[
              { value: "", label: "Block and allow" },
              { value: "block", label: "Block only" },
              { value: "allow", label: "Allow only" },
            ]}
          />
          <Select
            id="blocklist-scope"
            label="Applies to"
            value={scope}
            onChange={(e) => setScope(e.target.value)}
            options={[
              { value: "", label: "Every scope" },
              { value: "global", label: "All trunks" },
              ...trunks.map((t) => ({ value: String(t.id), label: t.name })),
            ]}
          />
        </div>
        {status === "loading" && <Spinner label="Loading the blocklist…" />}
        {status === "error" && (
          <Alert tone="bad" title="Could not load the blocklist">
            {message}
          </Alert>
        )}
        {status === "ready" && rows.length === 0 && (
          <EmptyState
            icon="shield-ban"
            title={filtered ? "No entry matches" : "The blocklist is empty"}
            description={
              filtered
                ? "Change the search or the filters."
                : "Add a number or a prefix such as +44 to turn those callers away before a phone rings."
            }
          />
        )}
        {status === "ready" && rows.length > 0 && (
          <>
            <Table
              caption="Blocklist entries"
              columns={columns}
              rows={rows}
              rowKey={(e) => String(e.id)}
            />
            <p className="az-field__hint">
              Showing {rows.length} of {total}{" "}
              {total === 1 ? "entry" : "entries"}.
            </p>
            {next !== "" && (
              <div>
                <Button variant="secondary" onClick={() => void more()}>
                  Load more
                </Button>
              </div>
            )}
          </>
        )}
      </div>
      {editing && (
        <EntryModal
          key={editing.kind === "edit" ? String(editing.entry.id) : "new"}
          entry={editing.kind === "edit" ? editing.entry : null}
          trunks={trunks}
          announcements={announcements}
          onClose={() => setEditing(null)}
          onSaved={(created) => {
            setEditing(null);
            toast.show(
              created ? "Blocklist entry added." : "Blocklist entry saved.",
            );
            refresh();
          }}
        />
      )}
      {importing && (
        <ImportModal
          onClose={() => setImporting(false)}
          onImported={(r) => {
            setImporting(false);
            toast.show(
              `Imported ${r.created + r.updated} entries (${r.created} new, ${r.updated} updated).`,
            );
            refresh();
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete the entry for ${deleting.pattern}?`}
          description="Calls from this number are no longer screened by it."
          confirmLabel="Delete entry"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

function EntryModal({
  entry,
  trunks,
  announcements,
  onClose,
  onSaved,
}: {
  entry: BlocklistEntry | null;
  trunks: Trunk[];
  announcements: Announcement[];
  onClose: () => void;
  onSaved: (created: boolean) => void;
}) {
  const [d, setD] = useState(() => draftOf(entry));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const isAdmin = useCan("admin");
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));

  async function onSubmit(ev: FormEvent<HTMLFormElement>) {
    ev.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const found: Record<string, string> = {};
    if (d.pattern.trim() === "") found.pattern = "Enter a number or a prefix.";
    if (d.reason.length > REASON_MAX) {
      found.reason = `Use at most ${REASON_MAX} characters.`;
    }
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      const body: BlocklistInput = {
        pattern: d.pattern.trim(),
        match: d.match,
        action: d.action,
        trunkId: d.scope === "" ? null : Number(d.scope),
        announcementId:
          d.action === "block" && d.announcementId !== ""
            ? Number(d.announcementId)
            : null,
        enabled: d.enabled,
        // Only an admin sees the stored note, so only an admin sends it back.
        ...(isAdmin || !entry ? { reason: d.reason } : {}),
      };
      if (entry) await putBlocklistEntry(entry.id, body);
      else await createBlocklistEntry(body);
      onSaved(entry === null);
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), KEYS);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Modal
      title={entry ? `Edit ${entry.pattern}` : "Add a blocklist entry"}
      description="Enter the number in any usual spelling; it is stored in one form. A prefix such as +44 matches every number that starts with it."
      onClose={busy ? undefined : onClose}
      width={560}
      actions={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="blocklist-form" disabled={busy}>
            {entry ? "Save entry" : "Add entry"}
          </Button>
        </>
      }
    >
      <form
        id="blocklist-form"
        className="prov-stack"
        aria-label={entry ? `Edit ${entry.pattern}` : "New blocklist entry"}
        onSubmit={(ev) => void onSubmit(ev)}
        noValidate
      >
        {formError && (
          <Alert tone="bad" title="Could not save the entry">
            {formError}
            {unmatched.length > 0 && (
              <ul>
                {unmatched.map((f, i) => (
                  <li key={i}>
                    {f.path}: {f.message}
                  </li>
                ))}
              </ul>
            )}
          </Alert>
        )}
        <Input
          id="blocklist-pattern"
          label="Number"
          mono
          autoFocus
          value={d.pattern}
          error={errors.pattern}
          hint="For example +41 22 555 00 00, or +44 for a prefix."
          onChange={(e) => set("pattern", e.target.value)}
        />
        <Select
          id="blocklist-match"
          label="Match"
          value={d.match}
          error={errors.match}
          onChange={(e) =>
            set("match", e.target.value === "prefix" ? "prefix" : "exact")
          }
          options={[
            { value: "exact", label: "Exactly this number" },
            { value: "prefix", label: "Every number that starts with this" },
          ]}
        />
        <Select
          id="blocklist-entry-action"
          label="Action"
          value={d.action}
          error={errors.action}
          onChange={(e) =>
            set("action", e.target.value === "allow" ? "allow" : "block")
          }
          options={[
            { value: "block", label: "Block" },
            { value: "allow", label: "Allow (beats every block entry)" },
          ]}
        />
        <Select
          id="blocklist-entry-scope"
          label="Applies to"
          value={d.scope}
          error={errors.trunkId}
          onChange={(e) => set("scope", e.target.value)}
          options={[
            { value: "", label: "All trunks" },
            ...trunks.map((t) => ({ value: String(t.id), label: t.name })),
          ]}
        />
        {d.action === "block" && (
          <Select
            id="blocklist-announcement"
            label="Announcement"
            value={d.announcementId}
            error={errors.announcementId}
            hint="Optional. Played to the caller before the call ends, instead of an immediate rejection."
            onChange={(e) => set("announcementId", e.target.value)}
            options={[
              { value: "", label: "None (reject at once)" },
              ...announcements.map((a) => ({
                value: String(a.id),
                label: a.name,
              })),
            ]}
          />
        )}
        <Input
          id="blocklist-reason"
          label="Reason"
          value={isAdmin || !entry ? d.reason : ""}
          error={errors.reason}
          disabled={!isAdmin && entry !== null}
          hint="A note for administrators; no one else sees it."
          onChange={(e) => set("reason", e.target.value)}
        />
        <Switch
          label="Enabled"
          hint="A disabled entry is kept but not applied."
          labelPosition="end"
          checked={d.enabled}
          onChange={(e) => set("enabled", e.target.checked)}
        />
      </form>
    </Modal>
  );
}

function ImportModal({
  onClose,
  onImported,
}: {
  onClose: () => void;
  onImported: (r: BlocklistImportResult) => void;
}) {
  const [csv, setCsv] = useState("");
  const [checked, setChecked] = useState<{
    csv: string;
    result: BlocklistImportResult;
  } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const current =
    checked !== null && checked.csv === csv ? checked.result : null;
  const canImport = current !== null && current.ok && !busy;

  async function onCheck() {
    setError(null);
    setBusy(true);
    try {
      setChecked({ csv, result: await importBlocklist(csv, { dryRun: true }) });
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
      onImported(await importBlocklist(csv, { dryRun: false }));
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  }

  const errorColumns: TableColumn<BlocklistImportResult["errors"][number]>[] = [
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
      title="Import blocklist entries"
      description="A CSV with a header row: number, action, match, scope, reason. Only number is required. action is block or allow, match is exact or prefix (a number ending in * is a prefix), scope is global or a trunk name. A number already listed is updated. Check the file first; the import applies every row or none."
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
        <div className="az-field">
          <label className="az-field__label" htmlFor="blocklist-csv">
            CSV
          </label>
          <textarea
            id="blocklist-csv"
            className="az-input prov-textarea"
            rows={8}
            spellCheck={false}
            placeholder={
              "number,action,reason\n+44*,block,UK spam\n+41 22 555 00 00,allow,our bank"
            }
            value={csv}
            onChange={(e) => setCsv(e.target.value)}
          />
        </div>
        <div className="az-field">
          <label className="az-field__label" htmlFor="blocklist-file">
            Or load a file
          </label>
          <input
            id="blocklist-file"
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
                <ul>
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
