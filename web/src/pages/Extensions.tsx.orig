import { useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import {
  errorMessage,
  listDevices,
  listExtensions,
  type Device,
  type Extension,
} from "../api";
import {
  devicesLabel,
  devicesOf,
  forwardingLabel,
  loadLiveDirectory,
  presenceOf,
  type LiveDirectory,
} from "../api/directory";
import {
  Alert,
  Badge,
  Button,
  EmptyState,
  Input,
  PageHeader,
  Spinner,
  Table,
  type TableColumn,
  useToast,
} from "../design/azrty/components";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";
import "./directory/directory.css";
import { Can } from "../role";
import { ExtensionDrawer, NewExtension } from "../forms/ExtensionForm";

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; extensions: Extension[]; devices: Device[] };

/** Extensions: the directory list, the extension drawer and "New extension". */
export function Extensions() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [q, setQ] = useState("");
  const [selected, setSelected] = useState<Extension["id"] | null>(null);
  const [params, setParams] = useSearchParams();
  // ?new=1 (the Dashboard's "New extension") opens the New extension modal.
  const [creating, setCreating] = useState(params.get("new") === "1");
  // Extensions created while the list was still loading (from the header or
  // ?new=1): the list response may predate them, so they are merged in when
  // it lands.
  const createdEarly = useRef<Extension[]>([]);
  const toast = useToast();

  useEffect(() => {
    if (params.get("new") !== "1") return;
    // Drop the flag so closing the modal or reloading does not reopen it.
    const next = new URLSearchParams(params);
    next.delete("new");
    setParams(next, { replace: true });
  }, [params, setParams]);
  const liveState = usePolling(loadLiveDirectory, LIVE_REFRESH_MS);
  const live: LiveDirectory =
    liveState.status === "loading" ? {} : (liveState.data ?? {});

  useEffect(() => {
    const controller = new AbortController();
    Promise.all([
      listExtensions(controller.signal),
      listDevices(controller.signal),
    ])
      .then(([extensions, devices]) => {
        const early = createdEarly.current.filter(
          (c) => !extensions.some((e) => e.id === c.id),
        );
        setList({
          status: "ready",
          extensions: [...extensions, ...early],
          devices,
        });
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  function setExtensions(fn: (items: Extension[]) => Extension[]) {
    setList((prev) =>
      prev.status === "ready"
        ? { ...prev, extensions: fn(prev.extensions) }
        : prev,
    );
  }

  const all = list.status === "ready" ? list.extensions : [];
  const devices = list.status === "ready" ? list.devices : [];
  const query = q.trim().toLowerCase();
  const shown = all.filter(
    (e) =>
      !query ||
      e.number.includes(query) ||
      e.name.toLowerCase().includes(query),
  );
  const current = all.find((e) => e.id === selected);

  const columns: TableColumn<Extension>[] = [
    {
      key: "number",
      label: "Number",
      mono: true,
      render: (e) => (
        <button
          type="button"
          className="dir-rowlink dir-number"
          aria-label={`Open extension ${e.number}`}
          onClick={() => setSelected(e.id)}
        >
          {e.number}
        </button>
      ),
    },
    {
      key: "name",
      label: "Name",
      render: (e) => <span className="az-table__primary">{e.name}</span>,
    },
    {
      key: "externalNumber",
      label: "External number",
      mono: true,
      render: (e) => e.externalNumber || "—",
    },
    {
      key: "devices",
      label: "Devices",
      render: (e) => devicesLabel(devicesOf(devices, e), live.bindings),
    },
    {
      key: "presence",
      label: "Presence",
      render: (e) => {
        const p = presenceOf(e, devicesOf(devices, e), live);
        return p ? (
          <Badge tone={p.tone} dot>
            {p.label}
          </Badge>
        ) : (
          "—"
        );
      },
    },
    {
      key: "forwarding",
      label: "Forwarding",
      render: (e) => <span className="dir-cell-sm">{forwardingLabel(e)}</span>,
    },
    {
      key: "voicemail",
      label: "Voicemail",
      render: (e) => (e.voicemailEnabled === false ? "Off" : "On"),
    },
    {
      key: "recording",
      label: "Recording",
      render: (e) => (e.recordDefault ? "Default on" : "Off"),
    },
  ];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title="Extensions"
        description="Dialable numbers, their devices and call features."
        actions={
          <Can>
            <Button icon="plus" onClick={() => setCreating(true)}>
              New extension
            </Button>
          </Can>
        }
      />

      {list.status === "loading" && <Spinner label="Loading extensions…" />}
      {list.status === "error" && (
        <Alert tone="bad" title="Could not load extensions">
          {list.message}
        </Alert>
      )}
      {list.status === "ready" && all.length === 0 && (
        <EmptyState
          icon="user-round"
          title="No extensions yet"
          description="An extension is a dialable number. Add one, then give it devices."
          action={
            <Can>
              <Button icon="plus" onClick={() => setCreating(true)}>
                New extension
              </Button>
            </Can>
          }
        />
      )}
      {list.status === "ready" && all.length > 0 && (
        <>
          <div className="dir-toolbar">
            <Input
              id="ext-search"
              aria-label="Search number or name"
              icon="search"
              size="sm"
              type="search"
              placeholder="Search number or name"
              value={q}
              onChange={(e) => setQ(e.target.value)}
              className="dir-toolbar__search"
            />
            <span className="dir-count" aria-live="polite">
              {shown.length} of {all.length}
            </span>
          </div>
          {shown.length === 0 ? (
            <EmptyState
              icon="search"
              title="No extensions match"
              description={`Nothing matches “${q.trim()}”. Search by number or name.`}
            />
          ) : (
            <Table
              caption="Extensions"
              columns={columns}
              rows={shown}
              rowKey={(e) => String(e.id)}
              onRowClick={(e) => setSelected(e.id)}
            />
          )}
        </>
      )}

      {current && (
        <ExtensionDrawer
          key={String(current.id)}
          ext={current}
          devices={devicesOf(devices, current)}
          live={live}
          onClose={() => setSelected(null)}
          onSaved={(saved) => {
            setExtensions((items) =>
              items.map((i) => (i.id === saved.id ? saved : i)),
            );
            setSelected(null);
            toast.show(`Extension ${saved.number} saved.`);
          }}
          onDeleted={() => {
            setExtensions((items) => items.filter((i) => i.id !== current.id));
            setList((prev) =>
              prev.status === "ready"
                ? {
                    ...prev,
                    devices: prev.devices.filter(
                      (d) => String(d.extensionId) !== String(current.id),
                    ),
                  }
                : prev,
            );
            setSelected(null);
            toast.show(`Extension ${current.number} and its devices deleted.`);
          }}
        />
      )}

      {creating && (
        <NewExtension
          onClose={() => setCreating(false)}
          onCreated={(ext) => {
            if (list.status !== "ready") createdEarly.current.push(ext);
            setExtensions((items) => [...items, ext]);
            setCreating(false);
            toast.show(`Extension ${ext.number} created.`);
          }}
        />
      )}

      {toast.node}
    </section>
  );
}
