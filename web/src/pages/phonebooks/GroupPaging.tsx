import {
  type CatalogChannel,
  MAX_GROUP_CHANNEL,
  MAX_PTT_CHANNEL,
  MIN_GROUP_CHANNEL,
  type MulticastInput,
  type MulticastRole,
  type PagingOptions,
  type Ptt,
  type PttChannel,
} from "../../api/phonebook";
import type { Id } from "../../api";
import {
  Alert,
  Button,
  IconButton,
  Input,
  Select,
  Switch,
} from "../../design/azrty/components";

/** Field errors by path, as the API reports them ("channels[0].channelId"). */
export type PathErrors = Record<string, string>;

export const MAX_MULTICAST_CHANNELS = 10;
/** Channels 1 to 25 (PTT) and 26 to 50 (group paging). */
export const MAX_PTT_CHANNELS = MAX_GROUP_CHANNEL;
export const DEFAULT_PTT_ADDRESS = "224.0.1.116:5001";

/**
 * A new group's PTT, as the API creates it: on, channel 1, priority 24,
 * emergency 25; group paging off (default 26, priority 49, emergency 50).
 */
export const emptyPtt = (): Ptt => ({
  enabled: true,
  channelId: null,
  defaultChannel: 1,
  priorityChannel: 24,
  emergencyChannel: 25,
  acceptWhileBusy: false,
  groupPaging: false,
  defaultGroup: 26,
  priorityGroup: 49,
  emergencyGroup: 50,
  channels: [
    { channel: 1, label: "Channel 1", transmit: true, subscribe: true },
  ],
});

const digits = (v: string) => Number(v.replace(/\D/g, ""));

const sameId = (a: Id | null | undefined, b: Id | null | undefined) =>
  a != null && b != null && String(a) === String(b);

/** "Label (239.255.0.1:5004, PCMU)". */
export const channelText = (c: {
  label: string;
  address: string;
  port: number;
  codec: string;
}) => `${c.label} (${c.address}:${String(c.port)}, ${c.codec})`;

/**
 * The PTT channel a phone or group gets: the chosen catalog channel when it
 * is enabled, else the catalog's first enabled PTT channel (by priority,
 * then id), else Grandstream's default.
 */
export function effectivePtt(
  catalog: CatalogChannel[],
  ...choices: Array<Id | null | undefined>
): { address: string; codec: string; label: string } {
  const ptts = catalog
    .filter((c) => c.kind === "ptt" && c.enabled)
    .sort((a, b) => a.priority - b.priority || Number(a.id) - Number(b.id));
  for (const id of choices) {
    const c = ptts.find((p) => sameId(p.id, id));
    if (c) {
      return {
        address: `${c.address}:${String(c.port)}`,
        codec: c.codec,
        label: c.label,
      };
    }
  }
  const first = ptts[0];
  return first
    ? {
        address: `${first.address}:${String(first.port)}`,
        codec: first.codec,
        label: first.label,
      }
    : {
        address: DEFAULT_PTT_ADDRESS,
        codec: "PCMU",
        label: "Grandstream default",
      };
}

/**
 * The catalog paging channels the group uses: every phone of the group
 * listens on the channels that listen and sends to those that send. With
 * none, the phones listen on every enabled paging channel. Hello is not in
 * the audio path, so it works only where multicast is delivered (one LAN).
 */
export function MulticastEditor({
  links,
  catalog,
  errors,
  onChange,
}: {
  links: MulticastInput[];
  catalog: CatalogChannel[];
  errors: PathErrors;
  onChange: (links: MulticastInput[]) => void;
}) {
  const paging = catalog.filter((c) => c.kind === "paging");
  const set = (i: number, patch: Partial<MulticastInput>) =>
    onChange(links.map((c, j) => (j === i ? { ...c, ...patch } : c)));
  const unused = paging.filter(
    (c) => !links.some((l) => sameId(l.channelId, c.id)),
  );
  return (
    <fieldset className="pb-numbers-editor">
      <legend className="az-field__label">Multicast paging channels</legend>
      <Alert tone="info">
        Pick channels from Paging &gt; Multicast channels. Phone-to-phone
        multicast on one LAN: Hello only tells the phones where to listen and
        send. Multicast has no authentication, and it does not cross routers
        unless they forward it.
      </Alert>
      {errors.channels && (
        <p className="az-field__error" role="alert">
          {errors.channels}
        </p>
      )}
      {links.length === 0 && (
        <p className="prov-muted">
          None picked: the phones listen on every enabled paging channel.
        </p>
      )}
      {links.map((l, i) => {
        const p = (f: string) => errors[`channels[${i}].${f}`];
        const choices = paging.filter(
          (c) =>
            sameId(c.id, l.channelId) ||
            !links.some((o) => sameId(o.channelId, c.id)),
        );
        return (
          <div
            key={i}
            className="prov-row"
            role="group"
            aria-label={`Channel ${i + 1}`}
          >
            <Select
              id={`mc-channel-${i}`}
              label={`Channel ${i + 1}`}
              size="sm"
              error={p("channelId")}
              value={String(l.channelId)}
              options={choices.map((c) => ({
                value: String(c.id),
                label: `${channelText(c)}${c.enabled ? "" : " — disabled"}`,
              }))}
              onChange={(e) => {
                const c = paging.find((x) => String(x.id) === e.target.value);
                if (c) set(i, { channelId: c.id });
              }}
            />
            <Select
              id={`mc-role-${i}`}
              label={`Role ${i + 1}`}
              size="sm"
              error={p("role")}
              value={l.role}
              options={[
                { value: "both", label: "Listen and send" },
                { value: "listen", label: "Listen" },
                { value: "send", label: "Send" },
              ]}
              onChange={(e) =>
                set(i, { role: e.target.value as MulticastRole })
              }
            />
            <IconButton
              icon="x"
              label={`Remove channel ${i + 1}`}
              size={14}
              onClick={() => onChange(links.filter((_, j) => j !== i))}
            />
          </div>
        );
      })}
      {paging.length === 0 ? (
        <p className="prov-muted">
          No paging channels yet: add them under Paging &gt; Multicast channels.
        </p>
      ) : (
        <Button
          variant="secondary"
          size="sm"
          icon="plus"
          disabled={
            links.length >= MAX_MULTICAST_CHANNELS || unused.length === 0
          }
          onClick={() => {
            const c = unused[0];
            if (c) onChange([...links, { channelId: c.id, role: "both" }]);
          }}
        >
          Add channel
        </Button>
      )}
    </fieldset>
  );
}

/**
 * How the group's phones take a multicast page and an intercom call
 * (Grandstream P8381, P1566, P1567, P22532, P26019; Yealink intercom barge).
 */
export function PagingOptionsEditor({
  options,
  errors,
  onChange,
}: {
  options: PagingOptions;
  errors: PathErrors;
  onChange: (options: PagingOptions) => void;
}) {
  const set = (patch: Partial<PagingOptions>) =>
    onChange({ ...options, ...patch });
  return (
    <fieldset className="pb-numbers-editor">
      <legend className="az-field__label">Paging options</legend>
      <Switch
        label="Play pages in Do Not Disturb"
        hint="Grandstream WP"
        labelPosition="end"
        checked={options.allowInDnd}
        onChange={(e) => set({ allowInDnd: e.target.checked })}
      />
      <Switch
        label="Priority active"
        hint="A higher-priority page interrupts a lower one"
        labelPosition="end"
        checked={options.priorityActive}
        onChange={(e) => set({ priorityActive: e.target.checked })}
      />
      <div className="prov-row">
        <Select
          id="paging-barge"
          label="Paging barge"
          size="sm"
          error={errors["options.barge"]}
          value={String(options.barge)}
          options={[
            { value: "0", label: "Off" },
            ...Array.from({ length: 10 }, (_, i) => ({
              value: String(i + 1),
              label: `Priority ${i + 1}${i === 0 ? " (highest)" : ""} or higher`,
            })),
          ]}
          onChange={(e) => set({ barge: Number(e.target.value) })}
        />
        <Input
          id="paging-timeout"
          label="Paging timeout (seconds)"
          size="sm"
          inputMode="numeric"
          hint="0 for none; Grandstream WP8x6"
          error={errors["options.timeout"]}
          value={String(options.timeout)}
          onChange={(e) => set({ timeout: digits(e.target.value) })}
        />
      </div>
      <Switch
        label="Intercom barging"
        hint="An intercom call cuts into a call in progress. Off: it waits like a second call."
        labelPosition="end"
        checked={options.intercomBarge}
        onChange={(e) => set({ intercomBarge: e.target.checked })}
      />
    </fieldset>
  );
}

/**
 * Push-to-talk and group paging of the group's Grandstream WP phones: one
 * multicast address, PTT channels 1 to 25 and group paging channels 26 to 50.
 */
export function PttEditor({
  ptt,
  catalog,
  errors,
  onChange,
}: {
  ptt: Ptt;
  catalog: CatalogChannel[];
  errors: PathErrors;
  onChange: (ptt: Ptt) => void;
}) {
  const ptts = catalog.filter((c) => c.kind === "ptt");
  const eff = effectivePtt(catalog, ptt.channelId);
  const set = (patch: Partial<Ptt>) => onChange({ ...ptt, ...patch });
  const setChannel = (i: number, patch: Partial<PttChannel>) =>
    set({
      channels: ptt.channels.map((c, j) => (j === i ? { ...c, ...patch } : c)),
    });
  const nextNumber = (from: number, to: number) => {
    for (let n = from; n <= to; n++) {
      if (!ptt.channels.some((c) => c.channel === n)) return n;
    }
    return 0;
  };
  const nextPtt = nextNumber(1, MAX_PTT_CHANNEL);
  const nextGroup = nextNumber(MIN_GROUP_CHANNEL, MAX_GROUP_CHANNEL);
  const add = (channel: number) =>
    set({
      channels: [
        ...ptt.channels,
        { channel, label: "", transmit: true, subscribe: true },
      ],
    });
  const num = (
    id: string,
    label: string,
    hint: string,
    field:
      | "defaultChannel"
      | "priorityChannel"
      | "emergencyChannel"
      | "defaultGroup"
      | "priorityGroup"
      | "emergencyGroup",
  ) => (
    <Input
      id={id}
      label={label}
      size="sm"
      inputMode="numeric"
      hint={hint}
      error={errors[field]}
      value={String(ptt[field])}
      onChange={(e) => set({ [field]: digits(e.target.value) })}
    />
  );
  const active = ptt.enabled || ptt.groupPaging;
  return (
    <fieldset className="pb-numbers-editor">
      <legend className="az-field__label">
        Push-to-talk and group paging (Grandstream WP)
      </legend>
      <Switch
        label="Push-to-talk"
        hint="The PTT key talks on the default channel; phones of the group hear the channels they subscribe to. Off leaves the PTT key unconfigured."
        labelPosition="end"
        checked={ptt.enabled}
        onChange={(e) => set({ enabled: e.target.checked })}
      />
      <Switch
        label="Group paging"
        hint="Channels 26 to 50 on the same address: the phone pages a group channel"
        labelPosition="end"
        checked={ptt.groupPaging}
        onChange={(e) => set({ groupPaging: e.target.checked })}
      />
      {active && (
        <>
          <Select
            id="ptt-channel"
            label="PTT channel"
            size="sm"
            error={errors.channelId}
            value={ptt.channelId == null ? "" : String(ptt.channelId)}
            options={[
              { value: "", label: "Catalog default (first PTT channel)" },
              ...ptts.map((c) => ({
                value: String(c.id),
                label: `${channelText(c)}${c.enabled ? "" : " — disabled"}`,
              })),
            ]}
            onChange={(e) => {
              const c = ptts.find((x) => String(x.id) === e.target.value);
              set({ channelId: c ? c.id : null });
            }}
          />
          {ptt.enabled && (
            <div className="prov-row">
              {num(
                "ptt-default",
                "Default channel",
                "1, 2, 24 or 25",
                "defaultChannel",
              )}
              {num(
                "ptt-priority",
                "Priority channel",
                "1 to 25",
                "priorityChannel",
              )}
              {num(
                "ptt-emergency",
                "Emergency channel",
                "1 to 25",
                "emergencyChannel",
              )}
            </div>
          )}
          {ptt.groupPaging && (
            <div className="prov-row">
              {num(
                "group-default",
                "Default group",
                "26, 27, 49 or 50",
                "defaultGroup",
              )}
              {num(
                "group-priority",
                "Priority group",
                "26 to 50",
                "priorityGroup",
              )}
              {num(
                "group-emergency",
                "Emergency group",
                "26 to 50",
                "emergencyGroup",
              )}
            </div>
          )}
          <p className="prov-muted" data-testid="ptt-effective">
            Phones talk and listen on{" "}
            <span className="cf-mono">{eff.address}</span> ({eff.codec},{" "}
            {eff.label}). A phone can override the channel on its own page.
          </p>
          <Switch
            label="Accept while busy"
            hint="Take a PTT or group page while in a call"
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
                aria-label={`${c.channel >= MIN_GROUP_CHANNEL ? "Group" : "PTT"} channel ${c.channel}`}
              >
                <Input
                  id={`ptt-channel-${i}`}
                  label={`Channel number ${i + 1}`}
                  size="sm"
                  inputMode="numeric"
                  error={p("channel")}
                  value={String(c.channel)}
                  onChange={(e) =>
                    setChannel(i, { channel: digits(e.target.value) })
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
                  label={`Remove ${c.channel >= MIN_GROUP_CHANNEL ? "group" : "PTT"} channel ${c.channel}`}
                  size={14}
                  onClick={() =>
                    set({ channels: ptt.channels.filter((_, j) => j !== i) })
                  }
                />
              </div>
            );
          })}
          <div className="prov-row">
            {ptt.enabled && (
              <Button
                variant="secondary"
                size="sm"
                icon="plus"
                disabled={nextPtt === 0}
                onClick={() => add(nextPtt)}
              >
                Add PTT channel
              </Button>
            )}
            {ptt.groupPaging && (
              <Button
                variant="secondary"
                size="sm"
                icon="plus"
                disabled={nextGroup === 0}
                onClick={() => add(nextGroup)}
              >
                Add group channel
              </Button>
            )}
          </div>
        </>
      )}
    </fieldset>
  );
}
