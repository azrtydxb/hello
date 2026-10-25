import {
  type MulticastCodec,
  type MulticastInput,
  type MulticastRole,
  type Ptt,
  type PttChannel,
} from "../../api/phonebook";
import {
  Alert,
  Button,
  IconButton,
  Input,
  Select,
  Switch,
} from "../../design/azrty/components";

/** Field errors by path, as the API reports them ("channels[0].address"). */
export type PathErrors = Record<string, string>;

export const MAX_MULTICAST_CHANNELS = 10;
export const MAX_PTT_CHANNELS = 25;
export const DEFAULT_PTT_ADDRESS = "224.0.1.116:5001";

export const emptyMulticast = (): MulticastInput => ({
  label: "",
  address: "239.255.0.1",
  port: 5004,
  codec: "PCMU",
  role: "both",
  priority: 1,
});

export const emptyPtt = (): Ptt => ({
  enabled: false,
  address: DEFAULT_PTT_ADDRESS,
  codec: "PCMU",
  defaultChannel: 1,
  acceptWhileBusy: false,
  channels: [],
});

/**
 * The group's multicast paging channels: every phone of the group listens on
 * the channels that listen and sends to those that send. Hello is not in the
 * audio path, so it works only where multicast is delivered (one LAN); a
 * paging group that names a channel also sends its page there.
 */
export function MulticastEditor({
  channels,
  errors,
  onChange,
}: {
  channels: MulticastInput[];
  errors: PathErrors;
  onChange: (channels: MulticastInput[]) => void;
}) {
  const set = (i: number, patch: Partial<MulticastInput>) =>
    onChange(channels.map((c, j) => (j === i ? { ...c, ...patch } : c)));
  return (
    <fieldset className="pb-numbers-editor">
      <legend className="az-field__label">Multicast paging channels</legend>
      <Alert tone="info">
        Phone-to-phone multicast on one LAN: Hello only tells the phones where
        to listen and send. Multicast has no authentication, and it does not
        cross routers unless they forward it.
      </Alert>
      {errors.channels && (
        <p className="az-field__error" role="alert">
          {errors.channels}
        </p>
      )}
      {channels.length === 0 && <p className="prov-muted">No channels.</p>}
      {channels.map((c, i) => {
        const p = (f: string) => errors[`channels[${i}].${f}`];
        return (
          <div
            key={i}
            className="prov-row"
            role="group"
            aria-label={`Channel ${i + 1}`}
          >
            <Input
              id={`mc-label-${i}`}
              label={`Label ${i + 1}`}
              size="sm"
              error={p("label")}
              value={c.label}
              onChange={(e) => set(i, { label: e.target.value })}
            />
            <Input
              id={`mc-address-${i}`}
              label={`Address ${i + 1}`}
              size="sm"
              mono
              error={p("address")}
              value={c.address}
              onChange={(e) => set(i, { address: e.target.value })}
            />
            <Input
              id={`mc-port-${i}`}
              label={`Port ${i + 1}`}
              size="sm"
              mono
              inputMode="numeric"
              error={p("port")}
              value={String(c.port)}
              onChange={(e) =>
                set(i, { port: Number(e.target.value.replace(/\D/g, "")) })
              }
            />
            <Select
              id={`mc-codec-${i}`}
              label={`Codec ${i + 1}`}
              size="sm"
              error={p("codec")}
              value={c.codec}
              options={[
                { value: "PCMU", label: "PCMU (G.711 µ-law)" },
                { value: "PCMA", label: "PCMA (G.711 A-law)" },
                { value: "G722", label: "G.722" },
              ]}
              onChange={(e) =>
                set(i, { codec: e.target.value as MulticastCodec })
              }
            />
            <Select
              id={`mc-role-${i}`}
              label={`Role ${i + 1}`}
              size="sm"
              error={p("role")}
              value={c.role}
              options={[
                { value: "both", label: "Listen and send" },
                { value: "listen", label: "Listen" },
                { value: "send", label: "Send" },
              ]}
              onChange={(e) =>
                set(i, { role: e.target.value as MulticastRole })
              }
            />
            <Input
              id={`mc-priority-${i}`}
              label={`Priority ${i + 1}`}
              size="sm"
              inputMode="numeric"
              error={p("priority")}
              value={String(c.priority)}
              onChange={(e) =>
                set(i, { priority: Number(e.target.value.replace(/\D/g, "")) })
              }
            />
            <IconButton
              icon="x"
              label={`Remove channel ${i + 1}`}
              size={14}
              onClick={() => onChange(channels.filter((_, j) => j !== i))}
            />
          </div>
        );
      })}
      <Button
        variant="secondary"
        size="sm"
        icon="plus"
        disabled={channels.length >= MAX_MULTICAST_CHANNELS}
        onClick={() => onChange([...channels, emptyMulticast()])}
      >
        Add channel
      </Button>
    </fieldset>
  );
}

/**
 * Push-to-talk of the group's Grandstream WP8x6 phones: one multicast
 * address, channels 1 to 25 by number. The WP822 has only multicast paging.
 */
export function PttEditor({
  ptt,
  errors,
  onChange,
}: {
  ptt: Ptt;
  errors: PathErrors;
  onChange: (ptt: Ptt) => void;
}) {
  const set = (patch: Partial<Ptt>) => onChange({ ...ptt, ...patch });
  const setChannel = (i: number, patch: Partial<PttChannel>) =>
    set({
      channels: ptt.channels.map((c, j) => (j === i ? { ...c, ...patch } : c)),
    });
  const nextNumber = () => {
    for (let n = 1; n <= MAX_PTT_CHANNELS; n++) {
      if (!ptt.channels.some((c) => c.channel === n)) return n;
    }
    return 0;
  };
  return (
    <fieldset className="pb-numbers-editor">
      <legend className="az-field__label">
        Push-to-talk (Grandstream WP8x6)
      </legend>
      <Switch
        label="Push-to-talk"
        hint="The side key talks on the default channel; phones of the group hear the channels they subscribe to"
        labelPosition="end"
        checked={ptt.enabled}
        onChange={(e) => set({ enabled: e.target.checked })}
      />
      {ptt.enabled && (
        <>
          <div className="prov-row">
            <Input
              id="ptt-address"
              label="PTT address"
              size="sm"
              mono
              error={errors.address}
              value={ptt.address}
              onChange={(e) => set({ address: e.target.value })}
            />
            <Select
              id="ptt-codec"
              label="PTT codec"
              size="sm"
              error={errors.codec}
              value={ptt.codec}
              options={[
                { value: "PCMU", label: "PCMU" },
                { value: "G722", label: "G.722" },
              ]}
              onChange={(e) => set({ codec: e.target.value as Ptt["codec"] })}
            />
            <Input
              id="ptt-default"
              label="Default channel"
              size="sm"
              inputMode="numeric"
              hint="1, 2, 24 or 25"
              error={errors.defaultChannel}
              value={String(ptt.defaultChannel)}
              onChange={(e) =>
                set({
                  defaultChannel: Number(e.target.value.replace(/\D/g, "")),
                })
              }
            />
          </div>
          <Switch
            label="Accept while busy"
            hint="Take a PTT while in a call"
            labelPosition="end"
            checked={ptt.acceptWhileBusy}
            onChange={(e) => set({ acceptWhileBusy: e.target.checked })}
          />
          {errors.channels && (
            <p className="az-field__error" role="alert">
              {errors.channels}
            </p>
          )}
          {ptt.channels.map((c, i) => {
            const p = (f: string) => errors[`channels[${i}].${f}`];
            return (
              <div
                key={i}
                className="prov-row"
                role="group"
                aria-label={`PTT channel ${c.channel}`}
              >
                <Input
                  id={`ptt-channel-${i}`}
                  label={`Channel number ${i + 1}`}
                  size="sm"
                  inputMode="numeric"
                  error={p("channel")}
                  value={String(c.channel)}
                  onChange={(e) =>
                    setChannel(i, {
                      channel: Number(e.target.value.replace(/\D/g, "")),
                    })
                  }
                />
                <Input
                  id={`ptt-label-${i}`}
                  label={`Channel name ${i + 1}`}
                  size="sm"
                  error={p("label")}
                  value={c.label}
                  onChange={(e) => setChannel(i, { label: e.target.value })}
                />
                <Switch
                  label={`Transmit ${i + 1}`}
                  labelPosition="end"
                  checked={c.transmit}
                  onChange={(e) =>
                    setChannel(i, { transmit: e.target.checked })
                  }
                />
                <Switch
                  label={`Subscribe ${i + 1}`}
                  labelPosition="end"
                  checked={c.subscribe}
                  onChange={(e) =>
                    setChannel(i, { subscribe: e.target.checked })
                  }
                />
                <IconButton
                  icon="x"
                  label={`Remove PTT channel ${c.channel}`}
                  size={14}
                  onClick={() =>
                    set({ channels: ptt.channels.filter((_, j) => j !== i) })
                  }
                />
              </div>
            );
          })}
          <Button
            variant="secondary"
            size="sm"
            icon="plus"
            disabled={ptt.channels.length >= MAX_PTT_CHANNELS}
            onClick={() =>
              set({
                channels: [
                  ...ptt.channels,
                  {
                    channel: nextNumber(),
                    label: "",
                    transmit: true,
                    subscribe: true,
                  },
                ],
              })
            }
          >
            Add PTT channel
          </Button>
        </>
      )}
    </fieldset>
  );
}
