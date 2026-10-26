import { useEffect, useState } from "react";
import { errorMessage, type Id } from "../../api";
import {
  listGroups,
  listMulticastChannels,
  type CatalogChannel,
  type Group,
} from "../../api/phonebook";
import { updatePhone, type Phone } from "../../api/prov";
import { Alert, Button, Select } from "../../design/azrty/components";
import { Can, useCan } from "../../role";
import { effectivePtt } from "../phonebooks/GroupPaging";

/**
 * Grandstream WP8x6, WP856, WP820 and WP810/822/825: the phones Hello gives
 * push-to-talk (the server's prov.WPFamily).
 */
export function hasPttKey(phone: Pick<Phone, "vendor" | "model">): boolean {
  return (
    phone.vendor === "grandstream" &&
    /^WP8([123]6|56|20|10|22|25)/i.test(phone.model.replace(/\s/g, ""))
  );
}

/**
 * The phone's push-to-talk channel: the group's (or the catalog default)
 * unless the phone overrides it with a catalog PTT channel.
 */
export function PttOverride({
  phone,
  onSaved,
}: {
  phone: Phone;
  onSaved: (phone: Phone) => void;
}) {
  const [catalog, setCatalog] = useState<CatalogChannel[]>([]);
  const [group, setGroup] = useState<Group | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [choice, setChoice] = useState(
    phone.pttChannelId == null ? "" : String(phone.pttChannelId),
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const admin = useCan("admin");

  useEffect(() => {
    const controller = new AbortController();
    listMulticastChannels(controller.signal)
      .then(setCatalog)
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setLoadError(errorMessage(err));
      });
    if (phone.groupId != null) {
      listGroups(controller.signal)
        .then((gs) =>
          setGroup(
            gs.find((g) => String(g.id) === String(phone.groupId)) ?? null,
          ),
        )
        .catch(() => undefined);
    }
    return () => controller.abort();
  }, [phone.groupId]);

  const groupPtt = group?.ptt;
  const pttOff = groupPtt ? !groupPtt.enabled : false;
  const override: Id | null = choice === "" ? null : Number(choice);
  const eff = effectivePtt(catalog, override, groupPtt?.channelId);
  const saved = phone.pttChannelId == null ? "" : String(phone.pttChannelId);

  async function onSave() {
    setBusy(true);
    setError(null);
    try {
      onSaved(await updatePhone(phone.id, { pttChannelId: override }));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="prov-stack">
      <h2 className="inv-section-title">Push-to-talk</h2>
      <p className="prov-muted">
        The PTT key talks on this multicast channel. Without an override the
        phone uses its phone group&apos;s PTT channel, or else the first enabled
        PTT channel under Paging &gt; Multicast channels. The phone picks up a
        change at its next resync.
      </p>
      {loadError && (
        <Alert tone="warn" title="Could not load the multicast channels">
          {loadError}
        </Alert>
      )}
      {pttOff && (
        <Alert tone="info" title="PTT is off for this phone's group">
          Turn it on in the phone group&apos;s push-to-talk settings.
        </Alert>
      )}
      <Select
        id="phone-ptt-channel"
        label="PTT channel"
        value={choice}
        disabled={!admin || busy}
        options={[
          { value: "", label: "Group's channel (or the catalog default)" },
          ...catalog
            .filter(
              (c) => c.kind === "ptt" && (c.enabled || String(c.id) === saved),
            )
            .map((c) => ({
              value: String(c.id),
              label: `${c.label} (${c.address}:${String(c.port)}, ${c.codec})${c.enabled ? "" : " · disabled"}`,
            })),
        ]}
        onChange={(e) => setChoice(e.target.value)}
      />
      <p className="prov-muted" data-testid="phone-ptt-effective">
        Talks on <span className="cf-mono">{eff.address}</span> ({eff.codec},{" "}
        {eff.label})
      </p>
      {error && (
        <Alert tone="bad" title="Could not save the PTT channel">
          {error}
        </Alert>
      )}
      <Can min="admin">
        <div className="prov-row">
          <Button
            variant="secondary"
            disabled={busy || choice === saved}
            onClick={() => void onSave()}
          >
            Save PTT channel
          </Button>
        </div>
      </Can>
    </div>
  );
}
