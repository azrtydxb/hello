import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router";
import {
  createTrunk,
  deleteTrunk,
  errorMessage,
  fieldErrors,
  listTrunks,
  listTrunkStatus,
  updateTrunk,
  type FieldError,
  type Trunk,
  type TrunkInput,
  type TrunkStatus,
} from "../api";
import {
  Alert,
  Badge,
  Button,
  Drawer,
  EmptyState,
  Icon,
  IconButton,
  Input,
  Meter,
  PropertyList,
  Select,
  Switch,
  type BadgeTone,
  type Property,
} from "../design/azrty/components";
import { mapFieldErrors, splitList, type ErrorMap } from "../forms";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";
import {
  formatLatency,
  minutesUntil,
  orUnknown,
  UNKNOWN,
} from "./callflow/format";
import {
  ConfirmDialog,
  FormAlert,
  Loading,
  PageHeader,
  useRestoreFocus,
  useToast,
} from "./callflow/ui";

const TRUNK_NAME_PATTERN = /^[A-Za-z0-9._-]{1,64}$/;
const FORM_ID = "trunk-form";

interface DestinationDraft {
  host: string;
  port: string;
  priority: string;
  weight: string;
}

/** The form's view of a trunk; never holds the password. */
interface TrunkDraft {
  name: string;
  mode: "registration" | "ip";
  username: string;
  realm: string;
  fromDomain: string;
  registerExpires: string;
  optionsInterval: string;
  sourceCidrs: string;
  maxCalls: string;
  defaultCallerId: string;
  enabled: boolean;
  destinations: DestinationDraft[];
}

const NEW_DESTINATION: DestinationDraft = {
  host: "",
  port: "5060",
  priority: "0",
  weight: "1",
};

const NEW_TRUNK: TrunkDraft = {
  name: "",
  mode: "registration",
  username: "",
  realm: "",
  fromDomain: "",
  registerExpires: "3600",
  optionsInterval: "30",
  sourceCidrs: "",
  maxCalls: "0",
  defaultCallerId: "",
  enabled: true,
  destinations: [NEW_DESTINATION],
};

function toDraft(t: Trunk): TrunkDraft {
  return {
    name: t.name,
    mode: t.mode,
    username: t.username,
    realm: t.realm,
    fromDomain: t.fromDomain,
    registerExpires: String(t.registerExpires),
    optionsInterval: String(t.optionsInterval),
    sourceCidrs: (t.sourceCidrs ?? []).join("\n"),
    maxCalls: String(t.maxCalls),
    defaultCallerId: t.defaultCallerId,
    enabled: t.enabled,
    destinations: (t.destinations ?? []).map((d) => ({
      host: d.host,
      port: String(d.port),
      priority: String(d.priority),
      weight: String(d.weight),
    })),
  };
}

const isInt = (v: string) => /^-?[0-9]+$/.test(v.trim());

/** Client-side checks; the server validates everything again. */
function validate(d: TrunkDraft): Record<string, string> {
  const e: Record<string, string> = {};
  if (!TRUNK_NAME_PATTERN.test(d.name)) {
    e.name = "Use 1 to 64 letters, digits, dots, underscores or hyphens.";
  }
  for (const key of [
    "registerExpires",
    "optionsInterval",
    "maxCalls",
  ] as const) {
    if (!isInt(d[key])) e[key] = "Enter a whole number.";
  }
  if (d.destinations.length === 0) e.destinations = "Add a destination.";
  d.destinations.forEach((dst, i) => {
    if (dst.host.trim() === "") e[`destinations[${i}].host`] = "Enter a host.";
    for (const f of ["port", "priority", "weight"] as const) {
      if (!isInt(dst[f])) e[`destinations[${i}].${f}`] = "Whole number.";
    }
  });
  return e;
}

function toInput(d: TrunkDraft): TrunkInput {
  return {
    name: d.name,
    mode: d.mode,
    username: d.username,
    realm: d.realm,
    fromDomain: d.fromDomain,
    registerExpires: Number(d.registerExpires),
    optionsInterval: Number(d.optionsInterval),
    sourceCidrs: splitList(d.sourceCidrs),
    maxCalls: Number(d.maxCalls),
    defaultCallerId: d.defaultCallerId,
    enabled: d.enabled,
    destinations: d.destinations.map((x) => ({
      host: x.host.trim(),
      port: Number(x.port),
      priority: Number(x.priority),
      weight: Number(x.weight),
    })),
  };
}

function knownKeys(d: TrunkDraft): string[] {
  return [
    "name",
    "mode",
    "username",
    "password",
    "realm",
    "fromDomain",
    "registerExpires",
    "optionsInterval",
    "sourceCidrs",
    "maxCalls",
    "defaultCallerId",
    "enabled",
    "destinations",
    ...d.destinations.flatMap((_, i) => [
      `destinations[${i}]`,
      ...["host", "port", "priority", "weight"].map(
        (f) => `destinations[${i}].${f}`,
      ),
    ]),
  ];
}

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: Trunk[] };

type Editing = { kind: "new" } | { kind: "edit"; trunk: Trunk } | null;

/** Trunks: carrier accounts and IP peers as cards, with live status every 5 s. */
export function Trunks() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [editing, setEditing] = useState<Editing>(null);
  const [deleting, setDeleting] = useState<Trunk | null>(null);
  const status = usePolling(listTrunkStatus, LIVE_REFRESH_MS);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    listTrunks(controller.signal)
      .then((items) => setList({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  const { show } = toast;
  const onSaved = useCallback(
    (saved: Trunk, created: boolean, rotated: boolean) => {
      setList((prev) =>
        prev.status !== "ready"
          ? prev
          : {
              status: "ready",
              items: created
                ? [...prev.items, saved]
                : prev.items.map((t) => (t.id === saved.id ? saved : t)),
            },
      );
      setEditing(null);
      show(
        rotated
          ? `Secret rotated. ${saved.name} uses the new password from its next request.`
          : created
            ? `Trunk ${saved.name} created.`
            : `Trunk ${saved.name} saved.`,
      );
    },
    [show],
  );

  async function onDelete(t: Trunk) {
    try {
      await deleteTrunk(t.id);
    } catch (err) {
      throw new Error(`Could not delete ${t.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    setList((prev) =>
      prev.status === "ready"
        ? { status: "ready", items: prev.items.filter((x) => x.id !== t.id) }
        : prev,
    );
    setDeleting(null);
    show(`Trunk ${t.name} deleted.`);
  }

  const statuses = status.status === "loading" ? undefined : status.data;
  const statusOf = (t: Trunk) =>
    statuses?.find((s) => String(s.trunkId) === String(t.id));
  const newTrunk = (
    <Button
      icon="plus"
      disabled={list.status !== "ready"}
      onClick={() => setEditing({ kind: "new" })}
    >
      New trunk
    </Button>
  );

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        title="Trunks"
        description="Carrier accounts and IP peers. Destinations are health-checked with OPTIONS; status refreshes every 5 s."
        actions={newTrunk}
      />
      <div className="cf-stack">
        {status.status === "error" && (
          <Alert tone="warn" title="Live status unavailable">
            {status.message} Registration, health and call counts show — until
            it returns.
          </Alert>
        )}
        {list.status === "loading" && <Loading what="trunks" />}
        {list.status === "error" && (
          <Alert tone="bad" title="Could not load trunks.">
            {list.message}
          </Alert>
        )}
        {list.status === "ready" && list.items.length === 0 && (
          <EmptyState
            icon="cable"
            title="No trunks yet."
            description="Add a carrier account or an IP peer to call numbers outside Hello."
            action={newTrunk}
          />
        )}
        {list.status === "ready" && list.items.length > 0 && (
          <ul className="cf-grid cf-plain" aria-label="Trunks">
            {list.items.map((t) => (
              <TrunkCard
                key={String(t.id)}
                trunk={t}
                status={statusOf(t)}
                onEdit={() => setEditing({ kind: "edit", trunk: t })}
                onDelete={() => setDeleting(t)}
              />
            ))}
          </ul>
        )}
      </div>

      {editing && (
        <TrunkDrawer
          key={editing.kind === "edit" ? String(editing.trunk.id) : "new"}
          trunk={editing.kind === "edit" ? editing.trunk : null}
          onClose={() => setEditing(null)}
          onSaved={onSaved}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete trunk ${deleting.name}?`}
          description="Routes that use it stop trying it. Calls in progress are not cut."
          confirmLabel="Delete trunk"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

/** The trunk's overall state badge, from its live status. */
function trunkState(
  t: Trunk,
  s: TrunkStatus | undefined,
): { tone: BadgeTone; label: string } {
  if (!t.enabled) return { tone: "outline", label: "Disabled" };
  if (!s) return { tone: "outline", label: "Status unknown" };
  const down = s.destinations.filter((d) => !d.up).length;
  const allDown = s.destinations.length > 0 && down === s.destinations.length;
  if (t.mode === "registration") {
    const reg = s.registration;
    if (!reg) return { tone: "outline", label: "Not registered yet" };
    switch (reg.state) {
      case "registered":
        return down > 0
          ? { tone: "warn", label: "Degraded" }
          : { tone: "good", label: "Registered" };
      case "registering":
        return { tone: "info", label: "Registering" };
      case "failed":
        return { tone: "bad", label: "Registration failed" };
      case "misconfigured":
        return { tone: "bad", label: "Misconfigured" };
      case "disabled":
        return { tone: "outline", label: "Disabled" };
      default:
        return { tone: "neutral", label: reg.state };
    }
  }
  if (s.destinations.length === 0) {
    return { tone: "outline", label: "Not checked yet" };
  }
  if (allDown) return { tone: "bad", label: "Down" };
  if (down > 0) return { tone: "warn", label: "Degraded" };
  return { tone: "good", label: "Up" };
}

/** "registered on hello-sip-1 (200) · expires in 51 min" or "accepts …". */
function trunkLine(t: Trunk, s: TrunkStatus | undefined): string {
  if (t.mode === "ip") {
    const cidrs = t.sourceCidrs ?? [];
    return cidrs.length > 0
      ? `accepts ${cidrs.join(", ")}`
      : "accepts no source addresses";
  }
  const reg = s?.registration;
  if (!reg) return "registration state —";
  const mins = minutesUntil(reg.expires);
  return [
    `${reg.state} on ${orUnknown(reg.node)}${reg.lastCode ? ` (${reg.lastCode})` : ""}`,
    mins !== null ? `expires in ${mins} min` : null,
  ]
    .filter(Boolean)
    .join(" · ");
}

function TrunkCard({
  trunk: t,
  status,
  onEdit,
  onDelete,
}: {
  trunk: Trunk;
  status: TrunkStatus | undefined;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const state = trunkState(t, status);
  const titleId = `trunk-${String(t.id)}-name`;
  const calls = status?.activeCalls;
  const callsLabel =
    calls === undefined
      ? UNKNOWN
      : t.maxCalls > 0
        ? `${calls} of ${t.maxCalls}`
        : `${calls} · no limit`;
  const password = t.hasPassword ? "set (write-only)" : "not set";
  const props: Property[] =
    t.mode === "registration"
      ? [
          { label: "Username", value: orUnknown(t.username), mono: true },
          { label: "Realm", value: orUnknown(t.realm), mono: true },
          { label: "OPTIONS interval", value: `${t.optionsInterval} s` },
          {
            label: "Default caller ID",
            value: orUnknown(t.defaultCallerId),
            mono: true,
          },
          { label: "Password", value: password },
        ]
      : [
          {
            label: "Source CIDRs",
            value: orUnknown((t.sourceCidrs ?? []).join(", ")),
            mono: true,
          },
          { label: "From domain", value: orUnknown(t.fromDomain), mono: true },
          { label: "OPTIONS interval", value: `${t.optionsInterval} s` },
          {
            label: "Default caller ID",
            value: orUnknown(t.defaultCallerId),
            mono: true,
          },
          { label: "Password", value: password },
        ];

  return (
    <li className="az-card cf-card" aria-labelledby={titleId}>
      <div className="cf-card__head">
        <span className="cf-tile">
          <Icon
            name={t.mode === "registration" ? "key-round" : "network"}
            size={18}
          />
        </span>
        <div style={{ minWidth: 0 }}>
          <h2 className="cf-card__name" id={titleId}>
            {t.name}
          </h2>
          <div className="cf-card__sub">
            {t.mode === "registration" ? "Registration" : "IP peer"} ·{" "}
            {trunkLine(t, status)}
          </div>
        </div>
        <Badge tone={state.tone} dot>
          {state.label}
        </Badge>
      </div>

      <table className="cf-dests">
        <caption className="visually-hidden">Destinations of {t.name}</caption>
        <thead>
          <tr className="cf-dests__row cf-dests__row--head az-eyebrow">
            <th scope="col">Destination</th>
            <th scope="col">Prio</th>
            <th scope="col">Weight</th>
            <th scope="col" className="cf-dests__right">
              Health
            </th>
          </tr>
        </thead>
        <tbody>
          {(t.destinations ?? []).map((d) => {
            const key = `${d.host}:${d.port}`;
            const h = status?.destinations.find((x) => x.destination === key);
            const latency = formatLatency(h?.latencyNs);
            const tone = !h ? "unknown" : h.up ? "good" : "bad";
            const text = !h
              ? UNKNOWN
              : h.up
                ? (latency ?? "up")
                : h.lastCode
                  ? `down · ${h.lastCode}`
                  : "down";
            return (
              <tr className="cf-dests__row" key={key}>
                <td className="cf-dests__host" title={key}>
                  {d.port === 0 ? `${d.host} (SRV)` : key}
                </td>
                <td className="cf-dests__num">{d.priority}</td>
                <td className="cf-dests__num">{d.weight}</td>
                <td className={`cf-health cf-health--${tone}`}>
                  <span className="az-dot" aria-hidden="true" />
                  {text}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>

      <Meter
        label="Concurrent calls"
        value={calls ?? 0}
        max={t.maxCalls > 0 ? t.maxCalls : Math.max(calls ?? 0, 1)}
        tone={t.maxCalls > 0 ? "auto" : "mint"}
        valueLabel={callsLabel}
      />
      <PropertyList items={props} />

      <div className="cf-card__foot">
        <Button
          variant="secondary"
          size="sm"
          icon="pencil"
          aria-label={`Edit trunk ${t.name}`}
          onClick={onEdit}
        >
          Edit
        </Button>
        <Link
          className="az-btn az-btn--ghost az-btn--sm"
          to={`/routes/test?from=${encodeURIComponent(`trunk:${String(t.id)}`)}`}
        >
          <Icon name="flask-conical" size={13} />
          Test a route
        </Link>
        <Button
          variant="ghost"
          size="sm"
          icon="trash-2"
          className="cf-danger"
          aria-label={`Delete trunk ${t.name}`}
          onClick={onDelete}
        >
          Delete
        </Button>
      </div>
    </li>
  );
}

function TrunkDrawer({
  trunk,
  onClose,
  onSaved,
}: {
  trunk: Trunk | null;
  onClose: () => void;
  onSaved: (t: Trunk, created: boolean, rotated: boolean) => void;
}) {
  useRestoreFocus();
  const [draft, setDraft] = useState<TrunkDraft>(() =>
    trunk ? toDraft(trunk) : NEW_TRUNK,
  );
  // The password is write-only: it starts empty, is sent only when the user
  // chose to set it, and is dropped with this component after saving.
  const [changingPassword, setChangingPassword] = useState(trunk === null);
  const [password, setPassword] = useState("");
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const set = <K extends keyof TrunkDraft>(key: K, value: TrunkDraft[K]) =>
    setDraft((d) => ({ ...d, [key]: value }));
  const setDest = (i: number, patch: Partial<DestinationDraft>) =>
    setDraft((d) => ({
      ...d,
      destinations: d.destinations.map((x, j) =>
        j === i ? { ...x, ...patch } : x,
      ),
    }));
  const fid = (key: string) => `trunk-${key.replace(/[^A-Za-z0-9_-]+/g, "-")}`;

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const found: Record<string, string> = { ...validate(draft) };
    if (trunk && changingPassword && password === "") {
      found.password = "Enter the new password, or keep the current one.";
    }
    setErrors(found);
    if (Object.keys(found).length > 0) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    const body = toInput(draft);
    const sendingPassword = changingPassword && password !== "";
    if (sendingPassword) body.password = password;
    setBusy(true);
    try {
      const saved = trunk
        ? await updateTrunk(trunk.id, body)
        : await createTrunk(body);
      setPassword("");
      onSaved(saved, trunk === null, trunk !== null && sendingPassword);
    } catch (err) {
      const mapped = mapFieldErrors(fieldErrors(err), knownKeys(draft));
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  const registration = draft.mode === "registration";
  return (
    <Drawer
      title={trunk ? `Trunk ${trunk.name}` : "New trunk"}
      description={
        trunk
          ? "Changes apply to new calls and the next registration."
          : "A carrier account that registers, or an IP peer that is trusted by address."
      }
      onClose={busy ? undefined : onClose}
      width={560}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form={FORM_ID} disabled={busy}>
            {trunk ? "Save trunk" : "Create trunk"}
          </Button>
        </>
      }
    >
      <form
        id={FORM_ID}
        className="cf-form"
        aria-label={trunk ? `Edit trunk ${trunk.name}` : "New trunk"}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <div className="cf-two">
          <Input
            id={fid("name")}
            label="Name"
            mono
            autoFocus
            autoComplete="off"
            value={draft.name}
            error={errors.name}
            hint="Letters, digits, dots, underscores or hyphens."
            onChange={(e) => set("name", e.target.value)}
          />
          <Select
            id={fid("mode")}
            label="Mode"
            value={draft.mode}
            error={errors.mode}
            options={[
              { value: "registration", label: "Registration" },
              { value: "ip", label: "IP peer" },
            ]}
            onChange={(e) => set("mode", e.target.value as TrunkDraft["mode"])}
          />
        </div>

        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">Credentials</legend>
          <div className="cf-two">
            <Input
              id={fid("username")}
              label="Username"
              mono
              autoComplete="off"
              value={draft.username}
              error={errors.username}
              onChange={(e) => set("username", e.target.value)}
            />
            <Input
              id={fid("realm")}
              label="Realm"
              mono
              value={draft.realm}
              error={errors.realm}
              onChange={(e) => set("realm", e.target.value)}
            />
          </div>
          <PasswordField
            id={fid("password")}
            isNew={trunk === null}
            hasPassword={trunk?.hasPassword ?? false}
            changing={changingPassword}
            value={password}
            error={errors.password}
            onChange={setPassword}
            onChanging={(on) => {
              setChangingPassword(on);
              setPassword("");
            }}
          />
          {registration && (
            <Input
              id={fid("registerExpires")}
              label="Register expires (s)"
              inputMode="numeric"
              value={draft.registerExpires}
              error={errors.registerExpires}
              hint="60 to 86400."
              onChange={(e) => set("registerExpires", e.target.value)}
            />
          )}
        </fieldset>

        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">Calls</legend>
          <div className="cf-two">
            <Input
              id={fid("maxCalls")}
              label="Concurrent calls"
              inputMode="numeric"
              value={draft.maxCalls}
              error={errors.maxCalls}
              hint="0 means no limit."
              onChange={(e) => set("maxCalls", e.target.value)}
            />
            <Input
              id={fid("optionsInterval")}
              label="OPTIONS interval (s)"
              inputMode="numeric"
              value={draft.optionsInterval}
              error={errors.optionsInterval}
              hint="5 to 3600."
              onChange={(e) => set("optionsInterval", e.target.value)}
            />
          </div>
          <Input
            id={fid("defaultCallerId")}
            label="Default caller ID"
            mono
            value={draft.defaultCallerId}
            error={errors.defaultCallerId}
            hint="Presented when the calling extension has no external number."
            onChange={(e) => set("defaultCallerId", e.target.value)}
          />
          <Input
            id={fid("fromDomain")}
            label="From domain"
            mono
            value={draft.fromDomain}
            error={errors.fromDomain}
            hint="Optional; the host of the From header sent to this trunk."
            onChange={(e) => set("fromDomain", e.target.value)}
          />
          <div className="az-field">
            <label className="az-field__label" htmlFor={fid("sourceCidrs")}>
              Source CIDRs
            </label>
            <textarea
              id={fid("sourceCidrs")}
              className="az-input az-textarea az-input--mono"
              rows={3}
              value={draft.sourceCidrs}
              aria-invalid={errors.sourceCidrs ? true : undefined}
              aria-describedby={`${fid("sourceCidrs")}-hint`}
              onChange={(e) => set("sourceCidrs", e.target.value)}
            />
            <span
              className={
                errors.sourceCidrs
                  ? "az-field__hint cf-form__error"
                  : "az-field__hint"
              }
              id={`${fid("sourceCidrs")}-hint`}
            >
              {errors.sourceCidrs ??
                "One per line, e.g. 203.0.113.0/24. Calls from these addresses are accepted as this trunk."}
            </span>
          </div>
          <Switch
            label="Enabled"
            hint="A disabled trunk is skipped by every route."
            labelPosition="end"
            checked={draft.enabled}
            onChange={(e) => set("enabled", e.target.checked)}
          />
        </fieldset>

        <fieldset
          className="cf-form__section"
          aria-describedby={
            errors.destinations ? `${fid("destinations")}-error` : undefined
          }
        >
          <legend className="az-eyebrow">Destinations</legend>
          <p className="cf-form__note">
            Lower priority is tried first; weight splits calls among equal
            priorities. Port 0 resolves SRV.
          </p>
          {errors.destinations && (
            <p id={`${fid("destinations")}-error`} className="cf-form__error">
              {errors.destinations}
            </p>
          )}
          <ul className="cf-rows">
            {draft.destinations.map((dst, i) => {
              const n = i + 1;
              const k = (f: string) => `destinations[${i}].${f}`;
              return (
                <li className="cf-row" key={i}>
                  <div className="cf-row__fields cf-row__fields--dest">
                    <Input
                      id={fid(k("host"))}
                      label={`Host ${n}`}
                      mono
                      size="sm"
                      value={dst.host}
                      error={errors[k("host")] ?? errors[`destinations[${i}]`]}
                      onChange={(e) => setDest(i, { host: e.target.value })}
                    />
                    <Input
                      id={fid(k("port"))}
                      label={`Port ${n}`}
                      mono
                      size="sm"
                      inputMode="numeric"
                      value={dst.port}
                      error={errors[k("port")]}
                      onChange={(e) => setDest(i, { port: e.target.value })}
                    />
                    <Input
                      id={fid(k("priority"))}
                      label={`Priority ${n}`}
                      mono
                      size="sm"
                      inputMode="numeric"
                      value={dst.priority}
                      error={errors[k("priority")]}
                      onChange={(e) => setDest(i, { priority: e.target.value })}
                    />
                    <Input
                      id={fid(k("weight"))}
                      label={`Weight ${n}`}
                      mono
                      size="sm"
                      inputMode="numeric"
                      value={dst.weight}
                      error={errors[k("weight")]}
                      onChange={(e) => setDest(i, { weight: e.target.value })}
                    />
                  </div>
                  <IconButton
                    icon="trash-2"
                    label={`Remove destination ${n}`}
                    onClick={() =>
                      setDraft((d) => ({
                        ...d,
                        destinations: d.destinations.filter((_, j) => j !== i),
                      }))
                    }
                  />
                </li>
              );
            })}
          </ul>
          <div>
            <Button
              variant="ghost"
              size="sm"
              icon="plus"
              onClick={() =>
                setDraft((d) => ({
                  ...d,
                  destinations: [...d.destinations, NEW_DESTINATION],
                }))
              }
            >
              Add destination
            </Button>
          </div>
        </fieldset>
        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Drawer>
  );
}

/**
 * The write-only password: never pre-filled, never displayed. Editing an
 * existing trunk shows only whether one is set; changing it is explicit.
 */
function PasswordField({
  id,
  isNew,
  hasPassword,
  changing,
  value,
  error,
  onChange,
  onChanging,
}: {
  id: string;
  isNew: boolean;
  hasPassword: boolean;
  changing: boolean;
  value: string;
  error?: string;
  onChange: (v: string) => void;
  onChanging: (on: boolean) => void;
}) {
  const statusId = `${id}-status`;
  if (!changing) {
    return (
      <div className="az-field">
        <span className="az-field__label">Password</span>
        <div className="cf-inline">
          <span id={statusId}>
            {hasPassword ? "set (write-only)" : "not set"}
          </span>
          <Button
            variant="secondary"
            size="sm"
            icon="key-round"
            aria-describedby={statusId}
            onClick={() => onChanging(true)}
          >
            {hasPassword ? "Change password" : "Set password"}
          </Button>
        </div>
      </div>
    );
  }
  return (
    <div className="cf-field-group">
      <Input
        id={id}
        label={isNew ? "Password (optional)" : "New password"}
        type="password"
        autoComplete="new-password"
        autoFocus={!isNew}
        value={value}
        error={error}
        hint="Write-only: stored encrypted and never shown again."
        onChange={(e) => onChange(e.target.value)}
      />
      {!isNew && (
        <div>
          <Button variant="ghost" size="sm" onClick={() => onChanging(false)}>
            Keep current password
          </Button>
        </div>
      )}
    </div>
  );
}
