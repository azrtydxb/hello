import { listRegistrations, type Binding } from "../api";
import { expiresIn } from "../api/directory";
import {
  Alert,
  Badge,
  EmptyState,
  Spinner,
  Table,
  type TableColumn,
} from "../design/azrty/components";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";
import { PageHeader } from "./directory/PageHeader";
import "./directory/directory.css";

const columns: TableColumn<Binding>[] = [
  {
    key: "aor",
    label: "AOR",
    mono: true,
    render: (b) => (
      <>
        {b.aor}
        <small>{b.device}</small>
      </>
    ),
  },
  { key: "contactUri", label: "Contact", mono: true },
  { key: "source", label: "Source", mono: true },
  {
    key: "transport",
    label: "Transport",
    render: (b) => (
      <Badge tone="outline">
        {b.transport ? b.transport.toUpperCase() : "—"}
      </Badge>
    ),
  },
  {
    key: "userAgent",
    label: "User agent",
    render: (b) => <span className="dir-cell-sm">{b.userAgent || "—"}</span>,
  },
  {
    key: "receivedNode",
    label: "Node",
    mono: true,
    render: (b) => b.receivedNode || "—",
  },
  {
    key: "expires",
    label: "Expires in",
    align: "right",
    mono: true,
    render: (b) => expiresIn(b.expires),
  },
];

/** Registrations: live SIP bindings from GET /api/v1/registrations, refreshed every 5 s. */
export function Registrations() {
  const state = usePolling(listRegistrations, LIVE_REFRESH_MS);
  const items = state.status === "loading" ? undefined : state.data;

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title="Registrations"
        description="Contacts bound in Valkey, as received by each SIP node through Kamailio."
        actions={
          state.status === "ready" ? (
            <span className="az-live">
              <span className="az-dot az-dot--pulse" aria-hidden="true" />
              LIVE · 5 S
            </span>
          ) : undefined
        }
      />
      <div className="dir-stack">
        {state.status === "loading" && (
          <Spinner label="Loading registrations…" />
        )}
        {state.status === "error" && (
          <Alert tone="bad" title="Could not load registrations.">
            {state.message}
            {items ? " Showing the last list received." : ""}
          </Alert>
        )}
        {items && items.length === 0 && (
          <EmptyState
            icon="radio-tower"
            title="No devices are registered"
            description="A device appears here once its phone sends REGISTER with the right secret."
          />
        )}
        {items && items.length > 0 && (
          <Table
            caption="Registrations"
            columns={columns}
            rows={items}
            rowKey={(b) => `${b.aor} ${b.contactUri}`}
          />
        )}
      </div>
    </section>
  );
}
