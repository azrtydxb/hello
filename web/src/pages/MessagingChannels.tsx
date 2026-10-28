import { useEffect, useState, type FormEvent } from "react";
import { errorMessage, fieldErrors, listTrunks, type Trunk } from "../api";
import {
  createAddress,
  createChannel,
  deleteAddress,
  deleteChannel,
  listAddresses,
  listChannels,
  testChannel,
  updateAddress,
  updateChannel,
  type ChannelTest,
  type MessagingAddress,
  type MessagingChannel,
} from "../api/inbox";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  Input,
  Modal,
  PageHeader,
  Select,
  Spinner,
  Switch,
  Table,
  useRestoreFocus,
  useToast,
} from "../design/azrty/components";
import { formatTime } from "../format";

type Data = {
  channels: MessagingChannel[];
  addresses: MessagingAddress[];
  trunks: Trunk[];
};

function ChannelDialog({
  channel,
  trunks,
  onClose,
  onSaved,
}: {
  channel: MessagingChannel | null;
  trunks: Trunk[];
  onClose: () => void;
  onSaved: (c: MessagingChannel) => void;
}) {
  useRestoreFocus();
  const [name, setName] = useState(channel?.name ?? "");
  const [trunkId, setTrunkId] = useState(
    channel?.trunkId ? String(channel.trunkId) : "",
  );
  const [enabled, setEnabled] = useState(channel?.enabled ?? true);
  const [error, setError] = useState<string | null>(null);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const chosen = trunks.find((t) => String(t.id) === trunkId);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setFields({});
    try {
      const body = {
        name: name.trim(),
        enabled,
        trunkId: trunkId ? Number(trunkId) : undefined,
      };
      onSaved(
        channel
          ? await updateChannel(channel.id, body)
          : await createChannel({
              ...body,
              kind: "sms",
              provider: "sip_trunk",
            }),
      );
    } catch (err: unknown) {
      setFields(
        Object.fromEntries(fieldErrors(err).map((f) => [f.path, f.message])),
      );
      setError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Modal
      title={channel ? `Edit ${channel.name}` : "Add a messaging channel"}
      description="A channel reads customer texts from a SIP trunk that carries SMS as SIP MESSAGE, and sends replies through it. It works only where your carrier supports that."
      onClose={onClose}
      actions={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="channel-form" disabled={busy}>
            {channel ? "Save channel" : "Add channel"}
          </Button>
        </>
      }
    >
      {error && (
        <Alert tone="bad" title="Could not save the channel">
          {error}
        </Alert>
      )}
      <form id="channel-form" className="cf-stack" onSubmit={save}>
        <Input
          label="Name"
          value={name}
          error={fields.name}
          autoFocus
          onChange={(e) => setName(e.target.value)}
        />
        <Select
          label="SIP trunk"
          value={trunkId}
          error={fields.trunkId}
          options={[
            { value: "", label: "Choose a trunk…" },
            ...trunks.map((t) => ({
              value: String(t.id),
              label: t.messaging ? t.name : `${t.name} (SMS over SIP is off)`,
            })),
          ]}
          onChange={(e) => setTrunkId(e.target.value)}
        />
        {chosen && !chosen.messaging && (
          <Alert tone="warn" title="SMS over SIP is off on this trunk">
            Turn it on in the trunk's settings, or no text arrives.
          </Alert>
        )}
        <Switch
          label="Channel on"
          hint="Off stops new texts and replies; the numbers keep their history."
          labelPosition="end"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
      </form>
    </Modal>
  );
}

function AddressDialog({
  address,
  channels,
  onClose,
  onSaved,
}: {
  address: MessagingAddress | null;
  channels: MessagingChannel[];
  onClose: () => void;
  onSaved: (a: MessagingAddress) => void;
}) {
  useRestoreFocus();
  const [channelId, setChannelId] = useState(
    String(address?.channelId ?? channels[0]?.id ?? ""),
  );
  const [number, setNumber] = useState(address?.address ?? "");
  const [label, setLabel] = useState(address?.label ?? "");
  const [enabled, setEnabled] = useState(address?.enabled ?? true);
  const [words, setWords] = useState(address?.optOutKeywords.join(", ") ?? "");
  const [help, setHelp] = useState(address?.helpText ?? "");
  const [optOut, setOptOut] = useState(address?.optOutText ?? "");
  const [error, setError] = useState<string | null>(null);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setFields({});
    try {
      const body = {
        label: label.trim(),
        enabled,
        optOutKeywords: words
          .split(",")
          .map((w) => w.trim())
          .filter(Boolean),
        helpText: help.trim(),
        optOutText: optOut.trim(),
      };
      onSaved(
        address
          ? await updateAddress(address.id, body)
          : await createAddress({
              ...body,
              channelId: Number(channelId),
              address: number.trim(),
            }),
      );
    } catch (err: unknown) {
      setFields(
        Object.fromEntries(fieldErrors(err).map((f) => [f.path, f.message])),
      );
      setError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Modal
      title={address ? `Edit ${address.address}` : "Add a number"}
      description="Texts a trunk delivers to this number become conversations in the inbox instead of going to an extension."
      onClose={onClose}
      actions={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="address-form" disabled={busy}>
            {address ? "Save number" : "Add number"}
          </Button>
        </>
      }
    >
      {error && (
        <Alert tone="bad" title="Could not save the number">
          {error}
        </Alert>
      )}
      <form id="address-form" className="cf-stack" onSubmit={save}>
        {!address && (
          <>
            <Select
              label="Channel"
              value={channelId}
              error={fields.channelId}
              options={channels.map((c) => ({
                value: String(c.id),
                label: c.name,
              }))}
              onChange={(e) => setChannelId(e.target.value)}
            />
            <Input
              label="Number"
              hint="The number customers text, as the carrier dials it to you, such as +3270123456."
              value={number}
              error={fields.address}
              autoFocus
              onChange={(e) => setNumber(e.target.value)}
            />
          </>
        )}
        <Input
          label="Label"
          value={label}
          error={fields.label}
          onChange={(e) => setLabel(e.target.value)}
        />
        <Input
          label="Opt-out words"
          hint="One word as the whole text opts the customer out. Empty keeps stop, stopall, unsubscribe, cancel, end and quit."
          value={words}
          error={fields.optOutKeywords}
          onChange={(e) => setWords(e.target.value)}
        />
        <Input
          label="Answer to HELP"
          hint="Sent when a customer texts HELP. Empty sends nothing."
          value={help}
          error={fields.helpText}
          onChange={(e) => setHelp(e.target.value)}
        />
        <Input
          label="Answer to an opt-out"
          hint="Sent once when a customer opts out. Empty sends a neutral sentence."
          value={optOut}
          error={fields.optOutText}
          onChange={(e) => setOptOut(e.target.value)}
        />
        <Switch
          label="Number on"
          hint="Off hands texts to this number back to the normal extension route."
          labelPosition="end"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
      </form>
    </Modal>
  );
}

/**
 * Messaging channels and their numbers. A channel is a SIP trunk that carries
 * SMS as SIP MESSAGE; a number is one a customer texts. Administrators only.
 */
export function MessagingChannels() {
  const [data, setData] = useState<Data | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [editChannel, setEditChannel] = useState<
    MessagingChannel | "new" | null
  >(null);
  const [editAddress, setEditAddress] = useState<
    MessagingAddress | "new" | null
  >(null);
  const [delChannel, setDelChannel] = useState<MessagingChannel | null>(null);
  const [delAddress, setDelAddress] = useState<MessagingAddress | null>(null);
  const [test, setTest] = useState<{
    channel: MessagingChannel;
    result: ChannelTest;
  } | null>(null);
  const [reload, setReload] = useState(0);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    Promise.all([
      listChannels(controller.signal),
      listAddresses(controller.signal),
      listTrunks(controller.signal),
    ])
      .then(([channels, addresses, trunks]) => {
        setData({ channels, addresses, trunks });
        setError(null);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      });
    return () => controller.abort();
  }, [reload]);
  const refresh = () => setReload((n) => n + 1);

  async function runTest(c: MessagingChannel) {
    try {
      setTest({ channel: c, result: await testChannel(c.id) });
    } catch (err: unknown) {
      toast.show(`Could not test ${c.name}: ${errorMessage(err)}`);
    }
  }

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Messaging"
        title="Channels"
        description="Where customer texts come from. A channel reads texts from a SIP trunk that carries SMS as SIP MESSAGE and sends replies through it; each number a customer texts becomes conversations in the inbox. Texts through a carrier's web interface and WhatsApp are not available yet."
        actions={
          <Button icon="plus" onClick={() => setEditChannel("new")}>
            Add channel
          </Button>
        }
      />
      {error && (
        <Alert tone="bad" title="Could not load the channels">
          {error}
        </Alert>
      )}
      {!data && !error && <Spinner label="Loading channels…" />}
      {data && data.channels.length === 0 && (
        <EmptyState
          icon="radio-tower"
          title="No channels yet"
          description="Add a channel for a SIP trunk with SMS over SIP turned on, then add the numbers customers text."
        />
      )}
      {data && data.channels.length > 0 && (
        <div className="cf-stack">
          <Table
            caption="Channels"
            rows={data.channels}
            rowKey={(c) => c.id}
            columns={[
              { key: "name", label: "Channel", render: (c) => c.name },
              {
                key: "trunk",
                label: "SIP trunk",
                render: (c) => c.trunkName || "Deleted",
              },
              {
                key: "state",
                label: "State",
                render: (c) =>
                  c.enabled ? (
                    <Badge tone={c.status === "ok" ? "good" : "warn"}>
                      {c.status === "ok" ? "On" : c.status}
                    </Badge>
                  ) : (
                    <Badge tone="neutral">Off</Badge>
                  ),
              },
              {
                key: "numbers",
                label: "Numbers",
                align: "right",
                render: (c) => c.addresses,
              },
              {
                key: "last",
                label: "Last text",
                render: (c) => formatTime(c.lastEventAt),
              },
              {
                key: "actions",
                label: "",
                align: "right",
                render: (c) => (
                  <span className="inbox-actions">
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() => void runTest(c)}
                    >
                      Test {c.name}
                    </Button>
                    <Button
                      size="sm"
                      variant="secondary"
                      onClick={() => setEditChannel(c)}
                    >
                      Edit {c.name}
                    </Button>
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={() => setDelChannel(c)}
                    >
                      Delete {c.name}
                    </Button>
                  </span>
                ),
              },
            ]}
          />
          <PageHeader
            title="Numbers"
            description="Texts to a number go to the inbox. Customers opt out by texting a word such as STOP; Hello handles that itself, whatever the carrier does."
            actions={
              <Button
                icon="plus"
                variant="secondary"
                onClick={() => setEditAddress("new")}
              >
                Add number
              </Button>
            }
          />
          {data.addresses.length === 0 ? (
            <EmptyState
              icon="phone"
              title="No numbers yet"
              description="Add the number customers text. Until then the inbox stays hidden."
            />
          ) : (
            <Table
              caption="Numbers"
              rows={data.addresses}
              rowKey={(a) => a.id}
              columns={[
                {
                  key: "number",
                  label: "Number",
                  mono: true,
                  render: (a) => a.address,
                },
                { key: "label", label: "Label", render: (a) => a.label || "—" },
                {
                  key: "channel",
                  label: "Channel",
                  render: (a) => a.channelName,
                },
                {
                  key: "state",
                  label: "State",
                  render: (a) =>
                    a.enabled ? (
                      <Badge tone="good">On</Badge>
                    ) : (
                      <Badge tone="neutral">Off</Badge>
                    ),
                },
                {
                  key: "conversations",
                  label: "Conversations",
                  align: "right",
                  render: (a) => a.conversations,
                },
                {
                  key: "actions",
                  label: "",
                  align: "right",
                  render: (a) => (
                    <span className="inbox-actions">
                      <Button
                        size="sm"
                        variant="secondary"
                        onClick={() => setEditAddress(a)}
                      >
                        Edit {a.address}
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={() => setDelAddress(a)}
                      >
                        Delete {a.address}
                      </Button>
                    </span>
                  ),
                },
              ]}
            />
          )}
        </div>
      )}
      {editChannel && data && (
        <ChannelDialog
          channel={editChannel === "new" ? null : editChannel}
          trunks={data.trunks}
          onClose={() => setEditChannel(null)}
          onSaved={(c) => {
            setEditChannel(null);
            toast.show(`Channel ${c.name} saved.`);
            refresh();
          }}
        />
      )}
      {editAddress && data && (
        <AddressDialog
          address={editAddress === "new" ? null : editAddress}
          channels={data.channels}
          onClose={() => setEditAddress(null)}
          onSaved={(a) => {
            setEditAddress(null);
            toast.show(`Number ${a.address} saved.`);
            refresh();
          }}
        />
      )}
      {delChannel && (
        <ConfirmDialog
          title={`Delete channel ${delChannel.name}?`}
          description="Its numbers go with it. A channel that has conversations cannot be deleted: retention or an erase request removes them first."
          confirmLabel="Delete channel"
          onConfirm={async () => {
            await deleteChannel(delChannel.id);
            setDelChannel(null);
            toast.show(`Channel ${delChannel.name} deleted.`);
            refresh();
          }}
          onClose={() => setDelChannel(null)}
        />
      )}
      {delAddress && (
        <ConfirmDialog
          title={`Delete number ${delAddress.address}?`}
          description="Texts to it take the normal extension route again. A number that has conversations cannot be deleted."
          confirmLabel="Delete number"
          onConfirm={async () => {
            await deleteAddress(delAddress.id);
            setDelAddress(null);
            toast.show(`Number ${delAddress.address} deleted.`);
            refresh();
          }}
          onClose={() => setDelAddress(null)}
        />
      )}
      {test && (
        <Modal
          title={`Test of ${test.channel.name}`}
          description="Nothing is sent."
          onClose={() => setTest(null)}
          actions={<Button onClick={() => setTest(null)}>Close</Button>}
        >
          <Alert
            tone={test.result.ok ? "good" : "warn"}
            title={
              test.result.ok
                ? "The channel can work"
                : "The channel cannot work yet"
            }
          >
            <ul>
              {test.result.checks.map((c) => (
                <li key={c.name}>
                  {c.ok ? "Passed" : "Failed"}: {c.name}, {c.detail}
                </li>
              ))}
            </ul>
          </Alert>
        </Modal>
      )}
      {toast.node}
    </section>
  );
}
