// Trace mode (spec flow-canvas S-18): ask what happens when X calls Y at a
// time, optionally pressing keys in menus, and see the path. The canvas
// highlights it; this panel lists it. Nothing is dialled and nothing is
// written, so a viewer can use it.

import { useEffect, useMemo, useState, type FormEvent } from "react";
import { errorMessage } from "../../api";
import type { FlowGraph } from "../../api/flow";
import {
  traceFlow,
  type FlowTrace,
  type FlowTraceStep,
} from "../../api/flowTrace";
import {
  Alert,
  Badge,
  Button,
  Drawer,
  Input,
  Select,
} from "../../design/azrty/components";
import { toRFC3339 } from "./traceHighlight";

export interface TracePanelProps {
  graph: FlowGraph;
  trace: FlowTrace | null;
  onTrace: (trace: FlowTrace | null) => void;
  /** Brings a node into view; the id may not be on this canvas. */
  onFocusNode: (id: string) => void;
  onClose: () => void;
}

const KEYS = ["1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "0", "#"];

const OUTCOME: Record<string, string> = {
  internal: "Internal call",
  inbound: "Inbound call",
  outbound: "Outbound call",
  reject: "Rejected",
};

export function TracePanel({
  graph,
  trace,
  onTrace,
  onFocusNode,
  onClose,
}: TracePanelProps) {
  const sources = useMemo(() => {
    const trunks = graph.nodes
      .filter((n) => n.kind === "trunk")
      .map((n) => ({ value: n.id, label: `Trunk ${n.label}` }));
    const exts = graph.nodes
      .filter((n) => n.kind === "extension")
      .map((n) => ({
        value: String(n.summary?.number ?? ""),
        label: `Extension ${n.label}`,
      }))
      .filter((o) => o.value);
    return [...trunks, ...exts];
  }, [graph.nodes]);

  const [from, setFrom] = useState(sources[0]?.value ?? "");
  const [number, setNumber] = useState("");
  const [callerId, setCallerId] = useState("");
  const [at, setAt] = useState("");
  const [digits, setDigits] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  // Escape closes trace mode, as it closes the node panel.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      if (document.querySelector('[aria-modal="true"]')) return;
      onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const fromTrunk = from.startsWith("trunk:");

  const run = async (e: FormEvent) => {
    e.preventDefault();
    if (!from || !number.trim()) {
      setError("Choose who calls and the number they dial.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      onTrace(
        await traceFlow({
          from,
          number: number.trim(),
          callerId: callerId.trim() || undefined,
          at: toRFC3339(at),
          digits: digits || undefined,
        }),
      );
    } catch (err) {
      onTrace(null);
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const labelOf = (id: string) =>
    graph.nodes.find((n) => n.id === id)?.label ?? id;
  const stale = trace !== null && trace.configRevision !== graph.configRevision;

  const stepList = (steps: FlowTraceStep[]) => (
    <ol className="flow-trace__steps">
      {steps.map((s, i) => (
        <li key={i}>
          <button
            type="button"
            className="flow-linkbtn"
            onClick={() => onFocusNode(s.node)}
          >
            {labelOf(s.node)}
          </button>
          <p>{s.text}</p>
          {s.branches && s.branches.length > 0 && (
            <ul className="flow-trace__branches">
              {s.branches.map((b, j) => (
                <li key={j}>
                  <strong>{b.label}</strong>
                  {stepList(b.steps)}
                </li>
              ))}
            </ul>
          )}
        </li>
      ))}
    </ol>
  );

  return (
    <aside className="flow-panel" aria-label="Trace a call">
      <Drawer
        inline
        open
        title="Trace a call"
        description="Follow a call through the flow. Nothing is dialled."
        onClose={onClose}
        width="100%"
      >
        <form className="flow-trace__form" onSubmit={(e) => void run(e)}>
          <Select
            id="flow-trace-from"
            label="Call comes from"
            value={from}
            onChange={(e) => setFrom(e.target.value)}
            options={sources}
          />
          <Input
            id="flow-trace-number"
            label={fromTrunk ? "Called number (DID)" : "Dialled number"}
            mono
            value={number}
            onChange={(e) => setNumber(e.target.value)}
          />
          <Input
            id="flow-trace-caller"
            label="Caller ID"
            mono
            placeholder="Optional"
            value={callerId}
            onChange={(e) => setCallerId(e.target.value)}
          />
          <Input
            id="flow-trace-at"
            label="At"
            type="datetime-local"
            value={at}
            hint="Empty means now. Schedules, holidays and overrides are read at this time."
            onChange={(e) => setAt(e.target.value)}
          />
          <fieldset className="flow-trace__keypad">
            <legend>Keys pressed in menus</legend>
            <Input
              id="flow-trace-digits"
              aria-label="Keys pressed"
              mono
              value={digits}
              placeholder="None: every key is listed"
              onChange={(e) =>
                setDigits(e.target.value.replace(/[^0-9*#]/g, "").slice(0, 32))
              }
            />
            <div className="flow-trace__keys">
              {KEYS.map((k) => (
                <Button
                  key={k}
                  type="button"
                  variant="secondary"
                  size="sm"
                  aria-label={k === "*" ? "Key star" : `Key ${k}`}
                  onClick={() => setDigits((d) => (d + k).slice(0, 32))}
                >
                  {k}
                </Button>
              ))}
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={!digits}
                onClick={() => setDigits("")}
              >
                Clear keys
              </Button>
            </div>
          </fieldset>
          <Button type="submit" icon="play" block disabled={busy}>
            {busy ? "Tracing…" : "Trace"}
          </Button>
        </form>

        {error && (
          <Alert tone="bad" title="Could not trace">
            {error}
          </Alert>
        )}
        {stale && (
          <Alert tone="warn">
            The configuration changed after this trace. Trace again.
          </Alert>
        )}
        {trace && (
          <div className="flow-panel__section" aria-live="polite">
            <h3>
              {OUTCOME[trace.outcome] ?? trace.outcome}{" "}
              <Badge tone={trace.outcome === "reject" ? "bad" : "good"}>
                {trace.route ? `Route ${trace.route}` : trace.outcome}
              </Badge>
            </h3>
            {trace.reason && <p>{trace.reason}</p>}
            {stepList(trace.steps)}
          </div>
        )}
      </Drawer>
    </aside>
  );
}
