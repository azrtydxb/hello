import { useState, type FormEvent } from "react";
import { errorMessage, fieldErrors, type FieldError } from "../../api";
import {
  createMulticastChannel,
  deleteMulticastChannel,
  updateMulticastChannel,
  type CatalogChannel,
  type CatalogChannelInput,
  type ChannelKind,
  type MulticastCodec,
} from "../../api/phonebook";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  IconButton,
  Input,
  Modal,
  Select,
  Switch,
  Table,
  type TableColumn,
} from "../../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../../forms";
import { FormAlert } from "../callflow/ui";
import { Can } from "../../role";

const FIELDS = [
  "label",
  "address",
  "port",
  "codec",
  "priority",
  "kind",
  "enabled",
] as const;

const KIND_LABEL: Record<ChannelKind, string> = {
  paging: "Paging",
  ptt: "Push-to-talk",
};

const emptyChannel = (): CatalogChannelInput => ({
  label: "",
  address: "239.255.0.1",
  port: 5004,
  codec: "PCMU",
  priority: 1,
  kind: "paging",
  enabled: true,
});

/**
 * The multicast channel catalog: paging channels that phone groups listen
 * on and paging groups send to, and push-to-talk channels for the PTT key
 * of Grandstream WP phones.
 */
export function MulticastChannels({
  channels,
  error,
  onChange,
  onToast,
}: {
  channels: CatalogChannel[];
  error: string | null;
  onChange: (channels: CatalogChannel[]) => void;
  onToast: (message: string) => void;
}) {
  const [editing, setEditing] = useState<CatalogChannel | "new" | null>(null);
  const [deleting, setDeleting] = useState<CatalogChannel | null>(null);

  async function onDelete(c: CatalogChannel) {
    try {
      await deleteMulticastChannel(c.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${c.label}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    onChange(channels.filter((x) => String(x.id) !== String(c.id)));
    setDeleting(null);
    onToast(`Multicast channel ${c.label} deleted.`);
  }

  const columns: TableColumn<CatalogChannel>[] = [
    { key: "label", label: "Name", render: (c) => c.label },
    {
      key: "kind",
      label: "Kind",
      render: (c) => (
        <Badge tone={c.kind === "ptt" ? "info" : "outline"}>
          {KIND_LABEL[c.kind]}
        </Badge>
      ),
    },
    {
      key: "address",
      label: "Address",
      mono: true,
      render: (c) => `${c.address}:${String(c.port)}`,
    },
    { key: "codec", label: "Codec", render: (c) => c.codec },
    {
      key: "priority",
      label: "Priority",
      mono: true,
      render: (c) => String(c.priority),
    },
    {
      key: "enabled",
      label: "State",
      render: (c) =>
        c.enabled ? (
          <Badge tone="good">Enabled</Badge>
        ) : (
          <Badge tone="warn">Disabled</Badge>
        ),
    },
    {
      key: "actions",
      label: "Actions",
      align: "right",
      render: (c) => (
        <Can min="admin">
          <div className="inv-actions">
            <IconButton
              icon="pencil"
              label={`Edit ${c.label}`}
              size={15}
              onClick={() => setEditing(c)}
            />
            <IconButton
              icon="trash-2"
              label={`Delete ${c.label}`}
              size={15}
              onClick={() => setDeleting(c)}
            />
          </div>
        </Can>
      ),
    },
  ];

  return (
    <section className="cf-stack" aria-labelledby="mc-title">
      <div className="cf-card__head">
        <h2 className="cf-card__group-name" id="mc-title">
          Multicast channels
        </h2>
        <Can min="admin">
          <Button
            variant="secondary"
            size="sm"
            icon="plus"
            onClick={() => setEditing("new")}
          >
            New channel
          </Button>
        </Can>
      </div>
      <p className="cf-form__note">
        Paging channels: phones listen on them (every enabled one, unless their
        phone group picks some) and a paging group can also send its page to
        one. Push-to-talk channels: the PTT key of Grandstream WP phones talks
        on the first enabled one, unless a phone group or a phone picks another.
        Phones pick up a change at their next resync. Multicast works on one LAN
        only.
      </p>
      {error && (
        <Alert tone="warn" title="Could not load the multicast channels">
          {error}
        </Alert>
      )}
      {!error && (
        <Table
          caption="Multicast channels"
          columns={columns}
          rows={channels}
          rowKey={(c) => String(c.id)}
        />
      )}
      {editing && (
        <ChannelModal
          channel={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={(saved, created) => {
            onChange(
              created
                ? [...channels, saved]
                : channels.map((c) =>
                    String(c.id) === String(saved.id) ? saved : c,
                  ),
            );
            setEditing(null);
            onToast(
              created
                ? `Multicast channel ${saved.label} created.`
                : `Multicast channel ${saved.label} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete multicast channel ${deleting.label}?`}
          description="Phone groups stop using it. A channel a paging group sends to cannot be deleted."
          confirmLabel="Delete channel"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </section>
  );
}

function ChannelModal({
  channel,
  onClose,
  onSaved,
}: {
  channel: CatalogChannel | null;
  onClose: () => void;
  onSaved: (channel: CatalogChannel, created: boolean) => void;
}) {
  const [d, setD] = useState<CatalogChannelInput>(() => {
    if (!channel) return emptyChannel();
    return {
      label: channel.label,
      address: channel.address,
      port: channel.port,
      codec: channel.codec,
      priority: channel.priority,
      kind: channel.kind,
      enabled: channel.enabled,
    };
  });
  const [errors, setErrors] = useState<ErrorMap>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [busy, setBusy] = useState(false);
  const set = <K extends keyof CatalogChannelInput>(
    k: K,
    v: CatalogChannelInput[K],
  ) => setD((prev) => ({ ...prev, [k]: v }));
  const title = channel ? `Edit ${channel.label}` : "New multicast channel";

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setErrors({});
    setFormError(null);
    setUnmatched([]);
    try {
      const input = { ...d, label: d.label.trim(), address: d.address.trim() };
      const saved = channel
        ? await updateMulticastChannel(channel.id, input)
        : await createMulticastChannel(input);
      onSaved(saved, channel === null);
    } catch (err) {
      const { byKey, unmatched: rest } = mapFieldErrors(fieldErrors(err), [
        ...FIELDS,
      ]);
      setErrors(byKey);
      setUnmatched(rest);
      if (Object.keys(byKey).length === 0) setFormError(errorMessage(err));
      setBusy(false);
    }
  }

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
          <Button type="submit" form="mc-form" disabled={busy}>
            {channel ? "Save" : "Create channel"}
          </Button>
        </>
      }
    >
      <form
        id="mc-form"
        className="cf-form"
        aria-label={title}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <Input
          id="mc-label"
          label="Name"
          autoFocus
          hint="Shown on the phone, up to 40 characters"
          value={d.label}
          error={errors.label}
          onChange={(e) => set("label", e.target.value)}
        />
        <Select
          id="mc-kind"
          label="Kind"
          value={d.kind}
          error={errors.kind}
          options={[
            { value: "paging", label: "Paging (phones listen, groups page)" },
            { value: "ptt", label: "Push-to-talk (Grandstream WP8x6)" },
          ]}
          onChange={(e) => {
            const kind = e.target.value as ChannelKind;
            setD((prev) => ({
              ...prev,
              kind,
              codec:
                kind === "ptt" && prev.codec === "PCMA" ? "PCMU" : prev.codec,
            }));
          }}
        />
        <div className="cf-two">
          <Input
            id="mc-address"
            label="Multicast address"
            mono
            hint="IPv4 multicast outside 224.0.0.0/24, such as 239.255.0.1"
            value={d.address}
            error={errors.address}
            onChange={(e) => set("address", e.target.value)}
          />
          <Input
            id="mc-port"
            label="Port"
            mono
            inputMode="numeric"
            hint="1024 to 65535"
            value={String(d.port)}
            error={errors.port}
            onChange={(e) =>
              set("port", Number(e.target.value.replace(/\D/g, "")))
            }
          />
        </div>
        <div className="cf-two">
          <Select
            id="mc-codec"
            label="Codec"
            value={d.codec}
            error={errors.codec}
            hint={
              d.kind === "ptt"
                ? "PTT takes PCMU or G.722"
                : "A paging group sends only to PCMU or PCMA"
            }
            options={[
              { value: "PCMU", label: "PCMU (G.711 µ-law)" },
              ...(d.kind === "ptt"
                ? []
                : [{ value: "PCMA", label: "PCMA (G.711 A-law)" }]),
              { value: "G722", label: "G.722" },
            ]}
            onChange={(e) => set("codec", e.target.value as MulticastCodec)}
          />
          <Input
            id="mc-priority"
            label="Priority"
            inputMode="numeric"
            hint="1 (highest) to 25"
            value={String(d.priority)}
            error={errors.priority}
            onChange={(e) =>
              set("priority", Number(e.target.value.replace(/\D/g, "")))
            }
          />
        </div>
        <Switch
          label="Enabled"
          hint="A disabled channel is not given to phones or paged"
          labelPosition="end"
          checked={d.enabled}
          onChange={(e) => set("enabled", e.target.checked)}
        />
        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Modal>
  );
}
