import { useCallback, useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router";
import { errorMessage, type Id } from "../api";
import { listGroups, type Group } from "../api/phonebook";
import {
  createDot1XProfile,
  deleteDot1XProfile,
  DOT1X_METHODS,
  getGroupSecurity,
  getSCEP,
  listCertificates,
  listDot1XProfiles,
  revokeCertificate,
  setGroupSecurity,
  setSCEPCA,
  updateDot1XProfile,
  type Dot1XMethod,
  type Dot1XProfile,
  type Dot1XProfileInput,
  type GroupSecurity,
  type SCEPCertificate,
  type SCEPStatus,
} from "../api/scep";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  IconButton,
  Input,
  Modal,
  PageHeader,
  PropertyList,
  Select,
  Spinner,
  Switch,
  Table,
  type BadgeTone,
  type TableColumn,
  useToast,
} from "../design/azrty/components";
import { Can } from "../role";
import { PhonesTabs } from "./phones/ui";

type Load<T> =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; value: T };

const formatDate = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleDateString();
};

const methodLabel = (m: Dot1XMethod) =>
  DOT1X_METHODS.find((x) => x.value === m)?.label ?? m;

const STATUS_TONE: Record<SCEPCertificate["status"], BadgeTone> = {
  active: "good",
  expired: "neutral",
  revoked: "bad",
};

/** Certificates and 802.1X: the device CA, each group's SCEP switch and
 * wired profile, the 802.1X profiles and the issued certificates. */
export function PhoneSecurity() {
  const toast = useToast();
  const [scep, setScep] = useState<Load<SCEPStatus>>({ status: "loading" });
  const [profiles, setProfiles] = useState<Load<Dot1XProfile[]>>({
    status: "loading",
  });
  const [certs, setCerts] = useState<Load<SCEPCertificate[]>>({
    status: "loading",
  });
  const [groups, setGroups] = useState<
    Load<{ group: Group; security: GroupSecurity }[]>
  >({ status: "loading" });

  const loadSCEP = useCallback((signal?: AbortSignal) => {
    getSCEP(signal)
      .then((value) => setScep({ status: "ready", value }))
      .catch((err: unknown) => {
        if (!signal?.aborted)
          setScep({ status: "error", message: errorMessage(err) });
      });
  }, []);
  const loadProfiles = useCallback((signal?: AbortSignal) => {
    listDot1XProfiles(signal)
      .then((value) => setProfiles({ status: "ready", value }))
      .catch((err: unknown) => {
        if (!signal?.aborted)
          setProfiles({ status: "error", message: errorMessage(err) });
      });
  }, []);
  const loadCerts = useCallback((signal?: AbortSignal) => {
    listCertificates({}, signal)
      .then((value) => setCerts({ status: "ready", value }))
      .catch((err: unknown) => {
        if (!signal?.aborted)
          setCerts({ status: "error", message: errorMessage(err) });
      });
  }, []);
  const loadGroups = useCallback((signal?: AbortSignal) => {
    listGroups(signal)
      .then((gs) =>
        Promise.all(
          gs.map(async (group) => ({
            group,
            security: await getGroupSecurity(group.id, signal),
          })),
        ),
      )
      .then((value) => setGroups({ status: "ready", value }))
      .catch((err: unknown) => {
        if (!signal?.aborted)
          setGroups({ status: "error", message: errorMessage(err) });
      });
  }, []);

  useEffect(() => {
    const c = new AbortController();
    loadSCEP(c.signal);
    loadProfiles(c.signal);
    loadCerts(c.signal);
    loadGroups(c.signal);
    return () => c.abort();
  }, [loadSCEP, loadProfiles, loadCerts, loadGroups]);

  const profileList = profiles.status === "ready" ? profiles.value : [];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title="Certificates and 802.1X"
        description="Hello's SCEP server issues each phone its own certificate from the device CA, for 802.1X (EAP-TLS) on the wired port and on Wi-Fi. SCEP is off until you switch it on for a phone group."
      />
      <PhonesTabs />
      <div className="prov-stack">
        <CACard
          scep={scep}
          onChanged={(m) => {
            loadSCEP();
            toast.show(m);
          }}
        />
        <GroupsCard
          groups={groups}
          profiles={profileList}
          onSaved={(g, m) => {
            setGroups((prev) =>
              prev.status === "ready"
                ? {
                    status: "ready",
                    value: prev.value.map((x) =>
                      String(x.group.id) === String(g.groupId)
                        ? { ...x, security: g }
                        : x,
                    ),
                  }
                : prev,
            );
            loadProfiles();
            toast.show(m);
          }}
        />
        <ProfilesCard
          profiles={profiles}
          onChanged={(m) => {
            loadProfiles();
            toast.show(m);
          }}
        />
        <CertificatesCard
          certs={certs}
          onRevoked={(c) => {
            setCerts((prev) =>
              prev.status === "ready"
                ? {
                    status: "ready",
                    value: prev.value.map((x) =>
                      String(x.id) === String(c.id) ? c : x,
                    ),
                  }
                : prev,
            );
            toast.show(`Certificate ${c.serial} revoked.`);
          }}
        />
      </div>
      {toast.node}
    </section>
  );
}

function CACard({
  scep,
  onChanged,
}: {
  scep: Load<SCEPStatus>;
  onChanged: (message: string) => void;
}) {
  const [dialog, setDialog] = useState<"generate" | "import" | null>(null);
  const [commonName, setCommonName] = useState("");
  const [certPem, setCertPem] = useState("");
  const [keyPem, setKeyPem] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  if (scep.status === "loading")
    return <Spinner label="Loading the device CA…" />;
  if (scep.status === "error")
    return (
      <Alert tone="bad" title="Could not load the SCEP status">
        {scep.message}
      </Alert>
    );
  const { ca, serverEnabled, caUrl, crlUrl } = scep.value;
  const replace = ca !== null;

  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    try {
      if (dialog === "import") {
        await setSCEPCA({ mode: "import", certPem, keyPem, replace });
      } else {
        await setSCEPCA({
          mode: "generate",
          commonName: commonName.trim() || undefined,
          replace,
        });
      }
      setDialog(null);
      setKeyPem("");
      setCertPem("");
      onChanged(
        dialog === "import" ? "Device CA imported." : "Device CA generated.",
      );
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="prov-card" aria-labelledby="ca-title" role="region">
      <h2 id="ca-title" className="prov-card__title">
        Device CA
      </h2>
      {!serverEnabled && (
        <Alert tone="warn" title="The SCEP server is off">
          HELLO_SCEP is false on this deployment, so no phone can enrol.
        </Alert>
      )}
      {ca ? (
        <PropertyList
          items={[
            { label: "Subject", value: ca.subject },
            {
              label: "Valid",
              value: `${formatDate(ca.notBefore)} to ${formatDate(ca.notAfter)}`,
            },
            {
              label: "SHA-256",
              value: <code>{ca.fingerprintSha256}</code>,
            },
            {
              label: "Source",
              value: ca.source === "generated" ? "Generated" : "Imported",
            },
            { label: "Certificate for RADIUS", value: <code>{caUrl}</code> },
            { label: "CRL", value: <code>{crlUrl}</code> },
          ]}
        />
      ) : (
        <p className="prov-muted">
          No device CA yet. Generate one, or import an intermediate of your
          enterprise CA so the RADIUS server already trusts it.
        </p>
      )}
      <div className="prov-row">
        <Can min="admin">
          <Button
            icon="shield-plus"
            variant={replace ? "secondary" : "primary"}
            onClick={() => {
              setError(null);
              setDialog("generate");
            }}
          >
            {replace ? "Replace with a new CA" : "Generate CA"}
          </Button>
          <Button
            icon="file-key"
            variant="secondary"
            onClick={() => {
              setError(null);
              setDialog("import");
            }}
          >
            Import CA
          </Button>
        </Can>
      </div>
      {dialog && (
        <Modal
          title={
            dialog === "import"
              ? "Import the device CA"
              : "Generate a device CA"
          }
          description={
            replace
              ? "This replaces the active CA. Certificates it issued keep working until they expire, but phones enrol again with their challenge."
              : undefined
          }
          onClose={() => setDialog(null)}
        >
          <form onSubmit={(e) => void submit(e)} noValidate>
            {dialog === "generate" ? (
              <Input
                id="ca-cn"
                label="Common name"
                hint="Default: Hello device CA."
                value={commonName}
                onChange={(e) => setCommonName(e.target.value)}
              />
            ) : (
              <>
                <PemField
                  id="ca-cert"
                  label="CA certificate (PEM)"
                  value={certPem}
                  onChange={setCertPem}
                />
                <PemField
                  id="ca-key"
                  label="CA private key (PEM, RSA)"
                  hint="Sealed on save and never shown again."
                  value={keyPem}
                  onChange={setKeyPem}
                />
              </>
            )}
            {error && (
              <Alert tone="bad" title="Could not save the CA">
                {error}
              </Alert>
            )}
            <div className="prov-row">
              <Button type="submit" disabled={busy}>
                {dialog === "import" ? "Import" : "Generate"}
              </Button>
              <Button
                variant="secondary"
                type="button"
                onClick={() => setDialog(null)}
              >
                Cancel
              </Button>
            </div>
          </form>
        </Modal>
      )}
    </div>
  );
}

function PemField({
  id,
  label,
  hint,
  value,
  onChange,
}: {
  id: string;
  label: string;
  hint?: string;
  value: string;
  onChange: (v: string) => void;
}) {
  return (
    <div className="az-field">
      <label className="az-field__label" htmlFor={id}>
        {label}
      </label>
      <textarea
        id={id}
        className="az-input az-textarea az-input--mono"
        rows={5}
        spellCheck={false}
        autoComplete="off"
        value={value}
        aria-describedby={hint ? `${id}-hint` : undefined}
        onChange={(e) => onChange(e.target.value)}
      />
      {hint && (
        <span className="az-field__hint" id={`${id}-hint`}>
          {hint}
        </span>
      )}
    </div>
  );
}

function GroupsCard({
  groups,
  profiles,
  onSaved,
}: {
  groups: Load<{ group: Group; security: GroupSecurity }[]>;
  profiles: Dot1XProfile[];
  onSaved: (g: GroupSecurity, message: string) => void;
}) {
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<Id | null>(null);

  async function save(group: Group, next: Omit<GroupSecurity, "groupId">) {
    setError(null);
    setBusy(group.id);
    try {
      const g = await setGroupSecurity(group.id, next);
      onSaved(
        g,
        `Saved ${group.name}; its phones take it at their next fetch.`,
      );
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(null);
    }
  }

  const profileOptions = [
    { value: "", label: "None (leave the port as it is)" },
    ...profiles.map((p) => ({ value: String(p.id), label: p.name })),
  ];

  const columns: TableColumn<{ group: Group; security: GroupSecurity }>[] = [
    { key: "name", label: "Group", render: (r) => r.group.name },
    {
      key: "scep",
      label: "SCEP",
      render: (r) => (
        <Can min="admin" fallback={r.security.scepEnabled ? "On" : "Off"}>
          <Switch
            checked={r.security.scepEnabled}
            disabled={busy !== null}
            aria-label={`SCEP for ${r.group.name}`}
            onChange={(e) =>
              void save(r.group, {
                scepEnabled: e.target.checked,
                certValidityDays: r.security.certValidityDays,
                wiredDot1xProfileId: r.security.wiredDot1xProfileId,
              })
            }
          />
        </Can>
      ),
    },
    {
      key: "days",
      label: "Certificate days",
      align: "right",
      render: (r) => r.security.certValidityDays,
    },
    {
      key: "wired",
      label: "Wired 802.1X",
      render: (r) => (
        <Can
          min="admin"
          fallback={
            profiles.find(
              (p) => String(p.id) === String(r.security.wiredDot1xProfileId),
            )?.name ?? "None"
          }
        >
          <Select
            aria-label={`Wired 802.1X profile for ${r.group.name}`}
            size="sm"
            options={profileOptions}
            disabled={busy !== null}
            value={
              r.security.wiredDot1xProfileId === null
                ? ""
                : String(r.security.wiredDot1xProfileId)
            }
            onChange={(e) =>
              void save(r.group, {
                scepEnabled: r.security.scepEnabled,
                certValidityDays: r.security.certValidityDays,
                wiredDot1xProfileId:
                  e.target.value === "" ? null : Number(e.target.value),
              })
            }
          />
        </Can>
      ),
    },
  ];

  return (
    <div className="prov-card" role="region" aria-labelledby="groups-title">
      <h2 id="groups-title" className="prov-card__title">
        Phone groups
      </h2>
      <p className="prov-muted">
        SCEP enrols Grandstream WP816/826/836 and GRP2601 to 2604 phones. The
        wired profile goes to GRP26xx desk phones; try a new one on a test group
        first, since a wrong profile takes a phone off the network.
      </p>
      {groups.status === "loading" && <Spinner label="Loading groups…" />}
      {groups.status === "error" && (
        <Alert tone="bad" title="Could not load phone groups">
          {groups.message}
        </Alert>
      )}
      {error && (
        <Alert tone="bad" title="Could not save the group">
          {error}
        </Alert>
      )}
      {groups.status === "ready" && groups.value.length === 0 && (
        <p className="prov-muted">
          No phone groups yet. <Link to="/phones/groups">Create one</Link>.
        </p>
      )}
      {groups.status === "ready" && groups.value.length > 0 && (
        <Table
          caption="SCEP and wired 802.1X per phone group"
          columns={columns}
          rows={groups.value}
          rowKey={(r) => String(r.group.id)}
        />
      )}
    </div>
  );
}

const EMPTY_PROFILE: Dot1XProfileInput = {
  name: "",
  method: "peap",
  identity: "{mac}",
  anonymousIdentity: "",
  password: "",
  caCertPem: "",
  serverDomain: "",
};

function ProfilesCard({
  profiles,
  onChanged,
}: {
  profiles: Load<Dot1XProfile[]>;
  onChanged: (message: string) => void;
}) {
  const [editing, setEditing] = useState<Dot1XProfile | "new" | null>(null);
  const [deleting, setDeleting] = useState<Dot1XProfile | null>(null);

  const columns: TableColumn<Dot1XProfile>[] = [
    { key: "name", label: "Name" },
    { key: "method", label: "Method", render: (p) => methodLabel(p.method) },
    { key: "identity", label: "Identity", mono: true },
    {
      key: "used",
      label: "Groups",
      align: "right",
      render: (p) => p.usedByGroups.length,
    },
    {
      key: "actions",
      label: <span className="visually-hidden">Actions</span>,
      align: "right",
      render: (p) => (
        <Can min="admin">
          <IconButton
            icon="pencil"
            label={`Edit ${p.name}`}
            onClick={() => setEditing(p)}
          />
          <IconButton
            icon="trash-2"
            label={`Delete ${p.name}`}
            onClick={() => setDeleting(p)}
          />
        </Can>
      ),
    },
  ];

  return (
    <div className="prov-card" role="region" aria-labelledby="dot1x-title">
      <h2 id="dot1x-title" className="prov-card__title">
        802.1X profiles
      </h2>
      <p className="prov-muted">
        Reusable EAP identities for the wired port and for enterprise Wi-Fi. In
        an identity, {"{mac}"} is replaced by each phone's MAC.
      </p>
      {profiles.status === "loading" && <Spinner label="Loading profiles…" />}
      {profiles.status === "error" && (
        <Alert tone="bad" title="Could not load 802.1X profiles">
          {profiles.message}
        </Alert>
      )}
      {profiles.status === "ready" && profiles.value.length === 0 && (
        <EmptyState
          icon="network"
          title="No 802.1X profiles"
          description="Add one, then name it for a phone group."
        />
      )}
      {profiles.status === "ready" && profiles.value.length > 0 && (
        <Table
          caption="802.1X profiles"
          columns={columns}
          rows={profiles.value}
          rowKey={(p) => String(p.id)}
        />
      )}
      <div className="prov-row">
        <Can min="admin">
          <Button icon="plus" onClick={() => setEditing("new")}>
            Add profile
          </Button>
        </Can>
      </div>
      {editing && (
        <ProfileDialog
          profile={editing === "new" ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={(p) => {
            setEditing(null);
            onChanged(`Saved ${p.name}.`);
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete ${deleting.name}?`}
          description="A profile a phone group uses cannot be deleted; change the group first."
          confirmLabel="Delete profile"
          errorTitle="Could not delete the profile"
          onConfirm={async () => {
            await deleteDot1XProfile(deleting.id);
            setDeleting(null);
            onChanged(`Deleted ${deleting.name}.`);
          }}
          onClose={() => setDeleting(null)}
        />
      )}
    </div>
  );
}

function ProfileDialog({
  profile,
  onClose,
  onSaved,
}: {
  profile: Dot1XProfile | null;
  onClose: () => void;
  onSaved: (p: Dot1XProfile) => void;
}) {
  const [draft, setDraft] = useState<Dot1XProfileInput>(
    profile
      ? {
          name: profile.name,
          method: profile.method,
          identity: profile.identity,
          anonymousIdentity: profile.anonymousIdentity,
          password: "",
          caCertPem: profile.caCertPem,
          serverDomain: profile.serverDomain,
        }
      : EMPTY_PROFILE,
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const set = <K extends keyof Dot1XProfileInput>(
    k: K,
    v: Dot1XProfileInput[K],
  ) => setDraft((d) => ({ ...d, [k]: v }));
  const tls = draft.method === "eap-tls";

  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);
    setBusy(true);
    // An empty password field keeps the stored one when editing.
    const body: Dot1XProfileInput = { ...draft };
    if (tls || (profile && draft.password === "")) delete body.password;
    try {
      const saved = profile
        ? await updateDot1XProfile(profile.id, body)
        : await createDot1XProfile(body);
      onSaved(saved);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title={profile ? `Edit ${profile.name}` : "Add an 802.1X profile"}
      onClose={onClose}
    >
      <form onSubmit={(e) => void submit(e)} noValidate>
        <Input
          id="dot1x-name"
          label="Name"
          value={draft.name}
          onChange={(e) => set("name", e.target.value)}
        />
        <Select
          id="dot1x-method"
          label="Method"
          options={DOT1X_METHODS}
          value={draft.method}
          onChange={(e) => set("method", e.target.value as Dot1XMethod)}
        />
        <Input
          id="dot1x-identity"
          label="Identity"
          mono
          hint="{mac} and {MAC} are filled in per phone; 64 characters at most."
          value={draft.identity}
          onChange={(e) => set("identity", e.target.value)}
        />
        {!tls && (
          <Input
            id="dot1x-password"
            label="Password"
            type="password"
            autoComplete="new-password"
            hint={
              profile?.hasPassword
                ? "Leave empty to keep the stored password."
                : "Sealed on save and never shown again."
            }
            value={draft.password ?? ""}
            onChange={(e) => set("password", e.target.value)}
          />
        )}
        {tls && (
          <p className="prov-muted">
            EAP-TLS uses the certificate the phone gets from SCEP; it is written
            only to phones whose group has SCEP on.
          </p>
        )}
        <Input
          id="dot1x-anon"
          label="Anonymous identity"
          mono
          hint="Optional; the outer identity on Wi-Fi."
          value={draft.anonymousIdentity}
          onChange={(e) => set("anonymousIdentity", e.target.value)}
        />
        <Input
          id="dot1x-domain"
          label="RADIUS server name"
          mono
          hint="Optional; the name the phone checks on the server certificate."
          value={draft.serverDomain}
          onChange={(e) => set("serverDomain", e.target.value)}
        />
        <PemField
          id="dot1x-ca"
          label="RADIUS server CA (PEM)"
          hint="Optional; the CA the RADIUS server's certificate chains to."
          value={draft.caCertPem}
          onChange={(v) => set("caCertPem", v)}
        />
        {error && (
          <Alert tone="bad" title="Could not save the profile">
            {error}
          </Alert>
        )}
        <div className="prov-row">
          <Button type="submit" disabled={busy}>
            Save
          </Button>
          <Button variant="secondary" type="button" onClick={onClose}>
            Cancel
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function CertificatesCard({
  certs,
  onRevoked,
}: {
  certs: Load<SCEPCertificate[]>;
  onRevoked: (c: SCEPCertificate) => void;
}) {
  const [revoking, setRevoking] = useState<SCEPCertificate | null>(null);
  const columns: TableColumn<SCEPCertificate>[] = [
    {
      key: "phone",
      label: "Phone",
      render: (c) =>
        c.phoneId === null ? (
          c.mac
        ) : (
          <Link to={`/phones/${c.phoneId}`}>{c.mac}</Link>
        ),
    },
    { key: "serial", label: "Serial", mono: true },
    {
      key: "kind",
      label: "Kind",
      render: (c) => (c.kind === "enrol" ? "Enrolment" : "Renewal"),
    },
    {
      key: "notAfter",
      label: "Valid until",
      render: (c) => formatDate(c.notAfter),
    },
    {
      key: "status",
      label: "Status",
      render: (c) => <Badge tone={STATUS_TONE[c.status]}>{c.status}</Badge>,
    },
    {
      key: "actions",
      label: <span className="visually-hidden">Actions</span>,
      align: "right",
      render: (c) =>
        c.status === "active" ? (
          <Can min="admin">
            <IconButton
              icon="shield-x"
              label={`Revoke ${c.serial}`}
              onClick={() => setRevoking(c)}
            />
          </Can>
        ) : null,
    },
  ];
  return (
    <div className="prov-card" role="region" aria-labelledby="certs-title">
      <h2 id="certs-title" className="prov-card__title">
        Issued certificates
      </h2>
      {certs.status === "loading" && <Spinner label="Loading certificates…" />}
      {certs.status === "error" && (
        <Alert tone="bad" title="Could not load certificates">
          {certs.message}
        </Alert>
      )}
      {certs.status === "ready" && certs.value.length === 0 && (
        <p className="prov-muted">No certificate issued yet.</p>
      )}
      {certs.status === "ready" && certs.value.length > 0 && (
        <Table
          caption="Issued certificates"
          columns={columns}
          rows={certs.value}
          rowKey={(c) => String(c.id)}
        />
      )}
      {revoking && (
        <ConfirmDialog
          title={`Revoke ${revoking.serial}?`}
          description="The certificate goes on the CRL and can no longer renew. The phone enrols again with its challenge."
          confirmLabel="Revoke certificate"
          errorTitle="Could not revoke the certificate"
          onConfirm={async () => {
            const c = await revokeCertificate(revoking.id, "unspecified");
            setRevoking(null);
            onRevoked(c);
          }}
          onClose={() => setRevoking(null)}
        />
      )}
    </div>
  );
}
