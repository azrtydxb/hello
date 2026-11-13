import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { errorMessage, fieldErrors, type Id } from "../api";
import { listGroups } from "../api/phonebook";
import { listPhones } from "../api/prov";
import { listDot1XProfiles, type Dot1XProfile } from "../api/scep";
import {
  createWifiProfile,
  deleteWifiProfile,
  getGroupWifi,
  getPhoneWifi,
  isEnterprise,
  listWifiProfiles,
  putGroupWifi,
  putPhoneWifi,
  updateWifiProfile,
  usesPSK,
  WIFI_SECURITIES,
  type WifiProfile,
  type WifiProfileInput,
  type WifiRadio,
  type WifiScope,
  type WifiSecurity,
} from "../api/wifi";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
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
import { useQuietList } from "./timecond/shared";
import { PhonesTabs } from "./phones/ui";

const securityLabel = (s: string) =>
  WIFI_SECURITIES.find((x) => x.value === s)?.label ?? s;

/**
 * Wi-Fi: the networks Wi-Fi handsets join, which phone groups and phones get
 * them, and the roaming settings of a group. A key or password is typed once
 * and never shown again. Nothing is written to a phone until one of its
 * profiles is marked verified on a test phone.
 */
export function Wifi() {
  const [profiles, setProfiles] = useState<
    | { status: "loading" }
    | { status: "error"; message: string }
    | { status: "ready"; items: WifiProfile[] }
  >({ status: "loading" });
  const reload = () =>
    listWifiProfiles()
      .then((items) => setProfiles({ status: "ready", items }))
      .catch((err: unknown) =>
        setProfiles({ status: "error", message: errorMessage(err) }),
      );
  useEffect(() => {
    const controller = new AbortController();
    listWifiProfiles(controller.signal)
      .then((items) => setProfiles({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setProfiles({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title="Wi-Fi"
        description="Networks for Grandstream Wi-Fi handsets and their roaming settings."
      />
      <PhonesTabs />
      <div className="prov-stack">
        {profiles.status === "loading" && (
          <Spinner label="Loading Wi-Fi profiles…" />
        )}
        {profiles.status === "error" && (
          <Alert tone="bad" title="Could not load the Wi-Fi profiles">
            {profiles.message}
          </Alert>
        )}
        {profiles.status === "ready" && (
          <>
            <Profiles items={profiles.items} onChanged={() => void reload()} />
            <Assignment
              profiles={profiles.items}
              onChanged={() => void reload()}
            />
          </>
        )}
      </div>
    </section>
  );
}

function Profiles({
  items,
  onChanged,
}: {
  items: WifiProfile[];
  onChanged: () => void;
}) {
  const [editing, setEditing] = useState<WifiProfile | "new" | null>(null);
  const [deleting, setDeleting] = useState<WifiProfile | null>(null);
  const toast = useToast();

  const columns: TableColumn<WifiProfile>[] = [
    { key: "name", label: "Name" },
    { key: "ssid", label: "SSID", mono: true },
    {
      key: "security",
      label: "Security",
      render: (p) => securityLabel(p.security),
    },
    { key: "priority", label: "Priority", align: "right", mono: true },
    {
      key: "verified",
      label: "Verified",
      render: (p) =>
        p.verified ? (
          <Badge tone="good">Verified</Badge>
        ) : (
          <Badge tone="warn">Not verified</Badge>
        ),
    },
    {
      key: "usedBy",
      label: "Used by",
      render: (p) => `${p.groups} groups, ${p.phones} phones`,
    },
    {
      key: "actions",
      label: <span className="visually-hidden">Actions</span>,
      align: "right",
      render: (p) => (
        <Can min="admin">
          <div className="prov-row">
            <IconButton
              icon="pencil"
              label={`Edit ${p.name}`}
              onClick={() => setEditing(p)}
            />
            <IconButton
              icon="trash-2"
              label={`Delete ${p.name}`}
              onClick={() => setDeleting(p)}
            />
          </div>
        </Can>
      ),
    },
  ];

  return (
    <div className="prov-stack">
      <div className="prov-row">
        <h2 className="az-h3">Profiles</h2>
        <Can min="admin">
          <Button onClick={() => setEditing("new")}>Add profile</Button>
        </Can>
      </div>
      {items.length === 0 ? (
        <EmptyState
          icon="wifi"
          title="No Wi-Fi profiles"
          description="Add a network, assign it to a test phone, check that the phone joins it, then mark it verified."
        />
      ) : (
        <Table
          caption="Wi-Fi profiles"
          columns={columns}
          rows={items}
          rowKey={(p) => String(p.id)}
        />
      )}
      {editing && (
        <ProfileModal
          profile={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={(created) => {
            setEditing(null);
            onChanged();
            toast.show(created ? "Profile created." : "Profile saved.");
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete ${deleting.name}?`}
          description="A profile that a phone group or phone uses cannot be deleted; remove it from them first."
          confirmLabel="Delete profile"
          onConfirm={async () => {
            await deleteWifiProfile(deleting.id);
            setDeleting(null);
            onChanged();
            toast.show("Profile deleted.");
          }}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </div>
  );
}

const PROFILE_FIELDS = [
  "name",
  "ssid",
  "security",
  "psk",
  "dot1xProfileId",
  "priority",
  "preferAccount",
];

function ProfileModal({
  profile,
  onClose,
  onSaved,
}: {
  profile: WifiProfile | null;
  onClose: () => void;
  onSaved: (created: boolean) => void;
}) {
  const [name, setName] = useState(profile?.name ?? "");
  const [ssid, setSsid] = useState(profile?.ssid ?? "");
  const [security, setSecurity] = useState<WifiSecurity>(
    profile?.security ?? "wpa2-psk",
  );
  // A stored secret is never shown; empty keeps it.
  const [psk, setPsk] = useState("");
  // The 802.1X profiles (shared with the wired ports) an enterprise network can use.
  const [dot1x, setDot1x] = useState<Dot1XProfile[] | null>(null);
  const [dot1xId, setDot1xId] = useState(
    profile?.dot1xProfileId == null ? "" : String(profile.dot1xProfileId),
  );
  const [priority, setPriority] = useState(String(profile?.priority ?? 0));
  const [prefer, setPrefer] = useState(String(profile?.preferAccount ?? 0));
  const [hidden, setHidden] = useState(profile?.hidden ?? false);
  const [verified, setVerified] = useState(profile?.verified ?? false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!isEnterprise(security) || dot1x !== null) return;
    const ctl = new AbortController();
    listDot1XProfiles(ctl.signal)
      .then((r) => setDot1x(r))
      .catch((err) => {
        if (!ctl.signal.aborted) setFormError(errorMessage(err));
      });
    return () => ctl.abort();
  }, [security, dot1x]);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setErrors({});
    const body: WifiProfileInput = {
      name: name.trim(),
      ssid,
      security,
      priority: Number(priority),
      preferAccount: Number(prefer),
      hidden,
      verified,
    };
    if (usesPSK(security) && psk !== "") body.psk = psk;
    if (isEnterprise(security) && dot1xId !== "") {
      body.dot1xProfileId = Number(dot1xId);
    }
    setBusy(true);
    try {
      if (profile) await updateWifiProfile(profile.id, body);
      else await createWifiProfile(body);
      onSaved(profile === null);
    } catch (err) {
      const { byKey, unmatched } = mapFieldErrors(
        fieldErrors(err),
        PROFILE_FIELDS,
      );
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

  const title = profile ? `Edit ${profile.name}` : "New Wi-Fi profile";
  const keyStored = profile?.pskSet && usesPSK(profile.security);
  const dot1xOptions = [
    { value: "", label: "Choose an 802.1X profile" },
    ...(dot1x ?? [])
      .filter((d) => d.method !== "eap-md5")
      .map((d) => ({
        value: String(d.id),
        label: `${d.name} (${d.method === "peap" ? "PEAP" : "EAP-TLS"})`,
      })),
  ];
  return (
    <Modal
      title={title}
      onClose={onClose}
      width={560}
      actions={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="wifi-profile-form" disabled={busy}>
            {profile ? "Save" : "Create profile"}
          </Button>
        </>
      }
    >
      <form
        id="wifi-profile-form"
        className="prov-stack"
        aria-label={title}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        {formError && <Alert tone="bad">{formError}</Alert>}
        <Input
          label="Name"
          value={name}
          error={errors.name}
          onChange={(e) => setName(e.target.value)}
        />
        <Input
          label="SSID"
          value={ssid}
          error={errors.ssid}
          hint="The network name, at most 32 bytes."
          onChange={(e) => setSsid(e.target.value)}
        />
        <Select
          label="Security"
          value={security}
          error={errors.security}
          options={WIFI_SECURITIES.map((s) => ({
            value: s.value,
            label: s.label,
          }))}
          hint="A WP810/822/825 takes open, WPA2 and WPA3 personal; a WP856 open and WPA2 personal; 802.1X and OWE are WP8x6 only."
          onChange={(e) => setSecurity(e.target.value as WifiSecurity)}
        />
        {usesPSK(security) && (
          <Input
            label="Pre-shared key"
            type="password"
            autoComplete="new-password"
            value={psk}
            error={errors.psk}
            placeholder={keyStored ? "•••••••• (stored)" : ""}
            hint={
              keyStored
                ? "A key is stored and never shown. Leave empty to keep it."
                : "8 to 63 characters. It is stored sealed and never shown again."
            }
            onChange={(e) => setPsk(e.target.value)}
          />
        )}
        {isEnterprise(security) && (
          <Select
            label="802.1X profile"
            value={dot1xId}
            error={errors.dot1xProfileId}
            options={dot1xOptions}
            hint="The EAP method, identity, password and server certificate come from this profile, the same one a wired port can use. Add one under Certificates; EAP-MD5 is wired only."
            onChange={(e) => setDot1xId(e.target.value)}
          />
        )}
        <Input
          label="Priority"
          type="number"
          min={0}
          max={10}
          value={priority}
          error={errors.priority}
          hint="0 to 10; a higher number is listed first. Written to a WP8x6; the other models take the order."
          onChange={(e) => setPriority(e.target.value)}
        />
        <Input
          label="Preferred account"
          type="number"
          min={0}
          max={3}
          value={prefer}
          error={errors.preferAccount}
          hint="0 is automatic. Written to a WP8x6 only."
          onChange={(e) => setPrefer(e.target.value)}
        />
        <Switch
          label="Hidden network"
          hint="Written to a WP810/822/825; a WP8x6 and WP856 have no key for it."
          checked={hidden}
          onChange={(e) => setHidden(e.target.checked)}
        />
        <Switch
          label="Verified on a test phone"
          hint="Nothing is written to any phone until one of its profiles is verified."
          checked={verified}
          onChange={(e) => setVerified(e.target.checked)}
        />
      </form>
    </Modal>
  );
}

const GROUP = "g";
const PHONE = "p";

const ROAM_MODES = [
  { value: "", label: "Phone default" },
  { value: "default", label: "Default" },
  { value: "boost", label: "Boost" },
  { value: "fast", label: "Fast roaming (WP856)" },
  { value: "custom", label: "Custom (WP856)" },
];
const BANDS = [
  { value: "", label: "Phone default" },
  { value: "both", label: "2.4 GHz and 5 GHz" },
  { value: "2.4", label: "2.4 GHz" },
  { value: "5", label: "5 GHz" },
];
const TRI = [
  { value: "", label: "Phone default" },
  { value: "on", label: "On" },
  { value: "off", label: "Off" },
];

type RadioDraft = {
  band: string;
  roamMode: string;
  threshold: string;
  poorScan: string;
  goodScan: string;
  gain: string;
  dot11r: string;
  dot11v: string;
  prompt: string;
};

const tri = (v: boolean | undefined) =>
  v === undefined ? "" : v ? "on" : "off";
const str = (v: number | string | undefined) =>
  v === undefined ? "" : String(v);

function toDraft(r: WifiRadio | undefined): RadioDraft {
  return {
    band: r?.band ?? "",
    roamMode: r?.roamMode ?? "",
    threshold: str(r?.threshold),
    poorScan: str(r?.poorScan),
    goodScan: str(r?.goodScan),
    gain: str(r?.gain),
    dot11r: tri(r?.dot11r),
    dot11v: tri(r?.dot11v),
    prompt: tri(r?.prompt),
  };
}

function fromDraft(d: RadioDraft): WifiRadio {
  const r: WifiRadio = {};
  if (d.band) r.band = d.band as WifiRadio["band"];
  if (d.roamMode) r.roamMode = d.roamMode as WifiRadio["roamMode"];
  if (d.threshold !== "") r.threshold = Number(d.threshold);
  if (d.poorScan !== "") r.poorScan = Number(d.poorScan);
  if (d.goodScan !== "") r.goodScan = Number(d.goodScan);
  if (d.gain !== "") r.gain = Number(d.gain);
  if (d.dot11r) r.dot11r = d.dot11r === "on";
  if (d.dot11v) r.dot11v = d.dot11v === "on";
  if (d.prompt) r.prompt = d.prompt === "on";
  return r;
}

/** Which profiles a phone group or a phone gets, and a group's roaming settings. */
function Assignment({
  profiles,
  onChanged,
}: {
  profiles: WifiProfile[];
  onChanged: () => void;
}) {
  const groups = useQuietList(listGroups);
  const [phones, setPhones] = useState<
    { id: Id; mac: string; label: string }[]
  >([]);
  useEffect(() => {
    const controller = new AbortController();
    listPhones(controller.signal)
      .then(setPhones)
      .catch(() => undefined);
    return () => controller.abort();
  }, []);
  const canWrite = useCan("admin");
  const [target, setTarget] = useState("");
  const [scope, setScope] = useState<WifiScope | null>(null);
  const [selected, setSelected] = useState<Id[]>([]);
  const [radio, setRadio] = useState<RadioDraft>(toDraft(undefined));
  const [errors, setErrors] = useState<ErrorMap>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  const kind = target.slice(0, 1);
  const id = target.slice(2);

  function apply(s: WifiScope) {
    setScope(s);
    setSelected(s.profileIds);
    setRadio(toDraft(s.radio));
  }

  // The latest choice wins: an answer for an earlier choice is dropped.
  const latest = useRef(0);
  function pickTarget(next: string) {
    const n = ++latest.current;
    setTarget(next);
    setScope(null);
    setLoadError(null);
    setFormError(null);
    setErrors({});
    if (!next) return;
    const nextId = next.slice(2);
    (next.startsWith(GROUP) ? getGroupWifi(nextId) : getPhoneWifi(nextId))
      .then((s) => {
        if (latest.current === n) apply(s);
      })
      .catch((err: unknown) => {
        if (latest.current === n) setLoadError(errorMessage(err));
      });
  }

  const sameId = (a: Id, b: Id) => String(a) === String(b);
  const nameOf = useMemo(
    () => (pid: Id) =>
      profiles.find((p) => sameId(p.id, pid))?.name ?? `#${String(pid)}`,
    [profiles],
  );

  async function onSave() {
    setFormError(null);
    setErrors({});
    setBusy(true);
    try {
      const saved =
        kind === GROUP
          ? await putGroupWifi(id, selected, fromDraft(radio))
          : await putPhoneWifi(id, selected);
      apply(saved);
      onChanged();
      toast.show("Saved. Phones pick it up at their next resync.");
    } catch (err) {
      const { byKey, unmatched } = mapFieldErrors(fieldErrors(err), [
        "profileIds",
        "radio.band",
        "radio.roamMode",
        "radio.threshold",
        "radio.poorScan",
        "radio.goodScan",
        "radio.gain",
      ]);
      setErrors(byKey);
      if (Object.keys(byKey).length === 0 || unmatched.length > 0) {
        setFormError(
          unmatched.length > 0
            ? unmatched.map((f) => `${f.path}: ${f.message}`).join(" ")
            : errorMessage(err),
        );
      }
    } finally {
      setBusy(false);
    }
  }

  const setR = (k: keyof RadioDraft) => (v: string) =>
    setRadio((r) => ({ ...r, [k]: v }));

  return (
    <div className="prov-stack">
      <h2 className="az-h3">Assignment and roaming</h2>
      <Select
        id="wifi-target"
        label="Applies to"
        value={target}
        options={[
          { value: "", label: "Choose a phone group or phone" },
          ...groups.map((g) => ({
            value: `${GROUP}:${String(g.id)}`,
            label: `Phone group ${g.name}`,
          })),
          ...phones.map((p) => ({
            value: `${PHONE}:${String(p.id)}`,
            label: `Phone ${p.label || p.mac}`,
          })),
        ]}
        hint="A phone's profiles come after its group's; one with the same SSID replaces the group's."
        onChange={(e) => pickTarget(e.target.value)}
      />
      {loadError && (
        <Alert tone="bad" title="Could not load the assignment">
          {loadError}
        </Alert>
      )}
      {target && !scope && !loadError && <Spinner label="Loading…" />}
      {scope && (
        <>
          {scope.pushing ? (
            <Alert tone="good" title="Pushing">
              A verified profile is among the profiles, so the networks are
              written to the phones.
            </Alert>
          ) : (
            <Alert tone="warn" title="Not pushing">
              No profile of this {kind === GROUP ? "group" : "phone"} is
              verified, so no network is written. Verify one on a test phone
              first.
            </Alert>
          )}
          {kind === PHONE && scope.inheritedProfileIds.length > 0 && (
            <p className="prov-muted">
              From its group: {scope.inheritedProfileIds.map(nameOf).join(", ")}
            </p>
          )}
          <fieldset className="prov-stack" aria-label="Profiles">
            <legend className="az-field__label">Profiles (at most 10)</legend>
            {profiles.length === 0 && (
              <p className="prov-muted">Add a profile first.</p>
            )}
            {profiles.map((p) => (
              <Checkbox
                key={String(p.id)}
                label={`${p.name} (${p.ssid})`}
                hint={p.verified ? "Verified" : "Not verified"}
                checked={selected.some((s) => sameId(s, p.id))}
                disabled={!canWrite || busy}
                onChange={(e) =>
                  setSelected((cur) =>
                    e.target.checked
                      ? [...cur, p.id]
                      : cur.filter((s) => !sameId(s, p.id)),
                  )
                }
              />
            ))}
            {errors.profileIds && (
              <span role="alert" className="az-field__error">
                {errors.profileIds}
              </span>
            )}
          </fieldset>
          {kind === GROUP && (
            <fieldset className="prov-stack" aria-label="Roaming and radio">
              <legend className="az-field__label">Roaming and radio</legend>
              <p className="prov-muted">
                A setting left on Phone default is not written. Each model takes
                only the keys its template has: a WP8x6 band, roaming mode
                (default, boost), threshold, poor scan and prompt; a
                WP810/822/825 threshold, poor scan, 802.11r and 802.11v; a WP856
                everything except the prompt. 802.11k has no phone key and is an
                access point setting.
              </p>
              <Select
                label="Band"
                value={radio.band}
                options={BANDS}
                error={errors["radio.band"]}
                disabled={!canWrite}
                onChange={(e) => setR("band")(e.target.value)}
              />
              <Select
                label="Roaming mode"
                value={radio.roamMode}
                options={ROAM_MODES}
                error={errors["radio.roamMode"]}
                disabled={!canWrite}
                onChange={(e) => setR("roamMode")(e.target.value)}
              />
              <Input
                label="Roaming threshold (dBm)"
                type="number"
                value={radio.threshold}
                placeholder="-100 to -30"
                error={errors["radio.threshold"]}
                disabled={!canWrite}
                onChange={(e) => setR("threshold")(e.target.value)}
              />
              <Input
                label="Poor signal scan interval (s)"
                type="number"
                value={radio.poorScan}
                placeholder="5 to 300"
                error={errors["radio.poorScan"]}
                disabled={!canWrite}
                onChange={(e) => setR("poorScan")(e.target.value)}
              />
              <Input
                label="Good signal scan interval (s)"
                type="number"
                value={radio.goodScan}
                placeholder="5 to 600 (WP856)"
                error={errors["radio.goodScan"]}
                disabled={!canWrite}
                onChange={(e) => setR("goodScan")(e.target.value)}
              />
              <Input
                label="Roaming gain (dB)"
                type="number"
                value={radio.gain}
                placeholder="4 to 20 (WP856)"
                error={errors["radio.gain"]}
                disabled={!canWrite}
                onChange={(e) => setR("gain")(e.target.value)}
              />
              <Select
                label="802.11r"
                value={radio.dot11r}
                options={TRI}
                disabled={!canWrite}
                onChange={(e) => setR("dot11r")(e.target.value)}
              />
              <Select
                label="802.11v"
                value={radio.dot11v}
                options={TRI}
                disabled={!canWrite}
                onChange={(e) => setR("dot11v")(e.target.value)}
              />
              <Select
                label="Roaming prompt"
                value={radio.prompt}
                options={TRI}
                disabled={!canWrite}
                onChange={(e) => setR("prompt")(e.target.value)}
              />
            </fieldset>
          )}
          {formError && <Alert tone="bad">{formError}</Alert>}
          <Can min="admin">
            <div>
              <Button disabled={busy} onClick={() => void onSave()}>
                Save Wi-Fi setup
              </Button>
            </div>
          </Can>
        </>
      )}
      {toast.node}
    </div>
  );
}
