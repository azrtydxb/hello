import { useState } from "react";
import { useSearchParams } from "react-router";
import { errorMessage } from "../api";
import {
  connectMyConnection,
  disconnectMyConnection,
  listMyConnections,
  setMyCalendarDND,
  type MyConnector,
} from "../api/crm";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  PageHeader,
  PropertyList,
  Spinner,
  Switch,
  useToast,
  type BadgeTone,
} from "../design/azrty/components";
import { formatTime } from "../format";
import { usePolling } from "../usePolling";
import "./platform/platform.css";

const ERRORS: Record<string, string> = {
  denied: "The sign-in was declined.",
  state: "The sign-in expired or was already used. Start it again.",
  failed:
    "The provider did not accept the sign-in. Ask your administrator to check the app settings.",
};

const TONE: Record<string, BadgeTone> = {
  connected: "good",
  needs_reauth: "bad",
  revoked: "warn",
};

const PROVIDER: Record<string, string> = {
  outlook: "Microsoft 365 / Outlook",
  google: "Google",
};

function hasCalendar(c: MyConnector): boolean {
  return (
    c.features.includes("calendar") &&
    (c.connection?.grantedScopes ?? []).some((s) =>
      s.toLowerCase().includes("calendar"),
    )
  );
}

/**
 * My connected accounts (/me/connections): sign in to Outlook or Google with
 * your own account so your contacts name the people who call you, and let your
 * calendar switch Do Not Disturb on while you are busy.
 */
export function MyConnections() {
  const list = usePolling(listMyConnections, 30_000);
  const [params] = useSearchParams();
  const toast = useToast();
  const error = params.get("error");
  const connected = params.get("connected");
  const items = list.status === "loading" ? null : (list.data ?? []);
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="My phone"
        title="Connected accounts"
        description="Connect your own Outlook or Google account. Your contacts then name the people who call you, and your calendar can switch Do Not Disturb on while you are busy. Hello only reads; it never changes your contacts or your calendar."
      />
      <div className="cf-stack">
        {connected && (
          <Alert tone="good" title="Connected">
            Your account is connected.
          </Alert>
        )}
        {error && (
          <Alert tone="bad" title="Sign-in failed">
            {ERRORS[error] ?? "The sign-in did not finish."}
          </Alert>
        )}
        {list.status === "loading" && <Spinner label="Loading…" />}
        {list.status === "error" && (
          <Alert tone="bad" title="Could not load your accounts">
            {list.message}
          </Alert>
        )}
        {items && items.length === 0 && (
          <EmptyState
            icon="plug"
            title="Nothing to connect"
            description="Your administrator has not set up Outlook or Google yet."
          />
        )}
        {items?.map((c) => (
          <AccountPanel
            key={c.id}
            connector={c}
            reload={list.reload}
            say={toast.show}
          />
        ))}
      </div>
      {toast.node}
    </section>
  );
}

function AccountPanel({
  connector: c,
  reload,
  say,
}: {
  connector: MyConnector;
  reload: () => void;
  say: (m: string) => void;
}) {
  const [err, setErr] = useState<string | null>(null);
  const [confirm, setConfirm] = useState(false);
  const conn = c.connection;

  async function connect() {
    setErr(null);
    try {
      window.location.assign(await connectMyConnection(c.id));
    } catch (x: unknown) {
      setErr(errorMessage(x));
    }
  }

  async function toggleDND(on: boolean) {
    setErr(null);
    try {
      await setMyCalendarDND(c.id, on);
      reload();
      say(
        on
          ? "Calendar Do Not Disturb is on."
          : "Calendar Do Not Disturb is off.",
      );
    } catch (x: unknown) {
      setErr(errorMessage(x));
    }
  }

  const uses = [
    c.features.includes("contacts") && "your contacts name callers",
    c.features.includes("calendar") && "your calendar can set Do Not Disturb",
  ].filter(Boolean);

  return (
    <article aria-label={`${c.name} account`}>
      <h2>
        {PROVIDER[c.kind] ?? c.name}{" "}
        {conn ? (
          <Badge tone={TONE[conn.status] ?? "neutral"}>
            {conn.status === "connected"
              ? "Connected"
              : conn.status === "needs_reauth"
                ? "Sign in again"
                : "Revoked"}
          </Badge>
        ) : (
          <Badge tone="warn">Not connected</Badge>
        )}
      </h2>
      <p>Once connected: {uses.join(", ")}.</p>
      {conn?.status === "needs_reauth" && (
        <Alert tone="bad" title="Sign in again">
          {conn.lastError || "The provider refused the stored sign-in."} Until
          you do, your contacts and calendar are not used.
        </Alert>
      )}
      {conn && (
        <PropertyList
          items={[
            {
              label: "Account",
              value: conn.externalAccount || "—",
              mono: true,
            },
            { label: "Connected", value: formatTime(conn.updatedAt) },
          ]}
        />
      )}
      {conn && hasCalendar(c) && (
        <div>
          <Switch
            label="Do Not Disturb while my calendar says busy"
            hint="Busy and out-of-office events switch Do Not Disturb on at your extensions and the end of the event switches it off. A Do Not Disturb you switch on yourself is never changed."
            checked={conn.calendarDnd}
            onChange={(e) => void toggleDND(e.target.checked)}
          />
          {conn.calendarDnd && conn.calendarActive && (
            <p>
              <Badge tone="info">Active</Badge> Your calendar is holding Do Not
              Disturb on
              {conn.calendarBusyUntil
                ? ` until ${formatTime(conn.calendarBusyUntil)}`
                : ""}
              .
            </p>
          )}
          {conn.calendarDnd && conn.calendarError && (
            <Alert tone="warn" title="Calendar not read">
              {conn.calendarError}
            </Alert>
          )}
          {conn.calendarDnd &&
            conn.calendarCheckedAt &&
            !conn.calendarError && (
              <p>Last checked {formatTime(conn.calendarCheckedAt)}.</p>
            )}
        </div>
      )}
      {conn && c.features.includes("calendar") && !hasCalendar(c) && (
        <Alert tone="info" title="Calendar access was added">
          Connect again to allow your calendar to be read.
        </Alert>
      )}
      {err && (
        <Alert tone="bad" title="Not done">
          {err}
        </Alert>
      )}
      <p>
        <Button icon="link" onClick={() => void connect()}>
          {conn ? "Connect again" : "Connect"}
        </Button>{" "}
        {conn && (
          <Button variant="secondary" onClick={() => setConfirm(true)}>
            Disconnect
          </Button>
        )}
      </p>
      {confirm && (
        <ConfirmDialog
          title={`Disconnect ${PROVIDER[c.kind] ?? c.name}?`}
          description="Hello forgets the sign-in and stops reading your contacts and calendar. A Do Not Disturb your calendar set is switched off."
          confirmLabel="Disconnect"
          errorTitle="Could not disconnect"
          onClose={() => setConfirm(false)}
          onConfirm={async () => {
            await disconnectMyConnection(c.id);
            reload();
            setConfirm(false);
            say("Disconnected.");
          }}
        />
      )}
    </article>
  );
}
