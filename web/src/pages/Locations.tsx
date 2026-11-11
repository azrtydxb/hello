import { useEffect, useMemo, useState } from "react";
import { errorMessage, listExtensions, listRingGroups } from "../api";
import {
  deleteLocation,
  getEmergencySettings,
  getPhoneGroupEmergency,
  listLocationAssignments,
  listLocations,
  putEmergencySettings,
  putLocationAssignment,
  putPhoneGroupEmergency,
  type Location,
  type LocationAssignment,
  type LocationKind,
} from "../api/location";
import { listGroups, type Group } from "../api/phonebook";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  IconButton,
  PageHeader,
  Select,
  Spinner,
  Switch,
  Tabs,
  useToast,
} from "../design/azrty/components";
import { LocationDrawer } from "../forms/LocationForm";
import { Can, useCan } from "../role";
import { useLoadedList, useQuietList } from "./timecond/shared";

type Tab = "places" | "assignments" | "emergency";

/** The names from the site down, as the API's place path writes them. */
export function pathOf(l: Location, all: readonly Location[]): string {
  const names = [l.name];
  const seen = new Set<number>([l.id]);
  for (let p = l; p.parentId !== null;) {
    const parentId: number = p.parentId;
    const next = all.find((x) => x.id === parentId);
    if (!next || seen.has(next.id)) break;
    seen.add(next.id);
    names.unshift(next.name);
    p = next;
  }
  return names.join(" / ");
}

/**
 * Locations: where phones are. A site carries the civic address, floors and
 * rooms narrow it. A location is assigned to phone groups, extensions and
 * single phones (the most specific wins); the HELD server answers a WP8x6
 * with it and an emergency call over a trunk that sends location carries it.
 */
export function Locations() {
  const [tab, setTab] = useState<Tab>("places");
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title="Locations"
        description="Where your phones are, for emergency calls. Phones that ask Hello for their location (WP8x6 with E911 on) and emergency calls through a trunk that sends location use the most specific one: the phone's own, else its extension's, else its phone group's."
      />
      <Tabs<Tab>
        aria-label="Locations: places, assignments and emergency"
        className="cf-tabs"
        idPrefix="loc"
        value={tab}
        onChange={setTab}
        items={[
          { id: "places", label: "Places" },
          { id: "assignments", label: "Assignments" },
          { id: "emergency", label: "Emergency" },
        ]}
      />
      <div
        role="tabpanel"
        id={`loc-panel-${tab}`}
        aria-labelledby={`loc-tab-${tab}`}
      >
        {tab === "places" && <Places />}
        {tab === "assignments" && <Assignments />}
        {tab === "emergency" && <Emergency />}
      </div>
    </section>
  );
}

// --- places ----------------------------------------------------------------

type Editing =
  | { kind: LocationKind; parent: Location | null; item: null }
  | { kind: LocationKind; parent: Location | null; item: Location };

function addressOf(l: Location): string {
  return [
    [l.street, l.houseNumber].filter((s) => s !== "").join(" "),
    l.postalCode,
    l.city,
    l.country,
  ]
    .filter((s) => s !== "")
    .join(", ");
}

function Places() {
  const [list, replace] = useLoadedList(listLocations);
  const [editing, setEditing] = useState<Editing | null>(null);
  const [deleting, setDeleting] = useState<Location | null>(null);
  const toast = useToast();
  const items = list.status === "ready" ? list.items : [];

  async function onDelete(l: Location) {
    try {
      await deleteLocation(l.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${l.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    replace((xs) => xs.filter((x) => x.id !== l.id));
    setDeleting(null);
    toast.show(`Location ${l.name} deleted.`);
  }

  const sites = items
    .filter((l) => l.kind === "site")
    .sort((a, b) => a.name.localeCompare(b.name));
  const childrenOf = (id: number) =>
    items
      .filter((l) => l.parentId === id)
      .sort((a, b) =>
        a.name.localeCompare(b.name, undefined, { numeric: true }),
      );

  const add = (
    <Can>
      <Button
        icon="plus"
        disabled={list.status !== "ready"}
        onClick={() => setEditing({ kind: "site", parent: null, item: null })}
      >
        New site
      </Button>
    </Can>
  );

  function row(l: Location, depth: number) {
    const kids = childrenOf(l.id);
    return (
      <li key={l.id}>
        <div
          className="cf-card__head"
          style={{ paddingLeft: depth * 20, alignItems: "center" }}
        >
          <div style={{ minWidth: 0 }}>
            <strong>{l.name}</strong> <Badge tone="pillar">{l.kind}</Badge>
            {l.latitude !== null && (
              <Badge tone="neutral">
                {`${String(l.latitude)}, ${String(l.longitude)}`}
              </Badge>
            )}
            {l.kind === "site" && (
              <div className="cf-card__sub">{addressOf(l)}</div>
            )}
          </div>
          <Can>
            <span style={{ display: "flex", gap: 4 }}>
              {l.kind === "site" && (
                <Button
                  variant="secondary"
                  size="sm"
                  aria-label={`Add a floor to ${l.name}`}
                  onClick={() =>
                    setEditing({ kind: "floor", parent: l, item: null })
                  }
                >
                  Add floor
                </Button>
              )}
              {l.kind !== "room" && (
                <Button
                  variant="secondary"
                  size="sm"
                  aria-label={`Add a room to ${l.name}`}
                  onClick={() =>
                    setEditing({ kind: "room", parent: l, item: null })
                  }
                >
                  Add room
                </Button>
              )}
              <IconButton
                icon="pencil"
                label={`Edit ${l.name}`}
                onClick={() =>
                  setEditing({
                    kind: l.kind,
                    parent: items.find((x) => x.id === l.parentId) ?? null,
                    item: l,
                  })
                }
              />
              <IconButton
                icon="trash-2"
                label={`Delete ${l.name}`}
                onClick={() => setDeleting(l)}
              />
            </span>
          </Can>
        </div>
        {kids.length > 0 && (
          <ul className="cf-plain" aria-label={`Inside ${l.name}`}>
            {kids.map((k) => row(k, depth + 1))}
          </ul>
        )}
      </li>
    );
  }

  return (
    <div className="cf-stack">
      <div>{add}</div>
      {list.status === "loading" && <Spinner label="Loading locations…" />}
      {list.status === "error" && (
        <Alert tone="bad" title="Could not load the locations">
          {list.message}
        </Alert>
      )}
      {list.status === "ready" && sites.length === 0 && (
        <EmptyState
          icon="map-pin"
          title="No locations yet"
          description="Add a site with its address, then floors and rooms inside it, and assign them to phone groups, extensions or phones."
        />
      )}
      {sites.length > 0 && (
        <ul className="az-card cf-plain" aria-label="Locations">
          {sites.map((s) => row(s, 0))}
        </ul>
      )}
      {editing && (
        <LocationDrawer
          key={editing.item ? String(editing.item.id) : editing.kind}
          location={editing.item}
          kind={editing.kind}
          parentId={editing.parent?.id ?? null}
          parentName={editing.parent ? pathOf(editing.parent, items) : ""}
          onClose={() => setEditing(null)}
          onSaved={(saved, created) => {
            replace((xs) =>
              created
                ? [...xs, saved]
                : xs.map((x) => (x.id === saved.id ? saved : x)),
            );
            setEditing(null);
            toast.show(
              created
                ? `Location ${saved.name} created.`
                : `Location ${saved.name} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete ${deleting.name}?`}
          description="A location with floors or rooms inside it, or one that a phone group, extension or phone is assigned to, cannot be deleted."
          confirmLabel="Delete location"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </div>
  );
}

// --- assignments -----------------------------------------------------------

const KIND_LABEL: Record<LocationAssignment["kind"], string> = {
  group: "Phone group",
  extension: "Extension",
  phone: "Phone",
};

function Assignments() {
  const [locs] = useLoadedList(listLocations);
  const [asg, replace] = useLoadedList(listLocationAssignments);
  const canWrite = useCan("operator");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState<string | null>(null);
  const toast = useToast();

  if (locs.status === "loading" || asg.status === "loading") {
    return <Spinner label="Loading assignments…" />;
  }
  if (locs.status === "error" || asg.status === "error") {
    return (
      <Alert tone="bad" title="Could not load the assignments">
        {locs.status === "error"
          ? locs.message
          : asg.status === "error"
            ? asg.message
            : ""}
      </Alert>
    );
  }
  const options = [
    { value: "", label: "None" },
    ...locs.items
      .map((l) => ({ value: String(l.id), label: pathOf(l, locs.items) }))
      .sort((a, b) =>
        a.label.localeCompare(b.label, undefined, { numeric: true }),
      ),
  ];

  async function change(a: LocationAssignment, value: string) {
    const key = `${a.kind}/${String(a.id)}`;
    const locationId = value === "" ? null : Number(value);
    setErrors((e) => ({ ...e, [key]: "" }));
    setBusy(key);
    try {
      await putLocationAssignment(a.kind, a.id, locationId);
      replace((xs) =>
        xs.map((x) =>
          x.kind === a.kind && x.id === a.id ? { ...x, locationId } : x,
        ),
      );
      toast.show(`${KIND_LABEL[a.kind]} ${a.name} saved.`);
    } catch (err: unknown) {
      setErrors((e) => ({ ...e, [key]: errorMessage(err) }));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="cf-stack">
      <p className="cf-card__sub">
        A phone&rsquo;s own location wins over its extension&rsquo;s, which wins
        over its phone group&rsquo;s.
      </p>
      <div className="az-table-wrap">
        <table className="az-table" aria-label="Location assignments">
          <thead>
            <tr>
              <th scope="col">Type</th>
              <th scope="col">Name</th>
              <th scope="col">Location</th>
            </tr>
          </thead>
          <tbody>
            {asg.items.map((a) => {
              const key = `${a.kind}/${String(a.id)}`;
              return (
                <tr key={key}>
                  <td>{KIND_LABEL[a.kind]}</td>
                  <th scope="row">{a.name}</th>
                  <td>
                    <Select
                      id={`assign-${a.kind}-${String(a.id)}`}
                      aria-label={`Location of ${KIND_LABEL[a.kind].toLowerCase()} ${a.name}`}
                      value={a.locationId === null ? "" : String(a.locationId)}
                      options={options}
                      disabled={!canWrite || busy === key}
                      error={errors[key] || undefined}
                      onChange={(e) => void change(a, e.target.value)}
                    />
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
      {toast.node}
    </div>
  );
}

// --- emergency -------------------------------------------------------------

function Emergency() {
  const groups = useQuietList(listGroups);
  const ringGroups = useQuietList(listRingGroups);
  const extensions = useQuietList(listExtensions);
  const [settings, setSettings] = useState<
    | { status: "loading" }
    | { status: "error"; message: string }
    | { status: "ready"; notifyGroup: string }
  >({ status: "loading" });
  const [draft, setDraft] = useState("");
  const [error, setError] = useState("");
  const canAdmin = useCan("admin");
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    getEmergencySettings(controller.signal)
      .then((s) => {
        setSettings({ status: "ready", notifyGroup: s.notifyGroup });
        setDraft(s.notifyGroup);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setSettings({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  async function saveNotify() {
    setError("");
    try {
      const saved = await putEmergencySettings({ notifyGroup: draft });
      setSettings({ status: "ready", notifyGroup: saved.notifyGroup });
      toast.show("Emergency settings saved.");
    } catch (err: unknown) {
      setError(errorMessage(err));
    }
  }

  if (settings.status === "loading") return <Spinner label="Loading…" />;
  if (settings.status === "error") {
    return (
      <Alert tone="bad" title="Could not load the emergency settings">
        {settings.message}
      </Alert>
    );
  }
  const targets = [
    { value: "", label: "None" },
    ...extensions.map((e) => ({
      value: e.number,
      label: `Extension ${e.number}${e.name ? ` (${e.name})` : ""}`,
    })),
    ...ringGroups.map((g) => ({
      value: g.name,
      label: `Ring group ${g.name}`,
    })),
  ];
  return (
    <div className="cf-stack">
      <div className="az-card cf-card">
        <h2 className="cf-card__group-name">Told of every emergency call</h2>
        <p className="cf-card__sub">
          The phones of this ring group are paged, one way, with a spoken notice
          naming the extension that placed an emergency call. A call back to
          that extension within 30 minutes rings with the tag emergency-callback
          through do-not-disturb.
        </p>
        <Select
          id="emergency-notify"
          label="Ring group"
          value={draft}
          error={error || undefined}
          disabled={!canAdmin}
          options={[
            { value: "", label: "None" },
            ...ringGroups.map((g) => ({ value: g.name, label: g.name })),
          ]}
          onChange={(e) => setDraft(e.target.value)}
        />
        <Can min="admin">
          <div>
            <Button
              disabled={draft === settings.notifyGroup}
              onClick={() => void saveNotify()}
            >
              Save emergency settings
            </Button>
          </div>
        </Can>
      </div>
      <div className="az-card cf-card">
        <h2 className="cf-card__group-name">Phone groups</h2>
        <p className="cf-card__sub">
          With E911 on, the WP8x6 phones of a group ask Hello for their location
          (HELD). With an alarm destination, a WP8x6 is provisioned to dial *55
          for its alarm and *55 from any phone of the group rings the
          destination through do-not-disturb with the ring tag alarm; without
          one *55 is answered 404. Send the location to the carrier by turning
          on the geolocation switch of the trunk.
        </p>
        {groups.length === 0 && (
          <p className="cf-card__sub">There are no phone groups yet.</p>
        )}
        {groups.map((g) => (
          <GroupEmergency
            key={g.id}
            group={g}
            targets={targets}
            onSaved={(name) => toast.show(`Phone group ${name} saved.`)}
          />
        ))}
      </div>
      {toast.node}
    </div>
  );
}

function GroupEmergency({
  group,
  targets,
  onSaved,
}: {
  group: Group;
  targets: { value: string; label: string }[];
  onSaved: (name: string) => void;
}) {
  const canAdmin = useCan("admin");
  const [state, setState] = useState<
    | { status: "loading" }
    | { status: "error"; message: string }
    | { status: "ready"; e911: boolean; alarm: string }
  >({ status: "loading" });
  const [e911, setE911] = useState(false);
  const [alarm, setAlarm] = useState("");
  const [error, setError] = useState("");
  const id = Number(group.id);

  useEffect(() => {
    const controller = new AbortController();
    getPhoneGroupEmergency(id, controller.signal)
      .then((s) => {
        setState({
          status: "ready",
          e911: s.e911Enabled,
          alarm: s.alarmTarget,
        });
        setE911(s.e911Enabled);
        setAlarm(s.alarmTarget);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [id]);

  const dirty = useMemo(
    () =>
      state.status === "ready" &&
      (state.e911 !== e911 || state.alarm !== alarm),
    [state, e911, alarm],
  );

  async function save() {
    setError("");
    try {
      const s = await putPhoneGroupEmergency(id, {
        e911Enabled: e911,
        alarmTarget: alarm,
      });
      setState({ status: "ready", e911: s.e911Enabled, alarm: s.alarmTarget });
      onSaved(group.name);
    } catch (err: unknown) {
      setError(errorMessage(err));
    }
  }

  return (
    <fieldset className="cf-form__section">
      <legend className="az-eyebrow">{group.name}</legend>
      {state.status === "loading" && (
        <Spinner label={`Loading ${group.name}…`} />
      )}
      {state.status === "error" && (
        <Alert tone="bad" title={`Could not load ${group.name}`}>
          {state.message}
        </Alert>
      )}
      {state.status === "ready" && (
        <>
          <Switch
            label={`E911 and HELD for ${group.name}`}
            labelPosition="end"
            checked={e911}
            disabled={!canAdmin}
            onChange={(e) => setE911(e.target.checked)}
          />
          <Select
            id={`alarm-${String(group.id)}`}
            label={`Alarm destination of ${group.name}`}
            value={alarm}
            options={targets}
            error={error || undefined}
            disabled={!canAdmin}
            onChange={(e) => setAlarm(e.target.value)}
          />
          <Can min="admin">
            <div>
              <Button
                disabled={!dirty}
                aria-label={`Save ${group.name}`}
                onClick={() => void save()}
              >
                Save
              </Button>
            </div>
          </Can>
        </>
      )}
    </fieldset>
  );
}
