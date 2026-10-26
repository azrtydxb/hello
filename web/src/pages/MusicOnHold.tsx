import { useEffect, useState, type FormEvent } from "react";
import {
  createMOHClass,
  deleteMOHClass,
  errorMessage,
  fieldErrors,
  listAnnouncements,
  listMOHClasses,
  putMOHClass,
  type Announcement,
  type FieldError,
  type Id,
  type MOHClass,
  type MOHClassFields,
  type MOHMode,
} from "../api";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  Drawer,
  EmptyState,
  IconButton,
  Input,
  PageHeader,
  Select,
  Spinner,
  Switch,
  useRestoreFocus,
  useToast,
} from "../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { FormAlert } from "./callflow/ui";
import { Can } from "../role";

const FORM_ID = "moh-form";
const MAX_TRACKS = 50;

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: MOHClass[] };

const MODE_LABEL: Record<MOHMode, string> = {
  loop: "Loop in order",
  shuffle: "Shuffle",
};

/**
 * Music on hold: named playlists of announcements. A held or parked party
 * hears the class of the extension that holds them, else the ring group's or
 * inbound route's, else the default class; with none, a built-in tune. A
 * ring group or inbound route may also play its class instead of ringback.
 */
export function MusicOnHold() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [announcements, setAnnouncements] = useState<Announcement[]>([]);
  const [annReady, setAnnReady] = useState(false);
  const [annError, setAnnError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<MOHClass | null>(null);
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; cls: MOHClass } | null
  >(null);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    listMOHClasses(controller.signal)
      .then((items) => setList({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    listAnnouncements(controller.signal)
      .then((items) => {
        setAnnouncements(items);
        setAnnReady(true);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setAnnError(errorMessage(err));
      });
    return () => controller.abort();
  }, []);

  async function onDelete(cls: MOHClass) {
    try {
      await deleteMOHClass(cls.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${cls.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    setList((prev) =>
      prev.status === "ready"
        ? { status: "ready", items: prev.items.filter((c) => c.id !== cls.id) }
        : prev,
    );
    setDeleting(null);
    toast.show(`Class ${cls.name} deleted.`);
  }

  const newClass = (
    <Can>
      <Button
        icon="plus"
        disabled={list.status !== "ready"}
        onClick={() => setEditing({ kind: "new" })}
      >
        New class
      </Button>
    </Can>
  );

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Media"
        title="Music on hold"
        description="What a held or parked caller hears, and what a caller hears instead of ringback. Built from announcements; with no class, a built-in tune plays."
        actions={newClass}
      />
      <div className="cf-stack">
        {annError && (
          <Alert tone="warn" title="Could not load the announcements">
            {annError} The track picker needs them.
          </Alert>
        )}
        {list.status === "loading" && <Spinner label="Loading classes…" />}
        {list.status === "error" && (
          <Alert tone="bad" title="Could not load the classes">
            {list.message}
          </Alert>
        )}
        {list.status === "ready" && list.items.length === 0 && (
          <EmptyState
            icon="music"
            title="No classes yet"
            description="Until you add one, held callers hear the built-in tune. Create a class and mark it as the default to replace it everywhere."
            action={newClass}
          />
        )}
        {list.status === "ready" && list.items.length > 0 && (
          <ul
            className="cf-grid cf-grid--groups cf-plain"
            aria-label="Music on hold classes"
          >
            {list.items.map((c) => (
              <ClassCard
                key={String(c.id)}
                cls={c}
                onEdit={() => setEditing({ kind: "edit", cls: c })}
                onDelete={() => setDeleting(c)}
              />
            ))}
          </ul>
        )}
      </div>

      {editing && (
        <ClassDrawer
          key={editing.kind === "edit" ? String(editing.cls.id) : "new"}
          cls={editing.kind === "edit" ? editing.cls : null}
          announcements={announcements}
          annReady={annReady}
          onClose={() => setEditing(null)}
          onSaved={(saved, created) => {
            setList((prev) => {
              if (prev.status !== "ready") return prev;
              // Saving a default clears the flag on the others.
              const others = prev.items
                .filter((c) => c.id !== saved.id)
                .map((c) => (saved.isDefault ? { ...c, isDefault: false } : c));
              return {
                status: "ready",
                items: [...others, saved].sort((a, b) =>
                  a.name.localeCompare(b.name),
                ),
              };
            });
            setEditing(null);
            toast.show(
              created
                ? `Class ${saved.name} created.`
                : `Class ${saved.name} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete class ${deleting.name}?`}
          description="Extensions, ring groups and routes that use it fall back to the default class."
          confirmLabel="Delete class"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

function ClassCard({
  cls: c,
  onEdit,
  onDelete,
}: {
  cls: MOHClass;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const titleId = `moh-${String(c.id)}-name`;
  return (
    <li className="az-card cf-card" aria-labelledby={titleId}>
      <div className="cf-card__head cf-card__head--top">
        <div style={{ minWidth: 0 }}>
          <h2 className="cf-card__group-name" id={titleId}>
            {c.name}
          </h2>
          <div className="cf-card__sub">
            {MODE_LABEL[c.mode]} ·{" "}
            {c.tracks.length === 0
              ? "built-in tune"
              : `${String(c.tracks.length)} track${c.tracks.length === 1 ? "" : "s"}`}
          </div>
        </div>
        {c.isDefault && <Badge tone="pillar">Default</Badge>}
      </div>
      {c.tracks.length > 0 && (
        <ol className="cf-members" aria-label={`Tracks of ${c.name}`}>
          {c.tracks.map((t, i) => (
            <li
              className="cf-member"
              key={`${String(t.announcementId)}-${String(i)}`}
            >
              <span className="cf-mono">{t.name}</span>
            </li>
          ))}
        </ol>
      )}
      <div className="cf-card__foot">
        <Button
          variant="secondary"
          size="sm"
          icon="pencil"
          aria-label={`Edit class ${c.name}`}
          onClick={onEdit}
        >
          Edit
        </Button>
        <Can>
          <Button
            variant="ghost"
            size="sm"
            icon="trash-2"
            className="cf-danger"
            aria-label={`Delete class ${c.name}`}
            onClick={onDelete}
          >
            Delete
          </Button>
        </Can>
      </div>
    </li>
  );
}

interface Draft {
  name: string;
  mode: MOHMode;
  isDefault: boolean;
  tracks: Id[];
}

const draftOf = (c: MOHClass | null): Draft =>
  c
    ? {
        name: c.name,
        mode: c.mode,
        isDefault: c.isDefault,
        tracks: c.tracks.map((t) => t.announcementId),
      }
    : { name: "", mode: "loop", isDefault: false, tracks: [] };

const KEYS = ["name", "mode", "isDefault", "announcementIds"];

function validate(d: Draft): Record<string, string> {
  const out: Record<string, string> = {};
  const name = d.name.trim();
  if (name === "" || name.length > 64) {
    out.name = "Use 1 to 64 characters.";
  }
  if (d.tracks.length > MAX_TRACKS) {
    out.announcementIds = `Use at most ${String(MAX_TRACKS)} tracks.`;
  }
  return out;
}

function ClassDrawer({
  cls,
  announcements,
  annReady,
  onClose,
  onSaved,
}: {
  cls: MOHClass | null;
  announcements: Announcement[];
  annReady: boolean;
  onClose: () => void;
  onSaved: (c: MOHClass, created: boolean) => void;
}) {
  useRestoreFocus();
  const [d, setD] = useState(() => draftOf(cls));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [pick, setPick] = useState("");
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));

  const nameOf = (annId: Id) =>
    announcements.find((a) => String(a.id) === String(annId))?.name ??
    cls?.tracks.find((t) => String(t.announcementId) === String(annId))?.name ??
    `#${String(annId)}`;

  function move(i: number, by: -1 | 1) {
    const next = [...d.tracks];
    const j = i + by;
    if (j < 0 || j >= next.length) return;
    const moved = next.splice(i, 1)[0];
    if (moved === undefined) return;
    next.splice(j, 0, moved);
    set("tracks", next);
  }

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const found = validate(d);
    setErrors(found);
    if (Object.keys(found).length > 0) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    try {
      const body: MOHClassFields = {
        name: d.name.trim(),
        mode: d.mode,
        isDefault: d.isDefault,
        announcementIds: d.tracks,
      };
      onSaved(
        cls ? await putMOHClass(cls.id, body) : await createMOHClass(body),
        cls === null,
      );
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
      title={cls ? `Class ${cls.name}` : "New class"}
      description="Tracks play in order and start over, or in a fresh random order each pass. A class with no tracks plays the built-in tune."
      onClose={busy ? undefined : onClose}
      width={520}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Can>
            <Button type="submit" form={FORM_ID} disabled={busy}>
              {cls ? "Save class" : "Create class"}
            </Button>
          </Can>
        </>
      }
    >
      <form
        id={FORM_ID}
        className="cf-form"
        aria-label={cls ? `Edit class ${cls.name}` : "New class"}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <Input
          id="moh-name"
          label="Name"
          autoFocus
          value={d.name}
          error={errors.name}
          onChange={(e) => set("name", e.target.value)}
        />
        <Select
          id="moh-mode"
          label="Play order"
          value={d.mode}
          error={errors.mode}
          options={[
            { value: "loop", label: MODE_LABEL.loop },
            { value: "shuffle", label: MODE_LABEL.shuffle },
          ]}
          onChange={(e) => set("mode", e.target.value as MOHMode)}
        />
        <Switch
          label="Default class"
          hint="Plays when no extension, ring group or route picks one. Replaces the current default."
          labelPosition="end"
          checked={d.isDefault}
          onChange={(e) => set("isDefault", e.target.checked)}
        />
        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">Tracks</legend>
          {errors.announcementIds && (
            <p className="cf-form__error">{errors.announcementIds}</p>
          )}
          {d.tracks.length === 0 && (
            <p className="cf-form__note">No tracks: the built-in tune plays.</p>
          )}
          <ol className="cf-rows">
            {d.tracks.map((annId, i) => (
              <li className="cf-row" key={`${String(annId)}-${String(i)}`}>
                <span className="cf-row__label cf-mono">{nameOf(annId)}</span>
                <div className="cf-row__actions">
                  <IconButton
                    icon="arrow-up"
                    label={`Move ${nameOf(annId)} up`}
                    disabled={i === 0}
                    onClick={() => move(i, -1)}
                  />
                  <IconButton
                    icon="arrow-down"
                    label={`Move ${nameOf(annId)} down`}
                    disabled={i === d.tracks.length - 1}
                    onClick={() => move(i, 1)}
                  />
                  <IconButton
                    icon="trash-2"
                    label={`Remove ${nameOf(annId)}`}
                    onClick={() =>
                      set(
                        "tracks",
                        d.tracks.filter((_, k) => k !== i),
                      )
                    }
                  />
                </div>
              </li>
            ))}
          </ol>
          <div className="cf-add">
            <Select
              id="moh-track-pick"
              label="Add a track"
              size="sm"
              value={pick}
              disabled={!annReady}
              options={[
                { value: "", label: annReady ? "Choose…" : "Loading…" },
                ...announcements.map((a) => ({
                  value: String(a.id),
                  label: a.name,
                })),
              ]}
              onChange={(e) => setPick(e.target.value)}
            />
            <Button
              variant="secondary"
              size="sm"
              icon="plus"
              disabled={pick === "" || d.tracks.length >= MAX_TRACKS}
              onClick={() => {
                const chosen = announcements.find((a) => String(a.id) === pick);
                if (!chosen) return;
                set("tracks", [...d.tracks, chosen.id]);
                setPick("");
              }}
            >
              Add
            </Button>
          </div>
          <p className="cf-form__note">
            Upload tracks on the Announcements page first. Each call hears them
            at its own quality: 8 kHz on G.711, 16 kHz on G.722 and 48 kHz on
            Opus, from the rate they were uploaded at.
          </p>
        </fieldset>
        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Drawer>
  );
}
