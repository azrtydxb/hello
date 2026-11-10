import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { errorMessage } from "../api";
import {
  cursorAfter,
  EVENT_TYPES,
  listEvents,
  streamEvents,
  type EventEntry,
} from "../api/events";
import {
  Alert,
  Badge,
  Button,
  EmptyState,
  Input,
  LiveTag,
  PageHeader,
  Select,
  Table,
  type BadgeTone,
} from "../design/azrty/components";
import { formatDuration, formatTime } from "../format";
import "./platform/platform.css";

/** How many events the tail keeps. */
const KEEP = 300;
/** A stream with no frame (a keepalive comment included) for this long is stalled. */
export const STALL_MS = 45_000;
const BACKOFF_MS = [1000, 2000, 5000, 15_000];

type Connection = "connecting" | "live" | "reconnecting" | "paused" | "stalled";

const TONES: Record<string, BadgeTone> = {
  "call.ringing": "info",
  "call.answered": "good",
  "call.ended": "neutral",
  "call.missed": "warn",
  "call.recording.ready": "outline",
  "call.completed": "outline",
  "call.originate.progress": "info",
};

function party(p: { number?: string; name?: string; extension?: string }) {
  const n = p.number || p.extension || "—";
  return p.name ? `${p.name} (${n})` : n;
}

function details(e: EventEntry): string {
  const d = e.data;
  const parts: string[] = [];
  if (d.direction) parts.push(d.direction);
  if (d.state) parts.push(d.state);
  if (d.reason) parts.push(`reason ${d.reason}`);
  if (d.disposition) parts.push(d.disposition);
  if (d.durationMs) parts.push(formatDuration(d.durationMs));
  if (d.cdrId) parts.push(`CDR ${d.cdrId}`);
  if (d.recordingId) parts.push(`recording ${d.recordingId}`);
  return parts.join(" · ");
}

/** Merges events into the tail: newest first, one row per event id. */
export function mergeEvents(
  tail: readonly EventEntry[],
  incoming: readonly EventEntry[],
): EventEntry[] {
  const seen = new Set(tail.map((e) => e.id));
  const fresh = incoming.filter((e) => !seen.has(e.id)).reverse();
  return [...fresh, ...tail].slice(0, KEEP);
}

/**
 * Events (/integrations/events): a live tail of the call events hello-sip
 * emits, for checking what an integration will receive. It shows the latest
 * stored events, then follows the event stream and, after any interruption,
 * resumes from the last event it saw (Last-Event-ID).
 */
export function IntegrationEvents() {
  const [type, setType] = useState("");
  const [extension, setExtension] = useState("");
  const [paused, setPaused] = useState(false);
  const [conn, setConn] = useState<Connection>("connecting");
  const [error, setError] = useState("");
  const [rows, setRows] = useState<EventEntry[]>([]);
  const [open, setOpen] = useState<string | null>(null);
  const cursor = useRef("");
  // The filter the tail was last loaded for: a pause and resume keeps the
  // tail and continues from its cursor; a new filter starts over.
  const loadedFor = useRef("");

  const filter = useMemo(
    () => ({
      types: type ? [type] : [],
      extension: /^[0-9]{2,10}$/.test(extension) ? extension : "",
    }),
    [type, extension],
  );
  const shown: Connection = paused ? "paused" : conn;
  const extensionInvalid = extension !== "" && filter.extension === "";

  const add = useCallback((incoming: EventEntry[]) => {
    for (const e of incoming) {
      if (cursorAfter(e.cursor, cursor.current)) cursor.current = e.cursor;
    }
    setRows((prev) => mergeEvents(prev, incoming));
  }, []);

  useEffect(() => {
    if (paused) return;
    const controller = new AbortController();
    const { signal } = controller;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let stall: ReturnType<typeof setTimeout> | undefined;
    let attempt = 0;
    const key = JSON.stringify(filter);
    const fresh = loadedFor.current !== key;
    loadedFor.current = key;

    const armStall = (run: AbortController) => {
      clearTimeout(stall);
      stall = setTimeout(() => {
        setConn("stalled");
        run.abort(); // the loop below reconnects
      }, STALL_MS);
    };

    async function loop() {
      setConn("connecting");
      if (fresh) {
        setRows([]);
        cursor.current = "";
      }
      try {
        if (!fresh) throw undefined; // resuming: the stream replays from the cursor
        const page = await listEvents(
          { ...filter, order: "desc", limit: 50 },
          signal,
        );
        if (signal.aborted) return;
        // Oldest first, so the newest ends up on top.
        add([...page.items].reverse());
      } catch (err) {
        if (signal.aborted) return;
        if (err !== undefined) setError(errorMessage(err));
      }
      while (!signal.aborted) {
        const run = new AbortController();
        const stop = () => run.abort();
        signal.addEventListener("abort", stop);
        armStall(run);
        try {
          await streamEvents(
            { ...filter, lastEventId: cursor.current },
            {
              onOpen: () => {
                attempt = 0;
                setConn("live");
                setError("");
              },
              onEvent: (e) => add([e]),
              onActivity: () => armStall(run),
            },
            run.signal,
          );
          if (!signal.aborted) setConn("reconnecting");
        } catch (err) {
          if (signal.aborted) return;
          if (run.signal.aborted) {
            // A stall: the connection state says so already.
          } else {
            setConn("reconnecting");
            setError(errorMessage(err));
          }
        } finally {
          clearTimeout(stall);
          signal.removeEventListener("abort", stop);
        }
        if (signal.aborted) return;
        const delay = BACKOFF_MS[Math.min(attempt, BACKOFF_MS.length - 1)];
        attempt++;
        await new Promise<void>((resolve) => {
          timer = setTimeout(resolve, delay);
          signal.addEventListener("abort", () => resolve(), { once: true });
        });
      }
    }
    void loop();
    return () => {
      controller.abort();
      clearTimeout(timer);
      clearTimeout(stall);
    };
  }, [filter, paused, add]);

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Integrations"
        title="Events"
        description="The call events integrations receive: ringing, answered, ended, missed, recording ready and completed. The newest are on top; the tail follows the live stream."
        actions={
          <>
            <LiveTag live={shown === "live"} />
            <Button
              variant="secondary"
              onClick={() => setPaused((p) => !p)}
              aria-pressed={paused}
            >
              {paused ? "Resume" : "Pause"}
            </Button>
          </>
        }
      />
      <div className="pf-stack">
        {shown === "stalled" && (
          <Alert tone="warn" title="Stream stalled">
            No data for {STALL_MS / 1000} seconds. Reconnecting; a proxy that
            buffers responses can cause this.
          </Alert>
        )}
        {error && (
          <Alert tone="bad" title="Event stream">
            {error}
          </Alert>
        )}
        <div
          style={{
            display: "flex",
            gap: "var(--az-space-3, 12px)",
            alignItems: "flex-end",
            flexWrap: "wrap",
          }}
        >
          <Select
            label="Event type"
            value={type}
            onChange={(e) => setType(e.target.value)}
            options={[
              { value: "", label: "All types" },
              ...EVENT_TYPES.map((t) => ({ value: t, label: t })),
            ]}
          />
          <Input
            label="Extension"
            value={extension}
            onChange={(e) => setExtension(e.target.value.trim())}
            placeholder="all extensions"
            error={extensionInvalid ? "2 to 10 digits" : undefined}
            mono
          />
          <Badge tone="outline">{shown === "live" ? "connected" : shown}</Badge>
        </div>
        <div className="az-card pf-card pf-card--tight">
          {rows.length === 0 ? (
            <EmptyState
              icon="radio-tower"
              title="No events yet"
              description="Events appear here as calls ring, are answered, end or are missed."
            />
          ) : (
            <Table
              caption="Call events"
              rows={rows}
              rowKey={(e) => e.id}
              onRowClick={(e) => setOpen((o) => (o === e.id ? null : e.id))}
              columns={[
                {
                  key: "time",
                  label: "Time",
                  render: (e) => formatTime(e.createdAt),
                },
                {
                  key: "type",
                  label: "Type",
                  render: (e) => (
                    <Badge tone={TONES[e.type] ?? "neutral"}>{e.type}</Badge>
                  ),
                },
                {
                  key: "ext",
                  label: "Extension",
                  mono: true,
                  render: (e) => e.data.extension ?? "—",
                },
                {
                  key: "from",
                  label: "From",
                  render: (e) => party(e.data.from),
                },
                { key: "to", label: "To", render: (e) => party(e.data.to) },
                {
                  key: "details",
                  label: "Details",
                  render: (e) => (
                    <>
                      {details(e)}
                      {open === e.id && (
                        <pre className="pf-mono" aria-label="Event payload">
                          {JSON.stringify(e, null, 2)}
                        </pre>
                      )}
                    </>
                  ),
                },
              ]}
            />
          )}
        </div>
      </div>
    </section>
  );
}
