import { useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { ApiError, errorMessage } from "../api";
import { streamEvents } from "../api/events";
import {
  cancelOriginate,
  dialable,
  getOriginate,
  newIdempotencyKey,
  originateCall,
  originateFinished,
  type OriginateReason,
  type OriginateState,
  type OriginateStatus,
} from "../api/originate";
import { useAuth } from "../auth";
import {
  Alert,
  Badge,
  Button,
  EmptyState,
  Input,
  PageHeader,
  Select,
  useToast,
  type BadgeTone,
} from "../design/azrty/components";
import { useCan } from "../role";
import "./platform/platform.css";

const POLL_MS = 1000;

/** What each state tells the person at the phone. */
const STATE_TEXT: Record<OriginateStatus["state"], string> = {
  queued: "Getting a line…",
  ringing_agent: "Your phone is ringing. Answer it to place the call.",
  agent_answered: "Dialling…",
  dialing_target: "Dialling…",
  connected: "Connected.",
  ended: "The call has ended.",
  failed: "The call could not be placed.",
};

const STATE_TONE: Record<OriginateStatus["state"], BadgeTone> = {
  queued: "neutral",
  ringing_agent: "info",
  agent_answered: "info",
  dialing_target: "info",
  connected: "good",
  ended: "neutral",
  failed: "bad",
};

/** Why a call failed, in words. */
export const REASON_TEXT: Record<OriginateReason, string> = {
  no_registration: "Your phone is not registered, so there is nothing to ring.",
  agent_no_answer: "Your phone was not answered.",
  agent_busy: "Your phone is busy.",
  agent_dnd: "Your phone is on Do Not Disturb.",
  target_busy: "The number is busy.",
  target_no_answer: "The number did not answer.",
  route_denied: "This number cannot be dialled from here.",
  rate_limited: "Too many calls were placed; try again in a moment.",
  node_unavailable: "The phone system did not respond; try again.",
  cancelled: "The call was cancelled.",
};

/**
 * Dial (/dial?n=<number>): places a click-to-call. Hello rings your own phone
 * first and dials the number when you answer. The page only shows the number
 * and waits for a click, so a link on another page cannot place a call by
 * itself. A browser asked to handle tel: links lands here (see
 * <PhoneLinkHandler>).
 */
export function Dial() {
  const [params] = useSearchParams();
  const { state } = useAuth();
  const canNameAny = useCan("operator");
  const linked = state.status === "signedIn" ? (state.me.extensions ?? []) : [];
  const fromLink = params.get("n");
  const [raw, setRaw] = useState(dialable(fromLink ?? "") || (fromLink ?? ""));
  const [extension, setExtension] = useState(linked[0]?.number ?? "");
  const [status, setStatus] = useState<OriginateStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const key = useRef(newIdempotencyKey());

  const destination = dialable(raw);
  const invalidLink =
    fromLink !== null && fromLink !== "" && dialable(fromLink) === "";
  const noPhone = linked.length === 0 && !canNameAny;
  const running = status !== null && !originateFinished(status.state);

  // Follow the call until it is over: from the event stream, and by polling
  // only while the stream is not there (it dropped or was refused).
  const id = status?.originateId;
  const finished = status !== null && originateFinished(status.state);
  useEffect(() => {
    if (!id || finished) return;
    const ctl = new AbortController();
    let timer: ReturnType<typeof setInterval> | undefined;
    const poll = () => {
      getOriginate(id, ctl.signal)
        .then((s) => {
          if (!ctl.signal.aborted) setStatus(s);
        })
        .catch((err: unknown) => {
          if (!ctl.signal.aborted) setError(errorMessage(err));
        });
    };
    const startPolling = () => {
      if (ctl.signal.aborted || timer !== undefined) return;
      timer = setInterval(poll, POLL_MS);
    };
    streamEvents(
      { types: ["call.originate.progress"] },
      {
        // One read now covers a state change that happened before the
        // stream opened.
        onOpen: poll,
        onEvent: (e) => {
          if (e.data.originateId !== id || !e.data.state) return;
          const state = e.data.state as OriginateState;
          const reason = e.data.reason as OriginateReason | undefined;
          setStatus((prev) =>
            prev && prev.originateId === id
              ? { ...prev, state, reason, updatedAt: e.createdAt }
              : prev,
          );
        },
      },
      ctl.signal,
    ).then(startPolling, startPolling);
    return () => {
      ctl.abort();
      clearInterval(timer);
    };
  }, [id, finished]);

  async function place() {
    if (busy || destination === "") return;
    setBusy(true);
    setError(null);
    try {
      const s = await originateCall({
        destination,
        ...(extension ? { extension } : {}),
        idempotencyKey: key.current,
      });
      setStatus(s);
    } catch (err) {
      setError(
        err instanceof ApiError && err.status === 429
          ? "Too many calls were placed; try again in a moment."
          : errorMessage(err),
      );
    } finally {
      setBusy(false);
    }
  }

  async function hangUp() {
    if (!status) return;
    try {
      setStatus(await cancelOriginate(status.originateId));
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  function again() {
    key.current = newIdempotencyKey();
    setStatus(null);
    setError(null);
  }

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="My phone"
        title="Dial"
        description="Hello rings your phone first and dials the number when you answer."
      />
      {noPhone ? (
        <EmptyState
          icon="phone-off"
          title="No extension linked"
          description="Your account is not linked to an extension, so there is no phone to ring. Ask an administrator to link it."
        />
      ) : (
        <div className="az-card pf-stack">
          {invalidLink && (
            <Alert tone="warn" title="That link is not a phone number">
              The link asked to dial {fromLink}, which is not a number Hello can
              dial. Check it before placing a call.
            </Alert>
          )}
          {status === null ? (
            <>
              <Input
                label="Number to call"
                mono
                value={raw}
                inputMode="tel"
                autoComplete="off"
                error={
                  raw !== "" && destination === ""
                    ? "Digits, +, * and # only."
                    : undefined
                }
                onChange={(e) => setRaw(e.target.value)}
              />
              {linked.length > 1 ? (
                <Select
                  label="Ring this extension first"
                  value={extension}
                  options={linked.map((x) => ({
                    value: x.number,
                    label: `${x.number} ${x.name}`,
                  }))}
                  onChange={(e) => setExtension(e.target.value)}
                />
              ) : linked.length === 1 ? (
                <p className="az-field__hint">
                  Your phone, extension{" "}
                  <span className="media-mono">{linked[0]?.number}</span>, rings
                  first.
                </p>
              ) : (
                <Input
                  label="Ring this extension first"
                  mono
                  value={extension}
                  hint="Your account has no extension of its own; name the extension to ring."
                  onChange={(e) => setExtension(e.target.value)}
                />
              )}
              {error && (
                <Alert tone="bad" title="Could not place the call">
                  {error}
                </Alert>
              )}
              <div>
                <Button
                  icon="phone-outgoing"
                  disabled={busy || destination === "" || extension === ""}
                  onClick={() => void place()}
                >
                  {destination ? `Call ${destination}` : "Call"}
                </Button>
              </div>
            </>
          ) : (
            <Progress
              status={status}
              error={error}
              running={running}
              onHangUp={() => void hangUp()}
              onAgain={again}
            />
          )}
        </div>
      )}
      <PhoneLinkHandler />
    </section>
  );
}

function Progress({
  status,
  error,
  running,
  onHangUp,
  onAgain,
}: {
  status: OriginateStatus;
  error: string | null;
  running: boolean;
  onHangUp: () => void;
  onAgain: () => void;
}) {
  return (
    <div className="pf-stack" aria-live="polite">
      <p>
        <span className="media-mono">{status.destination}</span>{" "}
        <Badge tone={STATE_TONE[status.state]}>{status.state}</Badge>
      </p>
      <p>{STATE_TEXT[status.state]}</p>
      {status.reason && (
        <Alert tone="bad" title="Why">
          {REASON_TEXT[status.reason]}
        </Alert>
      )}
      {error && (
        <Alert tone="bad" title="Lost track of the call">
          {error}
        </Alert>
      )}
      <div>
        {running ? (
          <Button variant="danger" icon="phone-off" onClick={onHangUp}>
            Hang up
          </Button>
        ) : (
          <Button variant="secondary" icon="phone-outgoing" onClick={onAgain}>
            Dial another number
          </Button>
        )}
      </div>
    </div>
  );
}

/**
 * Offers to make this console the browser's handler for tel: links. The
 * browser decides (it asks the user; Chrome and Edge support it, Firefox
 * needs a setting), so the button only asks. callto: is not on the HTML
 * safelist and is left to the browser extension.
 */
export function PhoneLinkHandler() {
  const toast = useToast();
  const supported =
    typeof navigator !== "undefined" &&
    typeof navigator.registerProtocolHandler === "function";
  if (!supported) {
    return (
      <p className="az-field__hint">
        This browser cannot register Hello for phone links.
      </p>
    );
  }
  function register() {
    try {
      navigator.registerProtocolHandler(
        "tel",
        `${window.location.origin}/dial?n=%s`,
      );
      toast.show(
        "Your browser may ask you to allow Hello to open phone links.",
      );
    } catch (err) {
      toast.show(`The browser refused: ${errorMessage(err)}`);
    }
  }
  return (
    <div className="az-card pf-stack">
      <h2>Phone links</h2>
      <p className="az-field__hint">
        Make a tel: link on any web page open Hello, which asks you to confirm
        before it rings your phone.
      </p>
      <div>
        <Button variant="secondary" icon="link" onClick={register}>
          Use Hello for phone links
        </Button>
      </div>
    </div>
  );
}
