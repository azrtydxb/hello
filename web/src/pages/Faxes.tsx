import { useCallback, useState, type FormEvent } from "react";
import {
  createFaxDestination,
  deleteFax,
  deleteFaxDestination,
  faxPdfPath,
  listFaxDestinations,
  listFaxes,
  MAX_FAX_BYTES,
  sendFax,
  type Fax,
  type FaxDestination,
  type FaxDirection,
  type FaxStatus,
} from "../api/fax";
import { errorMessage } from "../api";
import {
  Alert,
  Badge,
  Button,
  EmptyState,
  Input,
  PageHeader,
  Select,
  Spinner,
  useToast,
} from "../design/azrty/components";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";
import { Can } from "../role";

const STATUS_LABEL: Record<FaxStatus, string> = {
  queued: "Queued",
  sending: "Sending",
  receiving: "Receiving",
  received: "Received",
  sent: "Sent",
  partial: "Partial",
  failed: "Failed",
  cancelled: "Cancelled",
};

const STATUS_TONE: Record<FaxStatus, "good" | "warn" | "bad" | "info"> = {
  queued: "info",
  sending: "info",
  receiving: "info",
  received: "good",
  sent: "good",
  partial: "warn",
  failed: "bad",
  cancelled: "warn",
};

const MAIL_LABEL: Record<string, string> = {
  none: "—",
  pending: "Email pending",
  sending: "Emailing",
  sent: "Emailed",
  failed: "Email failed",
};

const MODE_LABEL: Record<string, string> = {
  g711: "G.711",
  t38: "T.38",
  "": "—",
};

const time = (iso: string) =>
  new Date(iso).toLocaleString(undefined, { hour12: false });

const E164 = /^\+[1-9][0-9]{6,14}$/;

/**
 * Faxes: what was received and sent with its state, a form to send a PDF, and
 * the fax destinations that routes and IVR keys name. A received fax is
 * downloaded as a PDF; an operator sends and deletes.
 */
export function Faxes({ mine = false }: { mine?: boolean }) {
  const [direction, setDirection] = useState<FaxDirection | "">("");
  const [status, setStatus] = useState<FaxStatus | "">("");
  const loadFaxes = useCallback(
    (signal: AbortSignal) => listFaxes({ direction, status }, signal, mine),
    [direction, status, mine],
  );
  const faxes = usePolling(loadFaxes, LIVE_REFRESH_MS);
  const loadDests = useCallback(
    (signal: AbortSignal) =>
      mine ? Promise.resolve([]) : listFaxDestinations(signal),
    [mine],
  );
  const dests = usePolling(loadDests, 30000);
  const toast = useToast();
  const [error, setError] = useState<string | null>(null);

  async function remove(f: Fax) {
    setError(null);
    try {
      await deleteFax(f.id);
      toast.show(`Fax ${f.id} deleted.`);
      faxes.reload();
    } catch (err: unknown) {
      setError(`Could not delete the fax: ${errorMessage(err)}`);
    }
  }

  const items: Fax[] = faxes.status === "loading" ? [] : (faxes.data ?? []);
  const destItems: FaxDestination[] =
    dests.status === "loading" ? [] : (dests.data ?? []);

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow={mine ? "My phone" : "Media"}
        title={mine ? "My faxes" : "Faxes"}
        description={
          mine
            ? "Faxes received at a fax destination your extension owns, with the result of each call. A fax that failed after some pages keeps them as partial."
            : "Faxes received at a fax destination and faxes sent from a PDF, with the result of each call. A fax that failed after some pages keeps them as partial. Faxes are kept for 90 days."
        }
      />
      <div className="cf-stack">
        <div className="cf-row">
          <Select
            label="Direction"
            value={direction}
            options={[
              { value: "", label: "Received and sent" },
              { value: "inbound", label: "Received" },
              { value: "outbound", label: "Sent" },
            ]}
            onChange={(e) => setDirection(e.target.value as FaxDirection | "")}
          />
          <Select
            label="Status"
            value={status}
            options={[
              { value: "", label: "Any status" },
              ...Object.entries(STATUS_LABEL).map(([value, label]) => ({
                value,
                label,
              })),
            ]}
            onChange={(e) => setStatus(e.target.value as FaxStatus | "")}
          />
        </div>
        {faxes.status === "loading" && <Spinner label="Loading faxes…" />}
        {faxes.status === "error" && (
          <Alert tone="bad" title="Could not load the faxes">
            {faxes.message}
          </Alert>
        )}
        {error && (
          <Alert tone="bad" title="Delete failed">
            {error}
          </Alert>
        )}
        {faxes.status !== "loading" && items.length === 0 && (
          <EmptyState
            icon="printer"
            title="No faxes"
            description="Create a fax destination, point an inbound route at it, and faxes sent to that number appear here. Send a fax from a PDF below."
          />
        )}
        {items.length > 0 && (
          <table className="az-table" aria-label="Faxes">
            <thead>
              <tr>
                <th>Time</th>
                <th>Direction</th>
                <th>Number</th>
                <th>Pages</th>
                <th>Status</th>
                <th>Mode</th>
                <th>Email</th>
                <th>
                  <span className="az-sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {items.map((f) => (
                <tr key={String(f.id)}>
                  <td className="cf-mono">{time(f.createdAt)}</td>
                  <td>{f.direction === "inbound" ? "Received" : "Sent"}</td>
                  <td>
                    <span className="cf-mono">{f.peerNumber || "—"}</span>
                    {f.destination ? ` → ${f.destination}` : ""}
                  </td>
                  <td className="cf-mono">{f.pages}</td>
                  <td>
                    <Badge tone={STATUS_TONE[f.status]}>
                      {STATUS_LABEL[f.status]}
                    </Badge>
                    {f.resultText &&
                    f.status !== "received" &&
                    f.status !== "sent" ? (
                      <span className="cf-hint"> {f.resultText}</span>
                    ) : null}
                    {f.status === "queued" && f.nextAttemptAt
                      ? ` next try ${time(f.nextAttemptAt)}`
                      : ""}
                  </td>
                  <td>{MODE_LABEL[f.mode] ?? f.mode}</td>
                  <td>{MAIL_LABEL[f.mailStatus] ?? f.mailStatus}</td>
                  <td>
                    <Can min={mine ? "user" : "operator"}>
                      {f.hasPdf && (
                        <a
                          className="az-btn az-btn--ghost az-btn--sm"
                          href={faxPdfPath(f.id, mine)}
                          download
                          aria-label={`Download fax ${f.id}`}
                        >
                          Download
                        </a>
                      )}
                      {!mine && f.status !== "receiving" && (
                        <Button
                          variant="ghost"
                          size="sm"
                          icon="trash-2"
                          aria-label={`Delete fax ${f.id}`}
                          onClick={() => void remove(f)}
                        >
                          {f.status === "sending" ? "Cancel" : "Delete"}
                        </Button>
                      )}
                    </Can>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}

        {!mine && (
          <Can>
            <SendForm onSent={() => faxes.reload()} />
          </Can>
        )}

        {!mine && <h2 className="az-h2">Fax destinations</h2>}
        {!mine && dests.status === "error" && (
          <Alert tone="bad" title="Could not load the fax destinations">
            {dests.message}
          </Alert>
        )}
        {!mine && dests.status !== "loading" && destItems.length === 0 && (
          <EmptyState
            icon="inbox"
            title="No fax destinations"
            description="A destination names the extension that owns the faxes arriving there and, optionally, an email address."
          />
        )}
        {!mine && destItems.length > 0 && (
          <table className="az-table" aria-label="Fax destinations">
            <thead>
              <tr>
                <th>Name</th>
                <th>Owner extension</th>
                <th>Email</th>
                <th>
                  <span className="az-sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {destItems.map((d) => (
                <tr key={String(d.id)}>
                  <td className="cf-mono">{d.name}</td>
                  <td className="cf-mono">{d.extension}</td>
                  <td>{d.email || "The owner's users"}</td>
                  <td>
                    <Can min="admin">
                      <Button
                        variant="ghost"
                        size="sm"
                        icon="trash-2"
                        aria-label={`Delete fax destination ${d.name}`}
                        onClick={() =>
                          void deleteFaxDestination(d.id)
                            .then(() => {
                              toast.show(`Fax destination ${d.name} deleted.`);
                              dests.reload();
                            })
                            .catch((err: unknown) =>
                              setError(
                                `Could not delete the destination: ${errorMessage(err)}`,
                              ),
                            )
                        }
                      >
                        Delete
                      </Button>
                    </Can>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
        {!mine && (
          <Can min="admin">
            <DestinationForm onCreated={() => dests.reload()} />
          </Can>
        )}
      </div>
      {toast.node}
    </section>
  );
}

/** The faxes of the signed-in user's own extensions (the "My phone" role). */
export function MyFaxes() {
  return <Faxes mine />;
}

function SendForm({ onSent }: { onSent: () => void }) {
  const [file, setFile] = useState<File | null>(null);
  const [to, setTo] = useState("");
  const [callerId, setCallerId] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ ok: boolean; text: string } | null>(
    null,
  );
  const errors: Record<string, string> = {};
  if (to !== "" && !E164.test(to))
    errors.to = "Use E.164, such as +41441234567.";
  if (file && file.size > MAX_FAX_BYTES) errors.file = "The PDF is over 10 MB.";

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (!file || !E164.test(to) || errors.file) return;
    setBusy(true);
    setMessage(null);
    try {
      const f = await sendFax(file, to, callerId.trim());
      setMessage({ ok: true, text: `Fax ${f.id} to ${to} queued.` });
      setFile(null);
      setTo("");
      onSent();
    } catch (err: unknown) {
      setMessage({ ok: false, text: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      className="cf-form"
      onSubmit={(e) => void submit(e)}
      aria-label="Send a fax"
    >
      <h2 className="az-h2">Send a fax</h2>
      <div className="cf-row">
        <Input
          label="PDF"
          type="file"
          accept="application/pdf"
          error={errors.file}
          hint="At most 10 MB and 50 pages."
          onChange={(e) => setFile(e.target.files?.[0] ?? null)}
        />
        <Input
          label="To"
          value={to}
          mono
          error={errors.to}
          placeholder="+41441234567"
          onChange={(e) => setTo(e.target.value)}
        />
        <Input
          label="Caller ID (optional)"
          value={callerId}
          mono
          onChange={(e) => setCallerId(e.target.value)}
        />
      </div>
      {message && (
        <Alert
          tone={message.ok ? "good" : "bad"}
          title={message.ok ? "Queued" : "Could not send the fax"}
        >
          {message.text}
        </Alert>
      )}
      <Button
        type="submit"
        icon="send"
        disabled={busy || !file || !E164.test(to)}
      >
        Send fax
      </Button>
    </form>
  );
}

function DestinationForm({ onCreated }: { onCreated: () => void }) {
  const [name, setName] = useState("");
  const [extension, setExtension] = useState("");
  const [email, setEmail] = useState("");
  const [error, setError] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setError(null);
    try {
      await createFaxDestination({
        name: name.trim(),
        extension: extension.trim(),
        email: email.trim(),
      });
      setName("");
      setExtension("");
      setEmail("");
      onCreated();
    } catch (err: unknown) {
      setError(errorMessage(err));
    }
  }

  return (
    <form
      className="cf-form"
      onSubmit={(e) => void submit(e)}
      aria-label="Add a fax destination"
    >
      <h3 className="az-h3">Add a fax destination</h3>
      <div className="cf-row">
        <Input
          label="Name"
          value={name}
          mono
          onChange={(e) => setName(e.target.value)}
        />
        <Input
          label="Owner extension"
          value={extension}
          mono
          onChange={(e) => setExtension(e.target.value)}
        />
        <Input
          label="Email (optional)"
          value={email}
          hint="Empty sends the PDF to the owner's users."
          onChange={(e) => setEmail(e.target.value)}
        />
      </div>
      {error && (
        <Alert tone="bad" title="Could not add the destination">
          {error}
        </Alert>
      )}
      <Button
        type="submit"
        icon="plus"
        disabled={name.trim() === "" || extension.trim() === ""}
      >
        Add destination
      </Button>
    </form>
  );
}
