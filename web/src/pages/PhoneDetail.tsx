import { useEffect, useState } from "react";
import { useNavigate, useParams } from "react-router";
import { errorMessage } from "../api";
import {
  getPhone,
  listPhoneFetches,
  previewPhoneFile,
  revealAdminPassword,
  rotateAdminPassword,
  vendorLabel,
  type Fetch,
  type Phone,
} from "../api/prov";
import {
  Alert,
  Badge,
  Button,
  CodeBlock,
  ConfirmDialog,
  Input,
  Modal,
  PageHeader,
  PropertyList,
  Spinner,
  Table,
  type BadgeTone,
  type TableColumn,
  Tabs,
  useToast,
} from "../design/azrty/components";
import { formatTime } from "../format";
import {
  DeletePhoneDialog,
  IssuedUrlModal,
  issuedView,
  type IssuedUrl,
  PhoneFlags,
  phoneName,
  RearmDialog,
  RotateTokenDialog,
  withoutUrl,
} from "./Phones";
import {
  CopyField,
  formatBytes,
  formatMac,
  manualRedirectStep,
  PhonesTabs,
  RedirectBadge,
  vendorModel,
} from "./phones/ui";
import "./phones/inventory.css";
import { Can } from "../role";

type PhoneState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; phone: Phone };

type Pending = "rotate" | "rearm" | "delete" | "rotate-admin" | null;
type Tab = "fetches" | "preview";

const FETCH_PAGE = 50;

/** The file a phone of this vendor asks for first, as a preview default. */
function defaultFile(phone: Phone): string {
  const mac = phone.mac.toLowerCase();
  switch (phone.vendor) {
    case "yealink":
    case "poly":
    case "fanvil":
      return `${mac}.cfg`;
    case "grandstream":
      return `cfg${mac}.xml`;
    case "snom":
      return mac.toUpperCase();
    default:
      return "";
  }
}

/** Denials are bad; served files good; anything else neutral. */
function resultTone(result: string): BadgeTone {
  const r = result.toLowerCase();
  if (/den|forbid|reject|revoked|unknown|disabled/.test(r)) return "bad";
  if (/error|fail/.test(r)) return "warn";
  if (/^(ok|served|success|boot)/.test(r)) return "good";
  return "neutral";
}

/** One phone: its state, its token and admin password, its fetch log and a preview. */
export function PhoneDetail() {
  const { id = "" } = useParams();
  const navigate = useNavigate();
  const [state, setState] = useState<PhoneState>({ status: "loading" });
  const [pending, setPending] = useState<Pending>(null);
  // Component state only: gone when the dialog closes or the page unmounts.
  const [issued, setIssued] = useState<IssuedUrl | null>(null);
  const [adminPassword, setAdminPassword] = useState<string | null>(null);
  const [revealError, setRevealError] = useState<string | null>(null);
  const [revealing, setRevealing] = useState(false);
  const [tab, setTab] = useState<Tab>("fetches");
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    getPhone(id, controller.signal)
      .then((phone) => setState({ status: "ready", phone }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [id]);

  const phone = state.status === "ready" ? state.phone : null;

  async function onReveal() {
    if (!phone) return;
    setRevealError(null);
    setRevealing(true);
    try {
      const { adminPassword } = await revealAdminPassword(phone.id);
      setAdminPassword(adminPassword);
    } catch (err) {
      setRevealError(errorMessage(err));
    } finally {
      setRevealing(false);
    }
  }

  const step = phone ? manualRedirectStep(phone.vendor) : null;

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title={phone ? phoneName(phone) : "Phone"}
        description={phone ? vendorModel(phone) : undefined}
        actions={
          phone && (
            <>
              <Can min="admin">
                <Button
                  variant="secondary"
                  icon="key-round"
                  onClick={() => setPending("rotate")}
                >
                  Rotate token
                </Button>
              </Can>
              <Can min="admin">
                <Button
                  variant="secondary"
                  icon="rotate-ccw"
                  onClick={() => setPending("rearm")}
                >
                  Re-arm
                </Button>
              </Can>
              <Can>
                <Button
                  variant="danger"
                  icon="trash-2"
                  onClick={() => setPending("delete")}
                >
                  Delete
                </Button>
              </Can>
            </>
          )
        }
      />
      <PhonesTabs />

      <div className="prov-stack">
        {state.status === "loading" && <Spinner label="Loading the phone…" />}
        {state.status === "error" && (
          <Alert tone="bad" title="Could not load the phone">
            {state.message}
          </Alert>
        )}
        {phone && (
          <>
            <PropertyList
              items={[
                { label: "MAC", value: formatMac(phone.mac), mono: true },
                { label: "Vendor", value: vendorLabel(phone.vendor) },
                { label: "Model", value: phone.model || "—" },
                { label: "Serial", value: phone.serial || "—", mono: true },
                { label: "Label", value: phone.label || "—" },
                {
                  label: "Extension",
                  value: phone.extensionNumber || "—",
                  mono: true,
                },
                {
                  label: "Device",
                  value: phone.deviceId === null ? "Unbound" : "Bound",
                },
                {
                  label: "Template",
                  value:
                    phone.templateId === null
                      ? "Automatic"
                      : `Override #${phone.templateId}`,
                },
                {
                  label: "BLF keys",
                  value: phone.blf.length ? phone.blf.join(", ") : "—",
                  mono: true,
                },
                {
                  label: "Enabled",
                  value: phone.enabled ? "Enabled" : "Disabled",
                },
                { label: "Flags", value: <PhoneFlags phone={phone} /> },
                { label: "First fetch", value: formatTime(phone.firstFetchAt) },
                { label: "Last fetch", value: formatTime(phone.lastFetchAt) },
                {
                  label: "Last IP",
                  value: phone.lastFetchIp ?? "—",
                  mono: true,
                },
                { label: "Last user agent", value: phone.lastFetchUa ?? "—" },
                {
                  label: "Last file",
                  value: phone.lastFetchFile ?? "—",
                  mono: true,
                },
                { label: "Firmware", value: phone.firmwareSeen ?? "—" },
                { label: "Added", value: formatTime(phone.createdAt) },
                { label: "Updated", value: formatTime(phone.updatedAt) },
              ]}
            />

            <div className="prov-stack">
              <h2 className="inv-section-title">Redirect</h2>
              <div className="prov-row">
                <RedirectBadge status={phone.redirectStatus} />
                {phone.redirectStatus?.at && (
                  <span className="prov-muted prov-cell-sm">
                    {formatTime(phone.redirectStatus.at)}
                  </span>
                )}
              </div>
              {step && (
                <Alert tone="info" title="Manual step">
                  {step}
                </Alert>
              )}
            </div>

            <div className="prov-stack">
              <h2 className="inv-section-title">Admin password</h2>
              <p className="prov-muted">
                The phone&apos;s web UI password. Revealing it is audited.
              </p>
              {revealError && (
                <Alert tone="bad" title="Could not reveal the password">
                  {revealError}
                </Alert>
              )}
              <div className="prov-row">
                <Can min="admin">
                  <Button
                    variant="secondary"
                    icon="eye"
                    disabled={revealing}
                    onClick={() => void onReveal()}
                  >
                    Reveal admin password
                  </Button>
                </Can>
                <Can min="admin">
                  <Button
                    variant="secondary"
                    icon="key-round"
                    onClick={() => setPending("rotate-admin")}
                  >
                    Rotate admin password
                  </Button>
                </Can>
              </div>
            </div>

            <Tabs<Tab>
              aria-label="Phone activity"
              idPrefix="phone"
              items={[
                { id: "fetches", label: "Fetch log" },
                { id: "preview", label: "Preview" },
              ]}
              value={tab}
              onChange={setTab}
            />
            <div
              role="tabpanel"
              id={`phone-panel-${tab}`}
              aria-labelledby={`phone-tab-${tab}`}
            >
              {tab === "fetches" ? (
                <FetchLog phoneId={phone.id} />
              ) : (
                <Preview phone={phone} />
              )}
            </div>
          </>
        )}
      </div>

      {phone && pending === "rotate" && (
        <RotateTokenDialog
          phone={phone}
          onClose={() => setPending(null)}
          onIssued={(p) => {
            setState({ status: "ready", phone: withoutUrl(p) });
            setPending(null);
            setIssued(issuedView("rotate", p));
          }}
        />
      )}
      {phone && pending === "rearm" && (
        <RearmDialog
          phone={phone}
          onClose={() => setPending(null)}
          onIssued={(p) => {
            setState({ status: "ready", phone: withoutUrl(p) });
            setPending(null);
            setIssued(issuedView("rearm", p));
          }}
        />
      )}
      {phone && pending === "delete" && (
        <DeletePhoneDialog
          phone={phone}
          onClose={() => setPending(null)}
          onDeleted={() => void navigate("/phones")}
        />
      )}
      {phone && pending === "rotate-admin" && (
        <ConfirmDialog
          title={`Rotate the admin password of ${phoneName(phone)}?`}
          description="A new password is generated; the phone picks it up on its next fetch. The old one stops working then."
          confirmLabel="Rotate admin password"
          confirmIcon="key-round"
          confirmVariant="primary"
          errorTitle="Could not rotate the admin password"
          onConfirm={async () => {
            await rotateAdminPassword(phone.id);
            setPending(null);
            toast.show(
              "Admin password rotated; the phone picks it up on its next fetch.",
            );
          }}
          onClose={() => setPending(null)}
        />
      )}

      {adminPassword !== null && phone && (
        <Modal
          title="Admin password"
          description={`The web UI password of ${phoneName(phone)}.`}
          onClose={() => setAdminPassword(null)}
          actions={<Button onClick={() => setAdminPassword(null)}>Done</Button>}
        >
          <CopyField
            id="admin-password"
            label="Admin password"
            value={adminPassword}
            what="Password"
            onCopied={toast.show}
            autoFocus
          />
        </Modal>
      )}

      {issued && (
        <IssuedUrlModal
          issued={issued}
          onCopied={toast.show}
          onClose={() => setIssued(null)}
        />
      )}

      {toast.node}
    </section>
  );
}

type FetchState = {
  items: Fetch[];
  next: string;
  loading: boolean;
  error: string | null;
};

function FetchLog({ phoneId }: { phoneId: Phone["id"] }) {
  const [log, setLog] = useState<FetchState>({
    items: [],
    next: "",
    loading: true,
    error: null,
  });

  useEffect(() => {
    const controller = new AbortController();
    listPhoneFetches(phoneId, { limit: FETCH_PAGE }, controller.signal)
      .then((page) =>
        setLog({
          items: page.items,
          next: page.next,
          loading: false,
          error: null,
        }),
      )
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setLog((l) => ({ ...l, loading: false, error: errorMessage(err) }));
        }
      });
    return () => controller.abort();
  }, [phoneId]);

  async function loadOlder() {
    setLog((l) => ({ ...l, loading: true, error: null }));
    try {
      const page = await listPhoneFetches(phoneId, {
        before: log.next,
        limit: FETCH_PAGE,
      });
      setLog((l) => ({
        items: [...l.items, ...page.items],
        next: page.next,
        loading: false,
        error: null,
      }));
    } catch (err) {
      setLog((l) => ({ ...l, loading: false, error: errorMessage(err) }));
    }
  }

  const columns: TableColumn<Fetch>[] = [
    { key: "at", label: "Time", render: (f) => formatTime(f.at) },
    { key: "ip", label: "IP", mono: true },
    {
      key: "userAgent",
      label: "User agent",
      render: (f) => <span className="prov-cell-sm">{f.userAgent || "—"}</span>,
    },
    { key: "path", label: "Path", mono: true },
    { key: "kind", label: "Kind" },
    {
      key: "result",
      label: "Result",
      render: (f) => <Badge tone={resultTone(f.result)}>{f.result}</Badge>,
    },
    {
      key: "status",
      label: "Status",
      mono: true,
      render: (f) => String(f.status),
    },
    {
      key: "bytes",
      label: "Bytes",
      align: "right",
      render: (f) => formatBytes(f.bytes),
    },
  ];

  return (
    <div className="prov-stack">
      {log.error && (
        <Alert tone="bad" title="Could not load the fetch log">
          {log.error}
        </Alert>
      )}
      {log.items.length === 0 && !log.loading && !log.error && (
        <p className="prov-muted">The phone has not fetched anything yet.</p>
      )}
      {log.items.length > 0 && (
        <Table
          caption="Fetch log"
          columns={columns}
          rows={log.items}
          rowKey={(f, i) => `${f.at}-${i}`}
        />
      )}
      {log.loading && <Spinner label="Loading fetches…" />}
      {log.next !== "" && !log.loading && (
        <div className="prov-row">
          <Button variant="secondary" onClick={() => void loadOlder()}>
            Load older
          </Button>
        </div>
      )}
    </div>
  );
}

function Preview({ phone }: { phone: Phone }) {
  const [file, setFile] = useState(() => defaultFile(phone));
  const [shown, setShown] = useState<{ file: string; body: string } | null>(
    null,
  );
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onPreview() {
    setError(null);
    setBusy(true);
    try {
      setShown({ file, body: await previewPhoneFile(phone.id, file) });
    } catch (err) {
      setShown(null);
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="prov-stack">
      <form
        className="prov-row"
        aria-label="Preview a file"
        onSubmit={(e) => {
          e.preventDefault();
          void onPreview();
        }}
      >
        <Input
          id="preview-file"
          label="File name"
          mono
          spellCheck={false}
          autoComplete="off"
          hint="Secrets are masked by the server."
          value={file}
          onChange={(e) => setFile(e.target.value)}
        />
        <Button type="submit" disabled={busy || file.trim() === ""}>
          Preview
        </Button>
      </form>
      {error && (
        <Alert tone="bad" title="Could not render the file">
          {error}
        </Alert>
      )}
      {shown && <CodeBlock title={shown.file} code={shown.body} copyable />}
    </div>
  );
}
