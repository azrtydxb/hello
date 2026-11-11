import { useEffect, useState, type FormEvent } from "react";
import { errorMessage, fieldErrors } from "../api";
import {
  listAddresses,
  listOptOuts,
  reconsentOptOut,
  recordOptOut,
  type MessagingAddress,
  type OptOut,
} from "../api/inbox";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  EmptyState,
  Input,
  Modal,
  PageHeader,
  Select,
  Spinner,
  Table,
  useRestoreFocus,
  useToast,
} from "../design/azrty/components";
import { formatTime } from "../format";

const SOURCES: Record<OptOut["source"], string> = {
  keyword: "Texted a stop word",
  provider: "Reported by the carrier",
  admin: "Recorded by an administrator",
  customer: "Texted START",
};

function RecordDialog({
  addresses,
  onClose,
  onSaved,
}: {
  addresses: MessagingAddress[];
  onClose: () => void;
  onSaved: () => void;
}) {
  useRestoreFocus();
  const [addressId, setAddressId] = useState(String(addresses[0]?.id ?? ""));
  const [peer, setPeer] = useState("");
  const [note, setNote] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await recordOptOut({
        addressId: Number(addressId),
        peer: peer.trim(),
        note,
      });
      onSaved();
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
      title="Record an opt-out"
      description="For a customer who asked another way, such as by phone. Nobody can text them from this number until they text START or you record their consent again."
      onClose={onClose}
      actions={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="optout-form" disabled={busy}>
            Record opt-out
          </Button>
        </>
      }
    >
      {error && (
        <Alert tone="bad" title="Could not record the opt-out">
          {error}
        </Alert>
      )}
      <form id="optout-form" className="cf-stack" onSubmit={save}>
        <Select
          label="Our number"
          value={addressId}
          error={fields.addressId}
          options={addresses.map((a) => ({
            value: String(a.id),
            label: a.label ? `${a.address} (${a.label})` : a.address,
          }))}
          onChange={(e) => setAddressId(e.target.value)}
        />
        <Input
          label="Customer's number"
          value={peer}
          error={fields.peer}
          autoFocus
          onChange={(e) => setPeer(e.target.value)}
        />
        <Input
          label="Note"
          hint="Why, in a few words. It is kept with the opt-out."
          value={note}
          error={fields.note}
          onChange={(e) => setNote(e.target.value)}
        />
      </form>
    </Modal>
  );
}

function ReconsentDialog({
  optOut,
  onClose,
  onSaved,
}: {
  optOut: OptOut;
  onClose: () => void;
  onSaved: () => void;
}) {
  useRestoreFocus();
  const [note, setNote] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      await reconsentOptOut(optOut.id, note);
      onSaved();
    } catch (err: unknown) {
      setError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Modal
      title={`Record consent from ${optOut.peer}`}
      description="Use this only when the customer consented to texts again, for example on the phone. Say how: the note is audited. A customer can also text START."
      onClose={onClose}
      actions={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="reconsent-form"
            disabled={busy || note.trim() === ""}
          >
            Record consent
          </Button>
        </>
      }
    >
      {error && (
        <Alert tone="bad" title="Could not record the consent">
          {error}
        </Alert>
      )}
      <form id="reconsent-form" className="cf-stack" onSubmit={save}>
        <Input
          label="How the customer consented"
          value={note}
          autoFocus
          onChange={(e) => setNote(e.target.value)}
        />
      </form>
    </Modal>
  );
}

/**
 * The customers who opted out of a number. Hello detects the stop words
 * itself and refuses every send to an opted-out customer; only the customer
 * texting START, or an administrator recording their consent, undoes it.
 */
export function MessagingOptOuts() {
  const [search, setSearch] = useState("");
  const [q, setQ] = useState("");
  const [all, setAll] = useState(false);
  const [rows, setRows] = useState<OptOut[] | null>(null);
  const [addresses, setAddresses] = useState<MessagingAddress[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [recording, setRecording] = useState(false);
  const [reconsent, setReconsent] = useState<OptOut | null>(null);
  const [reload, setReload] = useState(0);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    listOptOuts({ q: q || undefined, all }, controller.signal)
      .then((r) => {
        setRows(r);
        setError(null);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      });
    return () => controller.abort();
  }, [q, all, reload]);

  useEffect(() => {
    const controller = new AbortController();
    listAddresses(controller.signal)
      .then(setAddresses)
      .catch(() => undefined);
    return () => controller.abort();
  }, []);

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Messaging"
        title="Opt-outs"
        description="Customers who texted a stop word, or who an administrator recorded as opted out. Nobody can text them from that number: an agent cannot override it."
        actions={
          <Button
            icon="plus"
            disabled={addresses.length === 0}
            onClick={() => setRecording(true)}
          >
            Record an opt-out
          </Button>
        }
      />
      <form
        className="msg-toolbar"
        onSubmit={(e) => {
          e.preventDefault();
          setQ(search.trim());
        }}
      >
        <Input
          label="Search by the end of the number"
          value={search}
          inputMode="numeric"
          onChange={(e) => setSearch(e.target.value)}
        />
        <Checkbox
          label="Include customers who opted back in"
          checked={all}
          onChange={(e) => setAll(e.target.checked)}
        />
        <Button type="submit" variant="secondary">
          Search
        </Button>
      </form>
      {error && (
        <Alert tone="bad" title="Could not load the opt-outs">
          {error}
        </Alert>
      )}
      {!rows && !error && <Spinner label="Loading opt-outs…" />}
      {rows && rows.length === 0 && (
        <EmptyState
          icon="ban"
          title="Nobody has opted out"
          description="A customer who texts STOP shows here."
        />
      )}
      {rows && rows.length > 0 && (
        <Table
          caption="Opt-outs"
          rows={rows}
          rowKey={(o) => o.id}
          columns={[
            {
              key: "peer",
              label: "Customer",
              mono: true,
              render: (o) => o.peer || "Erased",
            },
            {
              key: "address",
              label: "Our number",
              mono: true,
              render: (o) => o.address,
            },
            {
              key: "state",
              label: "State",
              render: (o) =>
                o.state === "out" ? (
                  <Badge tone="bad">Opted out</Badge>
                ) : (
                  <Badge tone="good">Opted back in</Badge>
                ),
            },
            { key: "source", label: "How", render: (o) => SOURCES[o.source] },
            { key: "at", label: "When", render: (o) => formatTime(o.at) },
            {
              key: "actions",
              label: "",
              align: "right",
              render: (o) =>
                o.state === "out" && (
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => setReconsent(o)}
                  >
                    Record consent for {o.peer || "an erased customer"}
                  </Button>
                ),
            },
          ]}
        />
      )}
      {recording && (
        <RecordDialog
          addresses={addresses}
          onClose={() => setRecording(false)}
          onSaved={() => {
            setRecording(false);
            toast.show("Opt-out recorded.");
            setReload((n) => n + 1);
          }}
        />
      )}
      {reconsent && (
        <ReconsentDialog
          optOut={reconsent}
          onClose={() => setReconsent(null)}
          onSaved={() => {
            setReconsent(null);
            toast.show("Consent recorded.");
            setReload((n) => n + 1);
          }}
        />
      )}
      {toast.node}
    </section>
  );
}
