import { useEffect, useMemo, useState } from "react";
import { errorMessage, fieldErrors, type FieldError } from "../../api";
import {
  getRingMap,
  listRingTags,
  putRingMap,
  putRingMapGroup,
  type RingMapChoices,
  type RingMapEntry,
  type RingMapView,
  type RingTag,
  type RingVendor,
} from "../../api/ringtag";
import { listGroups } from "../../api/phonebook";
import {
  Alert,
  Button,
  Select,
  Spinner,
  useToast,
} from "../../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../../forms";
import { Can, useCan } from "../../role";
import { FormAlert } from "../callflow/ui";
import { useLoadedList, useQuietList } from "../timecond/shared";

/** The scope the map edits: every phone, or one phone group's overrides. */
const GLOBAL = "global";

const cellKey = (tag: string, vendor: string) => `${vendor}/${tag}`;

/**
 * The ring map: the tone each vendor's phones play for a ring tag. A cell
 * left on its default plays the vendor's ringer for the tag's Bellcore slot;
 * a choice on a phone group overrides the global one for that group's phones.
 * Saving reaches the affected phones through the automatic resync.
 */
export function RingMap() {
  const [map, setMap] = useState<
    | { status: "loading" }
    | { status: "error"; message: string }
    | { status: "ready"; view: RingMapView }
  >({ status: "loading" });
  const [tags] = useLoadedList(listRingTags);
  useEffect(() => {
    const controller = new AbortController();
    getRingMap(controller.signal)
      .then((view) => setMap({ status: "ready", view }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setMap({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);
  if (map.status === "loading" || tags.status === "loading") {
    return <Spinner label="Loading the ring map…" />;
  }
  if (map.status === "error" || tags.status === "error") {
    return (
      <Alert tone="bad" title="Could not load the ring map">
        {map.status === "error"
          ? map.message
          : tags.status === "error"
            ? tags.message
            : ""}
      </Alert>
    );
  }
  return (
    <MapEditor
      view={map.view}
      tags={tags.items}
      onSaved={(c: RingMapChoices) =>
        setMap({ status: "ready", view: { ...map.view, ...c } })
      }
    />
  );
}

function MapEditor({
  view,
  tags,
  onSaved,
}: {
  view: RingMapView;
  tags: RingTag[];
  onSaved: (c: RingMapChoices) => void;
}) {
  const groups = useQuietList(listGroups);
  const canWrite = useCan("operator");
  const [scope, setScope] = useState(GLOBAL);
  // The draft of the scope being edited: cell key to tone, "" for default.
  const [draft, setDraft] = useState<Record<string, string>>(() =>
    toDraft(view.global),
  );
  const [dirty, setDirty] = useState(false);
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const toast = useToast();

  const defaults = useMemo(() => {
    const m = new Map<string, string>();
    for (const d of view.defaults) m.set(cellKey(d.tag, d.vendor), d.tone);
    return m;
  }, [view.defaults]);
  const globals = useMemo(() => toDraft(view.global), [view.global]);

  const toneLabel = (v: RingVendor, id: string | undefined) =>
    v.tones.find((t) => t.id === id)?.label ?? id ?? "";
  // What a cell plays when left unchosen: the global choice (in a group), else
  // the vendor's default for the tag.
  const inherited = (v: RingVendor, tag: string) =>
    (scope !== GLOBAL ? globals[cellKey(tag, v.vendor)] : undefined) ??
    defaults.get(cellKey(tag, v.vendor));

  function pickScope(next: string) {
    const entries =
      next === GLOBAL
        ? view.global
        : (view.groups.find((g) => String(g.groupId) === next)?.entries ?? []);
    setScope(next);
    setDraft(toDraft(entries));
    setDirty(false);
    setErrors({});
    setUnmatched([]);
    setFormError(null);
  }

  async function onSave() {
    setFormError(null);
    setUnmatched([]);
    setErrors({});
    const entries: RingMapEntry[] = [];
    const keys: string[] = [];
    for (const v of view.vendors) {
      for (const t of tags) {
        const tone = draft[cellKey(t.name, v.vendor)];
        if (tone) {
          entries.push({ tag: t.name, vendor: v.vendor, tone });
          keys.push(cellKey(t.name, v.vendor));
        }
      }
    }
    setBusy(true);
    try {
      const saved =
        scope === GLOBAL
          ? await putRingMap(entries)
          : await putRingMapGroup(scope, entries);
      onSaved(saved);
      setDirty(false);
      toast.show(
        scope === GLOBAL
          ? "Ring map saved. Phones pick it up at their next resync."
          : "Group ring map saved. Phones pick it up at their next resync.",
      );
    } catch (err: unknown) {
      // entries[i].tone is reported at the cell the entry came from.
      const byPath: FieldError[] = fieldErrors(err).map((f) => {
        const m = /^entries\[(\d+)\](\..*)?$/.exec(f.path);
        return m ? { ...f, path: `cell:${keys[Number(m[1])] ?? ""}` } : f;
      });
      const mapped = mapFieldErrors(
        byPath,
        keys.map((k) => `cell:${k}`),
      );
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  // Tags that share a Bellcore slot ring the same on a phone that maps only
  // the five slots (Grandstream WP8x6).
  const shared = useMemo(() => {
    const bySlot = new Map<number, string[]>();
    for (const t of tags) {
      bySlot.set(t.bellcoreSlot, [
        ...(bySlot.get(t.bellcoreSlot) ?? []),
        t.name,
      ]);
    }
    return [...bySlot.values()].filter((names) => names.length > 1);
  }, [tags]);

  return (
    <div className="cf-stack">
      <Select
        id="ring-map-scope"
        label="Applies to"
        value={scope}
        options={[
          { value: GLOBAL, label: "All phones" },
          ...groups.map((g) => ({
            value: String(g.id),
            label: `Phone group ${g.name}`,
          })),
        ]}
        hint={
          scope === GLOBAL
            ? "The default for every phone."
            : "A choice here replaces the all-phones choice for the phones of this group; a cell left on default follows it."
        }
        onChange={(e) => pickScope(e.target.value)}
      />
      <div className="az-table-wrap">
        <table className="az-table" aria-label="Ring map">
          <thead>
            <tr>
              <th scope="col">Ring tag</th>
              {view.vendors.map((v) => (
                <th scope="col" key={v.vendor}>
                  {v.label}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {tags.map((t) => (
              <tr key={t.name}>
                <th scope="row">
                  {t.name}
                  <div className="cf-card__sub">
                    Bellcore-dr{t.bellcoreSlot}
                  </div>
                </th>
                {view.vendors.map((v) => {
                  const key = cellKey(t.name, v.vendor);
                  const inh = inherited(v, t.name);
                  return (
                    <td key={v.vendor}>
                      <Select
                        id={`ring-map-${v.vendor}-${t.name}`}
                        aria-label={`${v.label} tone for ${t.name}`}
                        value={draft[key] ?? ""}
                        disabled={!canWrite || busy}
                        error={errors[`cell:${key}`]}
                        options={[
                          {
                            value: "",
                            label: `${scope === GLOBAL ? "Default" : "Same as all phones"}: ${toneLabel(v, inh)}`,
                          },
                          ...v.tones.map((tn) => ({
                            value: tn.id,
                            label: tn.label,
                          })),
                        ]}
                        onChange={(e) => {
                          setDraft({ ...draft, [key]: e.target.value });
                          setDirty(true);
                        }}
                      />
                    </td>
                  );
                })}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <ul className="cf-plain cf-card__sub" aria-label="Ring map notes">
        {view.vendors.map((v) => (
          <li key={v.vendor}>
            {v.label}:{" "}
            {v.slots > 0
              ? `the first ${v.slots} tags in the list are written to the phone.`
              : "no tone is written to the phone."}
            {v.vendor === "grandstream" &&
              " A WP8x6 maps the five Bellcore slots; other Grandstream models may have no ring map key."}
          </li>
        ))}
        {shared.length > 0 && (
          <li>
            Tags with the same Bellcore slot ring the same on a phone that maps
            only the five slots (Grandstream WP8x6):{" "}
            {shared.map((n) => n.join(", ")).join("; ")}.
          </li>
        )}
      </ul>
      <FormAlert message={formError} unmatched={unmatched} />
      <Can>
        <div>
          <Button disabled={busy || !dirty} onClick={() => void onSave()}>
            Save ring map
          </Button>
        </div>
      </Can>
      {toast.node}
    </div>
  );
}

function toDraft(entries: RingMapEntry[]): Record<string, string> {
  const out: Record<string, string> = {};
  for (const e of entries) out[cellKey(e.tag, e.vendor)] = e.tone;
  return out;
}
