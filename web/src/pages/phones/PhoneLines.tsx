import { useEffect, useState } from "react";
import { errorMessage, listExtensions, type Extension } from "../../api";
import {
  clearPhoneLine,
  setPhoneLine,
  updatePhone,
  type Phone,
  type PhoneLine,
  type SharingRole,
} from "../../api/prov";
import {
  Alert,
  Badge,
  Button,
  Select,
  Table,
  type TableColumn,
} from "../../design/azrty/components";
import { Can, useCan } from "../../role";

/** Grandstream WP8x6: the only phones with account sharing (P22521, P22522). */
export function hasAccountSharing(
  phone: Pick<Phone, "vendor" | "model">,
): boolean {
  return (
    phone.vendor === "grandstream" &&
    /^WP8[123]6/i.test(phone.model.replace(/\s/g, ""))
  );
}

/** The lines a phone can still be given: the numbers above its last bound line up to the model's count. */
export function freeLineNumbers(phone: Pick<Phone, "lines" | "maxLines">) {
  const used = new Set((phone.lines ?? []).map((l) => l.line));
  const out: number[] = [];
  for (let n = 2; n <= (phone.maxLines ?? 1); n++) {
    if (!used.has(n)) out.push(n);
  }
  return out;
}

/**
 * The phone's lines (spec S-10): line 1 is the phone's own binding; lines 2
 * and up are further devices, each of an extension, that the phone
 * registers as further SIP accounts. Several phones may each have a line on
 * the same extension (a shared line appearance).
 */
export function PhoneLines({
  phone,
  onChanged,
}: {
  phone: Phone;
  onChanged: (phone: Phone) => void;
}) {
  const lines = phone.lines ?? [];
  const free = freeLineNumbers(phone);
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [line, setLine] = useState("");
  const [extension, setExtension] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const canWrite = useCan("operator");

  const canBind = free.length > 0 && lines.length > 0;

  useEffect(() => {
    if (!canBind) return;
    const controller = new AbortController();
    listExtensions(controller.signal)
      .then(setExtensions)
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      });
    return () => controller.abort();
  }, [canBind]);

  const taken = new Set(lines.map((l) => String(l.extensionId)));
  const choices = extensions.filter((e) => !taken.has(String(e.id)));
  const lineChoice =
    line !== "" ? line : free[0] != null ? String(free[0]) : "";

  async function run(action: () => Promise<Phone>) {
    setBusy(true);
    setError(null);
    try {
      onChanged(await action());
      setExtension("");
      setLine("");
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const columns: TableColumn<PhoneLine>[] = [
    { key: "line", label: "Line", render: (l) => `Line ${String(l.line)}` },
    {
      key: "extension",
      label: "Extension",
      render: (l) => `${l.extensionNumber} ${l.extensionName}`.trim(),
    },
    {
      key: "device",
      label: "Device",
      render: (l) => <span className="cf-mono">{l.sipUsername}</span>,
    },
    {
      key: "state",
      label: "Provisioned",
      render: (l) =>
        l.provisioned ? (
          <Badge tone="good">Written to the phone</Badge>
        ) : (
          <Badge tone="warn">Held, not written</Badge>
        ),
    },
    {
      key: "actions",
      label: "Actions",
      render: (l) =>
        l.line > 1 && canWrite ? (
          <Button
            variant="secondary"
            size="sm"
            icon="trash-2"
            disabled={busy}
            onClick={() => void run(() => clearPhoneLine(phone.id, l.line))}
          >
            {`Unbind line ${String(l.line)}`}
          </Button>
        ) : null,
    },
  ];

  return (
    <div className="prov-stack">
      <h2 className="inv-section-title">Lines</h2>
      <p className="prov-muted">
        This model takes {String(phone.maxLines ?? 1)} line
        {(phone.maxLines ?? 1) === 1 ? "" : "s"}. Each line is a device of an
        extension that the phone registers as its own SIP account, with its own
        voicemail light, BLF and ring tags. To let several phones answer one
        extension, give each phone a line on it: a call to the extension rings
        all of them.
      </p>
      {lines.some((l) => !l.provisioned) && (
        <Alert tone="info" title="Some lines are held, not written">
          This model's template does not write further lines. The line is kept
          here and reaches the phone when the model's template does.
        </Alert>
      )}
      {error && (
        <Alert tone="bad" title="Could not change the lines">
          {error}
        </Alert>
      )}
      {lines.length === 0 ? (
        <p className="prov-muted">Line 1 is not bound.</p>
      ) : (
        <Table
          caption="Phone lines"
          columns={columns}
          rows={lines}
          rowKey={(l) => String(l.line)}
        />
      )}
      {canBind && (
        <Can>
          <div className="prov-row">
            <Select
              id="phone-line-number"
              label="Line"
              value={lineChoice}
              disabled={busy}
              options={free.map((n) => ({
                value: String(n),
                label: `Line ${String(n)}`,
              }))}
              onChange={(e) => setLine(e.target.value)}
            />
            <Select
              id="phone-line-extension"
              label="Extension"
              value={extension}
              disabled={busy}
              options={[
                { value: "", label: "Choose…" },
                ...choices.map((e) => ({
                  value: String(e.id),
                  label: `${e.number} ${e.name}`.trim(),
                })),
              ]}
              onChange={(e) => setExtension(e.target.value)}
            />
            <Button
              variant="secondary"
              icon="plus"
              disabled={busy || extension === "" || lineChoice === ""}
              onClick={() =>
                void run(() =>
                  setPhoneLine(phone.id, Number(lineChoice), {
                    extensionId: Number(extension),
                  }),
                )
              }
            >
              Bind line
            </Button>
          </div>
        </Can>
      )}
    </div>
  );
}

/**
 * Account sharing (spec S-11): in a phone group with it switched on, a
 * WP8x6 is the host of the shared line or a guest on it.
 */
export function SharingRoleField({
  phone,
  onSaved,
}: {
  phone: Phone;
  onSaved: (phone: Phone) => void;
}) {
  const saved: SharingRole = phone.sharingRole ?? "host";
  const [role, setRole] = useState<SharingRole>(saved);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function onSave() {
    setBusy(true);
    setError(null);
    try {
      onSaved(await updatePhone(phone.id, { sharingRole: role }));
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="prov-stack">
      <h2 className="inv-section-title">Account sharing</h2>
      <p className="prov-muted">
        When the phone&apos;s phone group has account sharing on, several
        handsets share one line: this phone is the host that holds it, or a
        guest on it. Without the group setting this is not written. The phone
        picks up a change at its next resync.
      </p>
      {error && (
        <Alert tone="bad" title="Could not save the role">
          {error}
        </Alert>
      )}
      <Select
        id="phone-sharing-role"
        label="Role"
        value={role}
        disabled={busy}
        options={[
          { value: "host", label: "Host" },
          { value: "guest", label: "Guest" },
        ]}
        onChange={(e) => setRole(e.target.value as SharingRole)}
      />
      <Can>
        <div className="prov-row">
          <Button
            variant="secondary"
            disabled={busy || role === saved}
            onClick={() => void onSave()}
          >
            Save role
          </Button>
        </div>
      </Can>
    </div>
  );
}
