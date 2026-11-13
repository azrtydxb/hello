import { useEffect, useState, type FormEvent } from "react";
import {
  errorMessage,
  fieldErrors,
  listAnnouncements,
  listPagingGroups,
  listRingGroups,
  type Announcement,
  type Id,
  type PagingGroup,
  type RingGroup,
} from "../api";
import {
  createDoorWebhook,
  deleteDoorWebhook,
  doorTriggerUrl,
  listDoorWebhooks,
  putDoorWebhook,
  rotateDoorWebhookSecret,
  type DoorAction,
  type DoorWebhook,
  type DoorWebhookCreated,
  type DoorWebhookFields,
} from "../api/door";
import {
  Alert,
  Badge,
  Button,
  CodeBlock,
  ConfirmDialog,
  Drawer,
  EmptyState,
  Input,
  PageHeader,
  Select,
  Spinner,
  Switch,
  useToast,
} from "../design/azrty/components";
import { mapFieldErrors } from "../forms";
import { Can } from "../role";

const NAME_PATTERN = /^[A-Za-z0-9._ -]{1,64}$/;

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: DoorWebhook[] };

interface Draft {
  name: string;
  action: DoorAction;
  ringGroupId: string;
  pagingGroupId: string;
  announcementId: string;
  rateLimit: string;
  enabled: boolean;
}

const FIELDS = [
  "name",
  "action",
  "ringGroupId",
  "pagingGroupId",
  "announcementId",
  "rateLimit",
] as const;
type Errors = Partial<Record<(typeof FIELDS)[number], string>>;

const blank = (): Draft => ({
  name: "",
  action: "ring_door",
  ringGroupId: "",
  pagingGroupId: "",
  announcementId: "",
  rateLimit: "11",
  enabled: true,
});

const draftOf = (h: DoorWebhook): Draft => ({
  name: h.name,
  action: h.action,
  ringGroupId: h.ringGroupId == null ? "" : String(h.ringGroupId),
  pagingGroupId: h.pagingGroupId == null ? "" : String(h.pagingGroupId),
  announcementId: h.announcementId == null ? "" : String(h.announcementId),
  rateLimit: String(h.rateLimit),
  enabled: h.enabled,
});

function validate(d: Draft): Errors {
  const e: Errors = {};
  if (!NAME_PATTERN.test(d.name)) {
    e.name = "1-64 of A-Z a-z 0-9 . _ - and space.";
  }
  const rate = Number(d.rateLimit);
  if (!Number.isInteger(rate) || rate < 1 || rate > 600) {
    e.rateLimit = "A whole number from 1 to 600.";
  }
  if (d.action === "chime_page" && d.pagingGroupId === "") {
    e.pagingGroupId = "Choose the paging group to chime.";
  }
  return e;
}

const idOrNull = (v: string): Id | null => (v === "" ? null : Number(v));

const fieldsOf = (d: Draft): DoorWebhookFields => ({
  name: d.name,
  action: d.action,
  ringGroupId: d.action === "ring_door" ? idOrNull(d.ringGroupId) : null,
  pagingGroupId: d.action === "chime_page" ? idOrNull(d.pagingGroupId) : null,
  announcementId: idOrNull(d.announcementId),
  rateLimit: Number(d.rateLimit),
  enabled: d.enabled,
});

/**
 * Doorbell webhooks: non-SIP doorbells that post a signed request to Hello,
 * which then rings the Door group or chimes a paging group. The signing
 * secret is shown once, when the webhook is created or its secret replaced.
 */
export function Door() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [editing, setEditing] = useState<DoorWebhook | "new" | null>(null);
  const [deleting, setDeleting] = useState<DoorWebhook | null>(null);
  const [rotating, setRotating] = useState<DoorWebhook | null>(null);
  const [shown, setShown] = useState<DoorWebhookCreated | null>(null);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    listDoorWebhooks(controller.signal)
      .then((items) => setList({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  const replace = (fn: (items: DoorWebhook[]) => DoorWebhook[]) =>
    setList((prev) =>
      prev.status === "ready"
        ? { status: "ready", items: fn(prev.items) }
        : prev,
    );

  async function onDelete(h: DoorWebhook) {
    try {
      await deleteDoorWebhook(h.id);
      replace((items) => items.filter((x) => String(x.id) !== String(h.id)));
      toast.show(`Webhook ${h.name} deleted.`);
    } finally {
      setDeleting(null);
    }
  }

  async function onRotate(h: DoorWebhook) {
    try {
      setShown(await rotateDoorWebhookSecret(h.id));
    } catch (err: unknown) {
      toast.show(`Could not replace the secret: ${errorMessage(err)}`);
    } finally {
      setRotating(null);
    }
  }

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Call flow"
        title="Door"
        description="A doorbell that cannot register as a SIP phone posts a signed request to a webhook here. Hello then rings the Door group with the doorbell tone, or plays a chime to a paging group."
        actions={
          <Can min="admin">
            {/* A webhook created before the list arrives would be wiped
                when the list lands, so wait for it. */}
            <Button
              icon="plus"
              disabled={list.status === "loading"}
              onClick={() => setEditing("new")}
            >
              New webhook
            </Button>
          </Can>
        }
      />
      {shown && <SecretNotice hook={shown} onDone={() => setShown(null)} />}
      {list.status === "loading" && <Spinner label="Loading webhooks…" />}
      {list.status === "error" && (
        <Alert tone="bad" title="Could not load the webhooks">
          {list.message}
        </Alert>
      )}
      {list.status === "ready" && list.items.length === 0 && (
        <EmptyState
          icon="bell-electric"
          title="No doorbell webhooks"
          description="Door stations that register as SIP phones need none: give them an extension on the Extensions page."
        />
      )}
      {list.status === "ready" && list.items.length > 0 && (
        <ul className="cf-rows" aria-label="Doorbell webhooks">
          {list.items.map((h) => (
            <li className="cf-row" key={String(h.id)}>
              <span className="cf-row__label">
                <strong>{h.name}</strong>{" "}
                <Badge tone={h.action === "ring_door" ? "pillar" : "info"}>
                  {h.action === "ring_door"
                    ? `Rings ${h.ringGroup ? `Door group ${h.ringGroup}` : "every Door group"}`
                    : `Chimes ${h.pagingGroup ?? "a paging group"}`}
                </Badge>{" "}
                {!h.enabled && <Badge tone="warn">Disabled</Badge>}
                <span className="cf-card__sub">
                  {" "}
                  {String(h.rateLimit)} a minute
                  {h.lastTriggeredAt
                    ? ` · last pressed ${new Date(h.lastTriggeredAt).toLocaleString()}`
                    : " · never pressed"}
                </span>
              </span>
              <Can min="admin">
                <span className="cf-row__actions">
                  <Button
                    variant="secondary"
                    size="sm"
                    aria-label={`Edit ${h.name}`}
                    onClick={() => setEditing(h)}
                  >
                    Edit
                  </Button>
                  <Button
                    variant="secondary"
                    size="sm"
                    aria-label={`Replace the secret of ${h.name}`}
                    onClick={() => setRotating(h)}
                  >
                    New secret
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="cf-danger"
                    aria-label={`Delete ${h.name}`}
                    onClick={() => setDeleting(h)}
                  >
                    Delete
                  </Button>
                </span>
              </Can>
            </li>
          ))}
        </ul>
      )}
      {editing && (
        <HookDrawer
          hook={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onCreated={(h) => {
            replace((items) =>
              [...items, h].sort((a, b) => a.name.localeCompare(b.name)),
            );
            setEditing(null);
            setShown(h);
          }}
          onSaved={(h) => {
            replace((items) =>
              items.map((x) => (String(x.id) === String(h.id) ? h : x)),
            );
            setEditing(null);
            toast.show(`Webhook ${h.name} saved.`);
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete webhook ${deleting.name}?`}
          description="Its URL stops working at once; the doorbell will get 401."
          confirmLabel="Delete webhook"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {rotating && (
        <ConfirmDialog
          title={`Replace the secret of ${rotating.name}?`}
          description="The old secret stops working at once. You will see the new one once and must enter it in the doorbell."
          confirmLabel="Replace secret"
          onConfirm={() => onRotate(rotating)}
          onClose={() => setRotating(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

/** The secret and how to sign with it, shown once. */
function SecretNotice({
  hook,
  onDone,
}: {
  hook: DoorWebhookCreated;
  onDone: () => void;
}) {
  return (
    <Alert tone="warn" title={`Secret for ${hook.name} (shown once)`}>
      <p>
        Copy it now; it cannot be shown again. The doorbell posts to the URL
        below with <code>X-Hello-Timestamp</code> (Unix seconds) and{" "}
        <code>X-Hello-Signature</code>, the hex HMAC-SHA256 of the timestamp, a
        dot and the body under this secret.
      </p>
      <CodeBlock title="Secret" code={hook.secret} />
      <CodeBlock title="Trigger" code={`POST ${doorTriggerUrl(hook.id)}`} />
      <Button size="sm" variant="secondary" onClick={onDone}>
        I have copied it
      </Button>
    </Alert>
  );
}

function HookDrawer({
  hook,
  onClose,
  onCreated,
  onSaved,
}: {
  hook: DoorWebhook | null;
  onClose: () => void;
  onCreated: (h: DoorWebhookCreated) => void;
  onSaved: (h: DoorWebhook) => void;
}) {
  const [draft, setDraft] = useState<Draft>(hook ? draftOf(hook) : blank());
  const [errors, setErrors] = useState<Errors>({});
  const [serverError, setServerError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [rings, setRings] = useState<RingGroup[]>([]);
  const [pages, setPages] = useState<PagingGroup[]>([]);
  const [anns, setAnns] = useState<Announcement[]>([]);

  useEffect(() => {
    const controller = new AbortController();
    const quiet = () => undefined; // a list that fails leaves its select short
    listRingGroups(controller.signal).then(setRings).catch(quiet);
    listPagingGroups(controller.signal).then(setPages).catch(quiet);
    listAnnouncements(controller.signal).then(setAnns).catch(quiet);
    return () => controller.abort();
  }, []);

  const set =
    <K extends keyof Draft>(key: K) =>
    (value: Draft[K]) =>
      setDraft((d) => ({ ...d, [key]: value }));

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setServerError(null);
    const found = validate(draft);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setBusy(true);
    try {
      if (hook) onSaved(await putDoorWebhook(hook.id, fieldsOf(draft)));
      else onCreated(await createDoorWebhook(fieldsOf(draft)));
    } catch (err: unknown) {
      setErrors(mapFieldErrors(fieldErrors(err), FIELDS).byKey as Errors);
      setServerError(`Could not save: ${errorMessage(err)}`);
      setBusy(false);
    }
  }

  return (
    <Drawer
      title={hook ? `Webhook ${hook.name}` : "New webhook"}
      onClose={onClose}
      width={480}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="door-webhook-form" disabled={busy}>
            {hook ? "Save" : "Create webhook"}
          </Button>
        </>
      }
    >
      <form
        id="door-webhook-form"
        className="dir-form"
        onSubmit={onSubmit}
        noValidate
      >
        {serverError && <Alert tone="bad">{serverError}</Alert>}
        <Input
          id="door-name"
          label="Name"
          hint="Shown as the caller name on the phones that ring."
          error={errors.name}
          value={draft.name}
          autoFocus
          onChange={(e) => set("name")(e.target.value)}
        />
        <Select
          id="door-action"
          label="When pressed"
          value={draft.action}
          options={[
            { value: "ring_door", label: "Ring the Door group" },
            { value: "chime_page", label: "Chime a paging group" },
          ]}
          onChange={(e) => set("action")(e.target.value as DoorAction)}
        />
        {draft.action === "ring_door" ? (
          <Select
            id="door-ring-group"
            label="Door group"
            hint="Which Door group rings; every Door group when left open."
            error={errors.ringGroupId}
            value={draft.ringGroupId}
            options={[
              { value: "", label: "Every Door group" },
              ...rings
                .filter((g) => g.isDoor)
                .map((g) => ({ value: String(g.id), label: g.name })),
            ]}
            onChange={(e) => set("ringGroupId")(e.target.value)}
          />
        ) : (
          <Select
            id="door-paging-group"
            label="Paging group"
            error={errors.pagingGroupId}
            value={draft.pagingGroupId}
            options={[
              { value: "", label: "Choose…" },
              ...pages.map((g) => ({
                value: String(g.id),
                label: `${g.name} (${g.code})`,
              })),
            ]}
            onChange={(e) => set("pagingGroupId")(e.target.value)}
          />
        )}
        <Select
          id="door-announcement"
          label="Chime"
          hint="Played to whoever answers; the built-in chime when left open."
          error={errors.announcementId}
          value={draft.announcementId}
          options={[
            { value: "", label: "Built-in chime" },
            ...anns.map((a) => ({ value: String(a.id), label: a.name })),
          ]}
          onChange={(e) => set("announcementId")(e.target.value)}
        />
        <Input
          id="door-rate"
          label="Triggers a minute"
          inputMode="numeric"
          hint="Above this the doorbell gets 429."
          error={errors.rateLimit}
          value={draft.rateLimit}
          onChange={(e) => set("rateLimit")(e.target.value)}
        />
        <Switch
          label="Enabled"
          hint="A disabled webhook answers its doorbell with 401."
          labelPosition="end"
          checked={draft.enabled}
          onChange={(e) => set("enabled")(e.target.checked)}
        />
      </form>
    </Drawer>
  );
}
